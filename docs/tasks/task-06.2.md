# Task 6.2: Decouple Roles from API Keys

**Status:** done
**Depends on:** Task 6.1 (role-based access control)
**Needed by:** Task 6.3 (CLI), Task 11 (admin API)

---

## Goal

Move role assignment from API keys to callers. Currently each API key carries a role, and credential injection depends on which key authenticated. This forces callers to manage multiple keys to get the union of multiple roles, which isn't practical — MCP clients configure one token per server.

After this task: a caller has one API key for authentication and multiple roles assigned directly. Tool access is the union of all assigned roles. Credential injection resolves per-upstream by finding the first role (in config order) that has credentials for that upstream.

---

## 1. Database Schema Changes

Replace the `role` column on `api_keys` with a `caller_roles` join table:

```sql
CREATE TABLE callers (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL UNIQUE,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE api_keys (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    caller_id   INTEGER NOT NULL REFERENCES callers(id) ON DELETE CASCADE,
    key_hash    BLOB NOT NULL UNIQUE,
    label       TEXT,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE caller_roles (
    caller_id  INTEGER NOT NULL REFERENCES callers(id) ON DELETE CASCADE,
    role       TEXT NOT NULL,
    PRIMARY KEY (caller_id, role)
);

CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash);
```

Since this is pre-v1, a destructive migration is fine: drop and recreate the tables.

---

## 2. Caller Type Changes

```go
type Caller struct {
    Name         string
    Roles        []string    // all roles assigned to this caller, in config order
    AllowedTools []glob.Glob // union of all roles' patterns
}
```

The `Role` field (singular) is removed. Credential injection no longer depends on which key was used.

---

## 3. CallerStore Changes

### LookupByKey

Returns the caller name only (no role):

```go
func (s *SQLiteStore) LookupByKey(hashedKey [32]byte) (*Caller, error) {
    var name string
    err := s.db.QueryRow(`
        SELECT c.name
        FROM api_keys k
        JOIN callers c ON c.id = k.caller_id
        WHERE k.key_hash = ?
    `, hashedKey[:]).Scan(&name)
    if err != nil {
        return nil, fmt.Errorf("auth: key not found")
    }
    return &Caller{Name: name}, nil
}
```

### RolesForCaller

Now reads from the `caller_roles` table instead of querying distinct roles from `api_keys`:

```go
func (s *SQLiteStore) RolesForCaller(name string) ([]string, error) {
    rows, err := s.db.Query(`
        SELECT cr.role
        FROM caller_roles cr
        JOIN callers c ON c.id = cr.caller_id
        WHERE c.name = ?
    `, name)
    // ... collect into []string
}
```

### AddKey

No longer takes a `role` parameter:

```go
func (s *SQLiteStore) AddKey(callerName string, keyHash [32]byte, label string) error
```

### New: AssignRole / UnassignRole

```go
func (s *SQLiteStore) AssignRole(callerName string, role string) error
func (s *SQLiteStore) UnassignRole(callerName string, role string) error
```

`AssignRole` inserts into `caller_roles`. Idempotent — assigning an already-assigned role is a no-op (use `INSERT OR IGNORE`).

`UnassignRole` deletes from `caller_roles`. Returns error if the assignment didn't exist.

---

## 4. Authenticator Changes

### Credential injection

Replace the single-role lookup with a per-upstream resolution that walks the caller's roles in config order:

```go
func (a *Authenticator) UpstreamToken(roles []string, upstreamName string) (string, bool) {
    for _, role := range roles {
        if env, ok := a.credentials[role]; ok {
            if token, ok := env[upstreamName]; ok {
                return token, true
            }
        }
    }
    return "", false
}
```

The caller's `Roles` slice is ordered by config order (the order roles appear in the YAML). When two roles both have credentials for the same upstream, the first role in config order wins.

### Authenticate

```go
func (a *Authenticator) Authenticate(r *http.Request) (*Caller, error) {
    // ... extract token, hash, lookup ...
    caller, err := a.store.LookupByKey(hash)
    if err != nil {
        return nil, fmt.Errorf("unauthorized")
    }
    roles, err := a.store.RolesForCaller(caller.Name)
    if err != nil {
        return nil, fmt.Errorf("lookup roles: %w", err)
    }
    // Order roles by config order
    caller.Roles = a.orderByConfig(roles)
    caller.AllowedTools = a.unionAllowedTools(caller.Roles)
    return caller, nil
}
```

### Role ordering

The authenticator stores the config-order role names at construction time:

```go
type Authenticator struct {
    store        CallerStore
    credentials  map[string]map[string]string  // role name -> upstream -> token
    allowedTools map[string][]glob.Glob        // role name -> compiled patterns
    roleOrder    []string                       // role names in config order
}
```

```go
func (a *Authenticator) orderByConfig(roles []string) []string {
    roleSet := make(map[string]bool, len(roles))
    for _, r := range roles {
        roleSet[r] = true
    }
    var ordered []string
    for _, r := range a.roleOrder {
        if roleSet[r] {
            ordered = append(ordered, r)
        }
    }
    return ordered
}
```

---

## 5. Proxy Changes

Update credential injection call sites to pass `caller.Roles` instead of `caller.Role`:

```go
token, ok := authenticator.UpstreamToken(caller.Roles, upstreamName)
```

---

## 6. Seed Script Update

Update `cmd/seed/main.go`:
- `AddKey` no longer takes `role`
- Use `AssignRole` to assign roles to callers
- One key per caller is sufficient

---

## 7. Testable Deliverables

### Store tests
1. **AssignRole:** assign role to caller -> RolesForCaller returns it
2. **AssignRole idempotent:** assigning same role twice -> no error, still one entry
3. **UnassignRole:** unassign role -> no longer returned by RolesForCaller
4. **UnassignRole nonexistent:** -> returns error
5. **AddKey without role:** key is stored, LookupByKey returns caller name only
6. **DeleteCaller cascades roles:** delete caller -> caller_roles entries gone

### Auth tests
7. **Credential injection uses config order:** caller has roles [web-tools, database] both with credentials for same upstream -> web-tools credentials win (first in config order)
8. **Credential injection falls through:** first role has no credentials for upstream, second role does -> second role's credentials used
9. **Single key, multiple roles:** caller has one key and two assigned roles -> CanAccessTool matches union of both
10. **UpstreamToken takes role slice:** new signature works correctly

### Integration
11. **tools/list filtered by union of assigned roles:** caller with web-tools + database roles -> sees tools from both
12. **tools/call uses correct credentials per upstream:** call tool on upstream where only one role has credentials -> that role's token is injected

### Build check

```bash
go build ./...
go test ./...
go vet ./...
```

---

## 8. Migration Notes

- Existing `/tmp/stile-test.db` databases are incompatible — re-seed after this change
- `Caller.Role` (singular string) is removed in favor of `Caller.Roles` ([]string)
- `UpstreamToken` signature changes from `(role string, upstream string)` to `(roles []string, upstream string)`
- `AddKey` signature changes: role parameter removed
