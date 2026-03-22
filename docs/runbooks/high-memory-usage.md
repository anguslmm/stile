# High Memory Usage

## Severity

P2 (P1 if OOM kills are occurring)

## Symptoms

- Stile process memory usage is elevated or growing unbounded
- OOM kills in container orchestrator logs or `dmesg`
- Stile instances restarting unexpectedly
- System monitoring shows high RSS for the Stile process

## Likely Causes

1. **Goroutine leak from unclosed SSE streams** — HTTP upstream connections that were never properly closed, accumulating goroutines.
2. **Large request/response bodies** — tools returning very large results (Stile buffers the full response body).
3. **Tool limiter map growth** — many unique (caller, tool) combinations creating rate limiter entries (capped at 1000 per caller, but many callers can still accumulate).
4. **Tool cache with many upstreams/tools** — large number of upstreams each exposing many tools.
5. **Audit log buffering** — if audit writes are slow (database contention), pending entries may accumulate.

## Diagnosis Steps

1. Check current memory usage:
   ```bash
   # Process RSS
   ps -o pid,rss,vsz,comm -p $(pgrep stile)

   # In Kubernetes
   kubectl top pods -l app=stile
   ```

2. Check goroutine count (requires adding pprof — not exposed by default):
   ```bash
   # If you've added pprof to a debug build:
   curl http://localhost:6060/debug/pprof/goroutine?debug=1 | head -5
   ```

3. Check request volume and sizes in logs — look for unusually large responses:
   ```bash
   journalctl -u stile | grep -i 'error\|large\|body' | tail -20
   ```

4. Check the number of unique callers (each gets rate limiter state):
   ```bash
   curl -s -H "Authorization: Bearer $ADMIN_API_KEY" \
     http://<stile-host>:8080/admin/callers | jq 'length'
   ```

5. Check tool cache refresh metrics (large refreshes = many tools in memory):
   ```promql
   stile_tool_cache_refresh_total
   ```

6. Check upstream request rates (high rates = more in-flight memory):
   ```promql
   sum by (upstream) (rate(stile_requests_total[5m]))
   ```

7. Monitor memory over time (if container metrics are available):
   ```promql
   container_memory_working_set_bytes{pod=~"stile.*"}
   ```

## Remediation

**Immediate — restart the instance:**
- If memory is critically high, restart the Stile instance. Stile handles `SIGTERM` gracefully and drains in-flight requests:
  ```bash
  systemctl restart stile

  # In Kubernetes, the deployment handles this via OOM restart
  kubectl delete pod <stile-pod>  # replacement is created automatically
  ```

**Large response bodies:**
- Stile enforces a 10 MB request body limit. If responses from upstreams are large, work with upstream owners to paginate or reduce response sizes.

**Goroutine leak:**
- Check if a specific upstream is associated with the leak (e.g., an SSE upstream that isn't cleanly closing connections).
- Restarting Stile clears all goroutines.
- If the issue recurs with a specific upstream, consider adding a `timeout` to that upstream config to force connection cleanup.

**Too many callers/tools:**
- The per-caller tool limiter map is capped at 1000 entries. If you have many callers, the total memory for rate limiter state is proportional to the number of callers.
- Remove inactive callers via the Admin API.

**Set resource limits:**
- In Kubernetes, set memory limits to detect issues early:
  ```yaml
  resources:
    limits:
      memory: 512Mi
    requests:
      memory: 128Mi
  ```

## Escalation

- If memory growth is reproducible and not explained by traffic patterns, escalate to the Stile development team with memory profiles if available.
- If OOM kills are disrupting service, scale up instance memory limits as a short-term fix while investigating.
