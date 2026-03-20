package auth

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS callers (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL UNIQUE,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS api_keys (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    caller_id   INTEGER NOT NULL REFERENCES callers(id) ON DELETE CASCADE,
    key_hash    BLOB NOT NULL UNIQUE,
    role        TEXT NOT NULL,
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
// Returns the caller name and the role from the matched key.
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

	return &Caller{
		Name: name,
		Role: role,
	}, nil
}

// RolesForCaller returns all distinct roles assigned to a caller across all their keys.
func (s *SQLiteStore) RolesForCaller(name string) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT k.role
		FROM api_keys k
		JOIN callers c ON c.id = k.caller_id
		WHERE c.name = ?
	`, name)
	if err != nil {
		return nil, fmt.Errorf("auth: query roles: %w", err)
	}
	defer rows.Close()

	var roles []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, fmt.Errorf("auth: scan role: %w", err)
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
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

// AddCaller inserts a new caller (a named identity).
func (s *SQLiteStore) AddCaller(name string) error {
	_, err := s.db.Exec("INSERT INTO callers (name) VALUES (?)", name)
	if err != nil {
		return fmt.Errorf("auth: insert caller: %w", err)
	}
	return nil
}

// AddKey inserts an API key hash for a caller with the given role.
func (s *SQLiteStore) AddKey(callerName string, keyHash [32]byte, role string, label string) error {
	var callerID int64
	err := s.db.QueryRow("SELECT id FROM callers WHERE name = ?", callerName).Scan(&callerID)
	if err != nil {
		return fmt.Errorf("auth: caller %q not found", callerName)
	}
	_, err = s.db.Exec(
		"INSERT INTO api_keys (caller_id, key_hash, role, label) VALUES (?, ?, ?, ?)",
		callerID, keyHash[:], role, label,
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
