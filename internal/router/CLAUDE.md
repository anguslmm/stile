# router

Maps tool names to upstream transports, handling tool discovery, caching, conflict resolution, and background refresh.

## Key Types

- **`RouteTable`** — Central data structure. Holds the tool-name-to-route index and the upstream list. All public methods are goroutine-safe via an internal `sync.RWMutex`.
- **`Route`** — A resolved mapping: a `ToolSchema` (with prefixed name and annotations) plus a pointer to the `Upstream` that owns it, and `OriginalName` (the tool's name before prefix was applied).
- **`Upstream`** — An upstream entry: name, `Transport`, discovered `Tools` (original), `PrefixedTools` (with prefix and annotations applied), resolved `Prefix`, staleness flag, and last refresh time.
- **`RefreshResult`** / **`UpstreamStatus`** — Returned by `Refresh`; consumed by the admin endpoint to report per-upstream tool counts and staleness.

## Key Functions / Methods

- **`New`** — Constructs a `RouteTable` from a transport map and upstream configs, runs an initial `Refresh`. Individual upstream failures are non-fatal.
- **`Resolve(toolName)`** — Thread-safe lookup returning the `Route` for a tool, or an error if unknown.
- **`ListTools()`** — Returns the merged tool list across all upstreams (ordered by upstream order), with tool name prefixes and `x-stile-upstream`/`x-stile-original-name` annotations applied.
- **`Refresh(ctx)`** — Re-discovers tools from all upstreams sequentially, rebuilds the route index, emits metrics and logs. Upstream failures mark the upstream stale but preserve existing tools.
- **`RefreshUpstream(ctx, name)`** — Targeted single-upstream refresh.
- **`StartBackgroundRefresh(interval)`** — Launches a goroutine that calls `Refresh` on a ticker. Stop via `Close`.
- **`AddUpstream / RemoveUpstream`** — Dynamic upstream management; `RemoveUpstream` closes the transport.
- **`Close`** — Stops the background goroutine and closes all transports. Safe to call once only (uses `sync.Once`).

## Design Notes

- **Tool name prefixing**: by default, every tool is prefixed with `{upstream_name}__{tool_name}`. The `tool_prefix` config field overrides the prefix; setting it to `""` disables prefixing. Non-alphanumeric characters in the upstream name are replaced with `_` when used as the default prefix (e.g. `my-server` → `my_server`).
- **Upstream attribution**: `ListTools` annotates each tool with `x-stile-upstream` (upstream name) and `x-stile-original-name` (original tool name before prefix, only when a prefix was applied) in the MCP `annotations` field.
- **Duplicate prefixed tool names**: first upstream in config order wins; later duplicates are silently dropped with a warning log.
- **Tool discovery protocol**: `discoverTools` sends MCP `initialize` then `notifications/initialized` before `tools/list`. Both the initialize response error and the notification are treated as non-fatal to handle servers that don't require them; `tools/list` failure is fatal for that upstream.
- **Metrics**: `m` (a `*metrics.Metrics`) may be nil; all metric calls are guarded.
- **Background refresh goroutine**: `done` channel is pre-closed at construction so `Close` is safe to call even if `StartBackgroundRefresh` was never called.
