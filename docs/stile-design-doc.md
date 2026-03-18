# Stile Design Document

**Version:** 0.1 · **Status:** Draft · **Date:** March 2026

---

## 1. Overview

`stile` is a standalone, high-performance reverse proxy for the Model Context Protocol (MCP). It sits between AI agents and MCP tool servers, providing authentication, routing, observability, and policy enforcement in a single Go binary.

### 1.1 Problem Statement

Today, every agent-to-tool connection is direct, unauthenticated, and unobservable. Each agent process manages its own MCP server connections, credentials, and tool discovery. This creates several problems:

- No centralized auth or access control — any agent can call any tool with any credentials
- No visibility into tool usage — no logs, no metrics, no audit trail
- No rate limiting — a runaway agent can hammer upstream servers
- Credential sprawl — every agent config has raw tokens for every upstream
- No tool catalog management — agents see whatever each server exposes, with no filtering or curation

### 1.2 Goals

1. Single binary, zero-dependency deployment (ship as a Go binary or container image)
2. Config-driven upstream management with support for HTTP and stdio transports
3. Inbound authentication and per-caller tool-level access control
4. Outbound credential management — agents never touch upstream tokens
5. Request-level observability: structured logs, Prometheus metrics, optional audit log
6. Rate limiting per caller, per tool, and per upstream
7. Tool schema caching with configurable refresh

### 1.3 Non-Goals (v0.1)

- Multi-instance / distributed deployment (single process is sufficient for initial scale)
- OAuth2/OIDC for inbound auth (API keys first; OAuth is a later phase)
- Web UI or dashboard (CLI and config files only)
- Tool response transformation or rewriting

---

## 2. Architecture

### 2.1 Request Flow

The core proxy loop handles every MCP request through a consistent pipeline:

1. Agent sends JSON-RPC 2.0 request to the gateway's HTTP endpoint
2. Auth middleware extracts the bearer token and resolves the caller identity
3. Router resolves the target tool name to an upstream via the route table
4. Policy engine checks rate limits, ACLs, and optionally validates input against the tool's JSON Schema
5. Proxy forwards the request to the upstream using the appropriate transport (HTTP or stdio)
6. Response streams back to the agent (SSE for streaming, JSON for simple request/response)
7. Observability layer logs the request and updates metrics

### 2.2 System Components

| Layer | Responsibility | Key Types |
|-------|---------------|-----------|
| Transport | JSON-RPC 2.0 codec, HTTP server, stdio bridge | `Transport` interface, `HTTPTransport`, `StdioTransport` |
| Router | Tool-to-upstream resolution, route table, tool discovery | `RouteTable`, `Upstream`, `ToolSchema` |
| Auth | Inbound caller auth, outbound credential injection | `AuthMiddleware`, `Caller`, `CredentialStore` |
| Policy | Rate limiting, ACL enforcement, input validation | `RateLimiter`, `ACLChecker`, `SchemaValidator` |
| Proxy | Request dispatch, streaming passthrough, error handling | `ProxyHandler`, `Session` |
| Observability | Structured logging, Prometheus metrics, audit log | `Logger`, `MetricsCollector`, `AuditWriter` |

### 2.3 Project Structure

```
stile/
├── cmd/gateway/         # Entrypoint, CLI flags, config loading, server startup
├── internal/
│   ├── jsonrpc/         # JSON-RPC 2.0 types (Request, Response, Error), parser, batch handling
│   ├── transport/       # Transport interface, HTTP (Streamable HTTP + SSE) and stdio implementations
│   ├── router/          # RouteTable, upstream management, tool discovery and caching
│   ├── auth/            # Inbound auth middleware, outbound credential store
│   ├── policy/          # Rate limiter, ACL checker, JSON Schema input validator
│   ├── proxy/           # Core proxy handler, session management, streaming passthrough
│   ├── config/          # Config file parsing (YAML), validation, hot reload
│   └── health/          # Upstream health checks, readiness/liveness probes
├── configs/             # Example configuration files
└── go.mod
```

---

## 3. Protocol & Transport

### 3.1 JSON-RPC 2.0

MCP uses JSON-RPC 2.0 as its wire format. The gateway must handle requests (with an `id`, expecting a response), notifications (no `id`, no response expected), and batch requests (array of requests/notifications). The implementation is a small, hand-written codec — no framework required.

Key data types:

- **Request:** `jsonrpc`, `method`, `params` (optional), `id` (string or int)
- **Response:** `jsonrpc`, `result` or `error`, `id`
- **Error:** `code` (integer), `message` (string), `data` (optional)
- **Batch:** array of Request and/or Notification objects

### 3.2 Streamable HTTP Transport

The current MCP standard transport. The client POSTs JSON-RPC to a single endpoint. The server responds with either:

- `Content-Type: application/json` for simple request/response
- `Content-Type: text/event-stream` for streaming responses (progress notifications, partial results)

The gateway must handle both response modes on the same endpoint. For streaming, it maintains the SSE connection and forwards events from the upstream as they arrive.

### 3.3 Stdio Bridge

