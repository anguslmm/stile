package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anguslmm/stile/internal/auth"
	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/policy"
	"github.com/anguslmm/stile/internal/proxy"
	"github.com/anguslmm/stile/internal/router"
	"github.com/anguslmm/stile/internal/server"
	"github.com/anguslmm/stile/internal/transport"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "add-caller":
			runAddCaller(os.Args[2:])
			return
		case "add-key":
			runAddKey(os.Args[2:])
			return
		case "assign-role":
			runAssignRole(os.Args[2:])
			return
		case "unassign-role":
			runUnassignRole(os.Args[2:])
			return
		case "list-callers":
			runListCallers(os.Args[2:])
			return
		case "remove-caller":
			runRemoveCaller(os.Args[2:])
			return
		case "revoke-key":
			runRevokeKey(os.Args[2:])
			return
		}
	}

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

	opts := buildAuthOpts(cfg)

	rateLimiter := policy.NewRateLimiter(cfg)
	handler := proxy.NewHandler(rt, rateLimiter)
	srv := server.New(cfg, handler, rt, opts)

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

func buildAuthOpts(cfg *config.Config) *server.Options {
	dbPath := cfg.Server().DBPath()
	if dbPath == "" {
		return nil
	}

	store, err := auth.NewSQLiteStore(dbPath)
	if err != nil {
		log.Fatalf("open caller database: %v", err)
	}

	authenticator := auth.NewAuthenticator(store, cfg.Roles())

	opts := &server.Options{
		Authenticator: authenticator,
	}

	// Admin auth: read ADMIN_API_KEY from env.
	adminKey := os.Getenv("ADMIN_API_KEY")
	if adminKey != "" {
		adminHash := sha256.Sum256([]byte(adminKey))
		opts.AdminAuth = auth.AdminAuthMiddleware(adminHash, store)
	} else {
		var zeroHash [32]byte
		opts.AdminAuth = auth.AdminAuthMiddleware(zeroHash, store)
	}

	return opts
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
