# admin

HTTP admin API for managing callers, API keys, and roles at runtime.

## Key Exported Types

- **`Handler`** — HTTP handler that serves all `/admin/...` endpoints. Wraps an `auth.Store` and `*router.RouteTable`. Routes must be protected by admin auth middleware by the caller — `Register` itself applies no auth.
- **`Client`** — Thin HTTP client that mirrors the same operations as `auth.Store` (plus cache/refresh), targeting a remote Stile instance. Used by the admin CLI's `--remote` mode.

## Key Exported Functions

- **`NewHandler(store auth.Store, rt *router.RouteTable) *Handler`** — Constructs the handler.
- **`(h *Handler) Register(mux *http.ServeMux)`** — Registers all routes on the provided mux.
- **`NewClient(baseURL, adminKey string) *Client`** — Constructs the remote client; sends `Authorization: Bearer <adminKey>` on every request.

## Routes Registered by Handler

- `POST/GET /admin/callers` — create, list callers
- `GET/DELETE /admin/callers/{name}` — get, delete caller
- `POST/GET/DELETE /admin/callers/{name}/keys` and `.../keys/{id}` — create, list, delete keys
- `POST/GET/DELETE /admin/callers/{name}/roles` and `.../roles/{role}` — assign, list, remove roles
- `POST /admin/refresh` — trigger route table refresh
- `GET/DELETE /admin/cache` — cache stats / flush (no-ops if store is not `auth.Cacheable`)

## Design Notes

- `Handler` type-asserts `auth.Store` to `auth.Cacheable` at request time; cache endpoints gracefully return a status message if the store doesn't implement it.
- Raw API keys are never stored; `Handler.createKey` generates the key, SHA-256 hashes it, and stores only the hash. The raw key is returned once in the response.
- `Client.RevokeKey` resolves label to ID by calling `ListKeys` first — there is no server-side label-based delete endpoint.
- All response types (`callerListItem`, `keyItem`, etc.) are package-private; `Client` maps them to `auth.CallerInfo` / `auth.KeyInfo` for callers.
- Auth middleware is the caller's responsibility — `Register` does not wrap routes.