Some MCP servers (filesystem tools, custom local servers) communicate via stdin/stdout. The gateway spawns the process, pipes JSON-RPC over stdin/stdout using line-delimited JSON, and exposes the server as an HTTP-accessible upstream. This is managed via the `StdioTransport` implementation, which wraps `cmd.StdinPipe()` and `cmd.StdoutPipe()` with a JSON-RPC codec.

### 3.4 Transport Interface

Both transports implement a common interface:

```go
type Transport interface {
    Send(ctx context.Context, req *Request) (*Response, error)
    ListTools(ctx context.Context) ([]ToolSchema, error)
    Close() error
    Healthy() bool
}
```

This abstraction is the key seam in the architecture. The router and proxy layers never know whether they're talking to a remote HTTP server or a local stdio process.

---

## 4. Routing & Configuration

### 4.1 Upstream Configuration

Upstreams are declared in a YAML config file. Each upstream specifies a name, transport type, connection details, auth credentials, and an optional tool pattern filter.

```yaml
upstreams:
  - name: github
    url: https://mcp.github.com/sse
    transport: streamable-http
    auth:
      type: bearer
      token_env: GITHUB_MCP_TOKEN
    tools: ["github/*"]

  - name: local-db
    command: ["node", "./db-server/index.js"]
    transport: stdio
    tools: ["db_query", "db_schema"]

  - name: internal-tools
    url: http://localhost:8081/mcp
    transport: streamable-http
    tools: ["deploy/*", "oncall/*"]
```

### 4.2 Route Table

At startup, the gateway connects to each upstream, calls `tools/list`, and builds an in-memory route table mapping each tool name to its upstream. The route table is protected by a `sync.RWMutex` for concurrent access. Tool patterns from config provide initial routing hints before discovery completes.

When an agent calls `tools/list` on the gateway, the response is the merged superset of all tools from all upstreams that the caller is authorized to see. When an agent calls `tools/call`, the router looks up the tool name and dispatches to the correct upstream.

### 4.3 Tool Schema Caching

The `tools/list` call to each upstream is cached in memory. The cache is refreshed on a configurable interval (default: 5 minutes) or on demand via an admin endpoint (`POST /admin/refresh`). If an upstream is unreachable during refresh, its cached tools are marked stale but not removed — they return when the upstream recovers.

---

## 5. Authentication & Authorization

### 5.1 Inbound Auth (Agent → Gateway)

The gateway authenticates callers via API keys in the `Authorization: Bearer` header. Each API key maps to a caller identity with a name, a set of allowed tool patterns, and optional rate limit overrides.

```yaml
callers:
  - name: claude-code-dev
    api_key_env: DEV_GATEWAY_KEY
    allowed_tools: ["github/*", "linear/*", "db_query"]
    rate_limit: 100/min

  - name: ops-agent
    api_key_env: OPS_GATEWAY_KEY
    allowed_tools: ["deploy/*", "oncall/*", "datadog/*"]
    rate_limit: 30/min
```

API keys are stored as hashes (SHA-256) in memory. The auth middleware hashes the incoming bearer token and looks up the caller. If no match is found, the request is rejected with a JSON-RPC error (code `-32000`).

### 5.2 Outbound Auth (Gateway → Upstream)

Each upstream has its own auth requirements. The gateway manages credentials per-upstream, loaded from environment variables at startup. When proxying a request, the gateway injects the appropriate credentials into the upstream request. The agent never sees or manages upstream credentials.

Supported credential types for v0.1: bearer tokens (from env vars). Future: OAuth2 client credentials flow, mutual TLS.

### 5.3 Admin Auth (Operator → Gateway)

Admin endpoints (`/admin/refresh`, `/admin/reload`) are protected by a separate admin API key, configured via the `ADMIN_API_KEY` environment variable. Requests must include `Authorization: Bearer <token>` where the token matches the admin key. If `ADMIN_API_KEY` is not set and auth is disabled (no callers configured), admin endpoints are open — matching the dev-mode behavior. If `ADMIN_API_KEY` is not set but callers are configured (auth is enabled), admin endpoints return `403 Forbidden` to prevent accidental exposure.

Health and metrics endpoints (`/healthz`, `/readyz`, `/metrics`) are always unauthenticated — they are consumed by infrastructure tooling.

### 5.4 Authorization

After authentication, the gateway checks the caller's `allowed_tools` list against the requested tool name using glob matching. The `tools/list` response is also filtered per-caller, so agents only see the tools they're authorized to use.

---

## 6. Policy Enforcement

### 6.1 Rate Limiting

Token bucket rate limiting is applied at three granularities:

- **Per-caller:** total request rate across all tools
- **Per-caller-per-tool:** prevent one tool from consuming the full budget
- **Per-upstream:** protect upstream servers from aggregate load

The implementation uses `golang.org/x/time/rate`, which provides a thread-safe token bucket. Rate limits are configurable per-caller in the config file. Upstream rate limits are set on the upstream definition. Default limits apply when no override is specified.

### 6.2 Input Validation

MCP tool schemas include JSON Schema definitions for tool inputs. The gateway can optionally validate tool call inputs against the schema before proxying, catching malformed requests before they reach the upstream. This is opt-in per upstream to avoid blocking legitimate edge cases.

