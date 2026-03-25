package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anguslmm/stile/internal/config"

	_ "modernc.org/sqlite"
)

var (
	// ErrNotFound indicates the requested entity does not exist.
	ErrNotFound = errors.New("auth: not found")

	// ErrDuplicate indicates a uniqueness constraint violation.
	ErrDuplicate = errors.New("auth: duplicate")
)

var (
	cryptoRandRead = func(b []byte) error {
		_, err := rand.Read(b)
		return err
	}
	hexEncodeToString = hex.EncodeToString
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

// Store is the full interface for caller/key/role management.
// Both the admin handler and CLI operate against this interface.
type Store interface {
	CallerStore
	AddCaller(name string) error
	DeleteCaller(name string) error
	ListCallers() ([]CallerInfo, error)
	GetCaller(name string) (*CallerDetail, error)
	AddKey(callerName string, keyHash [32]byte, label string) error
	ListKeys(callerName string) ([]KeyInfo, error)
	DeleteKey(callerName string, keyID int64) error
	RevokeKey(callerName string, label string) error
	KeyCountForCaller(callerName string) (int, error)
	AssignRole(callerName string, role string) error
	UnassignRole(callerName string, role string) error
	EnsureCaller(name string, defaultRoles []string) error
	CallerExists(name string) (bool, error)
	Close() error
}

// OpenStore creates a Store from the given database config.
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

var (
	_ CallerStore = (*SQLiteStore)(nil)
	_ Store       = (*SQLiteStore)(nil)
)

// SQLiteStore implements CallerStore backed by a SQLite database.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (or creates) a SQLite database at dbPath, runs
// migrations, and returns the store.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	// Set PRAGMAs via DSN so they apply to every connection in the pool.
	dsn := dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(wal)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("auth: open database: %w", err)
	}

	// Configure connection pool.
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)

	// Migrate from old schema: if api_keys exists but caller_roles does not,
	// this is the pre-6.2 schema. Drop everything and recreate.
	if needsFullMigration(db) {
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
// Returns the caller name and key label — roles are resolved separately via RolesForCaller.
func (s *SQLiteStore) LookupByKey(hashedKey [32]byte) (*KeyLookupResult, error) {
	var name string
	var label sql.NullString
	err := s.db.QueryRow(`
		SELECT c.name, k.label
		FROM api_keys k
		JOIN callers c ON c.id = k.caller_id
		WHERE k.key_hash = ?
	`, hashedKey[:]).Scan(&name, &label)
	if err != nil {
		return nil, fmt.Errorf("auth: key not found: %w", ErrNotFound)
	}

	return &KeyLookupResult{Caller: &Caller{Name: name}, KeyLabel: label.String}, nil
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

// AddCaller inserts a new caller (a named identity).
func (s *SQLiteStore) AddCaller(name string) error {
	_, err := s.db.Exec("INSERT INTO callers (name) VALUES (?)", name)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("auth: caller %q already exists: %w", name, ErrDuplicate)
		}
		return fmt.Errorf("auth: insert caller: %w", err)
	}
	return nil
}

