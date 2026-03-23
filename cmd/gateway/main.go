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

	"github.com/redis/go-redis/v9"

	"github.com/anguslmm/stile/internal/admin"
	"github.com/anguslmm/stile/internal/audit"
	"github.com/anguslmm/stile/internal/auth"
	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/health"
	"github.com/anguslmm/stile/internal/logging"
	"github.com/anguslmm/stile/internal/metrics"
	"github.com/anguslmm/stile/internal/policy"
	"github.com/anguslmm/stile/internal/proxy"
	"github.com/anguslmm/stile/internal/resilience"
	"github.com/anguslmm/stile/internal/router"
	"github.com/anguslmm/stile/internal/server"
	"github.com/anguslmm/stile/internal/telemetry"
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
		case "health-agent":
			runHealthAgent(os.Args[2:])
			return
		case "wrap":
			runWrap(os.Args[2:])
			return
		case "cache-show":
			runCacheShow(os.Args[2:])
			return
		case "cache-flush":
			runCacheFlush(os.Args[2:])
			return
		}
	}

	configPath := flag.String("config", "configs/stile.yaml", "path to config file")
	devMode := flag.Bool("dev", false, "enable dev mode (open admin API without ADMIN_API_KEY)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("load config failed", "error", err)
		os.Exit(1)
	}

	// Initialize telemetry (tracer provider).
	tp, err := telemetry.Init(context.Background(), cfg.Telemetry())
	if err != nil {
		slog.Error("init telemetry failed", "error", err)
		os.Exit(1)
	}

	setupLogger(cfg)

	slog.Info("config loaded",
		"upstreams", len(cfg.Upstreams()),
		"roles", len(cfg.Roles()),
		"tracing", cfg.Telemetry().Traces().Enabled(),
	)

	m := metrics.New()

	transports, err := buildTransports(cfg, m)
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

	opts, callerStore := buildAuthOpts(cfg, *devMode)

	var auditStore audit.Store
	if cfg.Audit().Enabled() {
		auditStore, err = audit.OpenStore(cfg.Audit().DatabaseConfig())
		if err != nil {
			slog.Error("open audit database failed", "error", err)
			os.Exit(1)
		}
		slog.Info("audit logging enabled", "database", cfg.Audit().Database())
	}

	rateLimiter, err := policy.NewRateLimiterFromConfig(cfg)
	if err != nil {
		slog.Error("create rate limiter failed", "error", err)
		os.Exit(1)
	}
	handler := proxy.NewHandler(rt, rateLimiter, m, auditStore, proxy.WithTracer(tp.Tracer()))

	// Build health checker from router upstreams.
	healthChecker, healthRedisClient := buildHealthChecker(cfg, rt, m)
	healthChecker.Start()

	if opts == nil {
		opts = &server.Options{}
	}
	opts.HealthChecker = healthChecker
	opts.Tracer = tp.Tracer()

	// Create admin handler if auth is configured (store is available).
	if callerStore != nil {
		adminKey := os.Getenv("ADMIN_API_KEY")
		var adminKeyHash [32]byte
		if adminKey != "" {
			adminKeyHash = sha256.Sum256([]byte(adminKey))
		}

		adminOpts := []admin.Option{
			admin.WithHealthChecker(healthChecker),
			admin.WithConfig(cfg),
			admin.WithStartTime(time.Now()),
			admin.WithAdminKeyHash(adminKeyHash),
		}
		if auditStore != nil {
			if reader, ok := auditStore.(audit.Reader); ok {
				adminOpts = append(adminOpts, admin.WithAuditReader(reader))
			}
		}
		adminHandler := admin.NewHandler(callerStore, rt, adminOpts...)
		opts.AdminHandler = adminHandler

		// Rebuild admin auth middleware with session check for browser UI.
		if opts.AdminAuth != nil {
			opts.AdminAuth = auth.AdminAuthMiddleware(adminKeyHash, *devMode,
				auth.WithSessionCheck(adminHandler.SessionCheck))
		}
	}

	srv := server.New(cfg, handler, rt, m, opts)

	// Signal handling: SIGINT/SIGTERM for graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		for range sigCh {
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

			// 4. Flush and shutdown tracer provider.
			if err := tp.Shutdown(ctx); err != nil {
				slog.Error("tracer shutdown error", "error", err)
			}

			// 5. Close rate limiter (no-op for local, closes Redis connection for redis).
			policy.CloseRateLimiter(rateLimiter)

			// 6. Close health Redis client if applicable.
			if healthRedisClient != nil {
				healthRedisClient.Close()
			}

			// 7. Close audit log.
			if auditStore != nil {
				auditStore.Close()
			}

			slog.Info("shutdown complete")
			os.Exit(0)
		}
	}()

	slog.Info("stile listening", "address", cfg.Server().Address(), "tls", srv.TLSEnabled())
	if err := srv.ListenAndServe(); err != nil {
		slog.Info("server stopped", "error", err)
	}
}

// buildTransports creates transports for all upstreams in cfg,
// wrapping each with resilience (circuit breaker, retries) if configured.
func buildTransports(cfg *config.Config, m *metrics.Metrics) (map[string]transport.Transport, error) {
	transports := make(map[string]transport.Transport)
	for _, ucfg := range cfg.Upstreams() {
		t, err := transport.NewFromConfig(ucfg)
		if err != nil {
			slog.Warn("skip upstream", "upstream", ucfg.Name(), "error", err)
			continue
		}
		transports[ucfg.Name()] = resilience.Wrap(t, ucfg, m)
	}
	return transports, nil
}

