package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/proxy"
	"github.com/anguslmm/stile/internal/router"
	"github.com/anguslmm/stile/internal/server"
	"github.com/anguslmm/stile/internal/transport"
)

func main() {
	configPath := flag.String("config", "configs/stile.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	transports, err := buildTransports(cfg)
	if err != nil {
		log.Fatalf("create transports: %v", err)
	}

	rt, err := router.New(transports, cfg.Upstreams())
	if err != nil {
		log.Fatalf("create router: %v", err)
	}

	if ttl := cfg.Server().ToolCacheTTL(); ttl > 0 {
		rt.StartBackgroundRefresh(ttl)
	}
	defer rt.Close()

	handler := proxy.NewHandler(rt)
	srv := server.New(cfg, handler, rt)

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

func buildTransports(cfg *config.Config) (map[string]transport.Transport, error) {
	transports := make(map[string]transport.Transport)
	for _, ucfg := range cfg.Upstreams() {
		var t transport.Transport
		var err error
		switch ucfg.Transport() {
		case "streamable-http":
			t, err = transport.NewHTTPTransport(ucfg)
		case "stdio":
			t, err = transport.NewStdioTransport(ucfg)
		default:
			err = fmt.Errorf("unsupported transport type %q", ucfg.Transport())
		}
		if err != nil {
			log.Printf("skip upstream %q: %v", ucfg.Name(), err)
			continue
		}
		transports[ucfg.Name()] = t
	}
	return transports, nil
}
