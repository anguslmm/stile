# Task 16: Configurable Database Backend with Postgres Support

**Status:** todo
**Depends on:** 14, 15

---

## Goal

Make the database backend configurable so operators can choose between SQLite (current default, good for single-node) and PostgreSQL (for production/multi-instance deployments). Both the auth store and the audit store should support this.

---

## 1. Define a store interface and refactor SQLite behind it

The auth store already has a `CallerStore` interface (`internal/auth/auth.go:51-55`), but it's minimal — only `LookupByKey`, `RolesForCaller`, and `HasCallers`. The `SQLiteStore` has many more methods (AddCaller, DeleteCaller, AddKey, ListKeys, AssignRole, etc.) that are called directly by the admin handler and CLI.

**Steps:**

1. Define a full `Store` interface in `internal/auth/store.go` covering all operations the admin handler and CLI use:
   ```go
   type Store interface {
       CallerStore  // LookupByKey, RolesForCaller, HasCallers
       AddCaller(name string) error
       DeleteCaller(name string) error
       ListCallers() ([]CallerSummary, error)
       GetCaller(name string) (*CallerDetail, error)
       AddKey(caller string, hash [32]byte, label string) error
       ListKeys(caller string) ([]KeyInfo, error)
       DeleteKey(caller, label string) error
       RevokeKey(caller, label string) error
       KeyCountForCaller(name string) (int, error)
       AssignRole(caller, role string) error
       UnassignRole(caller, role string) error
       Close() error
   }
   ```

2. Rename `SQLiteStore` methods to satisfy this interface (they likely already do, just need the interface declaration and compile-time check).

3. Update admin handler, CLI, and main.go to accept `Store` instead of `*SQLiteStore`.

---

## 2. Add config support for database type

Update the YAML config to support a `database` section under `server`:

```yaml
server:
  address: ":8080"
  database:
    driver: sqlite        # or "postgres"
    dsn: stile.db         # file path for sqlite, connection string for postgres
```

For backwards compatibility, continue to support the existing `db_path` field as shorthand for `driver: sqlite, dsn: <path>`.

**Config changes:**
- Add `DatabaseConfig` type with `Driver()` and `DSN()` getters
- Add to `ServerConfig`
- Validate driver is "sqlite" or "postgres"
- If `db_path` is set and `database` is not, treat as `driver: sqlite, dsn: db_path`

---

## 3. Implement Postgres store

Create `internal/auth/postgres.go`:

1. Implement the `Store` interface using `database/sql` with the `pgx` driver (`github.com/jackc/pgx/v5/stdlib`)
2. Translate the SQLite schema to Postgres:
   - `INTEGER PRIMARY KEY AUTOINCREMENT` -> `SERIAL PRIMARY KEY`
   - `TEXT` stays `TEXT`
   - `DATETIME DEFAULT CURRENT_TIMESTAMP` -> `TIMESTAMPTZ DEFAULT NOW()`
   - `UNIQUE(caller_id, role)` and foreign key constraints stay the same
3. Handle Postgres-specific error detection (replace `strings.Contains(err.Error(), "UNIQUE constraint failed")` — this is already flagged as a code health issue in task 15 for sentinel errors, which makes this easier)
4. Use `$1, $2` placeholders instead of `?` (or use a query builder)

**Constructor:**
```go
func NewPostgresStore(dsn string) (*PostgresStore, error)
```

Apply the same hardening from task 14 (connection pool config, busy timeout equivalent via `context.WithTimeout`).

---

## 4. Implement Postgres audit store

Create `internal/audit/postgres.go`:

1. The audit store interface is simpler — just `Log()` and `Close()`
2. Same schema translation as above
3. Same constructor pattern

---

## 5. Factory function

Add a factory that reads the config and returns the right store:

```go
// internal/auth/store.go
func OpenStore(cfg config.DatabaseConfig) (Store, error) {
    switch cfg.Driver() {
    case "sqlite", "":
        return NewSQLiteStore(cfg.DSN())
    case "postgres":
        return NewPostgresStore(cfg.DSN())
    default:
        return nil, fmt.Errorf("auth: unsupported database driver %q", cfg.Driver())
    }
}
```

Same pattern for audit store.

Update `main.go` and `cli.go` to use the factory.

---

## 6. Update CLI commands

The CLI commands (`add-caller`, `add-key`, etc.) currently use `--db` for the SQLite path. Add `--driver` flag (default "sqlite") and rename `--db` to `--dsn` (keep `--db` as an alias for backwards compat):

```
stile add-caller --name foo --driver postgres --dsn "postgres://localhost/stile"
stile add-caller --name foo --db stile.db  # still works, implies sqlite
```

---

## Verification

- All existing SQLite tests pass unchanged
- Add integration tests for Postgres store (skip if no Postgres available, use `STILE_TEST_POSTGRES_DSN` env var)
- Test factory function with both drivers
- Test backwards compatibility: `db_path: foo.db` in config still works
- Test config validation rejects unknown drivers