### 6.3 Tool Filtering

Beyond per-caller ACLs, the gateway supports global tool blocklists and allowlists. A tool can be blocked entirely (removed from all callers' views) or restricted to specific caller groups. This provides defense-in-depth beyond per-caller `allowed_tools`.

---

## 7. Observability

### 7.1 Structured Logging

Every request is logged using Go's `slog` package with structured fields: caller name, tool name, upstream name, method, latency, status (success/error), and error details if applicable. Log level is configurable (debug, info, warn, error).

### 7.2 Prometheus Metrics

The gateway exposes a `/metrics` endpoint with the following:

| Metric | Type | Labels |
|--------|------|--------|
| `gateway_requests_total` | Counter | caller, tool, upstream, status |
| `gateway_request_duration_seconds` | Histogram | caller, tool, upstream |
| `gateway_upstream_health` | Gauge | upstream |
| `gateway_rate_limit_rejections_total` | Counter | caller, tool |
| `gateway_tool_cache_refresh_total` | Counter | upstream, status |

### 7.3 Audit Log

An optional append-only audit log records the full request (method, tool, params) and response status for compliance and debugging. This is opt-in per upstream to control data sensitivity. Output is JSONL to stdout or a file path.

---

## 8. Implementation Phases

### Phase 1: Transport & Routing (Weeks 1–2)

The proof-of-life milestone. Build the JSON-RPC codec, HTTP server, and stdio bridge. Connect to at least two upstreams (one HTTP, one stdio). Implement the route table and tool discovery. Verify end-to-end by pointing Claude Code at the gateway and confirming tool calls work.

**Deliverables:**

- JSON-RPC 2.0 parser with request, notification, and batch support
- Streamable HTTP server endpoint
- Stdio transport with process lifecycle management
- Route table with tool-to-upstream resolution
- Config file parsing (upstreams only)
- Manual end-to-end test with a real agent

### Phase 2: Auth & Access Control (Week 3)

Add inbound authentication and per-caller tool filtering.

**Deliverables:**

- API key auth middleware with hashed key lookup
- Caller config with `allowed_tools` glob patterns
- Filtered `tools/list` responses per caller
- Outbound credential injection per upstream
- Auth rejection error responses (JSON-RPC error codes)

### Phase 3: Policy & Rate Limiting (Week 4)

Add rate limiting and optional input validation.

**Deliverables:**

- Token bucket rate limiter (per-caller, per-tool, per-upstream)
- Rate limit config in caller and upstream definitions
- JSON Schema input validation (opt-in per upstream)
- Global tool blocklist/allowlist

### Phase 4: Observability (Weeks 4–5)

Add structured logging, Prometheus metrics, and the optional audit log.

**Deliverables:**

- `slog`-based structured request logging
- Prometheus `/metrics` endpoint with all defined metrics
- JSONL audit log writer (opt-in)
- Health check endpoints (`/healthz`, `/readyz`)

### Phase 5: Hardening & Release (Weeks 5–6)

Polish for production use.

**Deliverables:**

- Graceful shutdown with in-flight request draining
- Config hot-reload via SIGHUP or admin endpoint
- Integration test suite (real upstreams, real agent)
- README with setup, config reference, and deployment guide
- Dockerfile and example docker-compose.yml

---

## 9. Dependencies

The dependency footprint is intentionally minimal:

| Package | Purpose |
|---------|---------|
| `gopkg.in/yaml.v3` | Config file parsing |
| `golang.org/x/time/rate` | Token bucket rate limiting |
| `prometheus/client_golang` | Metrics exposition |
| `santhosh-tekuri/jsonschema` | JSON Schema validation |
| `gobwas/glob` | Tool name pattern matching |

Everything else is Go stdlib: `net/http`, `encoding/json`, `os/exec`, `log/slog`, `sync`, `context`.

---

## 10. Risks & Open Questions

| Risk | Severity | Mitigation |
|------|----------|------------|
| MCP spec is still evolving; transport details may change | Medium | Abstract transport behind an interface; isolate protocol-specific code |
| Stdio process lifecycle is fragile (crashes, hangs) | Medium | Health checks, automatic restart with backoff, timeout on all operations |
| SSE streaming adds complexity to proxy layer | Low | Keep streaming passthrough simple; avoid buffering full responses |
| Single-process rate limiting doesn't scale to multi-instance | Low | Acceptable for v0.1 scale; Redis-backed limiter is a known upgrade path |
| Tool schema caching can serve stale data | Low | Configurable TTL, manual refresh endpoint, stale-while-revalidate pattern |

---

## 11. Success Criteria

v0.1 is successful when:

- A developer can deploy the gateway as a single binary, configure upstreams in YAML, point an MCP-compatible agent at it, and use tools through the gateway without the agent knowing it's proxied
- Inbound auth prevents unauthorized callers from accessing tools
- Per-caller tool filtering correctly restricts tool visibility
- Request metrics are available in Prometheus format
- The gateway handles at least 100 concurrent tool calls without degradation
- Upstream failures are handled gracefully (errors returned, not crashes)
