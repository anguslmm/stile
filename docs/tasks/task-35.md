# Task 35 — Tool naming and upstream attribution

Status: **todo**

## Goal

Solve two related problems with how tools are presented to clients:

1. **Non-descript tool names** — Many MCP servers expose tools with generic names like `search`, `get`, or `run`. When Stile aggregates tools from multiple upstreams, clients see a flat list of ambiguous names with no way to tell which upstream they belong to.
2. **Upstream attribution** — Clients have no visibility into which upstream provides a given tool. This matters for debugging, access control reasoning, and operator UX.

## Background

Today, Stile's router caches tools from each upstream and merges them into a single `tools/list` response. If two upstreams both expose a tool called `search`, only one wins (last-registered or first-discovered, depending on timing). Even without collisions, a tool called `query` tells the client nothing about what it queries.

## Design

### Tool namespacing (prefix)

Add an optional `tool_prefix` field to the upstream config:

```yaml
upstreams:
  - name: github
    url: https://mcp-github.example.com
    tool_prefix: github     # tools become github_search, github_create_issue, etc.
  - name: jira
    url: https://mcp-jira.example.com
    tool_prefix: jira       # tools become jira_search, jira_create_ticket, etc.
```

- When `tool_prefix` is set, Stile prepends `{prefix}_` to every tool name from that upstream in `tools/list` responses.
- Inbound `tools/call` requests are matched after stripping the prefix, so the upstream receives the original tool name.
- If `tool_prefix` is not set, tool names pass through unchanged (backwards compatible).
- Collision detection: at startup (or tool discovery time), if two tools resolve to the same final name, log a warning and reject the config or use a deterministic tiebreak.

### Upstream metadata in tool listings

Extend the tool entries in `tools/list` responses with upstream attribution metadata. The MCP spec allows additional properties on tool objects, so this is spec-compliant:

```json
{
  "name": "github_search",
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

### Auto-prefix mode

Add a gateway-level config option:

```yaml
gateway:
  auto_prefix_tools: true   # default: false
```

When enabled, Stile automatically uses each upstream's `name` as the `tool_prefix` for any upstream that doesn't explicitly set one. This is a convenience for operators who want namespace isolation without manually configuring every prefix.

## Implementation plan

### 35.1 — Tool prefix in router

1. Add `tool_prefix` to upstream config.
2. Modify router's tool discovery to apply prefixes when building the merged tool list.
3. Modify router's tool lookup to strip prefixes when resolving inbound `tools/call` requests.
4. Collision detection and logging.
5. Tests: prefixed tool listing, correct routing after prefix stripping, collision warnings.

### 35.2 — Upstream metadata annotations

1. Add `x-stile-upstream` and `x-stile-original-name` to tool entries in `tools/list` responses.
2. Ensure annotations don't leak into the request sent to the upstream (strip before forwarding).
3. Tests.

### 35.3 — Auto-prefix mode

1. Add `auto_prefix_tools` gateway config option.
2. At config load / router init, apply upstream name as prefix where `tool_prefix` is empty and auto-prefix is enabled.
3. Tests and docs update.

## Files to create/modify

- **Modify**: `internal/config/config.go` (add `tool_prefix` to upstream, `auto_prefix_tools` to gateway)
- **Modify**: `internal/router/router.go` (prefix application, prefix stripping on lookup, collision detection)
- **Modify**: `internal/router/router_test.go`
- **Modify**: `internal/proxy/proxy.go` (if annotation injection happens at proxy level)

## What this does NOT include

- Tool aliasing (arbitrary rename mapping) — could be a future feature but adds complexity.
- Per-caller tool visibility filtering — already handled by ACLs in the policy layer.
- Tool description rewriting — operators can configure this at the upstream MCP server level.
