# jsonrpc

JSON-RPC 2.0 wire types and message parsing for MCP. Hand-written, no framework.

## Key Types

- **`ID`** — Sealed interface for request IDs. Three concrete types: `StringID`, `IntID`, `NullID`. A nil `ID` means the field was absent (notification); `NullID` means it was explicitly `null`.
- **`Request`** — A JSON-RPC 2.0 request or notification. `IsNotification()` returns true when `ID == nil`.
- **`Response`** — A JSON-RPC 2.0 response. Has custom `MarshalJSON`/`UnmarshalJSON` to handle the sealed `ID` interface.
- **`Error`** — JSON-RPC 2.0 error object. Implements `error`. Code + message + optional raw `Data`.

## Key Constants

- `Version = "2.0"`
- `CodeParseError`, `CodeInvalidRequest`, `CodeMethodNotFound`, `CodeInvalidParams`, `CodeInternalError` — standard error codes.

## Key Functions

- **`ParseMessage(data []byte) ([]*Request, bool, error)`** — Parses a single request or a batch. Returns requests, `isBatch` flag, and error. Validates `jsonrpc == "2.0"` and non-empty `method` at parse time.
- **`NewResponse(id ID, result any) (*Response, error)`** — Constructs a success response, marshaling `result` to JSON.
- **`NewErrorResponse(id ID, code int, message string) *Response`** — Constructs an error response.
- **`NewErrorResponseWithData(id ID, code int, message string, data json.RawMessage) *Response`** — Error response with extra data payload.

## Design Decisions

- `ID` is a sealed interface (unexported marker method `jsonrpcID()`); only this package can add implementations.
- Absent `id` field and `null` `id` are distinguished: absent → `nil` (notification), `null` → `NullID{}` (expects response).
- Validation happens at parse time in `parseOneRequest`; a `*Request` that exists is always well-formed.
- Errors from `ParseMessage` are `*Error` values (not wrapped), so callers can use them directly as JSON-RPC error responses.
- `Response` requires custom marshal/unmarshal because `encoding/json` cannot handle the sealed `ID` interface via struct tags alone.
