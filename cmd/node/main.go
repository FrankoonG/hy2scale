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
	cfgPath := flag.String("config", "/etc/hy2scale/config.yaml", "config file")
	apiAddr := flag.String("api", ":8080", "API/UI listen address")
	dataDir := flag.String("data", "/data", "persistent data directory")
	flag.Parse()

	a, err := app.New(*cfgPath, *dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sigCh; cancel() }()

	srv := api.NewServer(a, *apiAddr)
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
