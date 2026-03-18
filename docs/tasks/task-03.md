# Task 3: Inbound MCP Server + Proxy Handler

**Status:** not started
**Depends on:** Task 1 (JSON-RPC codec), Task 2 (config, transport interface, HTTP transport)
**Needed by:** Task 4 (stdio transport), Task 5 (router)

---

## Goal

Build the inbound HTTP server and proxy handler that makes Stile a working MCP gateway. After this task, you can point an MCP client (like Claude Code) at Stile and use tools from upstream MCP servers through it. This is the first "it works" milestone.

---

## 1. Inbound MCP Server

### Package: `internal/server`

The inbound server is an HTTP server that speaks MCP's Streamable HTTP transport. From the agent's perspective, Stile looks like any other MCP server.

### Endpoint

Single endpoint: `POST /mcp`

- Accepts `Content-Type: application/json` containing a JSON-RPC message (single or batch)
- Responds with either:
  - `Content-Type: application/json` for non-streaming responses
  - `Content-Type: text/event-stream` for SSE passthrough from upstream

### Request handling flow

1. Read the POST body
2. Parse as JSON-RPC using `jsonrpc.ParseMessage` (handles single and batch)
3. For each request, dispatch based on `method`:
   - `initialize` → handle directly (see below)
   - `notifications/initialized` → accept silently (it's a notification, no response)
   - `ping` → respond with empty result
   - `tools/list` → return merged tool list from all upstreams
   - `tools/call` → look up tool, forward to correct upstream via proxy handler
   - Anything else → return `MethodNotFound` error
4. For batch requests, process each request and return a batch response (array of responses). Notifications in a batch produce no response entry.

### MCP Initialize

The gateway handles `initialize` itself — it does not forward it to upstreams. This is the MCP handshake where the client learns what the server supports.

Request params (relevant fields):
```json
{
  "protocolVersion": "2025-11-25",
  "capabilities": {},
  "clientInfo": {"name": "claude-code", "version": "1.0"}
}
```

Response result:
```json
{
  "protocolVersion": "2025-11-25",
  "capabilities": {"tools": {}},
  "serverInfo": {"name": "stile", "version": "0.1.0"}
}
```

The gateway should store the protocol version from the client for future use, but for now just echo back the version it supports. If the client requests a version the gateway doesn't support, return an error.

### Server constructor

```go
func New(cfg *config.Config, proxy *proxy.Handler) *Server

func (s *Server) ListenAndServe() error
func (s *Server) Shutdown(ctx context.Context) error
```

The server takes the config (for listen address) and the proxy handler (for dispatching tool calls). It sets up the HTTP mux and manages the listener lifecycle.

---

## 2. Proxy Handler

### Package: `internal/proxy`

The proxy handler is responsible for forwarding requests to upstreams and returning responses. It owns the tool→upstream mapping.

### Startup: tool discovery

On construction, the proxy handler:

1. Creates a `Transport` for each upstream in the config (using `transport.NewHTTPTransport` for now — stdio comes in Task 4)
2. Calls `transport.Send()` with a `tools/list` request on each transport
3. Parses the results to extract `[]ToolSchema` per upstream
4. Builds an in-memory map: `toolName → upstream` (where upstream holds the transport + config)

If an upstream is unreachable at startup, log a warning and skip it. Don't crash.

### tools/list handling

Return the merged list of all tools from all upstreams as a JSON-RPC response. The result shape follows the MCP spec:

```json
{
  "tools": [
    {"name": "...", "description": "...", "inputSchema": {...}},
    ...
  ]
}
```

### tools/call handling

1. Extract the tool name from `params.name`
2. Look up the upstream in the tool map
3. If not found → return JSON-RPC error (code `-32602`, "unknown tool")
4. Forward the **original JSON-RPC request** to the upstream via `Transport.RoundTrip()`
5. Branch on `TransportResult`:
   - If `Response` is set → return it as `application/json`
   - If `Stream` is set → set response `Content-Type: text/event-stream` and pipe the stream through to the agent (SSE passthrough)

### Constructor

```go
type Handler struct { ... }

func NewHandler(cfg *config.Config) (*Handler, error)
```

The constructor creates transports and performs initial tool discovery. It returns an error only if the config is fundamentally broken (e.g., no upstreams at all). Individual upstream failures are non-fatal.

---

## 3. Entrypoint

### File: `cmd/gateway/main.go`

Wire everything together:

1. Parse a `-config` flag (default: `configs/stile.yaml`)
2. Load config via `config.Load()`
3. Create proxy handler via `proxy.NewHandler()`
4. Create server via `server.New()`
5. Start the server
6. Listen for SIGINT/SIGTERM → call `server.Shutdown()` with a timeout context

Keep it minimal. No fancy CLI framework — just `flag` package.

---

## 4. SSE Passthrough Detail

When an upstream responds with `text/event-stream`, the proxy must stream it through to the agent without buffering the entire response. The flow:

1. `Transport.RoundTrip()` returns `TransportResult` with `Stream` set
2. Proxy handler sets the HTTP response headers: `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`
3. Copy from `Stream` to the `http.ResponseWriter`, flushing after each write
4. Use `http.Flusher` interface to ensure events are sent immediately, not buffered

```go
flusher, ok := w.(http.Flusher)
// copy chunks from upstream stream to w, flush after each
```

If the agent disconnects mid-stream, detect via `ctx.Done()` (the request context) and close the upstream stream.

---

## 5. Testable Deliverables

### Unit tests

**Proxy handler tests** (`internal/proxy/`):

Use a mock transport (implement the `Transport` interface with canned responses) — do not use real HTTP servers for proxy unit tests.

1. **tools/list merges upstreams:** two mock transports with different tools → merged list contains all tools
2. **tools/call dispatches correctly:** call a tool that belongs to upstream A → request forwarded to upstream A's transport, not B's
3. **tools/call unknown tool:** call a tool that doesn't exist → JSON-RPC error response
4. **Upstream down at startup:** one transport returns error on tools/list → handler still starts, serves tools from the healthy upstream

**Server tests** (`internal/server/`):

Use `httptest.Server` to run the gateway server with mock transports.

5. **Initialize handshake:** POST `initialize` request → response has serverInfo with name "stile"
6. **Ping:** POST `ping` request → empty result response
7. **Unknown method:** POST request with method "foo/bar" → MethodNotFound error
8. **Notification produces no response body:** POST a notification → HTTP 202 Accepted with no body (per MCP spec)

### Integration-style tests

These wire up the full stack: gateway server ↔ mock upstream (httptest).

9. **End-to-end tools/list:** start mock upstream with 2 tools, start gateway pointing at it → agent calls tools/list on gateway → gets both tools
10. **End-to-end tools/call:** start mock upstream, start gateway → agent calls tools/call on gateway → gets the upstream's response
11. **End-to-end SSE passthrough:** mock upstream returns SSE events → agent receives them streamed through the gateway with correct Content-Type

### Manual smoke test

After all tests pass, you should be able to:

```bash
# Start the gateway with example config
go run ./cmd/gateway/ -config configs/example.yaml

# In another terminal (assuming an upstream is running)
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}},"id":1}'
```

This should return a valid initialize response. tools/list and tools/call will only work if a real upstream is running.

### Build check

```bash
go build ./...
go test ./...
go vet ./...
```

---

## 6. Out of Scope

- Stdio transport (Task 4)
- Router with caching, refresh, glob patterns (Task 5 — this task uses a simple static map)
- Authentication (Task 6)
- Rate limiting, ACLs (Task 7)
- Structured logging, metrics (Task 8)
- Graceful shutdown with draining (Task 9 — this task does basic signal handling only)
