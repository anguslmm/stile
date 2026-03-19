package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/proxy"
	"github.com/anguslmm/stile/internal/server"
)

func main() {
	configPath := flag.String("config", "configs/stile.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	handler, err := proxy.NewHandler(cfg)
	if err != nil {
		log.Fatalf("create proxy handler: %v", err)
	}
	defer handler.Close()

	srv := server.New(cfg, handler)

	// Graceful shutdown on SIGINT/SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}()

	log.Printf("stile listening on %s", cfg.Server().Address())
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("server stopped: %v", err)
	}
}
