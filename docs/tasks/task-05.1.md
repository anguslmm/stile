# Task 5.1: Role-Based Access Control

**Status:** not started
**Depends on:** Task 6 (auth — current implementation)
**Needed by:** Task 5.2 (CLI), Task 11 (admin API)

---

## Goal

Refactor auth so that roles define both upstream credentials **and** tool access patterns. Callers no longer have their own `allowed_tools`; instead, their access is determined by which roles their API keys map to. A caller's tool access is the **union** of all roles they hold.

This simplifies access management: update a role once, and every caller using that role gets the new permissions.

---

## 1. Config Changes

Rename `auth_envs` → `roles` in YAML and all config types.

```yaml
roles:
  web-tools:
    allowed_tools:
      - "github/*"
      - "notion/*"
    credentials:
      github: GITHUB_TOKEN
      notion: NOTION_TOKEN
  database:
    allowed_tools:
      - "db_*"
    credentials:
      postgres-mcp: POSTGRES_TOKEN
  full-access:
    allowed_tools:
      - "*"
    credentials:
      github: GITHUB_TOKEN
      notion: NOTION_TOKEN
      postgres-mcp: POSTGRES_TOKEN
```

### Updated config types

Rename `AuthEnvConfig` → `RoleConfig`:

```go
type RoleConfig struct {
    name         string
    allowedTools []string          // glob patterns
    credentials  map[string]string // upstream name → env var name
}

func (r *RoleConfig) Name() string
func (r *RoleConfig) AllowedTools() []string   // returns copy
func (r *RoleConfig) Credentials() map[string]string  // returns copy
```

Update top-level config:

```go
type Config struct {
    server    serverConfig
    upstreams []UpstreamConfig
    roles     []RoleConfig
}

func (c *Config) Roles() []RoleConfig  // returns copy
```

### Updated raw YAML structure

```go
type rawRoleConfig struct {
    AllowedTools []string          `yaml:"allowed_tools"`
    Credentials  map[string]string `yaml:"credentials"`
}

type rawConfig struct {
    Server    rawServerConfig            `yaml:"server"`
    Upstreams []rawUpstreamConfig        `yaml:"upstreams"`
    Roles     map[string]rawRoleConfig   `yaml:"roles"`
}
```

### Validation

- Role names must be non-empty and unique
- `allowed_tools` must be non-empty (every role must grant access to something)
- Each pattern must be a valid glob (compile with `gobwas/glob` at load time to fail fast)
- Each credential must reference a valid upstream name
- Env var names must be non-empty

---

## 2. Database Schema Changes

Remove `allowed_tools` from the `callers` table:

