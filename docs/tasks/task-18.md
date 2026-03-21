# Task 18: Config Reload Broadcast

**Status:** todo
**Depends on:** 16, 17

---

## Goal

When one Stile instance reloads its config (via SIGHUP or the admin `/admin/reload` endpoint), broadcast the event so all other instances reload too. Without this, operators must SIGHUP every instance individually, which is error-prone and leaves instances running different configs.

---

## Problem

Config reload is currently triggered per-instance via SIGHUP or the admin API. In a multi-instance deployment, only the targeted instance reloads. The others continue running the old config until manually signaled, leading to divergent behavior.

---

## 1. Choose a broadcast mechanism

Support two backends, matching what's already available in the deployment:

### Option A: Postgres LISTEN/NOTIFY (if using Postgres for auth)

- After a successful reload, publish: `NOTIFY stile_reload`
- Each instance listens: `LISTEN stile_reload`
- On notification, trigger the existing `reload()` closure
- Zero additional dependencies — uses the Postgres connection already present

### Option B: Redis pub/sub (if using Redis for rate limiting)

- After a successful reload, publish to channel `stile:reload`
- Each instance subscribes to `stile:reload`
- On message, trigger the existing `reload()` closure
- Zero additional dependencies — uses the Redis connection already present

---

## 2. Config

```yaml
server:
  reload_broadcast: auto  # "auto" | "postgres" | "redis" | "none"
```

- `auto` (default): use Postgres if configured, else Redis if configured, else none
- `none`: disable broadcast (single-instance or manual SIGHUP)

---

## 3. Implementation

Create `internal/broadcast/broadcast.go`:

```go
type Broadcaster interface {
    // Publish sends a reload signal to all instances.
    Publish(ctx context.Context) error
    // Subscribe returns a channel that receives a value when a reload is triggered.
    Subscribe(ctx context.Context) (<-chan struct{}, error)
    Close() error
}
```

Implementations:
- `internal/broadcast/postgres.go` — uses `pgx` connection for LISTEN/NOTIFY
- `internal/broadcast/redis.go` — uses go-redis pub/sub

---

## 4. Wire into main.go

After a successful reload (both SIGHUP and admin endpoint paths), call `broadcaster.Publish()`.

At startup, call `broadcaster.Subscribe()` and start a goroutine that triggers `reload()` on each received message. Add deduplication: if this instance just published the reload, don't re-trigger it.

```go
go func() {
    for range reloadCh {
        slog.Info("reload broadcast received, reloading config...")
        result, err := reload(context.Background())
        if err != nil {
            slog.Error("broadcast-triggered reload failed", "error", err)
        } else {
            slog.Info("broadcast-triggered reload complete",
                "upstreams_added", result.UpstreamsAdded,
                "upstreams_removed", result.UpstreamsRemoved,
            )
        }
    }
}()
```

---

## 5. Graceful shutdown

On shutdown, unsubscribe and close the broadcaster connection cleanly.

---

## Verification

- Test Postgres LISTEN/NOTIFY broadcast with two subscribers
- Test Redis pub/sub broadcast with two subscribers
- Test deduplication: publishing instance doesn't double-reload
- Test `auto` selection logic
- Test `none` disables broadcast
- Test graceful shutdown unsubscribes cleanly
