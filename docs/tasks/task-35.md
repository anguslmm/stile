# Task 35 — Tool naming and upstream attribution

Status: **todo**

## Goal

Solve two related problems with how tools are presented to clients:

1. **Non-descript tool names** — Many MCP servers expose tools with generic names like `search`, `get`, or `run`. When Stile aggregates tools from multiple upstreams, clients see a flat list of ambiguous names with no way to tell which upstream they belong to.
2. **Upstream attribution** — Clients have no visibility into which upstream provides a given tool. This matters for debugging, access control reasoning, and operator UX.

## Background

Today, Stile's router caches tools from each upstream and merges them into a single `tools/list` response. If two upstreams both expose a tool called `search`, only one wins (last-registered or first-discovered, depending on timing). Even without collisions, a tool called `query` tells the client nothing about what it queries.

### Tool name constraints

The MCP spec recommends tool names match `[A-Za-z0-9_\-.]` with a max length of 128 characters. However, Claude's client enforces a stricter pattern: `^[a-zA-Z0-9_]{1,64}$` — alphanumeric and underscore only, max 64 characters. Since Claude is a primary consumer, Stile treats Claude's constraints as the effective spec.

Snake_case is the dominant naming convention in real-world MCP servers (e.g. `create_pull_request`, `search_repositories`). This means a single underscore separator like `github_create_issue` is ambiguous — you can't tell where the prefix ends. Double underscore (`__`) is unambiguous and has precedent (Docker MCP Gateway uses it). Stile uses `__` as the prefix separator.

## Design

### Tool namespacing (prefix)

By default, Stile prefixes every tool with its upstream's `name` using `__` as the separator. The optional `tool_prefix` field overrides the default prefix. Setting `tool_prefix` to an explicit empty string disables prefixing for that upstream.

```yaml
upstreams:
  - name: github
    url: https://mcp-github.example.com
    # no tool_prefix set — tools become github__search, github__create_issue, etc.
  - name: jira
    url: https://mcp-jira.example.com
    tool_prefix: j            # override — tools become j__search, j__create_ticket, etc.
  - name: legacy
    url: https://mcp-legacy.example.com
    tool_prefix: ""           # explicit empty string — no prefix, tools pass through unchanged
```

- Stile prepends `{prefix}__` to every tool name from that upstream in `tools/list` responses.
- Inbound `tools/call` requests are matched after stripping the prefix, so the upstream receives the original tool name.
- Collision detection: at startup (or tool discovery time), if two tools resolve to the same final name, log a warning and reject the config or use a deterministic tiebreak.
- Validate at config load that `prefix + "__" + longest_tool_name` fits within 64 characters.

### Upstream metadata in tool listings

Extend the tool entries in `tools/list` responses with upstream attribution metadata. The MCP spec allows additional properties on tool objects, so this is spec-compliant:

```json
{
  "name": "github__search",
  "description": "Search GitHub repositories, issues, and PRs",
  "inputSchema": { ... },
  "annotations": {
    "x-stile-upstream": "github",
    "x-stile-original-name": "search"
  }
}
```

- `x-stile-upstream` — the upstream name from config.
- `x-stile-original-name` — the tool's original name before prefixing (only present when a prefix was applied).
- These go in the `annotations` field (MCP spec's extension point for tool metadata).

## Implementation plan

### 35.1 — Tool prefix in router

1. Add `tool_prefix` to upstream config as `*string` (nil = use upstream name as prefix, empty string = no prefix, non-empty = use as prefix).
2. Modify router's tool discovery to apply prefixes when building the merged tool list.
3. Modify router's tool lookup to strip prefixes when resolving inbound `tools/call` requests.
4. Validate prefixed name length <= 64 characters at config load.
5. Collision detection and logging.
6. Tests: prefixed tool listing, correct routing after prefix stripping, collision warnings, explicit empty prefix disables prefixing.

### 35.2 — Upstream metadata annotations

1. Add `x-stile-upstream` and `x-stile-original-name` to tool entries in `tools/list` responses.
2. Ensure annotations don't leak into the request sent to the upstream (strip before forwarding).
3. Tests.

## Files to create/modify

- **Modify**: `internal/config/config.go` (add `tool_prefix` to upstream config)
- **Modify**: `internal/router/router.go` (prefix application, prefix stripping on lookup, collision detection)
- **Modify**: `internal/router/router_test.go`
- **Modify**: `internal/proxy/proxy.go` (if annotation injection happens at proxy level)

## What this does NOT include

- Tool aliasing (arbitrary rename mapping) — could be a future feature but adds complexity.
- Per-caller tool visibility filtering — already handled by ACLs in the policy layer.
- Tool description rewriting — operators can configure this at the upstream MCP server level.
