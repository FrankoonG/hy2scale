//go:build linux

package tools

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// shellPath: $SHELL if it exists, else /bin/bash, else /bin/sh.
func shellPath() string {
	for _, p := range []string{os.Getenv("SHELL"), "/bin/bash", "/bin/sh"} {
		if p == "" || !filepath.IsAbs(p) {
			continue
		}
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

func terminalAvailable() (bool, string) {
	if shellPath() == "" {
		return false, "no shell found"
	}
	if _, err := os.Stat("/dev/ptmx"); err != nil {
		return false, "no /dev/ptmx"
	}
	return true, ""
}

// openPTY returns a new PTY pair (master non-blocking, for the poller).
func openPTY() (*os.File, *os.File, error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}
	fail := func(what string, err error) (*os.File, *os.File, error) {
		unix.Close(fd)
		return nil, nil, fmt.Errorf("%s: %w", what, err)
	}
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		return fail("unlock pty", err)
	}
	n, err := unix.IoctlGetUint32(fd, unix.TIOCGPTN)
	if err != nil {
		return fail("pty number", err)
	}
	name := "/dev/pts/" + strconv.Itoa(int(n))
	sfd, err := unix.Open(name, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fail("open "+name, err)
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(sfd)
		return fail("pty nonblock", err)
	}
	return os.NewFile(uintptr(fd), "/dev/ptmx"), os.NewFile(uintptr(sfd), name), nil
}

type ptyShell struct {
	master *os.File
	cmd    *exec.Cmd
	done   chan struct{}
	code   int
	once   sync.Once
}

func startShell(cols, rows int) (shellSession, error) {
	sh := shellPath()
	if sh == "" {
		return nil, errors.New("no shell found")
	}
	master, slave, err := openPTY()
	if err != nil {
		return nil, err
	}
	defer slave.Close()
	unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: uint16(cols), Row: uint16(rows)})
	cmd := exec.Command(sh)
	cmd.Args = []string{"-" + filepath.Base(sh)} // login shell
	cmd.Env = shellEnv(sh)
	cmd.Dir = "/"
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		master.Close()
		return nil, fmt.Errorf("start %s: %w", sh, err)
	}
	s := &ptyShell{master: master, cmd: cmd, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		s.code = exitCode(cmd, err)
		close(s.done)
	}()
	return s, nil
}

func shellEnv(sh string) []string {
	home, name := os.Getenv("HOME"), ""
	if u, err := user.Current(); err == nil {
		name = u.Username
		if home == "" {
			home = u.HomeDir
		}
	}
	if home == "" {
		home = "/"
	}
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	env := []string{"TERM=xterm-256color", "COLORTERM=truecolor", "HOME=" + home, "PATH=" + path, "SHELL=" + sh}
	if name != "" {
		env = append(env, "USER="+name, "LOGNAME="+name)
	}
	for _, k := range []string{"LANG", "LC_ALL", "TZ"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func exitCode(cmd *exec.Cmd, err error) int {
	st := cmd.ProcessState
	if st == nil {
		return -1
	}
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return st.ExitCode()
}

func (s *ptyShell) Read(p []byte) (int, error) {
	n, err := s.master.Read(p)
	if err != nil && errors.Is(err, syscall.EIO) {
		err = os.ErrClosed // every slave end is closed
	}
	return n, err
}

func (s *ptyShell) Write(p []byte) (int, error) { return s.master.Write(p) }

func (s *ptyShell) Resize(cols, rows int) error {
	return unix.IoctlSetWinsize(int(s.master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: uint16(cols), Row: uint16(rows)})
}

func (s *ptyShell) Done() <-chan struct{} { return s.done }

func (s *ptyShell) Code() int {
	<-s.done
	return s.code
}

// Kill hangs the session up, kills what is left of it (job control puts
// jobs in their own process groups, so the whole session is swept) and
// reaps those that became our children (we may be PID 1 in a container).
func (s *ptyShell) Kill() {
	s.once.Do(func() {
		sid := s.cmd.Process.Pid
		s.master.Close()
		signalSession(sid, syscall.SIGHUP)
		select {
		case <-s.done:
		case <-time.After(time.Second):
		}
		signalSession(sid, syscall.SIGKILL)
		select {
		case <-s.done:
		case <-time.After(2 * time.Second):
		}
		go reapSession(sid)
	})
}

// sessionPids lists the processes of session sid; with parent > 0 only
// those whose parent it is.
func sessionPids(sid, parent int) []int {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// pid (comm) state ppid pgrp session ...
		i := strings.LastIndexByte(string(b), ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(b[i+1:]))
		if len(f) < 4 {
			continue
		}
		ppid, _ := strconv.Atoi(f[1])
		ss, _ := strconv.Atoi(f[3])
		if ss == sid && (parent <= 0 || ppid == parent) {
			out = append(out, pid)
		}
	}
	return out
}

func signalSession(sid int, sig syscall.Signal) {
	syscall.Kill(-sid, sig)
	for _, pid := range sessionPids(sid, 0) {
		syscall.Kill(pid, sig)
	}
}

// reapSession waits for the session's processes that were re-parented to
// this process (nothing else here waits for them).
func reapSession(sid int) {
	self := os.Getpid()
	for i := 0; i < 30; i++ {
		pids := sessionPids(sid, self)
		if len(pids) == 0 {
			return
		}
		for _, pid := range pids {
			if pid == sid {
				continue // the shell: cmd.Wait has it
			}
			var ws syscall.WaitStatus
			syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
