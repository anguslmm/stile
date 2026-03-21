# Task 21: Trace Context Propagation

**Status:** done
**Depends on:** 16

---

## Goal

Propagate W3C Trace Context headers (`traceparent`, `tracestate`) between inbound requests and outbound upstream calls so that traces are continuous from agent through Stile to upstream MCP servers. Without this, Stile's traces are isolated — you can see what happened inside the proxy but not correlate with the agent's or upstream's traces.

---

## 1. Inbound: extract trace context from agent requests

When an agent sends a request to Stile with a `traceparent` header, Stile should extract it and use it as the parent span rather than starting a new root trace.

**Implementation:**

- Use `go.opentelemetry.io/otel/propagation` with `propagation.TraceContext{}`
- In `handleMCP` (or as middleware), extract the trace context from the incoming HTTP headers into the request context:
  ```go
  ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(r.Header))
  ```
- Any spans created from this context will automatically be children of the agent's span

---

## 2. Outbound: inject trace context into upstream requests

When Stile sends a request to an HTTP upstream, inject the current trace context into the outbound headers so the upstream can continue the trace.

**Implementation:**

- In `HTTPTransport.RoundTrip`, after creating the outbound `http.Request`, inject:
  ```go
  otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(httpReq.Header))
  ```
- This adds `traceparent` and `tracestate` headers to the upstream request

---

## 3. Register the propagator

In `main.go` during OTel setup, register the W3C propagator globally:

```go
otel.SetTextMapPropagator(propagation.TraceContext{})
```

This is a one-liner but is required for `Extract` and `Inject` to work.

---

## 4. Stdio transport consideration

Stdio transports communicate via JSON-RPC over stdin/stdout — there are no HTTP headers. Trace context cannot be propagated to stdio upstreams via the standard W3C mechanism.

Document this limitation. If a stdio MCP server wants to participate in tracing, it would need to accept trace context as a JSON-RPC parameter extension, which is outside the MCP spec. For now, Stile's span for the stdio round-trip is the leaf span in the trace.

---

## Verification

- Add test: inbound request with `traceparent` header creates a child span (not a new root)
- Add test: outbound HTTP request includes `traceparent` header matching the current span
- Add test: no `traceparent` on inbound request creates a new root trace (existing behavior)
- Add test: stdio transport works without error when tracing is enabled (no header injection attempted)
- End-to-end: agent → Stile → HTTP upstream produces a single connected trace with spans from all three
