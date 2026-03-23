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
	circuitState        otelmetric.Float64Gauge
	retriesTotal        otelmetric.Int64Counter
	authCacheHits       otelmetric.Int64Counter
	authCacheMisses     otelmetric.Int64Counter
	authCacheEvictions  otelmetric.Int64Counter
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

	circuitState, err := meter.Float64Gauge("stile_circuit_state",
		otelmetric.WithDescription("Circuit breaker state per upstream (0=closed, 1=open, 2=half-open)"),
	)
	if err != nil {
		panic("metrics: create stile_circuit_state gauge: " + err.Error())
	}

	retriesTotal, err := meter.Int64Counter("stile_retries",
		otelmetric.WithDescription("Total retry attempts per upstream"),
	)
	if err != nil {
		panic("metrics: create stile_retries counter: " + err.Error())
	}

	authCacheHits, err := meter.Int64Counter("stile_auth_cache_hits",
		otelmetric.WithDescription("Auth cache hits"),
	)
	if err != nil {
		panic("metrics: create stile_auth_cache_hits counter: " + err.Error())
	}

	authCacheMisses, err := meter.Int64Counter("stile_auth_cache_misses",
		otelmetric.WithDescription("Auth cache misses"),
	)
	if err != nil {
		panic("metrics: create stile_auth_cache_misses counter: " + err.Error())
	}

	authCacheEvictions, err := meter.Int64Counter("stile_auth_cache_evictions",
		otelmetric.WithDescription("Auth cache evictions"),
	)
	if err != nil {
		panic("metrics: create stile_auth_cache_evictions counter: " + err.Error())
	}

	return &Metrics{
		requestsTotal:       requestsTotal,
		requestDuration:     requestDuration,
		upstreamHealth:      upstreamHealth,
		rateLimitRejections: rateLimitRejections,
		toolCacheRefresh:    toolCacheRefresh,
		circuitState:        circuitState,
		retriesTotal:        retriesTotal,
		authCacheHits:       authCacheHits,
		authCacheMisses:     authCacheMisses,
		authCacheEvictions:  authCacheEvictions,
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

// SetCircuitState sets the circuit breaker state gauge for an upstream.
func (m *Metrics) SetCircuitState(upstream string, val float64) {
	m.circuitState.Record(nil, val,
		otelmetric.WithAttributes(
			attribute.String("upstream", upstream),
		),
	)
}

// RecordRetry increments the retry counter for an upstream.
func (m *Metrics) RecordRetry(upstream string) {
	m.retriesTotal.Add(nil, 1,
		otelmetric.WithAttributes(
			attribute.String("upstream", upstream),
		),
	)
}

// RecordAuthCacheHit increments the auth cache hit counter.
func (m *Metrics) RecordAuthCacheHit(cacheType string) {
	m.authCacheHits.Add(nil, 1,
		otelmetric.WithAttributes(
			attribute.String("type", cacheType),
		),
	)
}

// RecordAuthCacheMiss increments the auth cache miss counter.
func (m *Metrics) RecordAuthCacheMiss(cacheType string) {
	m.authCacheMisses.Add(nil, 1,
		otelmetric.WithAttributes(
			attribute.String("type", cacheType),
		),
	)
}

// RecordAuthCacheEviction increments the auth cache eviction counter.
func (m *Metrics) RecordAuthCacheEviction(cacheType string) {
	m.authCacheEvictions.Add(nil, 1,
		otelmetric.WithAttributes(
			attribute.String("type", cacheType),
		),
	)
}

// Handler returns the HTTP handler that serves the /metrics endpoint.
func (m *Metrics) Handler() http.Handler {
	return m.handler
}
