# Request Flow

This document traces the complete path a request follows through Stile, from an agent's HTTP request to the upstream MCP server response.

## 1. Startup wiring (`cmd/gateway/main.go`)

Config is loaded, then these are built in order:
- **Transports** — one per upstream (`streamable-http` or `stdio`), keyed by name
- **RouteTable** — takes the transports, calls `tools/list` on each upstream to discover what tools they offer, builds a `tool name -> upstream` map
- **Authenticator** — backed by a SQLite caller store + role config
- **RateLimiter** — token buckets from config (per-caller, per-tool, per-upstream)
- **proxy.Handler** — holds the RouteTable and RateLimiter
- **server.Server** — wires the HTTP mux, wraps the MCP endpoint with auth if configured

## 2. Request arrives: `POST /mcp`

The Go HTTP server routes it to the handler registered at `server.go:76`.

## 3. Auth (`server.go` -> `auth.go`)

If auth is configured, the closure fires:
- Grab `s.authenticator` under read lock (swappable for config reload)
- Call `a.Authenticate(r)` which:
  - Extracts the `Bearer` token from the `Authorization` header
  - SHA-256 hashes it
  - Looks up the hash in the SQLite store -> gets the caller name
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

**d. Rate limit** — grab the `RateLimiter` pointer, then call `rl.Allow(caller, tool, upstream)`. This checks three token buckets in order:
1. Per-caller bucket
2. Per-caller-per-tool bucket
3. Per-upstream bucket

Any bucket empty -> error response with which level was hit.

**e. Forward** — `route.Upstream.Transport.RoundTrip(ctx, req)`. This sends the original JSON-RPC request to the upstream MCP server. The `Transport` interface hides whether that's an HTTP POST or writing to a child process's stdin.

**f. Response** — `RoundTrip` returns a `TransportResult`, which is either:
- `JSONResult` — upstream returned `application/json`. The response is already parsed.
- `StreamResult` — upstream returned `text/event-stream` (SSE). The stream is still open.

`result.WriteResponse(ctx, w)` writes it back to the agent:
- JSON results get marshaled and written
- Stream results get piped through with flush-per-chunk until EOF or client disconnect

**g. Audit/metrics** — throughout step 6, the proxy records the caller, tool, upstream, status, and latency to structured logs, Prometheus counters/histograms, and the SQLite audit log.

## 7. Distributed tracing

When tracing is enabled (`telemetry.traces.enabled: true`), the request path is instrumented with OpenTelemetry spans:

```
[handleMCP]                              <- root span per request
  +- [auth]                              <- child span (auth middleware)
  +- [dispatch]                          <- child span (method dispatch)
  |    +- [route + rate limit]           <- child span (routing + rate limit check)
  |    +- [upstream.RoundTrip]           <- child span (HTTP call or stdio write to upstream)
  +- [StreamResult.WriteResponse]        <- child span if SSE (stream copy loop)
```

Key span attributes: `mcp.method`, `mcp.tool`, `mcp.upstream`, `mcp.caller`, `mcp.status`.

The `StreamResult.WriteResponse` span is the critical one for SSE streams — if the stream dies mid-flight, the span records the error and duration.

All structured log messages on the request path include `trace_id` and `span_id` fields for correlation.

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
Auth closure ---- s.authenticator (swappable via mutex/atomic)
  |                 |-- Authenticate: Bearer token -> SHA-256 -> SQLite lookup -> Caller
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
                     |    |-- HTTPTransport: POST to remote server
                     |    |-- StdioTransport: write to child stdin, read stdout
                     |-- result.WriteResponse(w) -> JSON or SSE back to agent
                     |-- audit + metrics
```
