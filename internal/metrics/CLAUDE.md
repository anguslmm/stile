# metrics

Registers and exposes Prometheus-compatible metrics for the Stile gateway using the OpenTelemetry metrics API with a Prometheus exporter.

## Key Types

- **`Metrics`** — Holds all OTel instrument handles (counters, histograms, gauges) and the `/metrics` HTTP handler. Created via `New()` or `NewForRegistry()`.

## Key Functions

- **`New()`** — Creates a `Metrics` instance wired to the default Prometheus registerer/gatherer.
- **`NewForRegistry(reg)`** — Creates a `Metrics` instance using a custom `*prometheus.Registry`; used in tests to avoid global state conflicts.
- **`(m) Handler()`** — Returns the `http.Handler` for serving the `/metrics` endpoint.

## Record/Set Methods

Each method maps to one Prometheus instrument:

| Method | Instrument | Labels |
|---|---|---|
| `RecordRequest` | counter `stile_requests` | caller, tool, upstream, status |
| `RecordDuration` | histogram `stile_request_duration_seconds` | caller, tool, upstream |
| `SetUpstreamHealth` | gauge `stile_upstream_health` | upstream (1=healthy, 0=unhealthy) |
| `RecordRateLimitRejection` | counter `stile_rate_limit_rejections` | caller, tool |
| `RecordToolCacheRefresh` | counter `stile_tool_cache_refresh` | upstream, status |
| `SetCircuitState` | gauge `stile_circuit_state` | upstream (0=closed, 1=open, 2=half-open) |
| `RecordRetry` | counter `stile_retries` | upstream |
| `RecordAuthCacheHit/Miss/Eviction` | counters `stile_auth_cache_*` | type |

## Design Notes

- Construction panics on instrument registration failure — errors here indicate a programming mistake (duplicate registration, bad config), not a runtime condition.
- OTel scope and target info are suppressed (`WithoutScopeInfo`, `WithoutTargetInfo`) to keep Prometheus output clean.
- All context arguments to OTel `Add`/`Record` calls are `nil`; no trace context is propagated.
- `NewForRegistry` is the test seam; tests must not use `New()` or they'll collide with the default global registry.
