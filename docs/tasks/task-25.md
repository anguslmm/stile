# Task 25: Centralized Health Checks

**Status:** todo
**Depends on:** 9, 18, 19

---

## Goal

Move upstream health checking out of individual Stile gateway instances and into a single centralized process. Today every gateway replica independently polls every upstream, which multiplies health-check traffic by the number of replicas. A centralized approach checks each upstream once and publishes the results to a shared store (Redis) that all gateway instances read.

---

## Problem

With N gateway replicas and M upstreams, the current design sends N*M health-check requests. This wastes upstream capacity and can look like a DDoS from the gateway fleet. It also means each replica may have a slightly different view of upstream health during transient failures, leading to inconsistent routing.

---

## Design Options

### Option A: Separate health-check sidecar

A small standalone binary (`stile-healthcheck` or a subcommand like `stile health-agent`) that:

1. Reads the same Stile config file to discover upstreams
2. Runs the health-check loop (reusing `internal/health` logic)
3. Writes results to Redis with a TTL (e.g. `stile:health:<upstream-name> = {status, last_checked, latency_ms}`, TTL = 2x check interval)
4. Gateway instances read from Redis instead of polling upstreams directly

**Pros:** Simple, stateless, no coordination needed — just run one instance (or two for redundancy). Easy to deploy as a Kubernetes CronJob or sidecar.

**Cons:** Another binary to deploy and monitor. If the health-checker dies, cached results expire and gateways must decide on a fallback (assume healthy? assume unhealthy?).

### Option B: Leader election among gateway instances

One gateway replica is elected leader and runs health checks on behalf of the fleet, publishing to Redis.

**Pros:** No separate binary.

**Cons:** Leader election adds significant complexity (Redis-based locking, lease renewal, split-brain handling). If the leader is slow or partitioned, health data goes stale. Not worth the complexity for a stateless proxy.

### Recommendation

**Option A** (separate sidecar). Leader election is over-engineered for this use case. A simple process that reads config, checks health, writes to Redis is easy to reason about, test, and operate.

---

## Implementation Plan

### 1. Health result cache interface

Define an interface in `internal/health/` for reading and writing health status:

```go
type StatusStore interface {
    Get(ctx context.Context, upstream string) (Status, error)
    Set(ctx context.Context, upstream string, status Status, ttl time.Duration) error
}
```

With two implementations:
- `LocalStore` — in-process (current behavior, for single-instance deployments)
- `RedisStore` — reads/writes health status to Redis (reuse the Redis connection from rate limiting)

### 2. Gateway: read health from store

When `health.store: redis` is configured, the gateway's health checker stops polling upstreams and instead reads cached results from Redis. The `/healthz` and `/readyz` endpoints use the store. If a key is missing or expired, treat the upstream as unknown (configurable: healthy-by-default or unhealthy-by-default).

### 3. Health-check agent

A new `cmd/healthagent/main.go` (or `stile health-agent` subcommand) that:

- Loads config (reuses `internal/config`)
- Creates transports for each upstream (reuses `internal/transport`)
- Runs the health-check loop (reuses `internal/health`)
- Writes results to Redis via `RedisStore`
- Exposes its own `/healthz` for liveness

### 4. Config additions

```yaml
health:
  store: local          # or "redis"
  check_interval: 10s
  redis:                # only needed for store: redis
    # inherits from top-level redis config, or can override
```

### 5. Fallback behavior

When using Redis store and a health key is missing/expired:

- Default: treat upstream as healthy (fail-open) to avoid outages if the health agent is down
- Configurable via `health.missing_status: healthy | unhealthy`

---

## Verification

- `go build ./...` passes for both gateway and health agent
- `go test ./...` passes
- Health agent writes status to Redis with correct TTL
- Gateway reads cached health status instead of polling upstreams
- When health agent stops, keys expire and gateway falls back to configured default
- Single-instance deployments still work with `store: local` (no Redis needed)
