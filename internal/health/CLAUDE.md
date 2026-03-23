# health

Upstream health monitoring and Kubernetes-style liveness/readiness HTTP endpoints.

## Key Types

- **`Checker`** — Core type. Periodically polls upstreams, tracks health state, and serves `/healthz` and `/readyz`. Created via `NewChecker`.
- **`UpstreamInfo`** — Input descriptor: transport reference, tool-count and staleness callbacks, and a `Local` flag distinguishing stdio from HTTP upstreams.
- **`UpstreamHealth`** — Per-upstream health snapshot (healthy bool, tool count, stale flag); used in readiness responses.
- **`ReadinessResponse`** — JSON body for `/readyz`: overall status string plus per-upstream `UpstreamHealth` map.
- **`StatusStore`** (interface) — Abstraction for reading/writing health state externally. Two implementations: `LocalStore` (in-memory) and `RedisStore` (Redis-backed with TTL).
- **`Status`** — Stored health record: `Healthy bool` + `CheckedAt time.Time`.

## Key Functions / Methods

- **`NewChecker(upstreams, metrics, opts...)`** — Constructor; accepts functional options.
- **`(*Checker).Start()` / `Stop()`** — Background polling goroutine lifecycle.
- **`(*Checker).HandleLiveness(w, r)`** — Always 200; suitable for liveness probe.
- **`(*Checker).HandleReadiness(w, r)`** — 200 if ready, 503 if not; includes upstream detail.
- **`(*Checker).IsReady()`** — Returns true if discovery is done and at least one upstream is healthy.
- **`(*Checker).UpdateUpstreams(upstreams)`** — Hot-replaces the upstream list without restarting.
- **`(*Checker).CheckNow(ctx)`** — Synchronous check; primarily for tests.
- **`NewLocalStore()` / `NewRedisStore(client, prefix)`** — Store constructors.

## Design Notes

- **Fail threshold**: an upstream is only marked unhealthy after 3 consecutive failures (configurable via `failThreshold`, not currently exported as an option). Recovery is immediate on the first success.
- **Local vs remote split**: stdio upstreams (`Local: true`) are always checked in-process via `Transport.Healthy()`. Only HTTP upstreams participate in store-backed (Redis) health sharing.
- **Active probe mode** (`WithActiveProbe`): the health agent never sends real MCP traffic, so `Transport.Healthy()` would never update. Active probing sends a JSON-RPC `ping` via `RoundTrip` to drive the transport's internal health tracking.
- **Store modes**: `WithReadFromStore(true)` puts the checker in passive/gateway mode (reads from store); without it, the checker writes results to the store (health agent mode). Default is fail-open (`missingStatus = true`) when store lookup fails.
- **`storeTTL` default** is 2× the check interval, ensuring a missed check cycle does not expire a healthy entry prematurely.
