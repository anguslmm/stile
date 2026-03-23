// Package audit provides an append-only audit log for tool call tracking.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/anguslmm/stile/internal/config"

	_ "modernc.org/sqlite"
)

// Entry represents a single audit log record.
type Entry struct {
	ID        int64
	Timestamp time.Time
	Caller    string
	Method    string
	Tool      string
	Upstream  string
	Params    json.RawMessage
	Status    string
	LatencyMS int64
	TraceID   string // OpenTelemetry trace ID; empty if trace was not sampled
	KeyLabel  string // label of the API key used; empty if auth disabled or unlabeled
}

// Store is a write-only audit log backend.
type Store interface {
	Log(ctx context.Context, entry Entry) error
	Close() error
}

// QueryFilter specifies filters for reading audit log entries.
type QueryFilter struct {
	Caller   string
	Tool     string
	Upstream string
	Status   string
	Start    time.Time
	End      time.Time
	Limit    int
	Offset   int
}

// Reader is a read-only audit log backend for querying entries.
type Reader interface {
	Query(ctx context.Context, filter QueryFilter) ([]Entry, error)
}

// OpenStore creates a Store from the given database config.
func OpenStore(cfg config.DatabaseConfig) (Store, error) {
	switch cfg.Driver() {
	case "sqlite", "":
		return NewSQLiteStore(cfg.DSN())
	case "postgres":
		return NewPostgresStore(cfg.DSN())
	default:
		return nil, fmt.Errorf("audit: unsupported database driver %q", cfg.Driver())
	}
}

// Compile-time interface checks.
var (
	_ Store  = (*SQLiteStore)(nil)
	_ Store  = (*PostgresStore)(nil)
	_ Reader = (*SQLiteStore)(nil)
	_ Reader = (*PostgresStore)(nil)
)

// SQLiteStore persists audit entries to a SQLite database.
type SQLiteStore struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS audit_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp  DATETIME NOT NULL,
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

const migrateTraceID = `ALTER TABLE audit_log ADD COLUMN trace_id TEXT`
const migrateKeyLabel = `ALTER TABLE audit_log ADD COLUMN key_label TEXT`

// NewSQLiteStore opens (or creates) a SQLite database at dbPath and
// ensures the audit_log table exists. Use ":memory:" for in-memory testing.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	// Migrate existing databases that lack new columns.
	db.Exec(migrateTraceID)
	db.Exec(migrateKeyLabel)
	return &SQLiteStore{db: db}, nil
}

// Log writes an audit entry to the database.
func (s *SQLiteStore) Log(ctx context.Context, entry Entry) error {
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
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.Timestamp, entry.Caller, entry.Method, entry.Tool, entry.Upstream, params, entry.Status, entry.LatencyMS, traceID, keyLabel,
	)
	return err
}

// Query reads audit log entries matching the filter.
func (s *SQLiteStore) Query(ctx context.Context, filter QueryFilter) ([]Entry, error) {
	return queryDB(ctx, s.db, filter, "?")
}

// Close closes the underlying database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// queryDB is the shared query implementation for both SQLite and Postgres.
// placeholder is "?" for SQLite and "$" for Postgres (positional params).
func queryDB(ctx context.Context, db *sql.DB, filter QueryFilter, placeholder string) ([]Entry, error) {
	query := `SELECT id, timestamp, caller, method, tool, upstream, params, status, latency_ms, trace_id, key_label FROM audit_log WHERE 1=1`
	var args []any
	paramIdx := 0

	ph := func() string {
		paramIdx++
		if placeholder == "$" {
			return fmt.Sprintf("$%d", paramIdx)
		}
		return "?"
	}

	if filter.Caller != "" {
		query += ` AND caller = ` + ph()
		args = append(args, filter.Caller)
	}
	if filter.Tool != "" {
		query += ` AND tool = ` + ph()
		args = append(args, filter.Tool)
	}
	if filter.Upstream != "" {
		query += ` AND upstream = ` + ph()
		args = append(args, filter.Upstream)
	}
	if filter.Status != "" {
		query += ` AND status = ` + ph()
		args = append(args, filter.Status)
	}
	if !filter.Start.IsZero() {
		query += ` AND timestamp >= ` + ph()
		args = append(args, filter.Start)
	}
	if !filter.End.IsZero() {
		query += ` AND timestamp <= ` + ph()
		args = append(args, filter.End)
	}

	query += ` ORDER BY timestamp DESC`

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	query += ` LIMIT ` + ph()
	args = append(args, limit)

	if filter.Offset > 0 {
		query += ` OFFSET ` + ph()
		args = append(args, filter.Offset)
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: query: %w", err)
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		var e Entry
		var params sql.NullString
		var tool, upstream, traceID, keyLabel sql.NullString
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.Caller, &e.Method, &tool, &upstream, &params, &e.Status, &e.LatencyMS, &traceID, &keyLabel); err != nil {
			return nil, fmt.Errorf("audit: scan: %w", err)
		}
		if tool.Valid {
			e.Tool = tool.String
		}
		if upstream.Valid {
			e.Upstream = upstream.String
		}
		if params.Valid {
			e.Params = json.RawMessage(params.String)
		}
		if traceID.Valid {
			e.TraceID = traceID.String
		}
		if keyLabel.Valid {
			e.KeyLabel = keyLabel.String
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
