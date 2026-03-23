package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const pgSchema = `
CREATE TABLE IF NOT EXISTS callers (
    id         SERIAL PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS api_keys (
    id          SERIAL PRIMARY KEY,
    caller_id   INTEGER NOT NULL REFERENCES callers(id) ON DELETE CASCADE,
    key_hash    BYTEA NOT NULL UNIQUE,
    label       TEXT,
    created_at  TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS caller_roles (
    caller_id  INTEGER NOT NULL REFERENCES callers(id) ON DELETE CASCADE,
    role       TEXT NOT NULL,
    PRIMARY KEY (caller_id, role)
);

CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash);
`

var _ Store = (*PostgresStore)(nil)

// PostgresStore implements Store backed by a PostgreSQL database.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore connects to the Postgres database at dsn and ensures
// the schema exists.
func NewPostgresStore(dsn string) (*PostgresStore, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("auth: open postgres: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("auth: ping postgres: %w", err)
	}

	// Use an advisory lock to prevent concurrent migration attempts.
	// Multiple Stile instances starting simultaneously can race on
	// CREATE TABLE IF NOT EXISTS with SERIAL columns (Postgres bug).
	if _, err := db.Exec("SELECT pg_advisory_lock(42)"); err != nil {
		db.Close()
		return nil, fmt.Errorf("auth: acquire migration lock: %w", err)
	}
	_, migErr := db.Exec(pgSchema)
	db.Exec("SELECT pg_advisory_unlock(42)") // best-effort release
	if migErr != nil {
		db.Close()
		return nil, fmt.Errorf("auth: run migrations: %w", migErr)
	}

	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) LookupByKey(hashedKey [32]byte) (*Caller, error) {
	var name string
	err := s.db.QueryRow(`
		SELECT c.name
		FROM api_keys k
		JOIN callers c ON c.id = k.caller_id
		WHERE k.key_hash = $1
	`, hashedKey[:]).Scan(&name)
	if err != nil {
		return nil, fmt.Errorf("auth: key not found: %w", ErrNotFound)
	}
	return &Caller{Name: name}, nil
}

func (s *PostgresStore) RolesForCaller(name string) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT cr.role
		FROM caller_roles cr
		JOIN callers c ON c.id = cr.caller_id
		WHERE c.name = $1
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

func (s *PostgresStore) AddCaller(name string) error {
	_, err := s.db.Exec("INSERT INTO callers (name) VALUES ($1)", name)
	if err != nil {
		if isPgUniqueViolation(err) {
			return fmt.Errorf("auth: caller %q already exists: %w", name, ErrDuplicate)
		}
		return fmt.Errorf("auth: insert caller: %w", err)
	}
	return nil
}

func (s *PostgresStore) AddKey(callerName string, keyHash [32]byte, label string) error {
	var callerID int64
	err := s.db.QueryRow("SELECT id FROM callers WHERE name = $1", callerName).Scan(&callerID)
	if err != nil {
		return fmt.Errorf("auth: caller %q not found: %w", callerName, ErrNotFound)
	}
	_, err = s.db.Exec(
		"INSERT INTO api_keys (caller_id, key_hash, label) VALUES ($1, $2, $3)",
		callerID, keyHash[:], label,
	)
	if err != nil {
		return fmt.Errorf("auth: insert key: %w", err)
	}
	return nil
}

func (s *PostgresStore) AssignRole(callerName string, role string) error {
	var callerID int64
	err := s.db.QueryRow("SELECT id FROM callers WHERE name = $1", callerName).Scan(&callerID)
	if err != nil {
		return fmt.Errorf("auth: caller %q not found: %w", callerName, ErrNotFound)
	}
	_, err = s.db.Exec(
		"INSERT INTO caller_roles (caller_id, role) VALUES ($1, $2) ON CONFLICT DO NOTHING",
		callerID, role,
	)
	if err != nil {
		return fmt.Errorf("auth: assign role: %w", err)
	}
	return nil
}

func (s *PostgresStore) UnassignRole(callerName string, role string) error {
	var callerID int64
	err := s.db.QueryRow("SELECT id FROM callers WHERE name = $1", callerName).Scan(&callerID)
	if err != nil {
		return fmt.Errorf("auth: caller %q not found: %w", callerName, ErrNotFound)
	}
	result, err := s.db.Exec(
		"DELETE FROM caller_roles WHERE caller_id = $1 AND role = $2",
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

func (s *PostgresStore) DeleteCaller(name string) error {
	result, err := s.db.Exec("DELETE FROM callers WHERE name = $1", name)
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

func (s *PostgresStore) ListCallers() ([]CallerInfo, error) {
	rows, err := s.db.Query(`
		SELECT c.name, COUNT(k.id), c.created_at
		FROM callers c
		LEFT JOIN api_keys k ON k.caller_id = c.id
		GROUP BY c.id, c.name, c.created_at
		ORDER BY c.name
	`)
	if err != nil {
		return nil, fmt.Errorf("auth: list callers: %w", err)
	}
	defer rows.Close()

	var callers []CallerInfo
	for rows.Next() {
		var ci CallerInfo
		if err := rows.Scan(&ci.Name, &ci.KeyCount, &ci.CreatedAt); err != nil {
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

func (s *PostgresStore) GetCaller(name string) (*CallerDetail, error) {
	var createdAt time.Time
	err := s.db.QueryRow("SELECT created_at FROM callers WHERE name = $1", name).Scan(&createdAt)
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
		CreatedAt: createdAt,
	}, nil
}

func (s *PostgresStore) ListKeys(callerName string) ([]KeyInfo, error) {
	rows, err := s.db.Query(`
		SELECT k.id, COALESCE(k.label, ''), k.created_at
		FROM api_keys k
		JOIN callers c ON c.id = k.caller_id
		WHERE c.name = $1
		ORDER BY k.id
	`, callerName)
	if err != nil {
		return nil, fmt.Errorf("auth: list keys: %w", err)
	}
	defer rows.Close()

	var keys []KeyInfo
	for rows.Next() {
		var ki KeyInfo
		if err := rows.Scan(&ki.ID, &ki.Label, &ki.CreatedAt); err != nil {
			return nil, fmt.Errorf("auth: scan key: %w", err)
		}
		keys = append(keys, ki)
	}
	return keys, rows.Err()
}

func (s *PostgresStore) DeleteKey(callerName string, keyID int64) error {
	result, err := s.db.Exec(`
		DELETE FROM api_keys
		WHERE id = $1
		AND caller_id = (SELECT id FROM callers WHERE name = $2)
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

func (s *PostgresStore) KeyCountForCaller(callerName string) (int, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(k.id)
		FROM api_keys k
		JOIN callers c ON c.id = k.caller_id
		WHERE c.name = $1
	`, callerName).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("auth: count keys: %w", err)
	}
	return count, nil
}

func (s *PostgresStore) RevokeKey(callerName string, label string) error {
	result, err := s.db.Exec(`
		DELETE FROM api_keys
		WHERE caller_id = (SELECT id FROM callers WHERE name = $1)
		AND label = $2
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

// DB returns the underlying database connection pool. Used by PGNotifyListener
// to send NOTIFY messages on the shared connection pool.
func (s *PostgresStore) DB() *sql.DB { return s.db }

func (s *PostgresStore) Close() error {
	return s.db.Close()
}

// isPgUniqueViolation reports whether err is a Postgres unique constraint violation (23505).
func isPgUniqueViolation(err error) bool {
	// pgx wraps errors; check the error string for the SQLSTATE code.
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key")
}
