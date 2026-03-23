# server

Inbound MCP HTTP server: routes requests from agents to the proxy pipeline.

## Key Types

- **`Server`** — wraps `net/http.Server` with a fixed route table; holds references to `proxy.Handler`, `router.RouteTable`, `auth.Authenticator`, and an OTel tracer.
- **`Options`** — optional wiring passed to `New`: authenticator, admin auth middleware, admin handler, health checker, tracer. All fields are nil-safe.
- **`AdminRegistrar`** — interface for registering `/admin/` routes onto a `*http.ServeMux`.

## Key Functions

- **`New(cfg, p, rt, m, opts) *Server`** — constructs the server, wires all routes, and applies TLS config if present. Calls `os.Exit(1)` on TLS config failure.
- **`(*Server).ListenAndServe() error`** — starts plain HTTP or HTTPS depending on TLS config.
- **`(*Server).Shutdown(ctx) error`** — graceful shutdown.
- **`(*Server).Handler() http.Handler`** — returns the mux; used by tests with `httptest`.
- **`(*Server).TLSEnabled() bool`** — reports whether TLS is configured.

## Routes

| Path | Method | Handler |
|---|---|---|
| `POST /mcp` | POST | MCP JSON-RPC endpoint (auth-wrapped if `Authenticator` set) |
| `/admin/` | any | `AdminRegistrar` routes (or fallback `POST /admin/refresh`) |
| `GET /healthz` | GET | liveness (optional) |
| `GET /readyz` | GET | readiness (optional) |
| `GET /metrics` | GET | Prometheus metrics (optional) |

## Design Notes

- `tools/call` is handled outside the normal `dispatch` switch because it may stream SSE directly to `http.ResponseWriter`. In batch mode, `tools/call` returns an error — SSE cannot be batched.
- Auth middleware extracts W3C Trace Context before creating the `auth` span so it becomes a child of the caller's trace. `handleMCP` guards against double-extraction when auth middleware has already done it.
- TLS: if `ClientCAFile` is set, mutual TLS (`RequireAndVerifyClientCert`) is enforced. The default min TLS version is 1.2.
- Request body is capped at 10 MB; batch size is capped at 100 entries.
- `initialize` response includes the full filtered tool list so clients can skip a separate `tools/list` round-trip.
