package audit

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const pgSchema = `
CREATE TABLE IF NOT EXISTS audit_log (
    id         SERIAL PRIMARY KEY,
    timestamp  TIMESTAMPTZ NOT NULL,
    caller     TEXT NOT NULL,
    method     TEXT NOT NULL,
    tool       TEXT,
    upstream   TEXT,
    params     TEXT,
    status     TEXT NOT NULL,
    latency_ms INTEGER NOT NULL,
    trace_id   TEXT,
    key_label  TEXT
);
CREATE INDEX IF NOT EXISTS idx_audit_log_timestamp ON audit_log(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_log_caller ON audit_log(caller);
`

const pgMigrateTraceID = `ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS trace_id TEXT`
const pgMigrateKeyLabel = `ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS key_label TEXT`

var _ Store = (*PostgresStore)(nil)

// PostgresStore persists audit entries to a PostgreSQL database.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore connects to the Postgres database at dsn and ensures
// the audit_log table exists.
func NewPostgresStore(dsn string) (*PostgresStore, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("audit: open postgres: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("audit: ping postgres: %w", err)
	}

	// Use an advisory lock to prevent concurrent migration attempts.
	if _, err := db.Exec("SELECT pg_advisory_lock(43)"); err != nil {
		db.Close()
		return nil, fmt.Errorf("audit: acquire migration lock: %w", err)
	}
	_, migErr := db.Exec(pgSchema)
	if migErr == nil {
		db.Exec(pgMigrateTraceID)
		db.Exec(pgMigrateKeyLabel)
	}
	db.Exec("SELECT pg_advisory_unlock(43)")
	if migErr != nil {
		db.Close()
		return nil, fmt.Errorf("audit: run migrations: %w", migErr)
	}

	return &PostgresStore{db: db}, nil
}

// Log writes an audit entry to the database.
func (s *PostgresStore) Log(ctx context.Context, entry Entry) error {
	var params *string
	if entry.Params != nil {
		p := string(entry.Params)
		params = &p
	}
	var traceID *string
	if entry.TraceID != "" {
		traceID = &entry.TraceID
	}
	var keyLabel *string
	if entry.KeyLabel != "" {
		keyLabel = &entry.KeyLabel
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log (timestamp, caller, method, tool, upstream, params, status, latency_ms, trace_id, key_label)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		entry.Timestamp, entry.Caller, entry.Method, entry.Tool, entry.Upstream, params, entry.Status, entry.LatencyMS, traceID, keyLabel,
	)
	return err
}

// Query reads audit log entries matching the filter.
func (s *PostgresStore) Query(ctx context.Context, filter QueryFilter) ([]Entry, error) {
	return queryDB(ctx, s.db, filter, "$")
}

// Close closes the underlying database connection.
func (s *PostgresStore) Close() error {
	return s.db.Close()
}
