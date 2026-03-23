# Task 10: Admin API for Caller Management

**Status:** done
**Depends on:** Task 6.1 (role-based access control), Task 6.2 (decouple roles from keys), Task 6.3 (CLI — establishes CallerStore methods)
**Needed by:** Task 11 (integration tests cover admin endpoints)

---

## Goal

Add HTTP admin endpoints for managing callers and API keys, so operators can manage access without CLI access to the gateway host. All endpoints live under `/admin/` and are protected by the existing admin auth middleware (`ADMIN_API_KEY`).

Under the role-based model (Task 6.2), callers are named identities. Roles are assigned to callers (not individual keys) via the `caller_roles` table. Tool access and upstream credentials are determined by the caller's assigned roles, which are defined in YAML config — not managed via the admin API.

---

## 1. Endpoints

### `POST /admin/callers`

Create a new caller.

**Request:**
```json
{
  "name": "angus"
}
```

**Response (201):**
```json
{
  "name": "angus",
  "created_at": "2026-03-19T12:00:00Z"
}
```

**Errors:**
- 409 if caller name already exists
- 400 if name is empty

### `GET /admin/callers`

List all callers.

**Response (200):**
```json
{
  "callers": [
    {
      "name": "angus",
      "key_count": 2,
      "roles": ["dev", "prod"],
      "created_at": "2026-03-19T12:00:00Z"
    }
  ]
}
```

`roles` lists the roles assigned to the caller (via the `caller_roles` table), so the operator can see which roles are assigned at a glance.

### `GET /admin/callers/{name}`

Get a single caller's details.

**Response (200):**
```json
{
  "name": "angus",
  "roles": ["dev", "prod"],
  "keys": [
    {
      "id": 1,
      "label": "laptop",
      "created_at": "2026-03-19T12:00:00Z"
    },
    {
      "id": 2,
      "label": "CI",
      "created_at": "2026-03-19T12:05:00Z"
    }
  ],
  "created_at": "2026-03-19T12:00:00Z"
}
```

Roles are listed at the caller level (not per-key), reflecting the Task 6.2 design where roles are assigned to callers. Keys are listed with metadata but never expose the key value or hash.

**Errors:**
- 404 if caller not found

### `DELETE /admin/callers/{name}`

Delete a caller and all their API keys.

**Response (204):** no body

**Errors:**
- 404 if caller not found

### `POST /admin/callers/{name}/keys`

Generate a new API key for a caller.

**Request:**
```json
{
  "label": "CI pipeline"
}
```

**Response (201):**
```json
{
  "key": "sk-a1b2c3d4e5f6...",
  "label": "CI pipeline",
  "created_at": "2026-03-19T12:00:00Z"
}
```

The `key` field is included **only** in the creation response. It is never returned by any other endpoint.

**Note:** The original design accepted a `role` parameter here (per-key role assignment). Task 6.2 decoupled roles from keys — roles are now assigned to callers via `POST /admin/callers/{name}/roles` (Task 10.1).

**Errors:**
- 404 if caller not found

### `GET /admin/callers/{name}/keys`

List keys for a caller (metadata only, no secrets).

**Response (200):**
```json
{
  "keys": [
    {
      "id": 1,
      "label": "laptop",
      "created_at": "2026-03-19T12:00:00Z"
    }
  ]
}
```

### `DELETE /admin/callers/{name}/keys/{id}`

Revoke a specific API key.

**Response (204):** no body

**Errors:**
- 404 if key not found

---

## 2. CallerStore Extensions

The existing `SQLiteStore` needs additional methods beyond what Task 6.3 adds:

```go
func (s *SQLiteStore) ListCallers() ([]CallerInfo, error)
func (s *SQLiteStore) GetCaller(name string) (*CallerDetail, error)
func (s *SQLiteStore) ListKeys(callerName string) ([]KeyInfo, error)
func (s *SQLiteStore) DeleteKey(callerName string, keyID int64) error
```

Types for API responses (no secrets):
```go
type CallerInfo struct {
    Name      string
    KeyCount  int
    Roles     []string  // roles assigned to caller (via caller_roles table)
    CreatedAt time.Time
}

type CallerDetail struct {
    Name      string
    Roles     []string
    Keys      []KeyInfo
    CreatedAt time.Time
}

type KeyInfo struct {
    ID        int64
    Label     string
    CreatedAt time.Time
}
```

---

## 3. Admin Handler

```go
type AdminHandler struct {
    store  *SQLiteStore
    router *router.RouteTable
}
```

The admin handler registers all `/admin/` routes. The existing `/admin/refresh` moves here too, consolidating all admin functionality.

---

## 4. Security Considerations

- All endpoints behind `AdminAuthMiddleware` (same `ADMIN_API_KEY` env var)
- API keys are never logged, never returned after creation
- Key hashes are never exposed via any endpoint
- Dev mode (no admin key, no callers) still allows open access for local development

---

## 5. Testable Deliverables

1. **Create caller:** POST → 201, caller exists in DB
2. **Create duplicate caller:** POST → 409
3. **List callers:** GET → returns all callers with key counts and roles
4. **Get caller detail:** GET with name → returns caller with key metadata
5. **Get unknown caller:** GET → 404
6. **Delete caller:** DELETE → 204, caller and keys removed
7. **Delete unknown caller:** DELETE → 404
8. **Create key:** POST → 201, response includes raw key, key hash in DB
9. **Create key unknown caller:** POST → 404
10. **List keys:** GET → returns key metadata, no secrets
11. **Revoke key:** DELETE → 204, key removed, other keys unaffected
12. **Revoke unknown key:** DELETE → 404
13. **Admin auth required:** all endpoints return 403 without valid admin key
14. **Existing /admin/refresh still works:** backwards compatible

### Build check

```bash
go build ./...
go test ./...
go vet ./...
```

---

## 6. Out of Scope

- Caller self-service (callers managing their own keys)
- API key rotation (revoke + create is the workflow)
- OAuth2/OIDC admin authentication
- Audit logging of admin actions (could be a future task)
- Bulk import/export of callers
- Managing role definitions via API (roles are defined in YAML config, not the database)
