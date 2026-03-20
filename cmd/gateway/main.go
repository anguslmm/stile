package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anguslmm/stile/internal/audit"
	"github.com/anguslmm/stile/internal/auth"
	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/metrics"
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
		slog.Error("load config failed", "error", err)
		os.Exit(1)
	}

	setupLogger(cfg)

	slog.Info("config loaded",
		"upstreams", len(cfg.Upstreams()),
		"roles", len(cfg.Roles()),
	)

	m := metrics.New()

	transports, err := buildTransports(cfg)
	if err != nil {
		slog.Error("create transports failed", "error", err)
		os.Exit(1)
	}

	rt, err := router.New(transports, cfg.Upstreams(), m)
	if err != nil {
		slog.Error("create router failed", "error", err)
		os.Exit(1)
	}

	if ttl := cfg.Server().ToolCacheTTL(); ttl > 0 {
		rt.StartBackgroundRefresh(ttl)
	}
	defer rt.Close()

	opts := buildAuthOpts(cfg)

	var auditStore audit.Store
	if cfg.Audit().Enabled() {
		auditStore, err = audit.NewSQLiteStore(cfg.Audit().Database())
		if err != nil {
			slog.Error("open audit database failed", "error", err)
			os.Exit(1)
		}
		defer auditStore.Close()
		slog.Info("audit logging enabled", "database", cfg.Audit().Database())
	}

	rateLimiter := policy.NewRateLimiter(cfg)
	handler := proxy.NewHandler(rt, rateLimiter, m, auditStore)
	srv := server.New(cfg, handler, rt, m, opts)

	// Graceful shutdown on SIGINT/SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		slog.Info("shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("shutdown error", "error", err)
		}
	}()

	slog.Info("stile listening", "address", cfg.Server().Address())
	if err := srv.ListenAndServe(); err != nil {
		slog.Info("server stopped", "error", err)
	}
}

func setupLogger(cfg *config.Config) {
	var level slog.Level
	switch cfg.Logging().Level() {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.Logging().Format() == "text" {
		handler = slog.NewTextHandler(os.Stderr, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(handler))
}

func buildAuthOpts(cfg *config.Config) *server.Options {
	dbPath := cfg.Server().DBPath()
	if dbPath == "" {
		return nil
	}

	store, err := auth.NewSQLiteStore(dbPath)
	if err != nil {
		slog.Error("open caller database failed", "error", err)
		os.Exit(1)
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
			slog.Warn("skip upstream", "upstream", ucfg.Name(), "error", err)
			continue
		}
		transports[ucfg.Name()] = t
	}
	return transports, nil
}
