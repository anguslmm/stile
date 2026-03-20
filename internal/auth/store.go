package auth

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/gobwas/glob"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS callers (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT NOT NULL UNIQUE,
    allowed_tools TEXT NOT NULL,
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS api_keys (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    caller_id   INTEGER NOT NULL REFERENCES callers(id) ON DELETE CASCADE,
    key_hash    BLOB NOT NULL UNIQUE,
    auth_env    TEXT NOT NULL,
    label       TEXT,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash);
`

// SQLiteStore implements CallerStore backed by a SQLite database.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (or creates) a SQLite database at dbPath, runs
// migrations, and returns the store.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("auth: open database: %w", err)
	}

	// Enable WAL mode and foreign keys.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("auth: set journal mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("auth: enable foreign keys: %w", err)
	}

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("auth: run migrations: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

// LookupByKey finds a caller by the SHA-256 hash of their API key.
func (s *SQLiteStore) LookupByKey(hashedKey [32]byte) (*Caller, error) {
	var name, allowedToolsJSON, authEnv string
	err := s.db.QueryRow(`
		SELECT c.name, c.allowed_tools, k.auth_env
		FROM api_keys k
		JOIN callers c ON c.id = k.caller_id
		WHERE k.key_hash = ?
	`, hashedKey[:]).Scan(&name, &allowedToolsJSON, &authEnv)
	if err != nil {
		return nil, fmt.Errorf("auth: key not found")
	}

	var patterns []string
	if err := json.Unmarshal([]byte(allowedToolsJSON), &patterns); err != nil {
		return nil, fmt.Errorf("auth: parse allowed_tools: %w", err)
	}

	globs := make([]glob.Glob, len(patterns))
	for i, p := range patterns {
		g, err := glob.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("auth: compile glob %q: %w", p, err)
		}
		globs[i] = g
	}

	return &Caller{
		Name:         name,
		AllowedTools: globs,
		AuthEnv:      authEnv,
	}, nil
}

// HasCallers reports whether the database contains any callers.
func (s *SQLiteStore) HasCallers() (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM callers").Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// AddCaller inserts a new caller with the given allowed tool patterns.
func (s *SQLiteStore) AddCaller(name string, allowedTools []string) error {
	toolsJSON, err := json.Marshal(allowedTools)
	if err != nil {
		return fmt.Errorf("auth: marshal allowed_tools: %w", err)
	}
	_, err = s.db.Exec("INSERT INTO callers (name, allowed_tools) VALUES (?, ?)", name, string(toolsJSON))
	if err != nil {
		return fmt.Errorf("auth: insert caller: %w", err)
	}
	return nil
}

// AddKey inserts an API key hash for a caller with the given auth env.
func (s *SQLiteStore) AddKey(callerName string, keyHash [32]byte, authEnv string, label string) error {
	var callerID int64
	err := s.db.QueryRow("SELECT id FROM callers WHERE name = ?", callerName).Scan(&callerID)
	if err != nil {
		return fmt.Errorf("auth: caller %q not found", callerName)
	}
	_, err = s.db.Exec(
		"INSERT INTO api_keys (caller_id, key_hash, auth_env, label) VALUES (?, ?, ?, ?)",
		callerID, keyHash[:], authEnv, label,
	)
	if err != nil {
		return fmt.Errorf("auth: insert key: %w", err)
	}
	return nil
}

// DeleteCaller removes a caller and their API keys (via CASCADE).
func (s *SQLiteStore) DeleteCaller(name string) error {
	result, err := s.db.Exec("DELETE FROM callers WHERE name = ?", name)
	if err != nil {
		return fmt.Errorf("auth: delete caller: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("auth: caller %q not found", name)
	}
	return nil
}

// Close closes the database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