```sql
CREATE TABLE callers (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL UNIQUE,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

Rename `auth_env` → `role` in the `api_keys` table:

```sql
CREATE TABLE api_keys (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    caller_id   INTEGER NOT NULL REFERENCES callers(id) ON DELETE CASCADE,
    key_hash    BLOB NOT NULL UNIQUE,
    role        TEXT NOT NULL,
    label       TEXT,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

### Migration

Since this is pre-v1, a destructive migration is fine: drop and recreate the tables. Document that existing databases need to be re-seeded.

---

## 3. Caller Type Changes

The `Caller` struct carries the union of all role permissions, plus the specific role used for credential injection:

```go
type Caller struct {
    Name         string
    Role         string       // from the key used to authenticate (for credential injection)
    AllowedTools []glob.Glob  // union of ALL roles the caller has keys for
}
```

**Tool access** is the union of all roles assigned to the caller (across all their keys). If a caller has a "web-tools" key (`github/*,notion/*`) and a "database" key (`db_*`), they can see and call all of those tools regardless of which key they authenticate with.

**Credential injection** uses the role from the specific key used to authenticate. If they auth with the web-tools key, upstream requests get web-tools credentials.

This means a caller can call a tool they have access to via their database role, but authenticate with their web-tools key — they'll see the tool, but the request to the upstream will carry web-tools credentials. If the web-tools role doesn't have credentials for that upstream, the request falls back to the upstream's default auth config.

### Resolve in Authenticator

The authenticator resolves both the key-specific role and the union of all roles:

```go
func (a *Authenticator) Authenticate(r *http.Request) (*Caller, error) {
    // ... extract token, hash, lookup ...
    caller, err := a.store.LookupByKey(hash)  // returns name + role (from this key)
    if err != nil {
        return nil, fmt.Errorf("unauthorized")
    }
    // Get ALL roles this caller has keys for
    roles, err := a.store.RolesForCaller(caller.Name)
    if err != nil {
        return nil, fmt.Errorf("lookup roles: %w", err)
    }
    // Union the allowed_tools from all roles
    caller.AllowedTools = a.unionAllowedTools(roles)
    return caller, nil
}
```

### CallerStore

`LookupByKey` returns the caller name and the role from the matched key:

```go
func (s *SQLiteStore) LookupByKey(hashedKey [32]byte) (*Caller, error) {
    var name, role string
    err := s.db.QueryRow(`
        SELECT c.name, k.role
        FROM api_keys k
        JOIN callers c ON c.id = k.caller_id
        WHERE k.key_hash = ?
    `, hashedKey[:]).Scan(&name, &role)
    if err != nil {
        return nil, fmt.Errorf("auth: key not found")
    }
    return &Caller{Name: name, Role: role}, nil
}
```

New method to get all roles for a caller:

```go
func (s *SQLiteStore) RolesForCaller(name string) ([]string, error) {
    rows, err := s.db.Query(`
        SELECT DISTINCT k.role
        FROM api_keys k
        JOIN callers c ON c.id = k.caller_id
        WHERE c.name = ?
    `, name)
    // ... collect into []string
}
```

### AddCaller simplification

```go
func (s *SQLiteStore) AddCaller(name string) error
```

No longer takes `allowedTools` — callers are just named identities.

---

## 4. Authenticator Changes

The authenticator pre-compiles glob patterns for each role at construction time:

```go
type Authenticator struct {
    store        CallerStore
    credentials  map[string]map[string]string  // role name → upstream → token
    allowedTools map[string][]glob.Glob        // role name → compiled patterns
}

func NewAuthenticator(store CallerStore, roles []config.RoleConfig) *Authenticator
```

The constructor resolves env vars to token values and compiles all glob patterns upfront. If an API key references a role that doesn't exist in config, that role contributes nothing to the union.

New helper to compute the union:

```go
func (a *Authenticator) unionAllowedTools(roleNames []string) []glob.Glob {
    var globs []glob.Glob
    for _, name := range roleNames {
        if g, ok := a.allowedTools[name]; ok {
            globs = append(globs, g...)
        }
    }
    return globs
}
```

Rename `UpstreamToken(authEnv, upstream)` → `UpstreamToken(role, upstream)`.

---

## 5. CanAccessTool unchanged

`Caller.CanAccessTool(toolName string) bool` works exactly the same — it iterates over the union of globs. A tool is accessible if **any** glob from **any** of the caller's roles matches.

---

## 6. Seed Script Update

Update `cmd/seed/main.go`:
- `AddCaller` no longer takes allowed_tools
- `AddKey` takes `role` instead of `authEnv`
- The config file needs roles with allowed_tools defined

---

## 7. Testable Deliverables

### Config tests
1. **Role config loads:** YAML with `roles` section → correct RoleConfig values
2. **Role missing allowed_tools:** → Load returns error
3. **Role with invalid glob pattern:** → Load returns error
4. **Old `auth_envs` key rejected:** → Load returns error (clean break)

### Auth tests
5. **Caller gets union of all roles:** caller with "web-tools" key (github/*) and "database" key (db_*) → CanAccessTool matches both regardless of which key is used
6. **Single role:** caller with only a "web-tools" key → access limited to web-tools' allowed_tools
7. **Unknown role contributes nothing:** key references nonexistent role → that role adds no tools, but other valid roles still contribute
8. **Filtered tools/list uses union:** caller with web-tools+database keys → sees tools from both
9. **Credential injection uses authenticating key's role:** caller auths with web-tools key → UpstreamToken returns web-tools credentials (not database)

### Store tests
10. **AddCaller without allowed_tools:** create caller → exists in DB
11. **LookupByKey returns name and role only:** no glob compilation in store
12. **RolesForCaller returns all distinct roles:** caller with 2 keys for different roles → both returned

### Build check

```bash
go build ./...
go test ./...
go vet ./...
```

---

## 8. Migration Notes

- Existing `/tmp/stile-test.db` databases are incompatible — re-seed after this change
- The `configs/test-auth.yaml` needs updating: `auth_envs` → `roles` with new structure
- All code references to `authEnv`/`AuthEnv`/`auth_env` become `role`/`Role`
