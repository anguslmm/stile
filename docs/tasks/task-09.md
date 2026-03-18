# Task 9: Health Checks + Graceful Shutdown + Hardening

**Status:** not started
**Depends on:** Task 8 (observability — health status feeds metrics)
**Needed by:** Task 10 (integration tests assume hardened behavior)

---

## Goal

Make the gateway production-ready: health check endpoints, graceful shutdown with request draining, config hot-reload, and robust upstream health monitoring. After this task, the gateway can be deployed behind a load balancer and operated safely.

---

## 1. Health Check Endpoints

### Package: `internal/health`

Two standard Kubernetes-style endpoints:

**`GET /healthz` — Liveness probe**
- Returns `200 OK` if the gateway process is running and responsive
- Returns `503` only if the process is wedged (this is a simple "am I alive" check)
- Response body: `{"status": "ok"}`

**`GET /readyz` — Readiness probe**
- Returns `200 OK` if the gateway is ready to serve traffic:
  - At least one upstream is healthy
  - Initial tool discovery has completed (even if some upstreams failed)
- Returns `503` if no upstreams are available
- Response body includes detail:
```json
{
  "status": "ready",
  "upstreams": {
    "github": {"healthy": true, "tools": 12, "stale": false},
    "local-db": {"healthy": true, "tools": 2, "stale": false}
  }
}
```

### Upstream health monitoring

Replace the stub `Healthy()` implementations:

**HTTP transport:** Periodically send a `ping` JSON-RPC request (or just check that the last `tools/list` refresh succeeded). Mark unhealthy after N consecutive failures (default: 3).

**Stdio transport:** Check that the process is running. Optionally send a `ping` and verify a response within a timeout.

Track health status per upstream and expose it via:
- The `/readyz` endpoint
- The `stile_upstream_health` Prometheus gauge (from Task 8)

---

## 2. Graceful Shutdown

### Current state

Task 3 added basic signal handling (SIGINT/SIGTERM → `server.Shutdown()`). This task makes it robust.

### Shutdown sequence

When SIGINT or SIGTERM is received:

1. **Stop accepting new connections** — call `http.Server.Shutdown()` which stops the listener
2. **Drain in-flight requests** — `Shutdown()` waits for active requests to complete, up to a configurable timeout (default: 30 seconds)
3. **Stop background goroutines** — cancel the router's refresh ticker, health check loops, etc.
4. **Close transports** — shut down all upstream connections:
   - HTTP: close idle connections
   - Stdio: send SIGTERM to child processes, wait, then SIGKILL
5. **Close audit log** — flush any buffered audit entries
6. **Exit**

### Config

```yaml
server:
  address: ":8080"
  shutdown_timeout: 30s
```

### In-flight request tracking

Use the `http.Server`'s built-in shutdown behavior — it already tracks active connections. For SSE streams that might run long, set a maximum stream duration or respect the shutdown timeout (whichever comes first).

---

## 3. Config Hot-Reload

### Trigger: SIGHUP or admin endpoint

**`POST /admin/reload`** — triggers a config reload and returns the result. This endpoint is protected by the admin auth middleware (see Task 6 — same `ADMIN_API_KEY` mechanism as `/admin/refresh`).

**SIGHUP signal** — same behavior, triggered from the terminal or a process manager.

### What can be reloaded

- Upstream list (add/remove upstreams)
- Caller list (add/remove callers, change ACLs)
- Rate limit values
- Tool policy (blocklist/allowlist)
- Logging level
- Audit settings

### What requires a restart

- Server listen address
- Fundamental config structure changes

### Reload process

1. Load and validate the new config file (same `config.Load()`)
2. If validation fails → log the error, keep running with old config, return error to caller
3. If valid → swap in the new config:
   - Create/remove transports for added/removed upstreams
   - Rebuild the authenticator with new callers
   - Update rate limiter configuration
   - Trigger a router refresh for new upstreams
4. Log the successful reload

**Atomicity:** the reload either fully succeeds or fully fails. Don't partially apply a new config.

### Admin endpoint response

```json
{
  "status": "ok",
  "changes": {
    "upstreams_added": ["new-server"],
    "upstreams_removed": [],
    "callers_added": ["new-agent"],
    "callers_removed": []
  }
}
```

---

## 4. Stdio Process Hardening

Improve the stdio transport's process management:

- **Restart with backoff:** if a process crashes repeatedly, use exponential backoff (1s, 2s, 4s, ... up to 60s) before restarting
- **Startup timeout:** if the process doesn't respond to an initial `ping` within 10 seconds, consider it failed
- **Stderr capture:** pipe stderr to the gateway's logger at WARN level, with the upstream name as a log field
- **Resource limits:** set a maximum restart count (default: 10) before giving up and marking the upstream as permanently failed

---

## 5. Testable Deliverables

### Health check tests (`internal/health/`)

1. **Liveness always passes:** GET /healthz → 200 with `{"status": "ok"}`
2. **Readiness with healthy upstreams:** all upstreams healthy → 200
3. **Readiness with no upstreams healthy:** all upstreams down → 503
4. **Readiness detail:** response includes per-upstream health info
5. **Upstream health tracking:** mock upstream fails 3 times → marked unhealthy

### Graceful shutdown tests

6. **In-flight request completes:** start a slow request, send shutdown signal → request finishes, then server stops
7. **Shutdown timeout enforced:** start a very slow request, short shutdown timeout → request is cancelled after timeout
8. **Transports closed on shutdown:** after shutdown → stdio processes are terminated, HTTP connections closed

### Config reload tests

9. **Reload via admin endpoint:** POST /admin/reload with valid admin key → new config applied
9a. **Reload without admin key rejected:** POST /admin/reload with no key → 403
10. **Invalid config rejected:** modify config to be invalid, POST reload → error, old config still active
11. **New upstream added on reload:** add upstream to config, reload → new upstream's tools appear in tools/list
12. **Caller change on reload:** add new caller, reload → new caller can authenticate

### Stdio hardening tests

13. **Restart with backoff:** kill process repeatedly → restart intervals increase
14. **Startup timeout:** mock server that hangs on start → transport reports unhealthy

### Build check

```bash
go build ./...
go test ./...
go vet ./...
```

---

## 6. Out of Scope

- Blue-green deployment support
- Clustering / multi-instance coordination
- External health check services
