# Task 10.1: Admin API — Role Management Endpoints

**Status:** done
**Depends on:** Task 10 (admin API for caller/key management)
**Needed by:** Task 11 (integration tests)

---

## Goal

Add admin API endpoints for managing caller roles, so operators can assign and unassign roles without CLI access to the gateway host.

---

## Endpoints

### `POST /admin/callers/{name}/roles`

Assign a role to a caller.

**Request:**
```json
{
  "role": "dev"
}
```

**Response (200):**
```json
{
  "name": "angus",
  "roles": ["dev"]
}
```

**Errors:**
- 404 if caller not found
- 400 if role is empty

### `DELETE /admin/callers/{name}/roles/{role}`

Unassign a role from a caller.

**Response (204):** no body

**Errors:**
- 404 if caller not found or role not assigned

### `GET /admin/callers/{name}/roles`

List roles assigned to a caller.

**Response (200):**
```json
{
  "roles": ["dev", "prod"]
}
```

**Errors:**
- 404 if caller not found

---

## Implementation Notes

- The store already has `AssignRole`, `UnassignRole`, and `RolesForCaller` methods from Task 6.2/6.3.
- Just needs HTTP handler methods in `internal/admin/handler.go` and route registration.
- Include the caller's roles in the `GET /admin/callers/{name}` detail response (currently only shows keys).

---

## Testable Deliverables

1. **Assign role:** POST → 200, role appears in caller's roles
2. **Assign role idempotent:** POST same role twice → 200, no duplicate
3. **Assign role unknown caller:** POST → 404
4. **Assign role empty:** POST with empty role → 400
5. **Unassign role:** DELETE → 204, role removed
6. **Unassign role not assigned:** DELETE → 404
7. **List roles:** GET → returns all assigned roles
8. **Caller detail includes roles:** GET /admin/callers/{name} shows roles

### Build check

```bash
go build ./...
go test ./...
go vet ./...
```
