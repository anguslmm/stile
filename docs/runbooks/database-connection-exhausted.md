# Database Connection Exhausted

## Severity

P1 (authentication and audit logging may fail, blocking all requests)

## Symptoms

- Stile returns errors on authentication (cannot look up API keys)
- Audit log entries are not being written
- Logs show database connection errors (e.g., `"too many connections"`, `"connection pool exhausted"`, `"database is locked"`)
- New caller/key creation via Admin API fails
- For SQLite: `"database is locked"` errors under concurrent load
- For Postgres: `"too many connections"` or `"sorry, too many clients already"` errors

## Likely Causes

1. **SQLite under concurrent write pressure** — SQLite handles one writer at a time; heavy audit logging or key management can cause lock contention.
2. **Postgres connection limit reached** — too many Stile instances or concurrent requests exhausting the Postgres `max_connections`.
3. **Long-running queries** — a slow query holding a connection/lock.
4. **Missing connection pooler** — multiple Stile instances connecting directly to Postgres without PgBouncer or equivalent.
5. **Deadlock** — rare, but possible with concurrent schema migrations or unusual access patterns.

## Diagnosis Steps

1. Check Stile logs for database errors:
   ```bash
   journalctl -u stile | grep -iE 'database|connection|locked|pool' | tail -20
   ```

2. **For Postgres** — check active connections:
   ```sql
   SELECT count(*) FROM pg_stat_activity WHERE datname = 'stile';
   SELECT * FROM pg_stat_activity WHERE datname = 'stile' AND state != 'idle' ORDER BY query_start;
   ```

3. **For Postgres** — check max connections:
   ```sql
   SHOW max_connections;
   ```

4. **For SQLite** — check for lock contention:
   ```bash
   # Check if the database file is locked
   fuser stile.db
   fuser audit.db
   ```

5. Check if the issue is with the auth database, audit database, or both. They can use different drivers and DSNs:
   - Auth DB: configured via `server.database.driver` / `server.database.dsn` (or legacy `server.db_path`)
   - Audit DB: configured via `audit.driver` / `audit.database`

6. Check the number of Stile instances connecting to the database:
   ```sql
   -- Postgres: connections by application
   SELECT client_addr, count(*) FROM pg_stat_activity WHERE datname = 'stile' GROUP BY client_addr;
   ```

## Remediation

**SQLite — database locked:**
- SQLite is not recommended for multi-instance deployments. Migrate to Postgres:
  ```yaml
  server:
    database:
      driver: postgres
      dsn: "postgres://stile:secret@db.internal:5432/stile?sslmode=require"
  ```
- For single-instance deployments, reduce concurrent load or ensure the SQLite busy timeout is configured (Stile sets a busy timeout internally).

**Postgres — too many connections:**
- Deploy a connection pooler (PgBouncer) between Stile and Postgres:
  ```bash
  # Point Stile at PgBouncer instead of Postgres directly
  dsn: "postgres://stile:secret@pgbouncer.internal:6432/stile"
  ```
- Increase Postgres `max_connections` (requires Postgres restart):
  ```sql
  ALTER SYSTEM SET max_connections = 200;
  ```

**Long-running queries (Postgres):**
- Identify and terminate the offending query:
  ```sql
  SELECT pid, now() - query_start AS duration, query
  FROM pg_stat_activity
  WHERE datname = 'stile' AND state = 'active'
  ORDER BY duration DESC;

  -- Terminate if needed
  SELECT pg_terminate_backend(<pid>);
  ```

**Deadlock (SQLite):**
- Restart the Stile instance. SQLite locks are process-level and will be released.

**Emergency — restore service:**
- If auth DB is down but upstreams are healthy, consider temporarily running Stile in dev mode (`--dev` flag) to bypass auth while the database issue is resolved. This removes all access control — use only as a last resort.

## Escalation

- If Postgres is unreachable or corrupted, escalate to the database administration team.
- If the issue is persistent lock contention, escalate to the Stile development team to investigate connection pool sizing.
