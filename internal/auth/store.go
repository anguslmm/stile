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
    label       TEXT,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS caller_roles (
    caller_id  INTEGER NOT NULL REFERENCES callers(id) ON DELETE CASCADE,
    role       TEXT NOT NULL,
    PRIMARY KEY (caller_id, role)
);

CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash);
`

var _ CallerStore = (*SQLiteStore)(nil)

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

	// Migrate from old schema: if api_keys has a "role" column, drop everything
	// and recreate. Pre-v1, so destructive migration is acceptable.
	if needsMigration(db) {
		for _, table := range []string{"api_keys", "caller_roles", "callers"} {
			if _, err := db.Exec("DROP TABLE IF EXISTS " + table); err != nil {
				db.Close()
				return nil, fmt.Errorf("auth: drop old %s: %w", table, err)
			}
		}
	}

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("auth: run migrations: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

// LookupByKey finds a caller by the SHA-256 hash of their API key.
// Returns the caller name only — roles are resolved separately via RolesForCaller.
func (s *SQLiteStore) LookupByKey(hashedKey [32]byte) (*Caller, error) {
	var name string
	err := s.db.QueryRow(`
		SELECT c.name
		FROM api_keys k
		JOIN callers c ON c.id = k.caller_id
		WHERE k.key_hash = ?
	`, hashedKey[:]).Scan(&name)
	if err != nil {
		return nil, fmt.Errorf("auth: key not found")
	}

	return &Caller{Name: name}, nil
}

// RolesForCaller returns all roles assigned to a caller via the caller_roles table.
func (s *SQLiteStore) RolesForCaller(name string) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT cr.role
		FROM caller_roles cr
		JOIN callers c ON c.id = cr.caller_id
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

// AddKey inserts an API key hash for a caller.
func (s *SQLiteStore) AddKey(callerName string, keyHash [32]byte, label string) error {
	var callerID int64
	err := s.db.QueryRow("SELECT id FROM callers WHERE name = ?", callerName).Scan(&callerID)
	if err != nil {
		return fmt.Errorf("auth: caller %q not found", callerName)
	}
	_, err = s.db.Exec(
		"INSERT INTO api_keys (caller_id, key_hash, label) VALUES (?, ?, ?)",
		callerID, keyHash[:], label,
	)
	if err != nil {
		return fmt.Errorf("auth: insert key: %w", err)
	}
	return nil
}

// AssignRole assigns a role to a caller. Idempotent — assigning an
// already-assigned role is a no-op.
func (s *SQLiteStore) AssignRole(callerName string, role string) error {
	var callerID int64
	err := s.db.QueryRow("SELECT id FROM callers WHERE name = ?", callerName).Scan(&callerID)
	if err != nil {
		return fmt.Errorf("auth: caller %q not found", callerName)
	}
	_, err = s.db.Exec(
		"INSERT OR IGNORE INTO caller_roles (caller_id, role) VALUES (?, ?)",
		callerID, role,
	)
	if err != nil {
		return fmt.Errorf("auth: assign role: %w", err)
	}
	return nil
}

// UnassignRole removes a role from a caller. Returns an error if the
// assignment didn't exist.
func (s *SQLiteStore) UnassignRole(callerName string, role string) error {
	var callerID int64
	err := s.db.QueryRow("SELECT id FROM callers WHERE name = ?", callerName).Scan(&callerID)
	if err != nil {
		return fmt.Errorf("auth: caller %q not found", callerName)
	}
	result, err := s.db.Exec(
		"DELETE FROM caller_roles WHERE caller_id = ? AND role = ?",
		callerID, role,
	)
	if err != nil {
		return fmt.Errorf("auth: unassign role: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("auth: caller %q does not have role %q", callerName, role)
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

// CallerInfo holds summary information about a caller for listing.
type CallerInfo struct {
	Name     string
	KeyCount int
	Roles    []string
}

// ListCallers returns all callers with their key count and assigned roles.
func (s *SQLiteStore) ListCallers() ([]CallerInfo, error) {
	rows, err := s.db.Query(`
		SELECT c.name, COUNT(k.id)
		FROM callers c
		LEFT JOIN api_keys k ON k.caller_id = c.id
		GROUP BY c.id
		ORDER BY c.name
	`)
	if err != nil {
		return nil, fmt.Errorf("auth: list callers: %w", err)
	}
	defer rows.Close()

	var callers []CallerInfo
	for rows.Next() {
		var ci CallerInfo
		if err := rows.Scan(&ci.Name, &ci.KeyCount); err != nil {
			return nil, fmt.Errorf("auth: scan caller: %w", err)
		}
		roles, err := s.RolesForCaller(ci.Name)
		if err != nil {
			return nil, err
		}
		ci.Roles = roles
		callers = append(callers, ci)
	}
	return callers, rows.Err()
}

// KeyInfo holds summary information about an API key (no secrets).
type KeyInfo struct {
	Label     string
	CreatedAt string
}

// ListKeysForCaller returns metadata for all keys belonging to a caller.
func (s *SQLiteStore) ListKeysForCaller(callerName string) ([]KeyInfo, error) {
	rows, err := s.db.Query(`
		SELECT COALESCE(k.label, ''), k.created_at
		FROM api_keys k
		JOIN callers c ON c.id = k.caller_id
		WHERE c.name = ?
	`, callerName)
	if err != nil {
		return nil, fmt.Errorf("auth: list keys: %w", err)
	}
	defer rows.Close()

	var keys []KeyInfo
	for rows.Next() {
		var ki KeyInfo
		if err := rows.Scan(&ki.Label, &ki.CreatedAt); err != nil {
			return nil, fmt.Errorf("auth: scan key: %w", err)
		}
		keys = append(keys, ki)
	}
	return keys, rows.Err()
}

// KeyCountForCaller returns the number of API keys a caller has.
func (s *SQLiteStore) KeyCountForCaller(callerName string) (int, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(k.id)
		FROM api_keys k
		JOIN callers c ON c.id = k.caller_id
		WHERE c.name = ?
	`, callerName).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("auth: count keys: %w", err)
	}
	return count, nil
}

// RevokeKey deletes an API key by caller name and label.
func (s *SQLiteStore) RevokeKey(callerName string, label string) error {
	result, err := s.db.Exec(`
		DELETE FROM api_keys
		WHERE caller_id = (SELECT id FROM callers WHERE name = ?)
		AND label = ?
	`, callerName, label)
	if err != nil {
		return fmt.Errorf("auth: revoke key: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("auth: no key with label %q found for caller %q", label, callerName)
	}
	return nil
}

// Close closes the database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// needsMigration checks if the database has the old schema (api_keys with a
// "role" column). Returns true if migration is needed, false for fresh or
// already-migrated databases.
func needsMigration(db *sql.DB) bool {
	var count int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM pragma_table_info('api_keys')
		WHERE name = 'role'
	`).Scan(&count)
	return err == nil && count > 0
}
