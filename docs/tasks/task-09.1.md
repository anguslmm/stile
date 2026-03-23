# Task 9.1: Cleanup — main.go and wiring layer

**Status:** done
**Depends on:** Task 9 (health checks, graceful shutdown, config reload)
**Needed by:** Task 10 (cleaner foundation for further work)

---

## Goal

Task 9 introduced health checks, config hot-reload, graceful shutdown, and stdio hardening. The internal packages are clean, but the wiring layer (main.go, server.go auth middleware, proxy.go mutex) accumulated mess during iterative development. This task is a focused cleanup — no behavior changes, just structure.

---

## 1. Clean up `reload` in main.go

~~The `reload` function has 8 parameters, 3 of which are unused. Fix by converting to a closure.~~

**Superseded:** The entire config hot-reload mechanism (reload function, SIGHUP handler, `/admin/reload` triggering a config re-parse) was removed in Task 18.1. This cleanup item was completed as part of that removal.

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

~~Replace `sync.RWMutex` with `atomic.Pointer[policy.RateLimiter]` for the swappable rate limiter.~~

**Superseded:** `SetRateLimiter` and the associated mutex were removed entirely in Task 18.1 when config hot-reload was deleted. The rate limiter is now set once at construction and never swapped, so no synchronization is needed.

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
