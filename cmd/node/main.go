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
	go func() {
		if err := srv.Start(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "api: %v\n", err)
		}
	}()

	if err := a.Run(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
