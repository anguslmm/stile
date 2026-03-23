# audit

Append-only audit log for recording tool call activity, backed by SQLite or PostgreSQL. Supports both write (logging) and read (querying) paths.

## Key Types

- **`Entry`** — A single audit record: ID, timestamp, caller, method, tool, upstream, params (raw JSON), status, latency.
- **`Store`** (interface) — Write-only backend: `Log(ctx, Entry) error` and `Close() error`.
- **`Reader`** (interface) — Read-only backend: `Query(ctx, QueryFilter) ([]Entry, error)`. Separate from `Store` — the proxy only needs `Store`; the admin handler needs `Reader`.
- **`QueryFilter`** — Filters for reading audit entries: caller, tool, upstream, status, time range (start/end), limit, offset. Default limit is 50, newest-first ordering.
- **`SQLiteStore`** — Implements both `Store` and `Reader`, backed by SQLite (via `modernc.org/sqlite`). Supports `:memory:` for testing.
- **`PostgresStore`** — Implements both `Store` and `Reader`, backed by PostgreSQL (via `pgx/v5/stdlib`). Uses a connection pool and a pg advisory lock (id `43`) to serialize schema migrations.

## Key Functions

- **`OpenStore(cfg config.DatabaseConfig) (Store, error)`** — Factory that selects SQLite (default) or Postgres based on `cfg.Driver()`.
- **`NewSQLiteStore(dbPath string) (*SQLiteStore, error)`** — Opens/creates SQLite DB and runs schema migration.
- **`NewPostgresStore(dsn string) (*PostgresStore, error)`** — Connects, pings, migrates, returns ready store.

## Design Notes

- `Store` (write path) and `Reader` (read path) are separate interfaces. Both SQLite and Postgres concrete types implement both.
- `params` is stored as `TEXT` (JSON string), nullable; a nil `Entry.Params` writes NULL.
- Both backends share identical schema (`audit_log` table with indexes on `timestamp` and `caller`).
- Postgres migration is guarded by `pg_advisory_lock(43)` to be safe under concurrent startup.
- Compile-time interface assertions are present for both concrete types for both interfaces.
- The shared `queryDB` helper builds dynamic SQL with parameterized queries, using `?` placeholders for SQLite and `$N` for Postgres.
