# Task 16: OpenTelemetry Observability (Traces, Metrics Migration, Log Correlation)

**Status:** done
**Depends on:** 15
**Needed by:** 17

---

## Goal

Add distributed tracing via OpenTelemetry, migrate existing Prometheus metrics to the OTel API (still exported as Prometheus), and correlate structured logs with traces. This closes the observability gap where streaming errors (e.g. SSE streams dying mid-flight) are invisible to metrics and hard to diagnose from logs alone.

---

## 1. Add OTel trace SDK and instrument the request path

**Dependencies:** `go.opentelemetry.io/otel`, `go.opentelemetry.io/otel/sdk/trace`, `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp`

Add a tracer provider in `main.go`, configured via a new `telemetry` config section:

```yaml
telemetry:
  traces:
    enabled: true
    endpoint: "localhost:4318"   # OTLP HTTP endpoint (Tempo, Jaeger, etc.)
    sample_rate: 1.0             # 0.0 to 1.0
```

If `telemetry.traces.enabled` is false or omitted, use a no-op tracer (zero overhead).

Instrument the request path with spans:

```
[handleMCP]                              <- root span per request
  +- [auth]                              <- child span
  +- [dispatch]                          <- child span
  |    +- [route + rate limit]
  |    +- [upstream.RoundTrip]           <- child span, covers HTTP call to upstream
  +- [StreamResult.WriteResponse]        <- child span if SSE, covers the stream copy loop
```

Key span attributes: `mcp.method`, `mcp.tool`, `mcp.upstream`, `mcp.caller`, `mcp.status`.

The critical span is `WriteResponse` for SSE streams -- if the stream dies mid-flight, the span records the error and duration, making the failure visible in traces.

---

## 2. Migrate Prometheus metrics to OTel metrics API

**Dependencies:** `go.opentelemetry.io/otel/exporters/prometheus`

Replace the direct `prometheus/client_golang` metric definitions in `internal/metrics/` with OTel meter equivalents:

- `prometheus.NewCounterVec(...)` -> `meter.Int64Counter(...)`
- `prometheus.NewHistogramVec(...)` -> `meter.Float64Histogram(...)`

Use the OTel Prometheus exporter bridge so the existing `/metrics` scrape endpoint continues to work unchanged. No backend changes required.

Update `internal/metrics/metrics.go` to initialize via OTel. The metric names and labels should stay the same to avoid breaking existing dashboards.

---

## 3. Add trace-correlated structured logging

Create a thin `slog.Handler` wrapper that extracts the trace ID and span ID from the context and injects them as log attributes automatically:

```go
// internal/logging/tracehandler.go
type TraceHandler struct {
    inner slog.Handler
}

func (h *TraceHandler) Handle(ctx context.Context, r slog.Record) error {
    span := trace.SpanFromContext(ctx)
    if span.SpanContext().IsValid() {
        r.AddAttrs(
            slog.String("trace_id", span.SpanContext().TraceID().String()),
            slog.String("span_id", span.SpanContext().SpanID().String()),
        )
    }
    return h.inner.Handle(ctx, r)
}
```

Wire this into the `slog` setup in `main.go`. Existing `slog.InfoContext(ctx, ...)` calls will automatically get trace correlation. For call sites that currently use `slog.Info(...)` (no ctx), update the hot-path ones (request handling, proxy, transport) to use `slog.InfoContext`.

Also fix the `log.Printf` in `StreamResult.WriteResponse` (`transport.go:104`) -- change it to `slog.ErrorContext` with tool/upstream context so stream errors are properly structured and trace-correlated.

---

## 4. Config and lifecycle

Add tracer provider setup and shutdown to `main.go`:

- Initialize the tracer provider after config load
- Register it as the global tracer provider
- Shut it down in the graceful shutdown sequence (flushes pending spans)

Config section:
```yaml
telemetry:
  traces:
    enabled: false              # opt-in
    endpoint: "localhost:4318"
    sample_rate: 1.0
```

---

## 5. Documentation

Update `docs/request-flow.md` to reflect the new span structure in the request path.

Add a `docs/observability.md` guide covering:

- **What Stile exports:** OTLP traces + Prometheus metrics, with trace-correlated structured logs
- **Configuration reference:** the `telemetry` config section with examples
- **Deployment patterns:**
  - Direct export: point `endpoint` at any OTLP-compatible receiver (Datadog Agent, Tempo, etc.)
  - OTel Collector sidecar: point `endpoint` at the Collector, which handles routing, batching, tail sampling, and backend auth. Include a recommended Collector config with tail sampling (keep 100% of error traces, sample baseline traffic)
  - Prometheus metrics: no change, `/metrics` endpoint works as before
- **Head vs. tail sampling:** explain that `sample_rate` in Stile's config controls head sampling (coarse volume reduction), while tail sampling (e.g. "keep all error traces") is configured in the Collector

Link to this guide from the README.

---

## Verification

- Existing tests pass (no-op tracer by default)
- `/metrics` endpoint still serves Prometheus format with the same metric names
- Add test: spans are created for a tools/call request (use a recording SpanExporter)
- Add test: SSE stream error produces a span with error status
- Add test: log output includes trace_id and span_id when a span is active
- Add test: no trace fields in logs when tracing is disabled
- Manual: send traffic with tracing enabled, verify traces appear in Jaeger/Tempo
