# telemetry

Initializes OpenTelemetry tracing for Stile and provides a tracer provider wrapper.

## Key Types

- **`Provider`** — Wraps `sdktrace.TracerProvider` and holds the named tracer. Obtained from `Init`. Must be shut down on exit to flush pending spans.

## Key Functions

- **`Init(ctx, cfg)`** — Constructs and registers the global OTel tracer provider from `config.TelemetryConfig`. Returns a `*Provider`. If tracing is disabled, returns a no-op `Provider` (safe to use; `Shutdown` is a no-op).
- **`(*Provider).Tracer()`** — Returns the `trace.Tracer` for creating spans.
- **`(*Provider).Shutdown(ctx)`** — Flushes pending spans and shuts down the provider. Safe to call when tracing is disabled (`tp == nil` guard).

## Design Notes

- The OTLP exporter always uses HTTP (not gRPC) and always connects with `WithInsecure()` — no TLS to the collector.
- Sampling uses `ParentBased(TraceIDRatioBased(rate))`, so the sample rate from config controls head-based sampling while honouring upstream sampling decisions.
- The global OTel provider (`otel.SetTracerProvider`) and W3C TraceContext propagator are registered as side effects of `Init`. Other packages that use `otel.Tracer(...)` directly will pick up this provider.
- The tracer name is the module path `github.com/anguslmm/stile`, set as the package-level constant `tracerName`.
- Service name and version are hardcoded (`"stile"`, `"0.1.0"`) in the OTLP resource attributes.
