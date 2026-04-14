package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

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
}
