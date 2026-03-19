# Task 2: Config + Transport Interface + HTTP Transport Client

**Status:** not started
**Depends on:** Task 1 (JSON-RPC codec)
**Needed by:** Task 3 (inbound server + proxy handler)

---

## Goal

Define the YAML config format, the Transport interface, and implement an HTTP transport client that can talk to Streamable HTTP MCP servers. After this task, you can programmatically connect to a real MCP server, call `tools/list`, and get back tool schemas. There's still no gateway server — that's Task 3.

---

## 1. Config Parsing

### Package: `internal/config`

Parse a YAML config file into Go structs. For this task, only upstream and server config is needed — caller auth, rate limits, etc. come in later tasks. Design the top-level struct to be extensible.

### Config types

All config types use **unexported fields with getter methods**. The only way to obtain a config is through `Load`, which validates before returning. This makes it impossible for callers to construct or mutate an invalid config.

```go
// Config is immutable after construction via Load.
type Config struct {
    server    serverConfig
    upstreams []UpstreamConfig
}

func (c *Config) Server() ServerConfig    { ... }
func (c *Config) Upstreams() []UpstreamConfig { ... } // returns a copy of the slice

type serverConfig struct {
    address string
}
type ServerConfig struct { ... } // public read-only view, or getter methods on serverConfig

type UpstreamConfig struct {
    name      string
    url       string
    command   []string
    transport string
    auth      *AuthConfig
    tools     []string
}

func (u *UpstreamConfig) Name() string       { ... }
func (u *UpstreamConfig) URL() string        { ... }
func (u *UpstreamConfig) Command() []string  { ... } // returns a copy
func (u *UpstreamConfig) Transport() string  { ... }
func (u *UpstreamConfig) Auth() *AuthConfig  { ... }
func (u *UpstreamConfig) Tools() []string    { ... } // returns a copy

type AuthConfig struct {
    authType string
    tokenEnv string
}

func (a *AuthConfig) Type() string     { ... }
func (a *AuthConfig) TokenEnv() string { ... }
```

Getters that return slices should return copies to prevent mutation of internal state.

The unexported fields still need YAML unmarshaling. Use a private intermediate struct for YAML parsing, then convert to the public types after validation. For example:

```go
// rawConfig is the YAML-friendly shape used only during parsing.
type rawConfig struct {
    Server    rawServerConfig   `yaml:"server"`
    Upstreams []rawUpstreamConfig `yaml:"upstreams"`
}

// Load unmarshals into rawConfig, validates, then converts to the immutable Config.
```

### Load function

- `Load(path string) (*Config, error)` — reads, parses, and validates the config file. Returns a valid `*Config` or an error. It is impossible to obtain an invalid `*Config` through this package's public API.

Validation rules (applied internally by `Load`, not exposed as a public method):
  - At least one upstream defined
  - Each upstream has a non-empty name
  - Names are unique
  - HTTP upstreams (`transport: streamable-http`) must have a `url`
  - Stdio upstreams (`transport: stdio`) must have a `command`
  - `transport` is one of `"streamable-http"` or `"stdio"`
  - Server address is non-empty (default to `":8080"` if missing)

Keep the validation logic in an unexported `validate` function for readability — but it's only called by `Load`, never by external callers.

### Example config file

Create `configs/example.yaml` with a representative config showing both upstream types.

### Dependency

This introduces the first external dependency: `gopkg.in/yaml.v3`. Run `go get` to add it.

---

## 2. Transport Interface

### Package: `internal/transport`

The Transport interface is the key abstraction in the architecture. The router and proxy never know whether they're talking to an HTTP server or a local stdio process.

### Types

```go
// ToolSchema represents an MCP tool definition returned by tools/list.
type ToolSchema struct {
    Name        string          `json:"name"`
    Description string          `json:"description,omitempty"`
    InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// TransportResult is a sealed union: either *JSONResult or *StreamResult.
type TransportResult interface {
    transportResult() // sealed
    ContentType() string
}

// JSONResult is a TransportResult for non-streaming (application/json) responses.
type JSONResult struct { /* unexported: response *jsonrpc.Response */ }
func (r *JSONResult) Response() *jsonrpc.Response { ... }

// StreamResult is a TransportResult for streaming (text/event-stream) responses.
// The caller is responsible for reading from and closing the stream.
type StreamResult struct { /* unexported: stream io.ReadCloser */ }
func (r *StreamResult) Stream() io.ReadCloser { ... }

// Constructors: NewJSONResult(resp), NewStreamResult(stream)

// Transport is the interface for communicating with an MCP upstream.
type Transport interface {
    // RoundTrip sends a JSON-RPC request to the upstream and returns the result.
    // Callers type-switch on *JSONResult / *StreamResult.
    RoundTrip(ctx context.Context, req *jsonrpc.Request) (TransportResult, error)

    // Close shuts down the transport and releases resources.
    Close() error

    // Healthy reports whether the upstream is reachable.
    Healthy() bool
}
```

