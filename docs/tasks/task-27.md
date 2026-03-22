# Task 27: `stile wrap` — Stdio-to-HTTP Adapter Subcommand

**Status:** done
**Depends on:** 4, 19

---

## Goal

Add a `stile wrap` subcommand that exposes any stdio MCP server as a Streamable HTTP endpoint. This eliminates the need for third-party wrappers (supergateway, mcp-proxy) when deploying stdio MCP servers in a multi-instance Stile setup.

```bash
stile wrap --command "npx -y @modelcontextprotocol/server-github" --port 9090
```

Agents or other Stile instances connect to `http://host:9090/mcp` and the wrapper translates between HTTP and stdio JSON-RPC.

---

## Design Principles

- **Minimal scope.** The wrapper does protocol translation only — no auth, no rate limiting, no routing. If you need those, point a full Stile instance at the wrapper as an HTTP upstream.
- **Single child process.** One stdio process per wrapper. Stdio MCP servers are not designed for multiplexed concurrent requests on the same stdin, so the wrapper must serialize requests to the child.
- **Reuse existing code.** The stdio transport (`internal/transport/stdio.go`) already handles process lifecycle, restart with backoff, and health checking. The HTTP server (`internal/server/`) already handles Streamable HTTP. Compose these rather than reimplementing.

---

## 1. CLI Subcommand

Add `wrap` to the CLI parser in `cmd/gateway/main.go`.

### Flags

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--command` | yes | — | Command to run (shell-split or repeated flag) |
| `--port` | no | `9090` | Listen port |
| `--address` | no | `:9090` | Full listen address (overrides `--port`) |
| `--env` | no | — | Extra env vars for the child (`KEY=VALUE`, repeatable) |
| `--health-interval` | no | `30s` | How often to check child process health |

Example:

```bash
stile wrap --command "python -m my_mcp_server --stdio" --port 9090 --env API_KEY=secret
```

---

## 2. Core Wrapper

### Request flow

```
HTTP POST /mcp
  → parse JSON-RPC request
  → acquire serialization lock
  → write to child stdin (via existing StdioTransport)
  → read response from child stdout
  → release lock
  → return JSON-RPC response as HTTP
```

### Implementation

Create `internal/wrap/handler.go` (or similar):

1. Construct a `StdioTransport` from the command/env flags (reuse `NewStdioTransport` or build a `StdioUpstreamConfig` programmatically).
2. Serve an HTTP endpoint at `/mcp` that:
   - Reads the request body as a JSON-RPC request
   - Calls `transport.Send(ctx, stdioTransport, req)` to get the response
   - Writes the JSON-RPC response back as `application/json`
3. Serve `/healthz` that returns 200 if the child process is alive.
4. Handle `SIGINT`/`SIGTERM` for graceful shutdown: close the stdio transport (which kills the child), drain HTTP connections.

### Serialization

The existing `StdioTransport` already serializes access to stdin/stdout via its mutex. No additional locking is needed at the handler level.

### Batching

Accept batch JSON-RPC requests (array of requests). Process each sequentially through the stdio transport and return the batch response. The existing `StdioTransport.RoundTrip` handles one request at a time, so iterate and collect results.

---

## 3. Tool Discovery

The wrapper should support `tools/list` passthrough — when a client sends a `tools/list` request, it forwards to the child and returns the result. This lets downstream Stile instances discover tools automatically.

The wrapper should also support `initialize` passthrough for clients that need to negotiate capabilities.

---

## 4. Logging

Use structured logging (slog) consistent with the rest of Stile:

- Log child process start/restart/exit
- Log incoming requests at debug level
- Log errors at error level

---

## 5. Docker Compose Example

Create `docker-compose.scaling.yml` showing the full multi-instance pattern:

- **nginx** — load balancer in front of multiple Stile instances
- **2+ Stile instances** — identical config, HTTP upstreams only
- **`stile wrap` containers** — one per stdio MCP server (e.g. GitHub, filesystem), each exposing an HTTP endpoint
- **Postgres** — shared caller/key store and audit log
- **Redis** — shared rate limiting

Include a matching config file (`configs/scaling.yaml`) that points Stile at the wrapper containers as HTTP upstreams. The compose file should demonstrate the key point from the scaling guide: stdio servers run once as wrapped services, not per-instance.

Example structure:

```
nginx (port 8080)
  ├── stile-1 (:8081) ──┐
  └── stile-2 (:8082) ──┤── github-wrapper (:9090)  [stile wrap]
                         ├── fetch-wrapper  (:9091)  [stile wrap]
                         ├── postgres (:5432)
                         └── redis (:6379)
```

---

## 6. Update Documentation

- Update `docs/horizontal-scaling.md` to mention `stile wrap` as the recommended approach (replacing the third-party tool suggestions).
- Add a `stile wrap` section to the README under an appropriate heading.
- Add usage examples showing common patterns (GitHub MCP server, filesystem server, etc.).

---

## Verification

- `stile wrap --command "echo" --port 0` starts and listens (smoke test)
- Send a `tools/list` request to the wrapper and get back a valid JSON-RPC response
- Send a `tools/call` request and get the correct response from the child
- Wrapper handles child process crash and restart gracefully
- `GET /healthz` returns 200 when child is alive
- Graceful shutdown on SIGTERM
- Config example in horizontal-scaling.md reflects `stile wrap` usage
- `configs/scaling.yaml` passes config loading
- `docker-compose.scaling.yml` is valid (`docker compose -f docker-compose.scaling.yml config`)
