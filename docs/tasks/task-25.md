# Task 25: Operational Runbooks

**Status:** todo
**Depends on:** 16, 19, 20, 21

---

## Goal

Write runbooks for common production failure scenarios so that an oncall engineer who has never worked on Stile can diagnose and resolve issues without escalating. This is a documentation task, not a code task.

---

## 1. Create `docs/runbooks/` directory

Each runbook follows a consistent structure:

```markdown
# <Alert / Symptom>

## Severity
P1 / P2 / P3

## Symptoms
What the oncall engineer observes (alert text, dashboard signals, user reports).

## Likely Causes
Ordered by probability.

## Diagnosis Steps
Specific commands and queries to narrow down the cause.

## Remediation
Step-by-step fix for each cause.

## Escalation
When and who to escalate to if remediation doesn't resolve.
```

---

## 2. Runbooks to write

### Upstream failures

**`upstream-unhealthy.md`** — One or more upstreams marked unhealthy.
- Check `/readyz`, upstream health dashboard
- Verify upstream is reachable from the Stile host
- Check circuit breaker state (task 21 metrics)
- Remediation: fix upstream, or remove from config and rolling restart if permanently gone

**`upstream-high-latency.md`** — Upstream responding slowly, causing timeouts.
- Check per-upstream latency metrics and traces
- Look for upstream resource exhaustion
- Remediation: increase per-upstream timeout (short-term), fix upstream (long-term)

### Rate limiting

**`rate-limit-exhaustion.md`** — Callers hitting rate limits unexpectedly.
- Check `stile_rate_limit_rejections_total` by caller and tool
- Verify rate limit config is correct
- Check for runaway agents making excessive calls
- Remediation: adjust limits, identify and fix misbehaving caller

### Database

**`database-connection-exhausted.md`** — Auth or audit DB connections maxed out.
- Check connection pool metrics
- Look for long-running queries or lock contention
- Remediation: increase pool size, investigate slow queries, restart if deadlocked

### Redis (if using Redis rate limiting)

**`redis-unavailable.md`** — Redis for rate limiting is unreachable.
- Rate limiter should fail closed (all requests denied)
- Check Redis connectivity, memory, replication status
- Remediation: restore Redis, or temporarily switch to local rate limiting with config change and rolling restart if global enforcement can be relaxed

### TLS

**`tls-certificate-expiry.md`** — TLS certificates approaching expiration.
- Check cert expiry dates
- Remediation: rotate certs, reload config (SIGHUP) or rolling restart

### Memory / resource

**`high-memory-usage.md`** — Stile instance consuming excessive memory.
- Check goroutine count (possible leak from unclosed SSE streams)
- Check request body sizes
- Check tool cache size
- Remediation: identify leak source, restart instance (short-term)

### Config

**`config-reload-failure.md`** — Config reload via SIGHUP or admin API failed.
- Check structured logs for reload error details
- Common causes: YAML syntax error, invalid upstream URL, missing TLS cert file
- Remediation: fix config file, retry reload

---

## 3. Link from operations docs

Add a "Troubleshooting" section to `docs/horizontal-scaling.md` (task 19) and the README linking to the runbooks directory.

---

## Verification

- Each runbook references specific metrics, endpoints, or log fields that exist in the codebase
- Runbooks are reviewed by someone unfamiliar with Stile to verify clarity
- All referenced config fields and commands are accurate
