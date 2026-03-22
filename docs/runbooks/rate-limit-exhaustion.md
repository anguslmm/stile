# Rate Limit Exhaustion

## Severity

P3 (P2 if a critical caller or workflow is blocked)

## Symptoms

- Callers receive HTTP 429 responses with `Retry-After` header
- JSON-RPC error responses with rate limit messages
- `stile_rate_limit_rejections_total` counter increasing
- Response headers show limits being hit:
  - `X-RateLimit-Remaining: 0`
  - `X-RateLimit-Reset: <timestamp>`
- Logs at WARN level: `"rate limit rejected"` with `caller=<name>`, `tool=<name>`, `level=<caller|tool|upstream>`

## Likely Causes

1. **Runaway agent** — an AI agent making excessive tool calls in a tight loop.
2. **Legitimate traffic spike** — a valid workflow that exceeds the configured limits.
3. **Misconfigured rate limits** — limits set too low for the expected workload.
4. **Rate limit level mismatch** — caller is hitting the per-tool or per-upstream limit rather than the per-caller limit (or vice versa).
5. **Role rate limit override missing** — caller's role doesn't have a rate limit override and is falling back to the restrictive global default.

## Diagnosis Steps

1. Identify which callers are being rate limited (Prometheus):
   ```promql
   topk(10, sum by (caller) (rate(stile_rate_limit_rejections_total[5m])))
   ```

2. Check which tools are involved:
   ```promql
   sum by (caller, tool) (rate(stile_rate_limit_rejections_total[5m]))
   ```

3. Check the log field `level` to determine which limit type is being hit:
   ```bash
   journalctl -u stile | grep 'rate limit rejected' | tail -20
   ```
   The `level` field will be `caller`, `tool`, or `upstream`.

4. Check the caller's actual request rate:
   ```promql
   sum by (caller) (rate(stile_requests_total{caller="<name>"}[5m]))
   ```

5. Review the rate limit configuration:
   - Global defaults in `rate_limits.default_caller`, `rate_limits.default_tool`, `rate_limits.default_upstream`
   - Per-role overrides in `roles.<role>.rate_limit` and `roles.<role>.tool_rate_limit`
   - Per-upstream limits in `upstreams[].rate_limit`

6. Check the caller's roles:
   ```bash
   curl -s -H "Authorization: Bearer $ADMIN_API_KEY" \
     http://<stile-host>:8080/admin/callers/<name>/roles | jq .
   ```

## Remediation

**Runaway agent:**
- Identify the caller via metrics and audit log.
- If the caller is misbehaving, revoke their API key:
  ```bash
  curl -X DELETE -H "Authorization: Bearer $ADMIN_API_KEY" \
    http://<stile-host>:8080/admin/callers/<name>/keys/<key-id>
  ```
- Alternatively, remove their role to block tool access while keeping the caller record.

**Adjust rate limits:**
- For a specific caller's role, update the role config:
  ```yaml
  roles:
    heavy-user:
      rate_limit: "500/min"       # per-caller limit
      tool_rate_limit: "50/sec"   # per-caller-per-tool limit
  ```
- For global defaults:
  ```yaml
  rate_limits:
    default_caller: "200/min"
    default_tool: "30/sec"
    default_upstream: "2000/min"
  ```
- Restart Stile after config changes (no hot-reload).

**Per-upstream limit hit:**
- Increase the upstream's `rate_limit` in config, or work with the upstream owner to understand their capacity.

**Using Redis rate limiting:**
- If running multiple Stile instances with `backend: local`, each instance tracks limits independently. Switch to `backend: redis` for global enforcement. See [horizontal-scaling.md](../horizontal-scaling.md).

## Escalation

- If a critical production workflow is blocked, temporarily increase the relevant rate limit and restart.
- If a single caller is generating excessive load that affects others, escalate to the team managing that agent.
