# Task 27.1: Add OpenTelemetry tracing to `stile wrap`

**Status:** done
**Depends on:** 27

---

## Goal

Add OpenTelemetry instrumentation to the `stile wrap` handler so that traces propagate end-to-end from the Stile gateway through the wrapper to the stdio child process.

Currently the Stile gateway injects `traceparent` headers in outbound HTTP requests to wrapped upstreams, but the wrapper ignores them. Traces end at the gateway's `upstream.RoundTrip` span — the stdio leg is invisible in Jaeger.

---

## Implementation

### 1. Extract incoming trace context

In the wrap handler's `handleMCP`, extract W3C Trace Context from the inbound HTTP request headers using `otel.GetTextMapPropagator().Extract()`.

### 2. Create spans

- **`wrap.forward`** span around each `transport.Send` call in `forward()`, with attributes for `mcp.method`
- **`wrap.handleMCP`** span around the full request handling

### 3. CLI flags

Add an optional `--otel-endpoint` flag to `stile wrap` that configures an OTLP exporter. When not set, tracing is disabled (no-op tracer). This keeps the wrapper zero-config by default.

### 4. Service name

Use `stile-wrap` (or `stile-wrap-<command>`) as the OTel service name so wrapper spans are distinguishable from gateway spans in Jaeger.

---

## Verification

- Start the scaling docker compose with Jaeger
- Make a `tools/call` request through the gateway
- In Jaeger, the trace should show spans from both `stile` (gateway) and `stile-wrap` (wrapper)
- The wrapper's `wrap.forward` span should be a child of the gateway's `upstream.RoundTrip` span
