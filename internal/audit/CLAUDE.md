# audit

Append-only audit log for recording tool call activity, backed by SQLite or PostgreSQL.

## Key Types

- **`Entry`** — A single audit record: timestamp, caller, method, tool, upstream, params (raw JSON), status, latency.
- **`Store`** (interface) — Write-only backend: `Log(ctx, Entry) error` and `Close() error`.
- **`SQLiteStore`** — `Store` implementation backed by SQLite (via `modernc.org/sqlite`). Supports `:memory:` for testing.
- **`PostgresStore`** — `Store` implementation backed by PostgreSQL (via `pgx/v5/stdlib`). Uses a connection pool and a pg advisory lock (id `43`) to serialize schema migrations.

## Key Functions

- **`OpenStore(cfg config.DatabaseConfig) (Store, error)`** — Factory that selects SQLite (default) or Postgres based on `cfg.Driver()`.
- **`NewSQLiteStore(dbPath string) (*SQLiteStore, error)`** — Opens/creates SQLite DB and runs schema migration.
- **`NewPostgresStore(dsn string) (*PostgresStore, error)`** — Connects, pings, migrates, returns ready store.

## Design Notes

- `Store` is intentionally write-only — there is no query/read API.
- `params` is stored as `TEXT` (JSON string), nullable; a nil `Entry.Params` writes NULL.
- Both backends share identical schema (`audit_log` table with indexes on `timestamp` and `caller`).
- Postgres migration is guarded by `pg_advisory_lock(43)` to be safe under concurrent startup.
- Compile-time interface assertions are present for both concrete types.
