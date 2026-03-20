// Package audit provides an append-only audit log for tool call tracking.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	_ "modernc.org/sqlite"
)

// Entry represents a single audit log record.
type Entry struct {
	Timestamp time.Time
	Caller    string
	Method    string
	Tool      string
	Upstream  string
	Params    json.RawMessage
	Status    string
	LatencyMS int64
}

// Store is a write-only audit log backend.
type Store interface {
	Log(ctx context.Context, entry Entry) error
	Close() error
}

// Compile-time interface check.
var _ Store = (*SQLiteStore)(nil)

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
    latency_ms INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_log_timestamp ON audit_log(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_log_caller ON audit_log(caller);
`

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
	return &SQLiteStore{db: db}, nil
}

// Log writes an audit entry to the database.
func (s *SQLiteStore) Log(ctx context.Context, entry Entry) error {
	var params *string
	if entry.Params != nil {
		p := string(entry.Params)
		params = &p
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log (timestamp, caller, method, tool, upstream, params, status, latency_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.Timestamp, entry.Caller, entry.Method, entry.Tool, entry.Upstream, params, entry.Status, entry.LatencyMS,
	)
	return err
}

// Close closes the underlying database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
