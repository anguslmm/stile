# Stile

[![CI](https://github.com/anguslmm/stile/actions/workflows/ci.yml/badge.svg)](https://github.com/anguslmm/stile/actions/workflows/ci.yml)

Stile is a reverse proxy gateway for the [Model Context Protocol (MCP)](https://modelcontextprotocol.io), written in Go. It sits between AI agents and MCP tool servers, providing authentication, routing, rate limiting, and observability as a single binary. Point your agents at Stile and it handles which tools they can see, who can call what, and how fast.

## Quick Start

**Build:**

```bash
go build -o stile ./cmd/gateway/
```

**Create a config** (`config.yaml`):

```yaml
server:
  address: ":8080"

upstreams:
  - name: my-tools
    transport: streamable-http
    url: https://mcp.example.com/v1
    auth:
      type: bearer
      token_env: MCP_TOKEN
```

**Run:**

```bash
export MCP_TOKEN="your-upstream-token"
./stile -config config.yaml
```

**Point your agent at it:**

Configure your MCP client to connect to `http://localhost:8080/mcp`. Stile proxies `initialize`, `tools/list`, and `tools/call` requests to the configured upstreams.

**Using with Claude Code:**

```bash
claude mcp add --transport http stile http://localhost:8080/mcp
```

## Configuration Reference

### Server

```yaml
server:
  address: ":8080"           # Listen address (default: ":8080")
  tool_cache_ttl: "5m"       # How often to re-discover tools from upstreams (default: "5m")
  db_path: "stile.db"        # SQLite database for caller/key storage (enables auth)
  shutdown_timeout: "30s"    # Graceful shutdown timeout (default: "30s")
```

### Upstreams

Each upstream is an MCP server that Stile proxies to.

```yaml
upstreams:
  - name: remote-tools                  # Unique name
    transport: streamable-http          # "streamable-http" or "stdio"
    url: https://mcp.example.com/v1     # Required for streamable-http
    auth:
      type: bearer
      token_env: REMOTE_TOOLS_TOKEN     # Env var containing the bearer token
    tools:                              # Optional: only expose these tools
      - search
      - summarize
    rate_limit: "1000/min"              # Per-upstream rate limit

  - name: local-tools
    transport: stdio
    command: ["python", "-m", "mcp_server", "--stdio"]  # Required for stdio
```

### Roles

Roles define tool access patterns and outbound credentials. Callers are assigned roles.

```yaml
roles:
  github-user:
    allowed_tools: ["github/*"]         # Glob patterns for tool access
    credentials:
      remote-tools: GITHUB_TOKEN_ENV    # Env var for outbound auth per upstream
    rate_limit: "100/min"               # Per-caller rate limit override
    tool_rate_limit: "10/sec"           # Per-caller-per-tool rate limit override

  admin:
    allowed_tools: ["*"]                # Full access
    rate_limit: "1000/min"
```

### Rate Limits

Global defaults for rate limiting. Role-specific overrides take precedence.

```yaml
rate_limits:
  default_caller: "100/min"             # Per-caller default
  default_tool: "10/sec"                # Per-caller-per-tool default
  default_upstream: "1000/min"          # Per-upstream default
```

Rate limit format: `N/unit` where unit is `sec`, `min`, or `hour`.

### Logging

```yaml
logging:
  level: "info"    # debug, info, warn, error
  format: "json"   # json or text
```

### Audit

```yaml
audit:
  enabled: true
  database: "audit.db"   # SQLite database for audit entries
```

### Telemetry

```yaml
telemetry:
  traces:
    enabled: false              # opt-in (default: false)
    endpoint: "localhost:4318"  # OTLP HTTP endpoint
    sample_rate: 1.0            # 0.0 to 1.0
```

## Authentication Setup

Stile uses API keys with SHA-256 hashing and a SQLite-backed caller store.

**1. Enable auth** by setting `db_path` in config and defining roles:

```yaml
server:
  db_path: "stile.db"
roles:
  reader:
    allowed_tools: ["search/*"]
```

**2. Create callers** via the CLI:

```bash
./stile add-caller -name alice -db stile.db
./stile add-key -caller alice -label "dev-key" -db stile.db
# Outputs: sk-<hex key>
./stile assign-role -caller alice -role reader -db stile.db
```

Or via the Admin API (see below).

**3. Use the key** in requests:

```bash
curl -X POST http://localhost:8080/mcp \
  -H "Authorization: Bearer sk-<your-key>" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"tools/list","id":1}'
```

Callers only see tools matching their roles' `allowed_tools` patterns. Calls to tools outside their access are rejected with a JSON-RPC error.

## Admin Endpoints

Admin endpoints require the `ADMIN_API_KEY` environment variable to be set. Without it (and with no callers), they run in open dev mode.

```bash
export ADMIN_API_KEY="my-admin-secret"
```

### Tool Cache

| Method | Path | Description |
|--------|------|-------------|
| POST | `/admin/refresh` | Refresh tool cache from all upstreams |

### Caller Management

| Method | Path | Description |
|--------|------|-------------|
| POST | `/admin/callers` | Create a caller (`{"name": "..."}`) |
| GET | `/admin/callers` | List all callers |
| GET | `/admin/callers/{name}` | Get caller details |
| DELETE | `/admin/callers/{name}` | Delete a caller |
| POST | `/admin/callers/{name}/keys` | Create API key (`{"label": "..."}`) |
| GET | `/admin/callers/{name}/keys` | List caller's keys |
| DELETE | `/admin/callers/{name}/keys/{id}` | Delete a key |
| POST | `/admin/callers/{name}/roles` | Assign role (`{"role": "..."}`) |
| GET | `/admin/callers/{name}/roles` | List caller's roles |
| DELETE | `/admin/callers/{name}/roles/{role}` | Unassign role |

All admin endpoints require `Authorization: Bearer <ADMIN_API_KEY>`.

## Observability

See [docs/observability.md](docs/observability.md) for the full observability guide covering traces, metrics, and log correlation.

### Distributed Tracing

Opt-in via the `telemetry` config section. Exports OTLP traces to any compatible backend (Tempo, Jaeger, Datadog, etc.):

```yaml
telemetry:
  traces:
    enabled: true
    endpoint: "localhost:4318"
    sample_rate: 1.0
```

### Logging

Structured JSON logs to stderr by default. When tracing is enabled, logs automatically include `trace_id` and `span_id` fields for correlation.

### Prometheus Metrics

Available at `GET /metrics`:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `stile_requests_total` | Counter | caller, tool, upstream, status | Total tool call requests |
| `stile_request_duration_seconds` | Histogram | caller, tool, upstream | Request latency |
| `stile_upstream_health` | Gauge | upstream | 1 = healthy, 0 = unhealthy |
| `stile_rate_limit_rejections_total` | Counter | caller, tool | Rate limit denials |
| `stile_tool_cache_refresh_total` | Counter | upstream, status | Tool discovery refresh attempts |

### Audit Log

When enabled, every `tools/call` is logged to a SQLite database with: timestamp, caller, method, tool, upstream, params, status, and latency.

## Deployment

### Docker

The default `Dockerfile` builds a minimal image (distroless, just the Stile binary) for HTTP-only upstreams:

```bash
docker build -t stile .
docker run -p 8080:8080 -v ./config.yaml:/etc/stile/config.yaml:ro stile
```

### Docker Compose

```bash
docker compose up
```

### Full example with stdio upstreams

`Dockerfile.example` and `docker-compose.example.yml` show a complete setup with GitHub, Filesystem, and Fetch MCP servers (stdio) plus a mock SSE server (HTTP). This requires Node.js and Python in the image:

```bash
export GITHUB_PERSONAL_ACCESS_TOKEN="$(gh auth token)"
export ADMIN_API_KEY="pick-a-secret"
docker compose -f docker-compose.example.yml up --build
```

See `configs/example.yaml` for the matching config with auth, roles, rate limiting, and audit logging.

### Health Check Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /healthz` | Liveness probe (always 200) |
| `GET /readyz` | Readiness probe (200 if at least one upstream healthy, 503 otherwise) |

Compatible with Kubernetes liveness/readiness probes:

```yaml
livenessProbe:
  httpGet:
    path: /healthz
    port: 8080
readinessProbe:
  httpGet:
    path: /readyz
    port: 8080
```

### Graceful Shutdown

Stile handles `SIGINT`/`SIGTERM` by draining in-flight requests before exiting.

### TLS / Transport Security

Stile does not terminate TLS natively (this is planned — see task 26). In production, terminate TLS at the layer in front of Stile:

- **Cloud load balancer** (AWS ALB, GCP HTTPS LB, Azure App Gateway) — the standard approach. TLS is handled for you; Stile receives plaintext on a private network.
- **Reverse proxy sidecar** — for bare-metal or VM deployments without a managed LB:
  - **[Caddy](https://caddyserver.com/)** — automatic HTTPS with Let's Encrypt, zero config. Recommended for simplicity.
    ```
    stile.example.com {
        reverse_proxy localhost:8080
    }
    ```
  - **nginx** — widely deployed, well-understood:
    ```nginx
    server {
        listen 443 ssl;
        ssl_certificate     /etc/ssl/cert.pem;
        ssl_certificate_key /etc/ssl/key.pem;
        location / {
            proxy_pass http://127.0.0.1:8080;
        }
    }
    ```
- **Service mesh** (Istio, Linkerd) — if you're in Kubernetes and need mTLS between services, the mesh sidecar handles it transparently. Stile still listens on plaintext.

In all cases, Stile should listen on `127.0.0.1` or a private network interface — never bind to `0.0.0.0` without TLS in front.

## Running Tests

```bash
go build ./...                              # Build everything
go test ./...                               # Run all tests (unit + integration)
go test ./tests/integration/ -v -count=1    # Run integration tests only
go vet ./...                                # Static analysis
```

## Architecture

See [docs/stile-design-doc.md](docs/stile-design-doc.md) for the full design and [docs/request-flow.md](docs/request-flow.md) for an end-to-end request walkthrough.

```
cmd/gateway/         Entrypoint (config, wiring, signal handling, CLI)
internal/
  jsonrpc/           JSON-RPC 2.0 codec
  config/            YAML config loading (immutable types with getters)
  transport/         Transport interface + HTTP and stdio implementations
  router/            Tool discovery, routing, caching
  auth/              API key auth, caller store, role-based access control
  policy/            Token bucket rate limiting (per-caller, per-tool, per-upstream)
  proxy/             Core proxy handler
  server/            Inbound HTTP server
  health/            Upstream health checks, /healthz and /readyz
  admin/             Admin API for caller/key management
  audit/             Append-only audit log (SQLite)
  metrics/           Prometheus metrics (OTel API + Prometheus exporter)
  telemetry/         OpenTelemetry tracer provider setup
  logging/           slog trace-correlation handler
tests/integration/   End-to-end integration tests
configs/             Example config files
docs/                Design doc, task definitions, request flow
```
