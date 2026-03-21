# Horizontal Scaling Guide

This guide covers deploying multiple Stile instances behind a load balancer for high availability and throughput.

## Architecture Overview

Stile is designed to run as multiple stateless instances behind a load balancer. Shared state is externalized:

- **Postgres** stores caller/key data and audit logs — all instances read from the same database
- **Redis** provides global rate limiting — counters are shared across instances
- **Each instance** independently health-checks its upstreams and maintains its own tool cache

```
                    ┌─────────────┐
                    │ Load Balancer│
                    └──────┬──────┘
               ┌───────────┼───────────┐
               ▼           ▼           ▼
          ┌─────────┐ ┌─────────┐ ┌─────────┐
          │ Stile 1 │ │ Stile 2 │ │ Stile 3 │
          └────┬────┘ └────┬────┘ └────┬────┘
               │           │           │
       ┌───────┴───────────┴───────────┴───────┐
       │                                       │
  ┌────▼────┐  ┌────▼────┐  ┌─────────────────▼──┐
  │ Postgres│  │  Redis  │  │ HTTP MCP upstreams  │
  └─────────┘  └─────────┘  └────────────────────┘
```

## Prerequisites

| Component | Purpose | Task |
|-----------|---------|------|
| Postgres | Shared caller/key store and audit log | [Task 17](tasks/task-17.md) |
| Redis | Global rate limiting across instances | [Task 18](tasks/task-18.md) |
| Load balancer | Distribute traffic (nginx, HAProxy, ALB, etc.) | Any |

## Example Config

A complete multi-instance configuration with Postgres and Redis:

```yaml
server:
  address: ":8080"
  database:
    driver: postgres
    dsn: "postgres://stile:secret@db.internal:5432/stile?sslmode=require"
  tool_cache_ttl: "5m"
  shutdown_timeout: "30s"

upstreams:
  - name: code-tools
    transport: streamable-http
    url: https://mcp-code.internal/v1
    auth:
      type: bearer
      token_env: CODE_TOOLS_TOKEN
    rate_limit: "2000/min"

  - name: search
    transport: streamable-http
    url: https://mcp-search.internal/v1
    rate_limit: "500/min"

roles:
  agent:
    allowed_tools: ["*"]
    rate_limit: "200/min"
    tool_rate_limit: "30/sec"

rate_limits:
  backend: redis
  redis:
    address: "redis.internal:6379"
    password: ""
    db: 0
    key_prefix: "stile:"
  default_caller: "100/min"
  default_tool: "20/sec"
  default_upstream: "1000/min"

logging:
  level: "info"
  format: "json"

audit:
  enabled: true
  driver: postgres
  database: "postgres://stile:secret@db.internal:5432/stile?sslmode=require"

telemetry:
  traces:
    enabled: true
    endpoint: "otel-collector.internal:4318"
    sample_rate: 0.1
```

Every instance runs with the same config file. No instance-specific configuration is needed.

## Stdio Transport Considerations

HTTP and stdio upstreams behave very differently in a multi-instance deployment:

**HTTP/Streamable-HTTP upstreams** work seamlessly. All Stile instances connect to the same remote MCP servers. Requests are stateless and can land on any instance.

**Stdio upstreams** spawn a local child process per Stile instance. With N instances, you get N copies of each stdio MCP server:

| Scenario | What happens |
|----------|-------------|
| Stateless tools (file readers, calculators, linters) | Works fine — each copy is independent and equivalent |
| Expensive tools (LLM-backed, GPU, large memory) | N copies means N times the resource usage |
| Stateful tools (tools with in-memory state or local storage) | Each copy has independent state — requests hitting different instances see different state |

**Recommendation:** For production multi-instance deployments, use HTTP upstreams exclusively. Convert stdio servers to HTTP using a wrapper.

### Wrapping stdio servers as HTTP

If you have a stdio MCP server that needs to be shared across instances, run it behind a thin HTTP adapter as a standalone service or sidecar:

```
stdio MCP server --> HTTP wrapper (runs once) --> Stile instances connect via HTTP
```

Community tools that do this:

- **[supergateway](https://github.com/supercorp-ai/supergateway)** — wraps any stdio MCP server as a Streamable HTTP server
- **[mcp-proxy](https://github.com/sparfenyuk/mcp-proxy)** — bidirectional proxy between stdio and SSE/Streamable HTTP

Example: wrapping a stdio server with supergateway and pointing Stile at it:

```bash
# Run the wrapper (once, as a service)
npx -y supergateway --stdio "npx -y @modelcontextprotocol/server-github" --port 9090
```

```yaml
# In your Stile config, use HTTP instead of stdio
upstreams:
  - name: github
    transport: streamable-http
    url: http://github-wrapper.internal:9090
```

## Load Balancer Configuration

Stile requires no sticky sessions — all shared state is in Postgres and Redis. Any load balancing strategy works (round-robin, least-connections, etc.).

### Health check endpoints

| Endpoint | Returns | Use for |
|----------|---------|---------|
| `GET /healthz` | Always 200 | Liveness probe — is the process alive? |
| `GET /readyz` | 200 if at least one upstream healthy, 503 otherwise | Readiness probe — should this instance receive traffic? |

### Example: nginx

```nginx
upstream stile {
    server stile-1.internal:8080;
    server stile-2.internal:8080;
    server stile-3.internal:8080;
}

server {
    listen 443 ssl;
    ssl_certificate     /etc/ssl/cert.pem;
    ssl_certificate_key /etc/ssl/key.pem;

    location / {
        proxy_pass http://stile;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }
}
```

### Example: Kubernetes

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: stile
spec:
  replicas: 3
  selector:
    matchLabels:
      app: stile
  template:
    metadata:
      labels:
        app: stile
    spec:
      containers:
        - name: stile
          image: stile:latest
          ports:
            - containerPort: 8080
          livenessProbe:
            httpGet:
              path: /healthz
              port: 8080
          readinessProbe:
            httpGet:
              path: /readyz
              port: 8080
          volumeMounts:
            - name: config
              mountPath: /etc/stile
      volumes:
        - name: config
          configMap:
            name: stile-config
---
apiVersion: v1
kind: Service
metadata:
  name: stile
spec:
  selector:
    app: stile
  ports:
    - port: 80
      targetPort: 8080
```

## Scaling Guidelines

### CPU

Each instance handles proxying independently. Stile is I/O-bound (waiting on upstreams), so a single instance handles many concurrent requests. Scale horizontally when you need more throughput or redundancy.

### Memory

Primary memory consumers are the tool cache (small, per-upstream) and in-flight request bodies. Use `max_body_bytes` (if configured) to cap per-request memory. Memory usage per instance is typically low.

### Redis

The rate limiter adds approximately 3 Redis round-trips per request (caller limit, tool limit, upstream limit). For high-throughput deployments:

- Use a Redis instance close to your Stile instances (same region/VPC)
- Monitor Redis latency — it's in the critical path for every request
- Consider Redis Cluster or a managed Redis service for HA

### Postgres

Auth lookups happen per-request but are keyed by the caller's API key hash. Connection pooling keeps this efficient. For very high throughput:

- Use a connection pooler like PgBouncer between Stile and Postgres
- Audit log writes are append-only and low-contention

### Monitoring

With multiple instances, aggregate metrics and logs:

- **Prometheus**: each instance exposes `/metrics` — use service discovery to scrape all instances
- **Logs**: all instances emit structured JSON logs to stderr — ship to a central log aggregator
- **Traces**: configure the OTLP endpoint to point at a shared collector (Tempo, Jaeger, Datadog)
