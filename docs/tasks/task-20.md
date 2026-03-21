# Task 20: Horizontal Scaling Documentation and Stdio Guidance

**Status:** todo
**Depends on:** 17, 18, 19

---

## Goal

Document how to deploy Stile in a multi-instance configuration, including the architectural constraints around stdio transports, and provide guidance for operators scaling beyond a single node.

---

## 1. Create `docs/horizontal-scaling.md`

Write an operations guide covering:

### Architecture overview

- Stile is designed to run as multiple stateless instances behind a load balancer
- Shared state (auth, audit) lives in Postgres
- Shared rate limiting uses Redis
- Config reload broadcast keeps instances in sync
- Each instance independently health-checks its upstreams

### Prerequisites

- Postgres database (task 17) — required for shared auth/audit
- Redis instance (task 18) — required for global rate limiting
- Load balancer (any — nginx, HAProxy, ALB, etc.)

### Config example

Full multi-instance config example with Postgres, Redis, and broadcast enabled.

### Stdio transport considerations

**This is the key guidance to document clearly:**

- HTTP/Streamable-HTTP upstreams work seamlessly — all instances connect to the same remote MCP servers
- Stdio upstreams spawn a **local child process per instance**. This means:
  - N instances = N copies of each stdio MCP server running
  - Each copy maintains independent state (if the tool server is stateful)
  - This is fine for stateless/lightweight tools (file readers, calculators, etc.)
  - For expensive or stateful tools, **wrap the stdio server in an HTTP adapter** and point Stile at it as an HTTP upstream instead
- Recommend: for production multi-instance deployments, prefer HTTP upstreams exclusively

### Recommended stdio-to-HTTP wrapper pattern

Document or link to a simple wrapper pattern:
```
stdio MCP server → thin HTTP wrapper (runs as a sidecar or service) → Stile connects via HTTP
```

Mention existing community tools like `mcp-proxy` or `supergateway` if applicable, or provide a minimal example.

### Load balancer configuration

- No sticky sessions required (all state is in Postgres/Redis)
- Health check endpoint: `GET /healthz` (returns 200 when process is alive)
- Readiness endpoint: `GET /readyz` (returns 200 when at least one upstream is healthy)
- Recommended: route health checks to `/healthz`, readiness to `/readyz`

### Scaling guidelines

- **CPU-bound**: each instance handles proxying independently; scale horizontally as needed
- **Memory**: primary memory consumers are the tool cache (small) and in-flight request bodies. Set body size limits (task 12) to cap per-request memory
- **Redis**: rate limiter adds ~3 Redis round-trips per request (caller, tool, upstream checks). Use Redis pipelining or a Lua script to reduce to 1 round-trip
- **Postgres**: auth lookups are per-request but cached by the caller's API key hash. Connection pooling (task 14) keeps this efficient

---

## 2. Update README

Add a "Scaling" section to the main README linking to the horizontal scaling doc, with a one-line summary: "Stile supports multi-instance deployment with Postgres and Redis — see docs/horizontal-scaling.md."

---

## Verification

- Doc review: have someone unfamiliar with the codebase follow the guide
- Verify all referenced config fields and endpoints exist
- Verify example config is valid YAML that passes config loading
