# wrap

Stdio-to-HTTP adapter: exposes a `StdioTransport`-backed MCP server as a Streamable HTTP endpoint.

## Key Types

- **`Handler`** — `http.Handler`-style type that forwards incoming JSON-RPC HTTP requests to a `*transport.StdioTransport`. Holds an optional OTel tracer.
- **`Option`** — Functional option for `Handler` configuration.
- **`propagatorCarrier`** — Internal adapter that makes `http.Header` satisfy the OTel `TextMapCarrier` interface for trace context propagation.

## Key Functions / Methods

- **`NewHandler(t, ...Option) *Handler`** — Constructs a Handler backed by the given StdioTransport.
- **`WithTracer(trace.Tracer) Option`** — Attaches an OTel tracer; enables span creation per request and per forward call.
- **`(*Handler).ServeMux() *http.ServeMux`** — Returns a mux with two routes: `POST /mcp` and `GET /healthz`.

## Design Notes

- Batch requests are processed sequentially (not concurrently) through the single stdio channel, which is inherently serial.
- Notifications in a batch are forwarded best-effort (errors ignored); no response is collected for them.
- A batch where every item is a notification returns `202 Accepted` with no body.
- Body is capped at 10 MB (`maxRequestBody`); oversized requests get a JSON-RPC error response, not an HTTP error.
- `/healthz` delegates directly to `StdioTransport.Healthy()` — returns 200 or 503.
- OTel trace context is extracted from inbound HTTP headers before any processing.
