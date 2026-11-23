package main

// "hy2scale ctl": the API of the running node, and of the nodes it manages,
// over its local control socket (internal/api/ctl.go).

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FrankoonG/hy2scale/internal/api"
)

const ctlUsage = `Usage: hy2scale ctl [options] <command> [arguments]

Commands:
  nodes [--probe]              this node and the nodes it manages
                               (--probe: how each can be signed in to)
  get PATH                     GET an API path, e.g. /api/diag/runtime
  call METHOD PATH [BODY]      any API request; BODY is JSON text,
                               @file or - (standard input)
  mesh [METHOD] PATH [BODY]    the same request to every managed node
                               (--nodes a,b  --no-self  --timeout 30s)
  runtime                      get /api/diag/runtime
  logs [N]                     the last N log lines (default 200)
  login --user U               save a sign-in to --node, password from
        [--password-hash H]    HY2SCALE_PASSWORD or standard input
  logins                       the saved sign-ins
  forget NODE                  remove a saved sign-in
  reset-login                  web sign-in back to admin/admin (this node)

Options:
  -n, --node CHAIN   act on another node: a name, or a chain A/B/C
  --sock PATH        control socket (default: <data>/ctl/hy2scale.sock)
  --data DIR         the node's data directory (default /data, ./hy2scale)
  --raw              print answers as they come (no JSON formatting)
  --timeout D        give up after D (default 2m; mesh: per node 30s)

Other nodes are signed in to with relay trust, or with a sign-in saved
when that node was signed in to from this node's web UI (or by "login").
`

type ctlOpts struct {
	node, sock, data string
	raw, probe       bool
	noSelf           bool
	nodes            string
	timeout          time.Duration
	user, hash, pass string
	args             []string
}

func parseCtl(argv []string) (*ctlOpts, error) {
	o := &ctlOpts{}
	takes := map[string]*string{
		"-n": &o.node, "--node": &o.node, "--sock": &o.sock, "--data": &o.data,
		"--nodes": &o.nodes, "--user": &o.user, "--password-hash": &o.hash, "--password": &o.pass,
	}
	bools := map[string]*bool{"--raw": &o.raw, "--probe": &o.probe, "--no-self": &o.noSelf}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			o.args = append(o.args, argv[i+1:]...)
			break
		}
		name, val, hasVal := strings.Cut(a, "=")
		if p, ok := takes[name]; ok {
			if !hasVal {
				if i+1 >= len(argv) {
					return nil, fmt.Errorf("%s needs a value", name)
				}
				i++
				val = argv[i]
			}
			*p = val
			continue
		}
		if p, ok := bools[a]; ok {
			*p = true
			continue
		}
		if name == "--timeout" {
			if !hasVal {
				if i+1 >= len(argv) {
					return nil, fmt.Errorf("--timeout needs a value")
				}
				i++
				val = argv[i]
			}
			d, err := time.ParseDuration(val)
			if err != nil {
				return nil, fmt.Errorf("--timeout: %v", err)
			}
			o.timeout = d
			continue
		}
		if a == "-h" || a == "--help" {
			return nil, nil
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			return nil, fmt.Errorf("unknown option %s", a)
		}
		o.args = append(o.args, a)
	}
	return o, nil
}

