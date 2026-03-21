// Package metrics registers and exposes Prometheus-compatible metrics for the
// Stile gateway, using the OpenTelemetry metrics API with a Prometheus exporter.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	otelprometheus "go.opentelemetry.io/otel/exporters/prometheus"
	otelmetric "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"go.opentelemetry.io/otel/attribute"
)

// Metrics holds all OTel metric instruments for the gateway.
// Use the Record*/Set* methods to update metrics.
type Metrics struct {
	requestsTotal       otelmetric.Int64Counter
	requestDuration     otelmetric.Float64Histogram
	upstreamHealth      otelmetric.Float64Gauge
	rateLimitRejections otelmetric.Int64Counter
	toolCacheRefresh    otelmetric.Int64Counter
	handler             http.Handler
}

// New creates and registers all metrics with the default Prometheus registerer.
func New() *Metrics {
	return newMetrics(prometheus.DefaultRegisterer, prometheus.DefaultGatherer)
}

// NewForRegistry creates and registers metrics with a custom Prometheus registry.
// Useful for testing to avoid global state conflicts.
func NewForRegistry(reg *prometheus.Registry) *Metrics {
	return newMetrics(reg, reg)
}

func newMetrics(registerer prometheus.Registerer, gatherer prometheus.Gatherer) *Metrics {
	exporter, err := otelprometheus.New(
		otelprometheus.WithRegisterer(registerer),
		otelprometheus.WithoutScopeInfo(),
		otelprometheus.WithoutTargetInfo(),
	)
	if err != nil {
		panic("metrics: create prometheus exporter: " + err.Error())
	}

	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	meter := mp.Meter("stile")

	requestsTotal, err := meter.Int64Counter("stile_requests",
		otelmetric.WithDescription("Total requests processed"),
	)
	if err != nil {
		panic("metrics: create stile_requests counter: " + err.Error())
	}

	requestDuration, err := meter.Float64Histogram("stile_request_duration_seconds",
		otelmetric.WithDescription("Request latency in seconds"),
		otelmetric.WithExplicitBucketBoundaries(
			.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10,
		),
	)
	if err != nil {
		panic("metrics: create stile_request_duration_seconds histogram: " + err.Error())
	}

	upstreamHealth, err := meter.Float64Gauge("stile_upstream_health",
		otelmetric.WithDescription("Upstream health status (1 = healthy, 0 = unhealthy)"),
	)
	if err != nil {
		panic("metrics: create stile_upstream_health gauge: " + err.Error())
	}

	rateLimitRejections, err := meter.Int64Counter("stile_rate_limit_rejections",
		otelmetric.WithDescription("Rate limit rejections"),
	)
	if err != nil {
		panic("metrics: create stile_rate_limit_rejections counter: " + err.Error())
	}

	toolCacheRefresh, err := meter.Int64Counter("stile_tool_cache_refresh",
		otelmetric.WithDescription("Tool cache refresh attempts"),
	)
	if err != nil {
		panic("metrics: create stile_tool_cache_refresh counter: " + err.Error())
	}

	return &Metrics{
		requestsTotal:       requestsTotal,
		requestDuration:     requestDuration,
		upstreamHealth:      upstreamHealth,
		rateLimitRejections: rateLimitRejections,
		toolCacheRefresh:    toolCacheRefresh,
		handler:             promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}),
	}
}

// RecordRequest increments the request counter.
func (m *Metrics) RecordRequest(caller, tool, upstream, status string) {
	m.requestsTotal.Add(nil, 1,
		otelmetric.WithAttributes(
			attribute.String("caller", caller),
			attribute.String("tool", tool),
			attribute.String("upstream", upstream),
			attribute.String("status", status),
		),
	)
}

// RecordDuration records a request duration.
func (m *Metrics) RecordDuration(caller, tool, upstream string, seconds float64) {
	m.requestDuration.Record(nil, seconds,
		otelmetric.WithAttributes(
			attribute.String("caller", caller),
			attribute.String("tool", tool),
			attribute.String("upstream", upstream),
		),
	)
}

// SetUpstreamHealth sets the health gauge for an upstream.
func (m *Metrics) SetUpstreamHealth(upstream string, val float64) {
	m.upstreamHealth.Record(nil, val,
		otelmetric.WithAttributes(
			attribute.String("upstream", upstream),
		),
	)
}

// RecordRateLimitRejection increments the rate limit rejection counter.
func (m *Metrics) RecordRateLimitRejection(caller, tool string) {
	m.rateLimitRejections.Add(nil, 1,
		otelmetric.WithAttributes(
			attribute.String("caller", caller),
			attribute.String("tool", tool),
		),
	)
}

// RecordToolCacheRefresh increments the tool cache refresh counter.
func (m *Metrics) RecordToolCacheRefresh(upstream, status string) {
	m.toolCacheRefresh.Add(nil, 1,
		otelmetric.WithAttributes(
			attribute.String("upstream", upstream),
			attribute.String("status", status),
		),
	)
}

// Handler returns the HTTP handler that serves the /metrics endpoint.
func (m *Metrics) Handler() http.Handler {
	return m.handler
}
