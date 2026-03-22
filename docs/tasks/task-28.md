# Task 28 — Admin CLI remote mode (`--remote`)

Status: **done**

## Problem

The CLI caller-management commands (`add-caller`, `add-key`, `list-callers`, etc.) all open the database directly via `openStore()`. This works for local development but is unusable in production — operators must resort to manual `curl` requests against the admin API.

We need a way to use the same ergonomic CLI commands against a remote Stile instance's admin API.

## Design

Add a `--remote <base-url>` flag (e.g. `--remote https://stile.prod.internal:9090`) to every admin CLI subcommand. When present, the command sends HTTP requests to the admin API instead of opening a local database.

Also add `--admin-key <key>` (or `STILE_ADMIN_KEY` env var) for authentication against the remote admin API.

### Flag behavior

| Flags present | Behavior |
|---|---|
| Neither `--remote` nor `--db`/`--config` | Open local `stile.db` (existing default) |
| `--db` or `--config` | Open local database (existing behavior) |
| `--remote <url>` | Send HTTP requests to the admin API at `<url>` |
| `--remote` + `--db` | Error: mutually exclusive |

### Implementation

1. **`internal/admin/client.go`** — A thin HTTP client that implements the same operations the CLI needs (create caller, list callers, create key, assign role, etc.). Not a general-purpose SDK — just enough for the CLI. Uses `net/http` from stdlib.

2. **Update `cmd/gateway/cli.go`** — Add `--remote` and `--admin-key` flags to `addCLIFlags()`. When `--remote` is set, construct the admin client instead of calling `openStore()`. Each `run*` function branches on whether it has a store or a remote client.

3. **Consider a shared interface** — If the branching gets messy, extract a small interface that both the direct store path and the HTTP client satisfy, so each `run*` function doesn't need an if/else. But only do this if it actually simplifies the code — don't over-abstract.

### Command mapping

| CLI command | HTTP method | Admin API path |
|---|---|---|
| `add-caller --name X` | `POST /admin/callers` | `{"name":"X"}` |
| `list-callers` | `GET /admin/callers` | — |
| `remove-caller --name X` | `DELETE /admin/callers/{name}` | — |
| `add-key --caller X` | `POST /admin/callers/{name}/keys` | `{"label":"..."}` |
| `revoke-key --caller X --label L` | `DELETE /admin/callers/{name}/keys/{id}` | Need to list keys first to resolve label→ID |
| `assign-role --caller X --role R` | `POST /admin/callers/{name}/roles` | `{"role":"R"}` |
| `unassign-role --caller X --role R` | `DELETE /admin/callers/{name}/roles/{role}` | — |

### Auth

- `--admin-key` flag or `STILE_ADMIN_KEY` environment variable.
- Sent as `Authorization: Bearer <key>` header.
- Required when `--remote` is set; error if missing.

### Output

CLI output should be identical regardless of whether the command went through the local store or the remote API. The admin client parses the JSON responses and the existing `run*` functions format the output the same way.

## Edge cases

- **`revoke-key` by label**: The admin API deletes keys by numeric ID, but the CLI uses `--label`. In remote mode, the client must first `GET /admin/callers/{name}/keys`, find the key with the matching label, then `DELETE` by ID.
- **`remove-caller --force`**: The `--force` flag skips the key-count safety check. In remote mode, the client can `GET /admin/callers/{name}` to check key count, then `DELETE` — or just `DELETE` directly when `--force` is set (the API doesn't enforce the safety check).
- **Connection errors**: Surface clear error messages — "cannot reach remote at <url>: <reason>".

## Testing

- Unit tests for the admin HTTP client using `httptest.Server` that mimics admin API responses.
- Update existing CLI tests to cover the `--remote` flag parsing and mutual-exclusion with `--db`.
- One or two integration-style tests that spin up a real server and exercise a round trip (e.g. `add-caller` via `--remote`, then `list-callers` via `--remote`).

## Files to create/modify

- **Create**: `internal/admin/client.go`, `internal/admin/client_test.go`
- **Modify**: `cmd/gateway/cli.go`, `cmd/gateway/cli_test.go`
