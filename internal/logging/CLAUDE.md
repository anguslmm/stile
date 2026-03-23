# logging

Provides an `slog.Handler` wrapper that injects OpenTelemetry trace correlation fields into every log record.

## Exported Types

- **`TraceHandler`** — wraps any `slog.Handler`; on each `Handle` call, extracts the active span from the context and appends `trace_id` and `span_id` string attributes. No-ops when there is no valid span. Implements `slog.Handler` (compile-time checked).

## Exported Functions

- **`NewTraceHandler(inner slog.Handler) *TraceHandler`** — wraps an existing handler with trace correlation.

## Design Notes

- Pure decorator: all methods delegate to the inner handler after (optionally) adding attributes. `WithAttrs` and `WithGroup` return new `*TraceHandler` wrappers preserving the decoration.
- Attributes are added only when `span.SpanContext().IsValid()` — avoids polluting logs with zero-value IDs when no span is active.
- No logger construction helpers are provided; callers are responsible for building and setting the root `slog.Logger`.
