# Upstream Unhealthy

## Severity

P2 (P1 if all upstreams are unhealthy — Stile is effectively down)

## Symptoms

- `GET /readyz` returns 503 with the affected upstream showing `"healthy": false`
- `stile_upstream_health{upstream="<name>"}` gauge drops to 0
- Logs at WARN level: `"upstream marked unhealthy"` with `upstream=<name>` and `consecutive_failures=<N>`
- Callers receive JSON-RPC errors for tools routed to the unhealthy upstream
- If all upstreams are unhealthy, `/readyz` returns 503 and load balancers may pull the instance out of rotation

## Likely Causes

1. **Upstream is down or unreachable** — the MCP server process crashed, the host is unreachable, or a network partition occurred.
2. **Upstream is overloaded** — responding too slowly, causing health check timeouts.
3. **Circuit breaker tripped** — consecutive failures exceeded the `failure_threshold`, so `stile_circuit_state{upstream="<name>"}` = 1 (open).
4. **DNS resolution failure** — the upstream hostname no longer resolves.
5. **Credential rotation** — the upstream started rejecting requests because the outbound auth token (configured via `auth.token_env`) expired or was rotated without updating Stile's environment.

## Diagnosis Steps

1. Check which upstreams are unhealthy:
   ```bash
   curl -s http://<stile-host>:8080/readyz | jq .
   ```

2. Check the upstream health metric over time (Prometheus):
   ```promql
   stile_upstream_health{upstream="<name>"}
   ```

3. Check circuit breaker state:
   ```promql
   stile_circuit_state{upstream="<name>"}
   ```
   Values: 0 = closed (normal), 1 = open (tripped), 2 = half-open (probing).

4. Check Stile logs for health check failures:
   ```bash
   # Look for health check and upstream error messages
   journalctl -u stile | grep -E 'upstream marked unhealthy|health check failed' | tail -20
   ```

5. Verify the upstream is reachable from the Stile host:
   ```bash
   curl -v <upstream-url>
   ```

6. Check retry metrics for the upstream:
   ```promql
   rate(stile_retries_total{upstream="<name>"}[5m])
   ```

7. Check DNS resolution:
   ```bash
   dig <upstream-hostname>
   ```

## Remediation

**Upstream is down:**
- Restart or redeploy the upstream MCP server.
- Once healthy, Stile's periodic health checker (every 30s by default) will automatically mark it healthy again.

**Circuit breaker tripped:**
- The circuit breaker will automatically transition to half-open after the configured `cooldown` period and probe the upstream.
- If the upstream is back, the circuit closes on the next successful probe. No Stile restart needed.

**Upstream permanently removed:**
- Remove the upstream from the Stile config file and redeploy/restart Stile.

**Credential issue:**
- Update the environment variable referenced in the upstream's `auth.token_env` and restart Stile.

**All upstreams unhealthy (P1):**
- Focus on restoring at least one upstream to bring `/readyz` back to 200.
- If the issue is Stile-side (e.g., network misconfiguration), restart Stile after fixing the underlying cause.

## Escalation

- If the upstream is a third-party service, contact the provider.
- If all upstreams are down and the cause is unclear, escalate to the infrastructure team.
- If the circuit breaker is cycling (open -> half-open -> open repeatedly), the upstream may be partially failing — escalate to the upstream owner.
