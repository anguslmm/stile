# Redis Unavailable

## Severity

P1 (all requests are denied when Redis rate limiting is configured and Redis is down)

## Symptoms

- All requests are rejected regardless of rate limit headroom
- Logs at ERROR level: `"redis rate limit check failed, denying request (fail-closed)"`
- `stile_rate_limit_rejections_total` spikes across all callers and tools
- Redis monitoring shows the instance is unreachable or unhealthy

## Likely Causes

1. **Redis process crashed or was OOM-killed** — check the Redis host.
2. **Network partition** — Stile cannot reach the Redis instance.
3. **Redis memory exhaustion** — Redis hit `maxmemory` and is evicting or rejecting writes.
4. **Redis authentication failure** — password was rotated without updating the Stile config.
5. **DNS resolution failure** — the Redis hostname no longer resolves.

## Diagnosis Steps

1. Check Stile logs for Redis errors:
   ```bash
   journalctl -u stile | grep -i redis | tail -20
   ```

2. Test Redis connectivity from the Stile host:
   ```bash
   redis-cli -h <redis-host> -p <redis-port> -a <password> ping
   ```

3. Check Redis status:
   ```bash
   redis-cli -h <redis-host> info server
   redis-cli -h <redis-host> info memory
   redis-cli -h <redis-host> info replication
   ```

4. Check Redis memory usage:
   ```bash
   redis-cli -h <redis-host> info memory | grep used_memory_human
   redis-cli -h <redis-host> info memory | grep maxmemory_human
   ```

5. Check network connectivity:
   ```bash
   nc -zv <redis-host> <redis-port>
   ```

6. Verify the Redis config in Stile:
   ```yaml
   rate_limits:
     backend: redis
     redis:
       address: "<host>:<port>"
       password: "..."
       db: 0
       key_prefix: "stile:"
   ```

## Remediation

**Restore Redis (preferred):**
- Restart the Redis process or service.
- If OOM-killed, increase memory limits or `maxmemory`.
- Once Redis is back, Stile will automatically resume normal rate limiting on the next request (no Stile restart needed).

**Switch to local rate limiting (emergency):**

If Redis cannot be restored quickly and denying all requests is unacceptable:

1. Update the Stile config to use local rate limiting:
   ```yaml
   rate_limits:
     backend: local    # was: redis
     # redis: ...      # can leave in place, will be ignored
     default_caller: "100/min"
     default_tool: "20/sec"
     default_upstream: "1000/min"
   ```
2. Rolling restart all Stile instances.

**Important trade-off:** Local rate limiting means each Stile instance tracks limits independently. With N instances, the effective global limit is N times the configured limit. This is acceptable as a temporary measure but should not be left in place long-term for multi-instance deployments.

**Redis authentication issue:**
- Update the `redis.password` in the config and restart Stile.

**Redis memory exhaustion:**
- Flush rate limit keys if safe (they are ephemeral sliding windows):
  ```bash
  redis-cli -h <redis-host> --scan --pattern "stile:rl:*" | xargs redis-cli -h <redis-host> del
  ```
- Increase `maxmemory` or add an eviction policy.

## Escalation

- If Redis is managed (ElastiCache, Memorystore, etc.), open a support ticket with the cloud provider.
- If Redis is self-hosted, escalate to the infrastructure team.
- If the outage is prolonged and local rate limiting is insufficient, escalate to the Stile team to discuss fail-open options.
