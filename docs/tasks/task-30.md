# Task 30 — Admin dashboard (embedded web UI)

Status: **todo**

## Goal

Add an embedded web UI to the Stile binary for managing callers, API keys, and role assignments, plus read-only visibility into the running configuration and upstream health. No config mutation — YAML stays in git as the source of truth.

## Design principles

- **Callers are runtime state** — already database-backed and managed via the admin API. The UI is a frontend for the existing `/admin/*` endpoints.
- **Config is declarative** — defined in YAML, committed to git, applied at deploy time. The UI shows what's running but doesn't modify it.
- **Single binary** — the UI is embedded via `embed.FS`, no separate process or deployment.

## New backend endpoints

Add these to `internal/admin/handler.go` alongside the existing caller/key/role endpoints:

### `GET /admin/config`

Returns a JSON representation of the running config (sanitized — no secrets, no TLS key paths). Enough for an operator to see what upstreams are configured, what roles exist, rate limit defaults, etc.

Implementation: add a method to `Config` (or a standalone function) that produces a JSON-safe view. Strip sensitive fields (auth token env var names are fine, but don't leak values).

### `GET /admin/status`

Returns upstream health and basic gateway status:

```json
{
  "upstreams": [
    {
      "name": "github",
      "transport": "streamable-http",
      "healthy": true,
      "tools_cached": 12,
      "last_check": "2026-03-22T10:00:00Z"
    }
  ],
  "callers_count": 5,
  "uptime_seconds": 3600
}
```

Draws from the health checker and router's cached tool state.

## Frontend

### Tech choice

Use a lightweight approach — either:

- **htmx + Go templates** — server-rendered, zero JS build step, smallest footprint
- **Preact/Svelte SPA** — richer interactivity, but needs a build step and `embed.FS`

Recommend starting with htmx + Go templates for simplicity. Can migrate to an SPA later if the UI needs grow.

### Pages

| Page | Description | Backend |
|---|---|---|
| Dashboard | Upstream health, caller count, uptime | `GET /admin/status` |
| Callers | List callers with key count and roles | `GET /admin/callers` |
| Caller detail | Keys, roles, with add/remove actions | `GET/POST/DELETE /admin/callers/{name}/*` |
| Config viewer | Read-only view of running configuration | `GET /admin/config` |
| Audit log | Filterable, paginated audit log browser | `GET /admin/audit` |

### Embedding

```go
//go:embed ui/dist/*
var uiFS embed.FS

// Serve under /admin/ui/
mux.Handle("/admin/ui/", http.StripPrefix("/admin/ui/", http.FileServerFS(uiFS)))
```

The admin auth middleware already protects `/admin/*`, so the UI is automatically gated behind the same admin key.

## Implementation plan

### 30.1 — Backend: config and status endpoints

1. Add `ConfigJSON()` method or standalone function that serializes a sanitized `Config` to a JSON-friendly struct.
2. Add `GET /admin/config` handler to `admin.Handler`.
3. Add `GET /admin/status` handler — needs access to health checker and router for tool cache stats. Extend `admin.NewHandler` params or add a `StatusProvider` interface.
4. Tests for both endpoints.

### 30.2 — Frontend: embedded UI

1. Create `internal/admin/ui/` with templates and static assets.
2. Dashboard page with upstream status cards.
3. Callers list page with table (name, key count, roles).
4. Caller detail page with key management (create key — show once, revoke) and role assignment (add/remove from dropdown of configured roles).
5. Config viewer page (formatted JSON or YAML-like display, read-only).
6. Wire up `embed.FS` and register the file server on `/admin/ui/`.

### 30.3 — Audit log viewer

1. Add a `Reader` interface to `internal/audit/` (separate from `Store`) with a `Query` method:
   - Filters: caller, tool, upstream, status, time range (start/end).
   - Pagination: limit + offset, default 50 rows, newest-first.
   - Returns `[]Entry` (reuse existing type, add `ID int64` field).
2. Implement `Reader` for both SQLite and Postgres stores.
3. Add `GET /admin/audit` endpoint to `admin.Handler` — accepts query params for filters, returns JSON.
4. Add audit log page to the UI:
   - Filter form: caller, tool, upstream, status dropdowns/inputs, date range picker.
   - Results table with pagination (prev/next).
   - Row expansion to view full params JSON.
5. Tests for the `Reader` implementations and the endpoint.

Design note: keep `Store` (write path) and `Reader` (read path) as separate interfaces. The proxy only needs `Store`; only the admin handler needs `Reader`. Both SQLite and Postgres concrete types implement both.

### 30.4 — Polish

1. Basic styling (keep it minimal — this is an ops tool, not a consumer product).
2. Error states and empty states.
3. Navigation between pages.
4. Docs update: add a section on the admin UI to the operational runbook (including the audit log viewer).

## Files to create/modify

- **Create**: `internal/admin/config_view.go` (sanitized config serialization)
- **Create**: `internal/admin/ui/` (templates, static assets)
- **Modify**: `internal/audit/audit.go` (add `Reader` interface, `ID` field to `Entry`, implement for SQLite)
- **Modify**: `internal/audit/postgres.go` (implement `Reader` for Postgres)
- **Modify**: `internal/admin/handler.go` (new endpoints, embed + file server, audit query endpoint)
- **Modify**: `cmd/gateway/main.go` (pass health checker / router / audit reader to admin handler)

## What this does NOT include

- Config mutation via the UI (config stays in YAML/git)
- User authentication for the UI (uses the same admin API key auth)
- Multi-tenancy or user accounts