func buildHealthChecker(cfg *config.Config, rt *router.RouteTable, m *metrics.Metrics) (*health.Checker, *redis.Client) {
	healthCfg := cfg.Health()
	upstreamInfos := buildUpstreamInfos(rt, cfg.Upstreams())

	if healthCfg.Store() == "redis" {
		redisCfg := healthCfg.Redis()
		client := redis.NewClient(&redis.Options{
			Addr:     redisCfg.Address(),
			Password: redisCfg.Password(),
			DB:       redisCfg.DB(),
		})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Ping(ctx).Err(); err != nil {
			slog.Error("health store redis connection failed", "error", err)
			os.Exit(1)
		}
		store := health.NewRedisStore(client, redisCfg.KeyPrefix())
		missingHealthy := healthCfg.MissingStatus() != "unhealthy"
		checker := health.NewChecker(upstreamInfos, m,
			health.WithStore(store),
			health.WithReadFromStore(true),
			health.WithMissingStatus(missingHealthy),
			health.WithCheckInterval(healthCfg.CheckInterval()),
		)
		var local, remote int
		for _, u := range upstreamInfos {
			if u.Local {
				local++
			} else {
				remote++
			}
		}
		slog.Info("health checking configured",
			"remote_upstreams", remote,
			"remote_mode", "redis",
			"local_upstreams", local,
			"local_mode", "in-process (stdio)",
			"redis", redisCfg.Address(),
			"missing_status", healthCfg.MissingStatus(),
			"check_interval", healthCfg.CheckInterval().String(),
		)
		return checker, client
	}

	checker := health.NewChecker(upstreamInfos, m,
		health.WithCheckInterval(healthCfg.CheckInterval()),
	)
	slog.Info("health checking: local (in-process)",
		"upstreams", len(upstreamInfos),
		"check_interval", healthCfg.CheckInterval().String(),
	)
	return checker, nil
}

func buildUpstreamInfos(rt *router.RouteTable, upstreamCfgs []config.UpstreamConfig) []health.UpstreamInfo {
	stdioUpstreams := make(map[string]bool)
	for _, ucfg := range upstreamCfgs {
		if _, ok := ucfg.(*config.StdioUpstreamConfig); ok {
			stdioUpstreams[ucfg.Name()] = true
		}
	}

	upstreamDetails := rt.UpstreamDetails()
	upstreamInfos := make([]health.UpstreamInfo, len(upstreamDetails))
	for i, u := range upstreamDetails {
		upstreamInfos[i] = health.UpstreamInfo{
			Name:      u.Name,
			Transport: u.Transport,
			Tools:     func() int { return len(u.Tools) },
			Stale:     func() bool { return u.Stale },
			Local:     stdioUpstreams[u.Name],
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
	handler = logging.NewTraceHandler(handler)
	slog.SetDefault(slog.New(handler))
}

func buildAuthOpts(cfg *config.Config, devMode bool) (*server.Options, auth.Store) {
	dbCfg := cfg.Server().Database()
	if dbCfg.Driver() == "" && dbCfg.DSN() == "" {
		return nil, nil
	}

	innerStore, err := auth.OpenStore(dbCfg)
	if err != nil {
		slog.Error("open caller database failed", "error", err)
		os.Exit(1)
	}

	// Wrap with in-memory cache if configured.
	store := auth.NewCachedStore(innerStore, cfg.Server().AuthCacheTTL())

	// Start Postgres LISTEN/NOTIFY listener for cross-instance cache invalidation.
	if cached, ok := store.(*auth.CachedStore); ok && dbCfg.Driver() == "postgres" {
		if pgStore, ok := innerStore.(*auth.PostgresStore); ok {
			listener, err := auth.NewPGNotifyListener(dbCfg.DSN(), pgStore.DB(), cached)
			if err != nil {
				slog.Warn("pg_notify listener failed to start, continuing without cross-instance invalidation", "error", err)
			} else {
				cached.SetNotify(listener.NotifyFunc())
				slog.Info("pg_notify listener started for cross-instance cache invalidation")
			}
		}
	}

	if ttl := cfg.Server().AuthCacheTTL(); ttl > 0 {
		slog.Info("auth cache enabled", "ttl", ttl)
	}

	authenticator := auth.NewAuthenticator(store, cfg.Roles())

	opts := &server.Options{
		Authenticator: authenticator,
	}

	// Admin auth: read ADMIN_API_KEY from env.
	adminKey := os.Getenv("ADMIN_API_KEY")
	if adminKey != "" {
		adminHash := sha256.Sum256([]byte(adminKey))
		opts.AdminAuth = auth.AdminAuthMiddleware(adminHash, devMode)
	} else {
		if !devMode {
			fmt.Fprintf(os.Stderr, "error: ADMIN_API_KEY not set and --dev not specified; refusing to start with open admin endpoints\n")
			os.Exit(1)
		}
		slog.Warn("running in dev mode — admin endpoints are open without authentication")
		var zeroHash [32]byte
		opts.AdminAuth = auth.AdminAuthMiddleware(zeroHash, devMode)
	}

	return opts, store
}
