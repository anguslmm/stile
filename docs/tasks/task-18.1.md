# Task 19.1: Remove Config Hot-Reload Mechanism

**Status:** todo
**Depends on:** 18

---

## Goal

Remove the config hot-reload mechanism (SIGHUP handler, admin reload endpoint, runtime component swapping). This was a poor design decision from a previous agent — it introduces race conditions for no real benefit. Config changes are a deployment concern: update the config, redeploy. Stile is a stateless proxy that starts in under a second; there is no justification for hot-reload complexity.

---

## Problem

The current reload path does multiple unsynchronized swaps (transports, rate limiter, authenticator, health checker). There is a window during reload where a request can see the new routes but the old auth config or old rate limiter. This is a correctness bug hiding behind a feature nobody asked for.

---

## What to remove

### 1. `cmd/gateway/main.go`

- Delete the entire `reload` closure and `reloadMu`
- Delete the SIGHUP branch in the signal handler (keep SIGINT/SIGTERM for graceful shutdown)
- Remove `opts.ReloadFunc` assignment
- Remove `admin.NewHandler`'s `reload` parameter (pass nil or remove the parameter)

### 2. `internal/proxy/proxy.go`

- Remove `SetRateLimiter` — the rate limiter is set once at construction and never changes
- Change `atomic.Pointer[rateLimiterBox]` to a plain `policy.RateLimiter` field — no atomics needed if it's immutable
- Simplify `NewHandler` accordingly

### 3. `internal/server/`

- Remove `ReloadResult` and `ReloadFunc` types
- Remove the `/admin/reload` route and handler
- Remove `Options.ReloadFunc`

### 4. `internal/admin/`

- Remove the reload endpoint from the admin handler
- Remove the reload parameter from `NewHandler`

### 5. Tests

- Remove or update any tests that exercise the reload path
- The integration test helper no longer needs reload wiring

### 6. Docs

- Remove references to SIGHUP reload and `/admin/reload` from `docs/request-flow.md` and any other docs
- Update task 19 (horizontal scaling) if it references reload

---

## Verification

- `go build ./...` and `go test ./...` pass
- SIGHUP no longer triggers a reload (process should ignore it or use default behavior)
- No `/admin/reload` endpoint exists
- Proxy handler has no `SetRateLimiter` or atomic pointer — just a plain field
