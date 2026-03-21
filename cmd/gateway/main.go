package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/anguslmm/stile/internal/admin"
	"github.com/anguslmm/stile/internal/audit"
	"github.com/anguslmm/stile/internal/auth"
	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/health"
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

	transports, err := buildTransports(cfg, nil)
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

	opts, callerStore := buildAuthOpts(cfg)

	var auditStore audit.Store
	if cfg.Audit().Enabled() {
		auditStore, err = audit.NewSQLiteStore(cfg.Audit().Database())
		if err != nil {
			slog.Error("open audit database failed", "error", err)
			os.Exit(1)
		}
		slog.Info("audit logging enabled", "database", cfg.Audit().Database())
	}

	rateLimiter := policy.NewRateLimiter(cfg)
	handler := proxy.NewHandler(rt, rateLimiter, m, auditStore)

	// Build health checker from router upstreams.
	healthChecker := buildHealthChecker(rt, m)
	healthChecker.Start()

	// Build reload closure — captures everything it needs.
	var reloadMu sync.Mutex
	reload := func(ctx context.Context) (*server.ReloadResult, error) {
		reloadMu.Lock()
		defer reloadMu.Unlock()

		newCfg, err := config.Load(*configPath)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}

		// Diff upstreams against the router's current list (not the original config,
		// which may be stale after prior reloads).
		oldUpstreams := make(map[string]bool)
		for _, name := range rt.Upstreams() {
			oldUpstreams[name] = true
		}
		newUpstreams := make(map[string]bool)
		for _, u := range newCfg.Upstreams() {
			newUpstreams[u.Name()] = true
		}

		var added, removed []string
		for name := range newUpstreams {
			if !oldUpstreams[name] {
				added = append(added, name)
			}
		}
		for name := range oldUpstreams {
			if !newUpstreams[name] {
				removed = append(removed, name)
			}
		}

		// Create transports for added upstreams.
		filter := make(map[string]bool, len(added))
		for _, n := range added {
			filter[n] = true
		}
		newTransports, err := buildTransports(newCfg, filter)
		if err != nil {
			return nil, fmt.Errorf("create transports for new upstreams: %w", err)
		}

		// Apply changes atomically (at this point, all prep has succeeded).

		// Remove old upstreams from router.
		for _, name := range removed {
			rt.RemoveUpstream(name)
		}

		// Add new upstreams to router.
		for _, ucfg := range newCfg.Upstreams() {
			if t, ok := newTransports[ucfg.Name()]; ok {
				rt.AddUpstream(ucfg.Name(), t, ucfg)
			}
		}

		// Update rate limiter.
		handler.SetRateLimiter(policy.NewRateLimiter(newCfg))

		// Update authenticator if auth is configured.
		if opts != nil && opts.Authenticator != nil {
			dbPath := newCfg.Server().DBPath()
			if dbPath != "" {
				store, err := auth.NewSQLiteStore(dbPath)
				if err != nil {
					slog.Error("reload: failed to open caller database", "error", err)
				} else {
					newAuth := auth.NewAuthenticator(store, newCfg.Roles())
					opts.Authenticator = newAuth
				}
			}
		}

		// Update health checker with current upstreams.
		upstreamInfos := buildUpstreamInfos(rt)
		healthChecker.UpdateUpstreams(upstreamInfos)
		healthChecker.CheckNow(context.Background())

		// Update logging level.
		setupLogger(newCfg)

		slog.Info("config reload applied",
			"upstreams_added", added,
			"upstreams_removed", removed,
		)

		return &server.ReloadResult{
			Status:           "ok",
			UpstreamsAdded:   added,
			UpstreamsRemoved: removed,
		}, nil
	}

	if opts == nil {
		opts = &server.Options{}
	}
	opts.HealthChecker = healthChecker
	opts.ReloadFunc = reload

	// Create admin handler if auth is configured (store is available).
	if callerStore != nil {
		opts.AdminHandler = admin.NewHandler(callerStore, rt, reload)
	}

	srv := server.New(cfg, handler, rt, m, opts)

	// Signal handling: SIGINT/SIGTERM for shutdown, SIGHUP for reload.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	go func() {
		for sig := range sigCh {
			if sig == syscall.SIGHUP {
				slog.Info("received SIGHUP, reloading config...")
				result, err := reload(context.Background())
				if err != nil {
					slog.Error("config reload failed", "error", err)
				} else {
					slog.Info("config reloaded",
						"upstreams_added", result.UpstreamsAdded,
						"upstreams_removed", result.UpstreamsRemoved,
					)
				}
				continue
			}

			// SIGINT or SIGTERM: graceful shutdown.
			slog.Info("shutting down...")
			ctx, cancel := context.WithTimeout(context.Background(), cfg.Server().ShutdownTimeout())
			defer cancel()

			// 1. Stop accepting new connections and drain in-flight requests.
			if err := srv.Shutdown(ctx); err != nil {
				slog.Error("server shutdown error", "error", err)
			}

			// 2. Stop background goroutines (health checker).
			healthChecker.Stop()

			// 3. Close router (stops background refresh and closes transports).
			rt.Close()

			// 4. Close audit log.
			if auditStore != nil {
				auditStore.Close()
			}

			slog.Info("shutdown complete")
			os.Exit(0)
		}
	}()

	slog.Info("stile listening", "address", cfg.Server().Address())
	if err := srv.ListenAndServe(); err != nil {
		slog.Info("server stopped", "error", err)
	}
}

// buildTransports creates transports for upstreams in cfg. If filter is non-nil,
// only upstreams whose names are in the filter are built. If filter is nil, all
// upstreams are built. On the startup path (nil filter), build errors are logged
// and skipped. On the reload path (non-nil filter), any error causes cleanup and
// an error return.
func buildTransports(cfg *config.Config, filter map[string]bool) (map[string]transport.Transport, error) {
	transports := make(map[string]transport.Transport)
	for _, ucfg := range cfg.Upstreams() {
		if filter != nil && !filter[ucfg.Name()] {
			continue
		}
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
			if filter != nil {
				// Reload path: clean up and return error.
				for _, created := range transports {
					created.Close()
				}
				return nil, fmt.Errorf("upstream %q: %w", ucfg.Name(), err)
			}
			// Startup path: log and skip.
			slog.Warn("skip upstream", "upstream", ucfg.Name(), "error", err)
			continue
		}
		transports[ucfg.Name()] = t
	}
	return transports, nil
}

func buildHealthChecker(rt *router.RouteTable, m *metrics.Metrics) *health.Checker {
	return health.NewChecker(buildUpstreamInfos(rt), m)
}

func buildUpstreamInfos(rt *router.RouteTable) []health.UpstreamInfo {
	upstreamDetails := rt.UpstreamDetails()
	upstreamInfos := make([]health.UpstreamInfo, len(upstreamDetails))
	for i, u := range upstreamDetails {
		upstreamInfos[i] = health.UpstreamInfo{
			Name:      u.Name,
			Transport: u.Transport,
			Tools:     func() int { return len(u.Tools) },
			Stale:     func() bool { return u.Stale },
		}
	}
	return upstreamInfos
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

func buildAuthOpts(cfg *config.Config) (*server.Options, *auth.SQLiteStore) {
	dbPath := cfg.Server().DBPath()
	if dbPath == "" {
		return nil, nil
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

	return opts, store
}
