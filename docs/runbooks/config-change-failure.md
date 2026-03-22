# Config Change Not Taking Effect

## Severity

P3 (P2 if the config change is needed to resolve an ongoing incident)

## Symptoms

- A config change was deployed but Stile behavior hasn't changed
- New upstreams are not appearing in `tools/list` responses
- Rate limits haven't updated
- Roles or access control changes aren't applied
- Stile fails to start after a config change

## Likely Causes

1. **Stile was not restarted** — Stile does not support hot-reload. Config changes require a full process restart.
2. **YAML syntax error** — the config file has invalid YAML and Stile rejected it at startup (old instance may still be running).
3. **Invalid config values** — valid YAML but semantically incorrect (e.g., invalid upstream URL, unknown transport type, malformed rate limit string).
4. **Missing environment variable** — a `token_env` or credential env var is not set in the new environment.
5. **Wrong config file** — Stile is reading a different config file than the one that was edited.
6. **Tool cache stale** — the config is loaded but the tool cache hasn't refreshed yet (tools from a new upstream won't appear until the next cache refresh cycle).

## Diagnosis Steps

1. **Verify Stile was restarted** after the config change:
   ```bash
   # Check process uptime
   ps -o pid,etime,comm -p $(pgrep stile)

   # Check systemd
   systemctl status stile

   # Check Kubernetes
   kubectl get pods -l app=stile -o wide
   ```

2. **Check Stile startup logs** for config errors:
   ```bash
   journalctl -u stile --since "5 minutes ago" | head -30
   ```
   Stile logs the config file path and key settings at INFO level on startup. If the config is invalid, it exits immediately with an error.

3. **Validate YAML syntax:**
   ```bash
   python3 -c "import yaml; yaml.safe_load(open('config.yaml'))"
   # or
   yq . config.yaml
   ```

4. **Check which config file Stile is using:**
   ```bash
   # Check the process arguments
   ps -o args -p $(pgrep stile)
   # Look for: stile -config <path>
   ```

5. **Check environment variables** are set:
   ```bash
   # Verify token env vars referenced in config
   grep token_env config.yaml
   # Then check each one is set in the environment
   ```

6. **Check the readyz endpoint** to see if upstreams are recognized:
   ```bash
   curl -s http://localhost:8080/readyz | jq .
   ```

7. **Trigger a manual tool cache refresh** (if the config is loaded but tools aren't appearing):
   ```bash
   curl -X POST -H "Authorization: Bearer $ADMIN_API_KEY" \
     http://localhost:8080/admin/refresh
   ```

## Remediation

**Stile not restarted:**
```bash
systemctl restart stile

# Kubernetes — rolling restart
kubectl rollout restart deployment/stile
```

**YAML syntax error:**
- Fix the syntax error in the config file. Common issues:
  - Tabs instead of spaces
  - Missing quotes around strings with special characters
  - Incorrect indentation
- Re-validate and restart.

**Missing environment variable:**
- Stile logs a warning: `"env var not set"` with `env_var=<name>`.
- Set the variable and restart:
  ```bash
  export MY_TOKEN="value"
  systemctl restart stile
  ```

**Invalid config values:**
- Check the error message on startup. Common issues:
  - Rate limit format must be `N/sec`, `N/min`, or `N/hour`
  - Upstream URLs must be valid HTTP(S) URLs
  - Transport must be `streamable-http` or `stdio`
  - Stdio upstreams require `command` to be set
- Fix the value and restart.

**Tool cache not yet refreshed:**
- Wait for the next automatic refresh (configured via `server.tool_cache_ttl`, default 5 minutes).
- Or trigger manually: `POST /admin/refresh`.

## Escalation

- If Stile refuses to start with a valid-looking config, escalate to the Stile development team with the full error output.
- If a config change is urgently needed during an incident, consider rolling back to the previous known-good config while debugging.
