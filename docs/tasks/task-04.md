# Task 4: Stdio Transport

**Status:** done
**Depends on:** Task 1 (JSON-RPC codec), Task 2 (transport interface), Task 3 (proxy handler)
**Needed by:** Task 5 (router)

---

## Goal

Implement the `Transport` interface for MCP servers that communicate via stdin/stdout. After this task, the gateway can proxy tool calls to local process-based MCP servers (e.g., `node ./server.js`) alongside HTTP upstreams.

---

## 1. Stdio Transport

### Package: `internal/transport` (same package as HTTP transport)

### File: `internal/transport/stdio.go`

The stdio transport spawns a child process, sends JSON-RPC requests over stdin (line-delimited JSON), and reads responses from stdout.

### Constructor

```go
func NewStdioTransport(cfg config.UpstreamConfig) (*StdioTransport, error)
```

- Validates that `cfg.Command()` is non-empty
- Does **not** start the process yet — that happens on first use or via an explicit `Start()` method
- Stores the command and args for process creation

### Process lifecycle

**Starting:**
- Use `os/exec` to create the command
- Set up `cmd.StdinPipe()` and `cmd.StdoutPipe()`
- Redirect stderr to the gateway's stderr (or a logger) for debugging
- Start the process

**Sending requests:**
- Write the JSON-RPC request as a single line of JSON to stdin (marshal + newline)
- Read the response line from stdout
- Parse as JSON-RPC Response
- Stdio is inherently request/response — no SSE. `RoundTrip` always returns a `*JSONResult`.

**Request/response correlation:**
- MCP stdio servers process requests sequentially — one request in, one response out
- Use a mutex to ensure only one in-flight request at a time
- Match response `id` to request `id` as a sanity check

**Process crashes:**
- If the process exits unexpectedly, `RoundTrip` returns an error
- Implement automatic restart: if the process is dead when `RoundTrip` is called, attempt to restart it once
- If restart fails, return an error — don't retry in a loop

**Shutdown:**
- `Close()` sends SIGTERM to the process, waits briefly, then SIGKILL if it hasn't exited
- Close stdin/stdout pipes
- Use a timeout (e.g., 5 seconds) to avoid hanging on a stuck process

### Healthy()

Return `true` if the process is running (check `cmd.Process` and that it hasn't exited). This replaces the stub from the HTTP transport — the HTTP transport's `Healthy()` can remain as `true` for now.

---

## 2. Integration with Proxy Handler

The proxy handler from Task 3 creates transports based on `cfg.Transport()`. Update `proxy.NewHandler()` to create `StdioTransport` for upstreams with `transport: "stdio"` and `HTTPTransport` for `transport: "streamable-http"`.

This should be a small change — the handler already works with the `Transport` interface, so it just needs the constructor switch.

---

## 3. Test Helper: Mock Stdio MCP Server

Create a small Go program that acts as a stdio MCP server for testing. Location: `internal/transport/testdata/mock_stdio_server.go` (or build it as part of the test).

The mock server:
- Reads JSON-RPC from stdin (line by line)
- Handles `initialize` → returns server info
- Handles `tools/list` → returns a canned tool list (e.g., one tool called `"test_echo"`)
- Handles `tools/call` → echoes back the params it received as the result
- Handles `ping` → returns empty result
- Anything else → returns MethodNotFound error

Build this as a `TestMain` helper or a standalone Go file compiled during tests with `go build`.

---

## 4. Testable Deliverables

### Unit tests (`internal/transport/`)

1. **Stdio round-trip:** start mock stdio server, send a tools/list request → get back tool list
2. **Stdio tools/call:** send a tools/call request → get back the echoed result
3. **Result is always non-streaming:** result is `*JSONResult`
4. **Process crash recovery:** kill the process, send another request → transport restarts the process and succeeds
5. **Close shuts down process:** call `Close()`, verify process is no longer running
6. **Concurrent requests are serialized:** send multiple requests concurrently → all get correct responses (no interleaving)

### Integration with proxy handler

7. **Mixed upstreams:** configure one HTTP upstream (httptest) and one stdio upstream (mock server), start the proxy handler → tools/list returns tools from both, tools/call routes correctly to each

### Build check

```bash
go build ./...
go test ./...
go vet ./...
```

---

## 5. Out of Scope

- Process restart with backoff (Task 9 hardening)
- Stderr logging integration (Task 8 observability)
- Health check probes beyond simple "is process alive" (Task 9)
