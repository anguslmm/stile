// Package metrics registers and exposes Prometheus metrics for the Stile gateway.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds all Prometheus metric collectors for the gateway.
type Metrics struct {
	RequestsTotal       *prometheus.CounterVec
	RequestDuration     *prometheus.HistogramVec
	UpstreamHealth      *prometheus.GaugeVec
	RateLimitRejections *prometheus.CounterVec
	ToolCacheRefresh    *prometheus.CounterVec
}

// New creates and registers all Prometheus metrics with the default registerer.
func New() *Metrics {
	m := &Metrics{
		RequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "stile_requests_total",
				Help: "Total requests processed",
			},
			[]string{"caller", "tool", "upstream", "status"},
		),
		RequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "stile_request_duration_seconds",
				Help:    "Request latency in seconds",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"caller", "tool", "upstream"},
		),
		UpstreamHealth: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "stile_upstream_health",
				Help: "Upstream health status (1 = healthy, 0 = unhealthy)",
			},
			[]string{"upstream"},
		),
		RateLimitRejections: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "stile_rate_limit_rejections_total",
				Help: "Rate limit rejections",
			},
			[]string{"caller", "tool"},
		),
		ToolCacheRefresh: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "stile_tool_cache_refresh_total",
				Help: "Tool cache refresh attempts",
			},
			[]string{"upstream", "status"},
		),
	}

	prometheus.MustRegister(
		m.RequestsTotal,
		m.RequestDuration,
		m.UpstreamHealth,
		m.RateLimitRejections,
		m.ToolCacheRefresh,
	)

	return m
}

// NewForRegistry creates and registers metrics with a custom registry.
// Useful for testing to avoid global state conflicts.
func NewForRegistry(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		RequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "stile_requests_total",
				Help: "Total requests processed",
			},
			[]string{"caller", "tool", "upstream", "status"},
		),
		RequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "stile_request_duration_seconds",
				Help:    "Request latency in seconds",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"caller", "tool", "upstream"},
		),
		UpstreamHealth: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "stile_upstream_health",
				Help: "Upstream health status (1 = healthy, 0 = unhealthy)",
			},
			[]string{"upstream"},
		),
		RateLimitRejections: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "stile_rate_limit_rejections_total",
				Help: "Rate limit rejections",
			},
			[]string{"caller", "tool"},
		),
		ToolCacheRefresh: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "stile_tool_cache_refresh_total",
				Help: "Tool cache refresh attempts",
			},
			[]string{"upstream", "status"},
		),
	}

	reg.MustRegister(
		m.RequestsTotal,
		m.RequestDuration,
		m.UpstreamHealth,
		m.RateLimitRejections,
		m.ToolCacheRefresh,
	)

	return m
}