// ctlSocket: the socket to use.
func (o *ctlOpts) socket() string {
	if o.sock != "" {
		return o.sock
	}
	if v := os.Getenv("HY2SCALE_CTL_SOCK"); v != "" {
		return v
	}
	if o.data != "" {
		return api.CtlSocket(o.data)
	}
	wd, _ := os.Getwd()
	for _, d := range []string{"/data", filepath.Join(wd, "hy2scale")} {
		if p := api.CtlSocket(d); exists(p) {
			return p
		}
	}
	return api.CtlSocket("/data")
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func runCtl(argv []string) int {
	o, err := parseCtl(argv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hy2scale ctl: %v\n\n%s", err, ctlUsage)
		return 2
	}
	if o == nil || len(o.args) == 0 {
		fmt.Fprint(os.Stderr, ctlUsage)
		if o == nil {
			return 0
		}
		return 2
	}
	sock := o.socket()
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	timeout := o.timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
	defer cancel()

	do := func(method, path string, q url.Values, body []byte) (*http.Response, error) {
		u := "http://ctl" + path
		if len(q) > 0 {
			u += "?" + q.Encode()
		}
		req, _ := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
		if len(body) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("control socket %s: %v (is the node running? see --data / --sock)", sock, err)
		}
		return resp, nil
	}
	call := func(method, path string, body []byte) int {
		q := url.Values{"method": {method}, "path": {path}}
		if o.node != "" {
			q.Set("node", o.node)
		}
		resp, err := do("POST", "/ctl/v1/call", q, body)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		return o.output(resp)
	}

	cmd, args := o.args[0], o.args[1:]
	switch cmd {
	case "nodes":
		q := url.Values{}
		if o.probe {
			q.Set("probe", "1")
		}
		resp, err := do("GET", "/ctl/v1/nodes", q, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		return o.output(resp)
	case "get":
		if len(args) != 1 {
			return usageErr("get PATH")
		}
		return call("GET", apiPath(args[0]), nil)
	case "runtime":
		return call("GET", "/api/diag/runtime", nil)
	case "logs":
		n := "200"
		if len(args) > 0 {
			n = args[0]
		}
		return call("GET", "/api/logs?limit="+url.QueryEscape(n), nil)
	case "call":
		if len(args) < 2 || len(args) > 3 {
			return usageErr("call METHOD PATH [BODY]")
		}
		body, err := readBody(args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return call(strings.ToUpper(args[0]), apiPath(args[1]), body)
	case "mesh":
		method := "GET"
		if len(args) > 0 && !strings.HasPrefix(args[0], "/") && !strings.HasPrefix(args[0], "api/") {
			method, args = strings.ToUpper(args[0]), args[1:]
		}
		if len(args) < 1 || len(args) > 2 {
			return usageErr("mesh [METHOD] PATH [BODY]")
		}
		body, err := readBody(args[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		q := url.Values{"method": {method}, "path": {apiPath(args[0])}}
		if o.nodes != "" {
			q.Set("nodes", o.nodes)
		}
		if o.noSelf {
			q.Set("self", "0")
		}
		if o.timeout > 0 {
			q.Set("timeout", o.timeout.String())
		}
		resp, err := do("POST", "/ctl/v1/mesh", q, body)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		return o.output(resp)
	case "login":
		if o.node == "" || o.user == "" {
			return usageErr("login --node CHAIN --user U [--password-hash H]")
		}
		hash := o.hash
		if hash == "" {
			pw := o.pass
			if pw == "" {
				pw = os.Getenv("HY2SCALE_PASSWORD")
			}
			if pw == "" || pw == "-" {
				fmt.Fprint(os.Stderr, "password: ")
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				pw = strings.TrimRight(line, "\r\n")
			}
			if pw == "" {
				fmt.Fprintln(os.Stderr, "no password")
				return 2
			}
			h := sha256.Sum256([]byte(pw))
			hash = hex.EncodeToString(h[:])
		}
		b, _ := json.Marshal(map[string]string{"node": o.node, "username": o.user, "password_hash": hash})
		resp, err := do("POST", "/ctl/v1/logins", nil, b)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		return o.output(resp)
	case "reset-login":
		resp, err := do("POST", "/ctl/v1/reset-login", nil, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		return o.output(resp)
	case "logins":
		resp, err := do("GET", "/ctl/v1/logins", nil, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		return o.output(resp)
	case "forget":
		node := o.node
		if len(args) == 1 {
			node = args[0]
		}
		if node == "" {
			return usageErr("forget NODE")
		}
		resp, err := do("DELETE", "/ctl/v1/logins", url.Values{"node": {node}}, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		return o.output(resp)
	}
	fmt.Fprintf(os.Stderr, "hy2scale ctl: unknown command %q\n\n%s", cmd, ctlUsage)
	return 2
}

func usageErr(s string) int {
	fmt.Fprintf(os.Stderr, "usage: hy2scale ctl %s\n", s)
	return 2
}

// apiPath accepts "/api/x", "api/x" and "/scale/api/x".
func apiPath(p string) string {
	p = "/" + strings.TrimPrefix(p, "/")
	p = strings.TrimPrefix(p, "/scale")
	if !strings.HasPrefix(p, "/api/") {
		p = "/api" + p
	}
	return p
}

func readBody(args []string) ([]byte, error) {
	if len(args) == 0 {
		return nil, nil
	}
	a := args[0]
	switch {
	case a == "-":
		return io.ReadAll(os.Stdin)
	case strings.HasPrefix(a, "@"):
		return os.ReadFile(a[1:])
	}
	return []byte(a), nil
}

// output prints an answer: JSON formatted (unless --raw), anything else
// as it is. Exit code 0 for 2xx, 1 otherwise (3: not reached).
func (o *ctlOpts) output(resp *http.Response) int {
	defer resp.Body.Close()
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	out := os.Stdout
	if !ok {
		out = os.Stderr
	}
	ct := resp.Header.Get("Content-Type")
	if o.raw || !strings.Contains(ct, "json") {
		io.Copy(out, resp.Body)
	} else {
		b, _ := io.ReadAll(resp.Body)
		var buf bytes.Buffer
		if json.Indent(&buf, b, "", "  ") == nil {
			buf.WriteByte('\n')
			out.Write(buf.Bytes())
		} else {
			out.Write(b)
		}
	}
	if ok {
		return 0
	}
	msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
	switch resp.Header.Get("X-Hy2scale-Ctl-Error") {
	case "needs_login":
		chain := resp.Header.Get("X-Hy2scale-Chain")
		msg += fmt.Sprintf(" — %s does not trust this node's relay and no sign-in is saved: sign in to it once from this node's web UI, or run: hy2scale ctl login --node %s --user <name>", chain, chain)
	case "unreachable":
		fmt.Fprintf(os.Stderr, "\nnot reached\n")
		return 3
	}
	fmt.Fprintln(os.Stderr, "\n"+msg)
	return 1
}
