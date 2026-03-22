package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/health"
	"github.com/anguslmm/stile/internal/metrics"
	"github.com/anguslmm/stile/internal/transport"
)

func runHealthAgent(args []string) {
	fs := flag.NewFlagSet("health-agent", flag.ExitOnError)
	configPath := fs.String("config", "configs/stile.yaml", "path to config file")
	listenAddr := fs.String("listen", ":8081", "liveness endpoint listen address")
	fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("load config failed", "error", err)
		os.Exit(1)
	}

	setupLogger(cfg)

	healthCfg := cfg.Health()

	// Resolve Redis config: health-specific, or fall back to rate_limits.
	redisCfg := healthCfg.Redis()
	if redisCfg == nil {
		redisCfg = cfg.RedisConfig()
	}
	if redisCfg == nil {
		slog.Error("health agent requires redis configuration (health.redis or rate_limits.redis)")
		os.Exit(1)
	}

	client := redis.NewClient(&redis.Options{
		Addr:     redisCfg.Address(),
		Password: redisCfg.Password(),
		DB:       redisCfg.DB(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := client.Ping(ctx).Err(); err != nil {
		cancel()
		slog.Error("redis connection failed", "error", err)
		os.Exit(1)
	}
	cancel()

	store := health.NewRedisStore(client, redisCfg.KeyPrefix())

	// Build transports for remote (HTTP) upstreams only. Stdio upstreams are
	// process-local subprocesses owned by each gateway instance — an external
	// health agent cannot meaningfully check them.
	m := metrics.New()
	transports := make(map[string]transport.Transport)
	for _, ucfg := range cfg.Upstreams() {
		if _, ok := ucfg.(*config.StdioUpstreamConfig); ok {
			slog.Info("skip stdio upstream (checked locally by gateway)", "upstream", ucfg.Name())
			continue
		}
		t, err := transport.NewFromConfig(ucfg)
		if err != nil {
			slog.Warn("skip upstream", "upstream", ucfg.Name(), "error", err)
			continue
		}
		transports[ucfg.Name()] = t
	}

	var upstreamInfos []health.UpstreamInfo
	for name, t := range transports {
		upstreamInfos = append(upstreamInfos, health.UpstreamInfo{
			Name:      name,
			Transport: t,
			Tools:     func() int { return 0 },
			Stale:     func() bool { return false },
		})
	}

	interval := healthCfg.CheckInterval()
	checker := health.NewChecker(upstreamInfos, m,
		health.WithStore(store),
		health.WithCheckInterval(interval),
		health.WithStoreTTL(2*interval),
		health.WithActiveProbe(true),
	)
	checker.Start()

	slog.Info("health agent started",
		"upstreams", len(upstreamInfos),
		"check_interval", interval.String(),
		"store_ttl", (2 * interval).String(),
		"redis", redisCfg.Address(),
		"listen", *listenAddr,
	)

	// Serve a minimal /healthz endpoint for the agent's own liveness.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	srv := &http.Server{Addr: *listenAddr, Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("health agent http server error", "error", err)
		}
	}()

	// Wait for signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	slog.Info("shutting down health agent...")
	checker.Stop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	srv.Shutdown(shutdownCtx)

	// Close transports.
	for _, t := range transports {
		t.Close()
	}

	client.Close()
	slog.Info("health agent stopped")
}
