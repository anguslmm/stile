# Task 5: Router + Route Table + Tool Discovery & Caching

**Status:** done
**Depends on:** Task 3 (proxy handler), Task 4 (stdio transport)
**Needed by:** Task 6 (auth — tool filtering uses the route table)

---

## Goal

Replace the proxy handler's simple tool→upstream map with a proper router that supports tool schema caching, background refresh, and an admin refresh endpoint. After this task, the gateway handles upstream flakiness gracefully and keeps its tool catalog up to date without restarts.

---

## 1. Router

### Package: `internal/router`

### Route Table

The route table maps tool names to upstreams. It is the source of truth for "which upstream owns this tool" and "what tools are available."

```go
type RouteTable struct {
    mu      sync.RWMutex
    entries map[string]*Route  // tool name → route
}

type Route struct {
    Tool      transport.ToolSchema
    Upstream  *Upstream
}

type Upstream struct {
    Name      string
    Transport transport.Transport
    Config    config.UpstreamConfig
    Tools     []transport.ToolSchema  // last known tool list
    Stale     bool                     // true if last refresh failed
    LastRefresh time.Time
}
```

### Key methods

- `Resolve(toolName string) (*Route, error)` — look up which upstream handles a tool. Returns an error if the tool is not found.
- `ListTools() []transport.ToolSchema` — return the merged list of all tools from all upstreams.
- `Refresh(ctx context.Context)` — re-discover tools from all upstreams and rebuild the route table.
- `RefreshUpstream(ctx context.Context, name string) error` — refresh a single upstream.

### Constructor

```go
func New(transports map[string]transport.Transport, configs []config.UpstreamConfig) (*RouteTable, error)
```

On construction:
1. Build the `Upstream` list from transports and configs
2. Run an initial `Refresh` to populate the route table
3. Individual upstream failures during initial refresh are non-fatal — log a warning, mark as stale

---

## 2. Tool Discovery

When refreshing an upstream:

1. Call `transport.Send()` with a `tools/list` JSON-RPC request
2. Parse the result to extract `[]ToolSchema`
3. Update the upstream's `Tools` and `LastRefresh`
4. If the call fails, mark the upstream as `Stale` but **keep its existing tools** in the route table — don't remove them. They become available again when the upstream recovers.

### Conflict resolution

If two upstreams expose a tool with the same name, the first upstream in config order wins. Log a warning about the conflict.

---

## 3. Background Refresh

Start a background goroutine that calls `Refresh()` on a configurable interval.

### Config addition

Add to the config structs (extend as needed):

```yaml
server:
  address: ":8080"
  tool_cache_ttl: 5m   # how often to refresh tool schemas
```

Default: 5 minutes. The goroutine ticks on this interval and refreshes all upstreams.

Stop the goroutine cleanly via a `Close()` method on the router.

---

## 4. Admin Refresh Endpoint

Add `POST /admin/refresh` to the server's HTTP mux (in `internal/server`). When hit, it triggers an immediate `Refresh()` on the router and returns the result (number of tools, any errors).

Response:
```json
{
  "upstreams": {
    "github": {"tools": 12, "stale": false},
    "local-db": {"tools": 2, "stale": false}
  },
  "total_tools": 14
}
```

This is a convenience endpoint for operators, not part of the MCP protocol.

**Auth:** This endpoint must NOT be inside the MCP auth middleware (it is not an MCP call, and operators are not MCP callers). Admin endpoint auth is added in Task 6 — see that task for details. For now, register the route outside the auth middleware wrapper. Task 6 will add an admin auth guard.

---

## 5. Integrate with Proxy Handler

Refactor `internal/proxy` to use the router instead of its own tool map:

- `tools/list` calls `router.ListTools()`
- `tools/call` calls `router.Resolve(toolName)` to get the transport, then forwards

The proxy handler should accept the router as a dependency:

```go
func NewHandler(router *router.RouteTable) *Handler
```

The transport creation and tool discovery that was previously in the proxy handler moves to wherever the router is constructed (likely `cmd/gateway/main.go` or a setup function).

---

## 6. Testable Deliverables

### Router unit tests (`internal/router/`)

Use mock transports.

1. **Initial discovery:** two upstreams with different tools → route table has all tools, Resolve works for each
2. **ListTools merges correctly:** tools from all upstreams appear in the merged list
3. **Resolve unknown tool:** returns error
4. **Refresh updates tools:** upstream adds a new tool → after refresh, new tool is resolvable
5. **Stale upstream keeps tools:** upstream fails on refresh → existing tools still resolvable, upstream marked stale
6. **Upstream recovery:** stale upstream succeeds on next refresh → stale flag cleared
7. **Duplicate tool name:** two upstreams expose same tool → first in config order wins, no crash
8. **Background refresh fires:** start router with short TTL (e.g. 50ms), verify refresh is called multiple times, then Close() stops it cleanly

### Server tests

9. **Admin refresh endpoint:** POST /admin/refresh → returns upstream status JSON

### Integration

10. **Proxy uses router:** full stack with mock upstreams → tools/list and tools/call work through the router

### Build check

```bash
go build ./...
go test ./...
go vet ./...
```

---

## 7. Out of Scope

- Per-caller tool filtering (Task 6 — auth)
- Global blocklists/allowlists (Task 7 — policy)
- Glob-pattern tool matching in config `tools` field (nice to have — can be added here or in Task 7)
