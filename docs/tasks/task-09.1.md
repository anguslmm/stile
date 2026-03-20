# Task 9.1: Cleanup — main.go and wiring layer

**Status:** not started
**Depends on:** Task 9 (health checks, graceful shutdown, config reload)
**Needed by:** Task 10 (cleaner foundation for further work)

---

## Goal

Task 9 introduced health checks, config hot-reload, graceful shutdown, and stdio hardening. The internal packages are clean, but the wiring layer (main.go, server.go auth middleware, proxy.go mutex) accumulated mess during iterative development. This task is a focused cleanup — no behavior changes, just structure.

---

## 1. Clean up `reload` in main.go

The `reload` function has 8 parameters, 3 of which are unused (`_ context.Context`, `_ *config.Config`, `_ *metrics.Metrics`). It's also called identically from two places (the `reloadFunc` closure and the SIGHUP handler).

**Fix:**
- Remove the free `reload` function entirely.
- Make the reload logic a closure in `main()` that captures the variables it needs (`configPath`, `rt`, `handler`, `opts`, `hc`). This eliminates the parameter list problem.
- Both the `reloadFunc` (for `/admin/reload`) and the SIGHUP handler should call this single closure.

---

## 2. Deduplicate transport builders

`buildTransports` and `buildTransportsForNames` contain the same transport type switch/case. One takes a full config and builds all transports; the other takes a config + name filter and builds a subset.

**Fix:**
- Unify into a single `buildTransports(cfg *config.Config, filter map[string]bool) (map[string]transport.Transport, error)` where a nil filter means "build all". The startup path passes nil; the reload path passes the added names.
- The error handling differs slightly (startup logs and skips; reload returns an error and cleans up). Make this a parameter or have the caller handle the difference.

---

## 3. Fix per-request middleware allocation in server.go

The current code for swappable auth is:
```go
mcpHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    s.mu.RLock()
    a := s.authenticator
    s.mu.RUnlock()
    a.Middleware(http.HandlerFunc(s.handleMCP)).ServeHTTP(w, r)
})
```

This allocates a new middleware handler chain on every request.

**Fix:**
- Instead, inline the auth logic: read the authenticator under the lock, call `a.Authenticate(r)`, and if it succeeds, set the caller in the context and call `s.handleMCP`. This is what `Middleware` does internally — just do it directly to avoid the allocation.

---

## 4. Use `atomic.Pointer` in proxy.go

`proxy.Handler` uses a `sync.RWMutex` just to protect `SetRateLimiter`/reading the rate limiter pointer. This is a textbook case for `atomic.Pointer[policy.RateLimiter]`.

**Fix:**
- Replace `mu sync.RWMutex` + `rateLimiter *policy.RateLimiter` with `rateLimiter atomic.Pointer[policy.RateLimiter]`.
- `SetRateLimiter` becomes `h.rateLimiter.Store(rl)`.
- The read path becomes `rl := h.rateLimiter.Load()`.

---

## 5. Testable Deliverables

No new tests needed — this is a pure refactor. All existing tests (unit + integration) must continue to pass:

```bash
go build ./...
go test ./...
go vet ./...
./scripts/test-health-reload.sh
```

---

## 6. Out of Scope

- Behavioral changes to health checks, reload, or shutdown
- Changes to internal packages (health, transport, config, router)
- New features
