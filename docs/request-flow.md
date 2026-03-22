# Request Flow

This document traces the complete path a request follows through Stile, from an agent's HTTP request to the upstream MCP server response.

## 1. Startup wiring (`cmd/gateway/main.go`)

Config is loaded, then these are built in order:
- **Transports** — one per upstream (`streamable-http` or `stdio`), keyed by name. If an upstream has `circuit_breaker` or `retry` config, the transport is wrapped in a `ResilientTransport` that adds circuit breaking and retries.
- **RouteTable** — takes the transports, calls `tools/list` on each upstream to discover what tools they offer, builds a `tool name -> upstream` map
- **Authenticator** — backed by a caller store (SQLite or Postgres) + role config
- **RateLimiter** — created via `NewRateLimiterFromConfig`: `LocalRateLimiter` (in-memory token buckets) or `RedisRateLimiter` (Redis sliding windows) based on `rate_limits.backend`
- **proxy.Handler** — holds the RouteTable and RateLimiter
- **server.Server** — wires the HTTP mux, wraps the MCP endpoint with auth if configured

## 2. Request arrives: `POST /mcp`

The Go HTTP server routes it to the handler registered at `server.go:76`.

## 3. Auth (`server.go` -> `auth.go`)

If auth is configured, the closure fires:
- Call `s.authenticator.Authenticate(r)` which:
  - Extracts the `Bearer` token from the `Authorization` header
  - SHA-256 hashes it
  - Looks up the hash in the database store -> gets the caller name
  - Resolves the caller's roles, computes their allowed tool globs
  - Returns a `*Caller`
- If auth fails -> writes a JSON-RPC error (`-32000 unauthorized`), request stops here
- If auth succeeds -> stores the `Caller` in the request context via `context.WithValue`

## 4. JSON-RPC parsing (`server.go` `handleMCP`)

Reads the body and calls `jsonrpc.ParseMessage`:
- Determines if it's a single request or batch
- Single requests go to `handleSingle`
- Batches loop through, dispatching each

## 5. Method dispatch (`server.go` `handleSingle` / `dispatch`)

`handleSingle` checks the method:
- **`initialize`** — returns server info and capabilities. No proxying.
- **`ping`** — returns `{}`. No proxying.
- **`tools/list`** — calls `proxy.HandleToolsList`, which asks the router for all tools, then filters by the caller's allowed globs if a caller is in context. Returns the filtered list. No upstream call.
- **`tools/call`** — the interesting one. Goes to `proxy.HandleToolsCall`.

## 6. Proxy: `tools/call` (`proxy.go` `HandleToolsCall`)

This is the core path:

**a. Parse** — extract `params.name` (the tool being called)

**b. ACL check** — if there's a caller in context, check `caller.CanAccessTool(toolName)`. Denied -> error response.

**c. Route** — `router.Resolve(toolName)` looks up the tool name in the route table -> returns a `Route` containing the `Upstream` (which has the `Transport`). Unknown tool -> error response.

**d. Rate limit** — grab the `RateLimiter` (interface), then call `rl.Allow(caller, tool, upstream, roles)`. This checks three limits in order:
1. Per-caller limit
2. Per-caller-per-tool limit
3. Per-upstream limit

The backend is configurable: `LocalRateLimiter` uses in-memory token buckets (single instance), `RedisRateLimiter` uses Redis sliding windows (multi-instance). `Allow()` returns a `*RateLimitResult` containing the allow/deny decision plus rate limit state (limit, remaining, reset time). This is used to populate `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset` headers on every `tools/call` response. On denial, a `Retry-After` header is also included. The headers report the most restrictive of the applicable limits.

**e. Forward** — `route.Upstream.Transport.RoundTrip(ctx, req)`. If the transport is wrapped in a `ResilientTransport`, the request first passes through:
1. **Circuit breaker** — if the upstream's circuit is open, the request fails immediately with `"upstream circuit open"`. After a cooldown period, one probe request is allowed through (half-open state). If it succeeds, the circuit closes; if it fails, the circuit reopens.
2. **Retry loop** — on retryable errors (connection failures or configured HTTP status codes like 502/503/504), the request is retried with jittered exponential backoff up to `max_attempts`.
3. **Actual transport** — sends the JSON-RPC request to the upstream. Per-upstream `timeout` controls the HTTP `ResponseHeaderTimeout`.

