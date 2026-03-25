# proxy

Core HTTP handler that dispatches MCP tool calls through the full policy pipeline to upstream transports.

## Key Types

- **`Handler`** — The central dispatch struct. Holds references to the router, rate limiter, metrics, audit store, tracer, and optional upstream auth resolver. Constructed via `NewHandler`.
- **`HandlerOption`** — Functional option type for configuring `Handler` at construction time (`WithTracer`, `WithAuthResolver`).
- **`UpstreamAuthResolver`** (interface) — Resolves per-user OAuth tokens for upstreams. `ResolveToken(ctx, callerName, upstreamName) (string, error)` returns the bearer token or `("", nil)` if the upstream doesn't use OAuth.

## Key Functions / Methods

- **`NewHandler(rt, rateLimiter, m, auditStore, ...opts) *Handler`** — Constructor. All dependencies except `rt` may be nil to disable that feature.
- **`WithTracer(trace.Tracer) HandlerOption`** — Attaches an OTel tracer.
- **`WithAuthResolver(UpstreamAuthResolver) HandlerOption`** — Attaches the OAuth token resolver for per-user token injection.
- **`HandleToolsList(ctx, id) (*jsonrpc.Response, error)`** — Returns merged tool list from all upstreams, filtered by the caller's ACL.
- **`FilteredTools(ctx) []transport.ToolSchema`** — Same filter logic as `HandleToolsList` but returns the raw slice (used by the HTTP layer for SSE/streaming).
- **`HandleToolsCall(ctx, w, req)`** — Full dispatch path: ACL check → route resolution → rate limit → OAuth token injection → upstream `RoundTrip` → response write. Writes directly to `http.ResponseWriter` to support SSE passthrough.

## Design Decisions / Constraints

- `HandleToolsCall` writes directly to `http.ResponseWriter` rather than returning a value; this is required for transparent SSE passthrough where the upstream response is streamed back verbatim.
- All optional dependencies (rate limiter, metrics, audit store, tracer) are nil-safe — each is guarded before use. Nil means the feature is disabled, not an error.
- Rate limit response headers (`X-RateLimit-*`, `Retry-After`) are set on every `tools/call` response when a rate limiter is present, not only on rejections.
- Audit and metrics recording happen on every code path (success and error) via `recordRequest`, called at the end of `HandleToolsCall`.
- OTel tracing uses child spans for "route + rate limit" and "upstream.RoundTrip" separately, with attributes set on the parent dispatch span as well.
- ACL filtering in `HandleToolsList` / `FilteredTools` is caller-optional: if no caller is in context, all tools are returned.
- OAuth token injection happens after rate limiting and before the upstream `RoundTrip`. If the auth resolver returns a token, it's placed in the context via `auth.ContextWithUpstreamToken` and read by `HTTPTransport.RoundTrip`.
