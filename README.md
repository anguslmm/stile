# Stile

A reverse proxy gateway for the [Model Context Protocol (MCP)](https://modelcontextprotocol.io/), written in Go. Stile sits between AI agents and MCP tool servers, providing a single endpoint that routes tool calls to the correct upstream.

## Status

Stile is under active development. The core proxy pipeline works — you can point an MCP client at Stile and use tools from upstream HTTP servers through it.

**What works today:**

- JSON-RPC 2.0 codec (single and batch requests)
- YAML config loading
- HTTP transport client (Streamable HTTP + SSE passthrough)
- Inbound MCP server (`POST /mcp`) handling initialize, ping, tools/list, tools/call
- Proxy handler with tool discovery and upstream routing

**Not yet implemented:** stdio transport, glob-based routing, auth, rate limiting, ACLs, observability, health endpoints, graceful shutdown.

## Quick Start

```bash
go build ./...
```

### Testing with the fake upstream

A fake MCP upstream server is included for local testing.

**Terminal 1 — start the fake upstream:**

```bash
go run ./scripts/fake-upstream.go
# Listens on :9090, serves "echo" and "add" tools
```

**Terminal 2 — start Stile:**

```bash
go run ./cmd/gateway/ -config configs/test-local.yaml
# Listens on :8080, proxies to the fake upstream
```

**Terminal 3 — send requests:**

```bash
# Initialize handshake
curl -s -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}},"id":1}' | jq .

# List available tools
curl -s -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"tools/list","id":2}' | jq .

# Call the echo tool
curl -s -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"tools/call","params":{"name":"echo","arguments":{"message":"hello"}},"id":3}' | jq .

# Call the add tool
curl -s -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"tools/call","params":{"name":"add","arguments":{"a":17,"b":25}},"id":4}' | jq .
```

### Using with Claude Code

You can also connect Claude Code to Stile as an MCP server:

```bash
claude mcp add --transport http stile http://localhost:8080/mcp
```

After restarting Claude Code, the tools from upstream servers will be available as MCP tools.

## Running Tests

```bash
go test ./...
go vet ./...
```

## Project Structure

```
cmd/gateway/         Entrypoint — wires config, proxy, and server together
configs/             YAML config files
internal/
  jsonrpc/           JSON-RPC 2.0 codec
  config/            Config loading (immutable types with getters)
  transport/         Transport interface + HTTP client implementation
  proxy/             Proxy handler (tool discovery, request forwarding)
  server/            Inbound HTTP server (MCP Streamable HTTP)
scripts/             Test/dev helper scripts
docs/                Design doc and task definitions
```