### Helper function

```go
// Send is a convenience that sends a request and returns the final Response.
// If the upstream responds with SSE, it reads events until the final
// JSON-RPC response and returns it. For non-streaming responses, it
// returns the Response directly.
func Send(ctx context.Context, t Transport, req *jsonrpc.Request) (*jsonrpc.Response, error)
```

This helper is used internally (e.g. for tool discovery). The proxy handler uses `RoundTrip` directly for passthrough.

---

## 3. SSE Event Reader

### Location: `internal/transport/sse.go` (or similar)

A minimal SSE event parser for reading upstream streaming responses. This is used by the `Send` helper to extract the final JSON-RPC response from an SSE stream.

SSE format:
```
event: message
data: {"jsonrpc":"2.0","result":{...},"id":1}

```

Implement:
- `type SSEEvent struct { Event, Data string }`
- `type SSEReader struct` — wraps an `io.Reader`, reads events one at a time
- `(r *SSEReader) Next() (*SSEEvent, error)` — returns the next event, or `io.EOF` at end of stream

Keep it minimal — just enough to parse `event:` and `data:` lines separated by blank lines. No need to handle `id:`, `retry:`, or multi-line `data:` fields.

---

## 4. HTTP Transport Client

### Location: `internal/transport/http.go`

Implement the `Transport` interface for Streamable HTTP MCP servers.

### Constructor

```go
func NewHTTPTransport(cfg config.UpstreamConfig) (*HTTPTransport, error)
```

- Reads the bearer token from the env var specified in `cfg.Auth().TokenEnv()` (if auth is configured)
- Creates an `*http.Client` (use defaults for now — timeouts can be tuned later)

### RoundTrip behavior

1. Marshal the JSON-RPC request to JSON
2. POST to the upstream URL with `Content-Type: application/json` and `Accept: application/json, text/event-stream`
3. If `cfg.Auth()` is non-nil, add `Authorization: Bearer <token>` header
4. Check the response status code (non-2xx → return an error)
5. Branch on response `Content-Type`:
   - `application/json`: read body, unmarshal as `jsonrpc.Response`, return as `*JSONResult`
   - `text/event-stream`: return body as `*StreamResult` (caller closes it)
6. Return the `ContentType` in the result

### Close / Healthy

- `Close()`: no-op for HTTP (no persistent connection to clean up)
- `Healthy()`: for now, always return `true`. Real health checks come in Task 9.

---

## 5. Testable Deliverables

### Config tests (`internal/config/`)

1. **Load valid config:** parse a YAML string with one HTTP upstream and one stdio upstream → correct struct values
2. **Valid config loads successfully:** well-formed YAML with valid upstreams → no error
3. **Missing name:** upstream without name → `Load` returns error
4. **HTTP without URL:** streamable-http upstream with no url → `Load` returns error
5. **Stdio without command:** stdio upstream with no command → `Load` returns error
6. **Bad transport:** upstream with transport "websocket" → `Load` returns error
7. **Duplicate names:** two upstreams with same name → `Load` returns error
8. **Default server address:** config with no server.address → loaded config has ":8080"

### Transport tests (`internal/transport/`)

Use `net/http/httptest` to create mock MCP servers.

1. **JSON response round-trip:** mock server returns `application/json` with a valid JSON-RPC response → result is `*JSONResult`
2. **SSE response round-trip:** mock server returns `text/event-stream` with SSE events → result is `*StreamResult`
3. **Send helper with JSON:** `Send()` with a JSON-responding mock → returns the `*Response` directly
4. **Send helper with SSE:** `Send()` with an SSE-responding mock that sends a notification then a final response → returns the final `*Response`
5. **Auth header injection:** mock server that checks for `Authorization: Bearer` header → token from env var is sent
6. **Upstream error (non-2xx):** mock server returns 500 → `RoundTrip` returns an error
7. **SSE reader:** parse a byte stream with two SSE events → correct event/data extraction

### Build check

```bash
go build ./...
go test ./...
go vet ./...
```

---

## 6. Out of Scope

- Stdio transport (Task 4)
- Inbound HTTP server / proxy handler (Task 3)
- Caller config, rate limit config (later tasks)
- Connection pooling, timeouts, retries (Task 9)
