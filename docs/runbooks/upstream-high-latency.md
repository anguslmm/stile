# Upstream High Latency

## Severity

P2 (P1 if latency causes widespread timeouts affecting all callers)

## Symptoms

- `stile_request_duration_seconds` histogram shows elevated latency for a specific upstream
- Callers experience slow responses or timeouts
- Logs may show timeout errors with `upstream=<name>`
- `stile_retries_total{upstream="<name>"}` increasing (requests being retried due to timeouts)
- Circuit breaker may eventually trip: `stile_circuit_state{upstream="<name>"}` = 1

## Likely Causes

1. **Upstream resource exhaustion** — the MCP server is CPU/memory/IO bound.
2. **Upstream dependency slow** — the MCP server is waiting on its own downstream services (database, LLM API, etc.).
3. **Network latency** — increased round-trip time between Stile and the upstream.
4. **Upstream concurrency limits** — the MCP server is queueing requests internally.
5. **Large response payloads** — certain tools returning unusually large results.

## Diagnosis Steps

1. Check per-upstream latency (Prometheus):
   ```promql
   histogram_quantile(0.99, rate(stile_request_duration_seconds_bucket{upstream="<name>"}[5m]))
   ```

2. Compare against baseline:
   ```promql
   histogram_quantile(0.50, rate(stile_request_duration_seconds_bucket{upstream="<name>"}[5m]))
   ```

3. Check if specific tools are slow:
   ```promql
   histogram_quantile(0.99, rate(stile_request_duration_seconds_bucket{upstream="<name>", tool="<tool>"}[5m]))
   ```

4. Check retry rate (retries amplify load on a slow upstream):
   ```promql
   rate(stile_retries_total{upstream="<name>"}[5m])
   ```

5. If tracing is enabled, find slow traces:
   - Filter traces by `mcp.upstream=<name>` and sort by duration.
   - Look at span breakdown to see where time is spent.

6. Check upstream health directly:
   ```bash
   time curl -X POST <upstream-url> \
     -H "Content-Type: application/json" \
     -d '{"jsonrpc":"2.0","method":"tools/list","id":1}'
   ```

7. Check the upstream server's own metrics/logs for resource exhaustion.

## Remediation

**Short-term — increase the per-upstream timeout:**

Update the upstream's `timeout` in the config and restart Stile:
```yaml
upstreams:
  - name: slow-upstream
    timeout: 120s  # increase from default 60s
```

**Short-term — disable retries (to reduce upstream load):**

If retries are amplifying the problem, temporarily reduce max attempts:
```yaml
upstreams:
  - name: slow-upstream
    retry:
      max_attempts: 1  # effectively disables retries
```

**Medium-term — rate limit the upstream:**

Add or tighten the per-upstream rate limit to reduce load:
```yaml
upstreams:
  - name: slow-upstream
    rate_limit: "100/min"  # reduce from current value
```

**Long-term — fix the upstream:**
- Scale up the upstream MCP server.
- Optimize slow tools on the upstream side.
- If the upstream is inherently slow (e.g., LLM-backed), set appropriate timeout and caller expectations.

## Escalation

- If the upstream is a third-party service, contact the provider with latency data.
- If latency is network-related, escalate to the infrastructure/networking team.
- If the upstream is internal, escalate to the upstream service owner with trace IDs showing the slow spans.
