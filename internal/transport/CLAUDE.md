# transport

Defines the `Transport` interface and its two implementations (HTTP and stdio) for communicating with MCP upstream servers.

## Key Interfaces and Types

- **`Transport`** — Core interface: `RoundTrip(ctx, *jsonrpc.Request) (TransportResult, error)`, `Close() error`, `Healthy() bool`.
- **`TransportResult`** — Sealed union (unexported marker method) representing a response. Either `*JSONResult` or `*StreamResult`. Both implement `Resolve() (*jsonrpc.Response, error)` and `WriteResponse(ctx, w, tracer)`.
- **`JSONResult`** — Wraps a `*jsonrpc.Response` for `application/json` upstreams.
- **`StreamResult`** — Wraps an `io.ReadCloser` for `text/event-stream` upstreams. Caller or `Resolve`/`WriteResponse` is responsible for closing.
- **`HTTPTransport`** — Sends JSON-RPC over HTTP POST. Supports bearer token auth (static from env or per-request from context via `auth.UpstreamTokenFromContext`), custom TLS (CA, mTLS, skip-verify), and W3C trace context propagation. Health tracked via consecutive failure count (threshold: 3). Per-request token from context takes priority over static token.
- **`StdioTransport`** — Spawns a child process and communicates via line-delimited JSON on stdin/stdout. Requests are serialized with a mutex (stdio is inherently sequential). Supports automatic restart with exponential backoff (up to 10 restarts, 1s–60s). Marks permanently failed after max restarts.
- **`SSEReader`** / **`SSEEvent`** — Minimal SSE parser (event + data fields only; ignores id, retry, comments).
- **`ToolSchema`** — MCP tool definition (`name`, `description`, `inputSchema`).
- **`ConnectError`** / **`StatusError`** — Typed errors from `HTTPTransport.RoundTrip` for connection failures vs. HTTP error status codes.

## Key Functions

- **`NewFromConfig(cfg config.UpstreamConfig) (Transport, error)`** — Factory; dispatches on config type to create the right transport.
- **`Send(ctx, Transport, *jsonrpc.Request) (*jsonrpc.Response, error)`** — Convenience wrapper: calls `RoundTrip` then `Resolve`. Use when the caller doesn't need to stream the response.
- **`NewHTTPTransport(cfg) (*HTTPTransport, error)`** — Reads token from env at construction time.
- **`NewStdioTransport(cfg) (*StdioTransport, error)`** — Process is lazy-started on first request, not at construction.
- **`NewJSONResult` / `NewStreamResult`** — Constructors for the two result types.
- **`NewSSEReader(r io.Reader) *SSEReader`** — Creates an SSE parser with 1 MB max line buffer.

## Design Notes

- `TransportResult` is sealed: only `JSONResult` and `StreamResult` exist. Callers must handle both or use `Resolve()`/`WriteResponse()` to avoid switching on the type.
- `StdioTransport` releases the mutex during backoff sleep so `Close()` can proceed; it rechecks `t.closed` after reacquiring.
- `HTTPTransport.Healthy()` is based on request outcomes, not active probing. It resets to healthy on any success; only 5xx and connection errors count as failures.
- `StdioTransport.Healthy()` returns `false` until the process has been started at least once (lazy start).
- Non-JSON lines on stdio stdout are skipped (up to 50) to tolerate servers that write logs to stdout.
- `StreamResult.WriteResponse` propagates OTel error status on stream read errors.
