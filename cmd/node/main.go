package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/FrankoonG/hy2scale/internal/api"
	"github.com/FrankoonG/hy2scale/internal/app"
)

func main() {
	dataDir := flag.String("data", "", "configuration and data directory")
	apiAddr := flag.String("api", "", "API/UI listen address (overrides config)")
	basePath := flag.String("base-path", "", "UI base path (e.g. /scale)")
	showVersion := flag.Bool("version", false, "print version and exit")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options]\n\nOptions:\n", filepath.Base(os.Args[0]))
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nIf --data is not provided, uses ./hy2scale as the data directory.\n")
	}
	flag.Parse()

	if *showVersion {
		fmt.Printf("hy2scale %s\n", api.Version)
		return
	}

	// Resolve data directory: explicit flag > ./hy2scale
	dir := *dataDir
	if dir == "" {
		dir = "hy2scale"
	}
	// Ensure absolute path
	if !filepath.IsAbs(dir) {
		wd, _ := os.Getwd()
		dir = filepath.Join(wd, dir)
	}
	// Create if not exists
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create data directory %s: %v\n", dir, err)
		os.Exit(1)
	}

	// Opt-in profiling listener for leak investigations. Off unless
	// HY2SCALE_PPROF is set (e.g. "127.0.0.1:6060"); never on the UI port.
	if addr := os.Getenv("HY2SCALE_PPROF"); addr != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		go func() {
			log.Printf("[pprof] listening on %s", addr)
			if err := http.ListenAndServe(addr, mux); err != nil {
				log.Printf("[pprof] %v", err)
			}
		}()
	}

	a, err := app.New(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sigCh; cancel() }()

	cfg := a.Store().Get()
	listenAddr := "0.0.0.0:5565"
	if cfg.UIListen != "" {
		listenAddr = cfg.UIListen
	}
	if *apiAddr != "" {
		listenAddr = *apiAddr
	}

	bp := "/scale"
	if cfg.UIBasePath != "" {
		bp = cfg.UIBasePath
	}
	if *basePath != "" {
		bp = *basePath
	}

	srv := api.NewServer(a, listenAddr, bp)
	// Restart from the web UI: stop like SIGTERM, then come back.
	var restart atomic.Bool
	srv.SetRestart(func() {
		restart.Store(true)
		cancel()
	})

	// Start API/UI server — fatal if port conflict
	apiReady := make(chan error, 1)
	go func() {
		err := srv.Start(ctx)
		if err != nil && ctx.Err() == nil {
			apiReady <- err
		}
	}()
	// Give the API server a moment to bind
	go func() {
		select {
		case err := <-apiReady:
			fmt.Fprintf(os.Stderr, "FATAL: API/UI server failed to start on %s: %v\n", listenAddr, err)
			fmt.Fprintf(os.Stderr, "Another process may be using port %s. Exiting.\n", listenAddr)
			os.Exit(1)
		case <-ctx.Done():
		}
	}()

	go srv.StartSubPeersUpdater(ctx)

	if err := a.Run(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	a.WaitPersisted(3 * time.Second)
	if restart.Load() {
		restartProcess()
	}
}

// restartExitCode: non-zero so "on-failure" restart policies restart too.
const restartExitCode = 75

// restartProcess brings the node back after a restart requested from the web
// UI. Under a supervisor (container restart policy, systemd Restart=) exiting
// is the clean way: it also ends child daemons (strongSwan, xl2tpd) and
// resets kernel state. A bare process re-executes itself.
func restartProcess() {
	if supervised() {
		log.Printf("restart: exiting (%d) for the supervisor to start a fresh process", restartExitCode)
		os.Exit(restartExitCode)
	}
	exe, err := os.Executable()
	if err == nil {
		log.Printf("restart: re-executing %s", exe)
		err = syscall.Exec(exe, os.Args, os.Environ())
	}
	fmt.Fprintf(os.Stderr, "restart failed: %v\n", err)
	os.Exit(1)
}

func supervised() bool {
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return os.Getenv("INVOCATION_ID") != "" // systemd service
}