// AddKey inserts an API key hash for a caller.
func (s *SQLiteStore) AddKey(callerName string, keyHash [32]byte, label string) error {
	var callerID int64
	err := s.db.QueryRow("SELECT id FROM callers WHERE name = ?", callerName).Scan(&callerID)
	if err != nil {
		return fmt.Errorf("auth: caller %q not found: %w", callerName, ErrNotFound)
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
		return fmt.Errorf("auth: caller %q not found: %w", callerName, ErrNotFound)
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
		return fmt.Errorf("auth: caller %q not found: %w", callerName, ErrNotFound)
	}
	result, err := s.db.Exec(
		"DELETE FROM caller_roles WHERE caller_id = ? AND role = ?",
		callerID, role,
	)
	if err != nil {
		return fmt.Errorf("auth: unassign role: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("auth: caller %q does not have role %q: %w", callerName, role, ErrNotFound)
	}
	return nil
}

// DeleteCaller removes a caller and their API keys (via CASCADE).
func (s *SQLiteStore) DeleteCaller(name string) error {
	result, err := s.db.Exec("DELETE FROM callers WHERE name = ?", name)
	if err != nil {
		return fmt.Errorf("auth: delete caller: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("auth: caller %q not found: %w", name, ErrNotFound)
	}
	return nil
}

// CallerInfo holds summary information about a caller for listing.
type CallerInfo struct {
	Name      string
	KeyCount  int
	Roles     []string
	CreatedAt time.Time
}

// ListCallers returns all callers with their key count, roles, and creation time.
func (s *SQLiteStore) ListCallers() ([]CallerInfo, error) {
	rows, err := s.db.Query(`
		SELECT c.name, COUNT(k.id), c.created_at
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
		var createdStr string
		if err := rows.Scan(&ci.Name, &ci.KeyCount, &createdStr); err != nil {
			return nil, fmt.Errorf("auth: scan caller: %w", err)
		}
		ci.CreatedAt = parseTime(createdStr)
		roles, err := s.RolesForCaller(ci.Name)
		if err != nil {
			return nil, err
		}
		ci.Roles = roles
		callers = append(callers, ci)
	}
	return callers, rows.Err()
}

// CallerDetail holds full information about a caller including key metadata.
type CallerDetail struct {
	Name      string
	Keys      []KeyInfo
	CreatedAt time.Time
}

// GetCaller retrieves a single caller's details including key metadata.
func (s *SQLiteStore) GetCaller(name string) (*CallerDetail, error) {
	var createdStr string
	err := s.db.QueryRow("SELECT created_at FROM callers WHERE name = ?", name).Scan(&createdStr)
	if err != nil {
		return nil, fmt.Errorf("auth: caller %q not found: %w", name, ErrNotFound)
	}

	keys, err := s.ListKeys(name)
	if err != nil {
		return nil, err
	}

	return &CallerDetail{
		Name:      name,
		Keys:      keys,
		CreatedAt: parseTime(createdStr),
	}, nil
}

// KeyInfo holds summary information about an API key (no secrets).
type KeyInfo struct {
	ID        int64
	Label     string
	CreatedAt time.Time
}

// ListKeys returns metadata for all keys belonging to a caller.
func (s *SQLiteStore) ListKeys(callerName string) ([]KeyInfo, error) {
	rows, err := s.db.Query(`
		SELECT k.id, COALESCE(k.label, ''), k.created_at
		FROM api_keys k
		JOIN callers c ON c.id = k.caller_id
		WHERE c.name = ?
		ORDER BY k.id
	`, callerName)
	if err != nil {
		return nil, fmt.Errorf("auth: list keys: %w", err)
	}
	defer rows.Close()

	var keys []KeyInfo
	for rows.Next() {
		var ki KeyInfo
		var createdStr string
		if err := rows.Scan(&ki.ID, &ki.Label, &createdStr); err != nil {
			return nil, fmt.Errorf("auth: scan key: %w", err)
		}
		ki.CreatedAt = parseTime(createdStr)
		keys = append(keys, ki)
	}
	return keys, rows.Err()
}

// DeleteKey removes an API key by caller name and key ID.
func (s *SQLiteStore) DeleteKey(callerName string, keyID int64) error {
	result, err := s.db.Exec(`
		DELETE FROM api_keys
		WHERE id = ?
		AND caller_id = (SELECT id FROM callers WHERE name = ?)
	`, keyID, callerName)
	if err != nil {
		return fmt.Errorf("auth: delete key: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("auth: key %d not found for caller %q: %w", keyID, callerName, ErrNotFound)
	}
	return nil
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

// EnsureCaller creates a caller if it doesn't exist, assigning default roles
// only on creation. Uses INSERT OR IGNORE for safe concurrent access.
func (s *SQLiteStore) EnsureCaller(name string, defaultRoles []string) error {
	result, err := s.db.Exec("INSERT OR IGNORE INTO callers (name) VALUES (?)", name)
	if err != nil {
		return fmt.Errorf("auth: ensure caller: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return nil // already exists
	}
	for _, role := range defaultRoles {
		if _, err := s.db.Exec(
			"INSERT OR IGNORE INTO caller_roles (caller_id, role) VALUES ((SELECT id FROM callers WHERE name = ?), ?)",
			name, role,
		); err != nil {
			return fmt.Errorf("auth: assign default role %q: %w", role, err)
		}
	}
	return nil
}

// CallerExists reports whether a caller with the given name exists.
func (s *SQLiteStore) CallerExists(name string) (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM callers WHERE name = ?", name).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("auth: check caller exists: %w", err)
	}
	return count > 0, nil
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
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("auth: no key with label %q found for caller %q: %w", label, callerName, ErrNotFound)
	}
	return nil
}

// Close closes the database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// GenerateAPIKey creates a cryptographically random API key.
func GenerateAPIKey() (string, error) {
	b := make([]byte, 16)
	if err := cryptoRandRead(b); err != nil {
		return "", fmt.Errorf("auth: generate key: %w", err)
	}
	return "sk-" + hexEncodeToString(b), nil
}

// needsFullMigration checks if the database has the pre-6.2 schema
// (api_keys exists but caller_roles does not). Returns true if a full
// drop-and-recreate migration is needed.
func needsFullMigration(db *sql.DB) bool {
	// Check if api_keys table exists.
	var apiKeysExists int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type='table' AND name='api_keys'
	`).Scan(&apiKeysExists)
	if err != nil || apiKeysExists == 0 {
		return false // Fresh database, no migration needed.
	}

	// Check if caller_roles table exists.
	var callerRolesExists int
	err = db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type='table' AND name='caller_roles'
	`).Scan(&callerRolesExists)
	if err != nil {
		return false
	}

	// Pre-6.2: api_keys exists but caller_roles doesn't.
	return callerRolesExists == 0
}

// isUniqueViolation reports whether err is a SQLite UNIQUE constraint failure.
func isUniqueViolation(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// parseTime parses a SQLite DATETIME string into time.Time.
func parseTime(s string) time.Time {
	// modernc.org/sqlite returns RFC3339; other drivers may return space-separated.
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
