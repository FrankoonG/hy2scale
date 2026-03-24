package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/FrankoonG/hy2scale/internal/api"
	"github.com/FrankoonG/hy2scale/internal/app"
)

func main() {
	apiAddr := flag.String("api", "", "API/UI listen address (default 0.0.0.0:5565)")
	basePath := flag.String("base-path", "", "UI base path (e.g. /scale)")
	dataDir := flag.String("data", "/data", "persistent data directory")
	flag.Parse()

	a, err := app.New(*dataDir)
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
