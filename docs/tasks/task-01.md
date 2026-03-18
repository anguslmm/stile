# Task 1: Project Scaffold + JSON-RPC 2.0 Codec

**Status:** done
**Depends on:** nothing
**Needed by:** all subsequent tasks

---

## Goal

Set up the Go project and implement a JSON-RPC 2.0 codec that every other package will use. After this task, `go build ./...` and `go test ./...` pass, and the jsonrpc package can parse and serialize every message type the gateway will handle.

---

## 1. Project Scaffold

Initialize the Go module and create the directory structure:

```
stile/
├── cmd/gateway/         # main.go (placeholder — just package main + empty func main)
├── internal/
│   ├── jsonrpc/         # This task
│   ├── transport/       # Future
│   ├── router/          # Future
│   ├── auth/            # Future
│   ├── policy/          # Future
│   ├── proxy/           # Future
│   ├── config/          # Future
│   └── health/          # Future
├── configs/             # Example config files (empty for now)
├── docs/                # Already exists — design docs and task docs
└── go.mod
```

- Module path: `github.com/anguslmm/stile`
- Go version: 1.22 or later
- No external dependencies for this task — stdlib only
- Future directories should exist but can be empty (add a `.gitkeep` or a placeholder `doc.go` with just the package declaration)

---

## 2. JSON-RPC 2.0 Implementation

### Package: `internal/jsonrpc`

Implement the JSON-RPC 2.0 wire types used by MCP. This is a hand-written codec — no framework, no external dependency.

### Types to implement

**Request:**
```go
type Request struct {
    JSONRPC string          `json:"jsonrpc"`
    Method  string          `json:"method"`
    Params  json.RawMessage `json:"params,omitempty"`
    ID      *ID             `json:"id,omitempty"`
}
```

**Response:**
```go
type Response struct {
    JSONRPC string          `json:"jsonrpc"`
    Result  json.RawMessage `json:"result,omitempty"`
    Error   *Error          `json:"error,omitempty"`
    ID      *ID             `json:"id"`
}
```

**Error:**
```go
type Error struct {
    Code    int             `json:"code"`
    Message string          `json:"message"`
    Data    json.RawMessage `json:"data,omitempty"`
}
```

**ID type:**

The JSON-RPC `id` field can be a string, an integer, or null. It must also be possible to distinguish "id is absent" (notification) from "id is null". Implement a custom `ID` type with `MarshalJSON`/`UnmarshalJSON` that handles this.

### Key behaviors

1. **Request vs Notification:** A request has an `id` field (even if null). A notification has no `id` field at all. Use `ID *ID` where a nil pointer means "absent" (notification) and a non-nil pointer to a zero/null ID means "id is null". Provide an `IsNotification()` method.

2. **Batch messages:** A batch is a JSON array of requests/notifications. Implement a `ParseMessage(data []byte) (requests []*Request, isBatch bool, err error)` function. For a single request, return a one-element slice with `isBatch` false. For a batch, return all parsed requests with `isBatch` true. The caller needs `isBatch` to determine response framing: the JSON-RPC 2.0 spec requires that a non-batch request gets a single response object, while a batch gets an array of response objects. Detection: if the first non-whitespace byte is `[`, it's a batch.

3. **Response construction helpers:**
   - `NewResponse(id *ID, result any) (*Response, error)` — marshals result to JSON
   - `NewErrorResponse(id *ID, code int, message string) *Response`

4. **Standard error codes:** Define constants for the JSON-RPC and MCP error codes:
   - `-32700` ParseError
   - `-32600` InvalidRequest
   - `-32601` MethodNotFound
   - `-32602` InvalidParams
   - `-32603` InternalError

5. **Validation:** There is no public `Validate()` method. Instead, validation happens at construction time — `ParseMessage` must check that `jsonrpc` is `"2.0"` and `method` is non-empty for every parsed request, returning an error (or a JSON-RPC InvalidRequest error response) for any that fail. Callers can trust that if they received a `*Request`, it's well-formed.

---

## 3. Testable Deliverables

All tests go in `internal/jsonrpc/jsonrpc_test.go` (or split across files as appropriate).

### Tests that must pass:

1. **Parse single request:** `{"jsonrpc":"2.0","method":"tools/list","id":1}` → one-element slice with method "tools/list", integer ID 1, `isBatch` false
2. **Parse request with params:** `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"db_query"},"id":"abc"}` → Request with string ID "abc", params preserved as raw JSON
3. **Parse notification:** `{"jsonrpc":"2.0","method":"notifications/cancelled"}` → Request with nil ID, `IsNotification()` returns true
4. **Parse batch:** `[{"jsonrpc":"2.0","method":"ping","id":1},{"jsonrpc":"2.0","method":"ping","id":2}]` → slice of 2 requests, `isBatch` true
5. **Round-trip marshal/unmarshal:** Create a Request, marshal it, unmarshal it, verify equality
6. **Response with result:** NewResponse with a map result → valid JSON with result field
7. **Error response:** NewErrorResponse with MethodNotFound → correct code and message, no result field
8. **ID edge cases:** String ID, integer ID, null ID (present but null) all marshal/unmarshal correctly and are distinguishable
9. **Validation at parse time:** `ParseMessage` with empty method or wrong jsonrpc version returns an error
10. **Error implements error interface:** The `Error` type should satisfy the `error` interface via `Error() string`

### Build check:

```bash
go build ./...
go test ./internal/jsonrpc/ -v
go vet ./...
```

All three must pass with zero errors.

---

## 4. Out of Scope

- No MCP-specific method handling (tools/list, tools/call, etc.) — that's for later tasks
- No networking, no HTTP, no server
- No external dependencies