The `Transport` interface hides whether the underlying transport is an HTTP POST or writing to a child process's stdin.

**f. Response** — `RoundTrip` returns a `TransportResult`, which is either:
- `JSONResult` — upstream returned `application/json`. The response is already parsed.
- `StreamResult` — upstream returned `text/event-stream` (SSE). The stream is still open.

`result.WriteResponse(ctx, w)` writes it back to the agent:
- JSON results get marshaled and written
- Stream results get piped through with flush-per-chunk until EOF or client disconnect

**g. Audit/metrics** — throughout step 6, the proxy records the caller, tool, upstream, status, and latency to structured logs, Prometheus counters/histograms, and the audit log database.

## 7. Distributed tracing

When tracing is enabled (`telemetry.traces.enabled: true`), the request path is instrumented with OpenTelemetry spans:

```
[handleMCP]                              <- root span per request (or child of agent's span)
  +- [auth]                              <- child span (auth middleware)
  +- [dispatch]                          <- child span (method dispatch)
  |    +- [route + rate limit]           <- child span (routing + rate limit check)
  |    +- [upstream.RoundTrip]           <- child span (HTTP call or stdio write to upstream)
  +- [StreamResult.WriteResponse]        <- child span if SSE (stream copy loop)
```

Key span attributes: `mcp.method`, `mcp.tool`, `mcp.upstream`, `mcp.caller`, `mcp.status`.

The `StreamResult.WriteResponse` span is the critical one for SSE streams — if the stream dies mid-flight, the span records the error and duration.

All structured log messages on the request path include `trace_id` and `span_id` fields for correlation.

### Trace context propagation (W3C)

Stile propagates W3C Trace Context (`traceparent`/`tracestate` headers) so that traces are continuous across agent → Stile → upstream:

- **Inbound**: `handleMCP` extracts the `traceparent` header from the agent's request. If present, Stile's spans become children of the agent's trace. If absent, a new root trace is created.
- **Outbound**: `HTTPTransport.RoundTrip` injects the current `traceparent` header into requests sent to HTTP upstreams, so the upstream can continue the trace.
- **Stdio limitation**: Stdio transports communicate via JSON-RPC over stdin/stdout — there are no HTTP headers. Trace context cannot be propagated to stdio upstreams. Stile's span for the stdio round-trip is the leaf span in the trace.

## 8. Return path

The response flows back through the same HTTP connection to the agent. There's no post-processing middleware on the way out — auth is inbound only.

## Visual summary

```
Agent
  |
  v
POST /mcp
  |
  v
Auth closure ---- s.authenticator
  |                 |-- Authenticate: Bearer token -> SHA-256 -> database lookup -> Caller
  |                 |-- Caller set in context
  v
handleMCP -------- Parse JSON-RPC body
  |
  v
handleSingle
  |
  |-- initialize -- return server info
  |-- ping -------- return {}
  |-- tools/list -- router.ListTools() -> filter by caller globs -> return
  |-- tools/call -- proxy.HandleToolsCall:
                     |
                     |-- ACL check (caller.CanAccessTool)
                     |-- router.Resolve(toolName) -> Route{Upstream, Transport}
                     |-- rateLimiter.Allow(caller, tool, upstream)
                     |-- transport.RoundTrip(req) -> TransportResult
                     |    |-- [ResilientTransport wrapper, if configured:]
                     |    |    |-- circuit breaker check (fail fast if open)
                     |    |    |-- retry loop with jittered backoff
                     |    |-- HTTPTransport: POST to remote server (per-upstream timeout)
                     |    |-- StdioTransport: write to child stdin, read stdout
                     |-- result.WriteResponse(w) -> JSON or SSE back to agent
                     |-- audit + metrics
```
