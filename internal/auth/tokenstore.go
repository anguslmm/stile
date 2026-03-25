package auth

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/anguslmm/stile/internal/config"
)

// OAuthToken holds a user's OAuth token for a provider.
type OAuthToken struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	Expiry       time.Time
	Scopes       string
}

// Expired reports whether the token has expired (or will within 30 seconds).
func (t *OAuthToken) Expired() bool {
	if t.Expiry.IsZero() {
		return false // no expiry set
	}
	return time.Now().After(t.Expiry.Add(-30 * time.Second))
}

// TokenStore manages per-user OAuth tokens.
type TokenStore interface {
	StoreToken(ctx context.Context, user, provider string, token *OAuthToken) error
	GetToken(ctx context.Context, user, provider string) (*OAuthToken, error)
	DeleteToken(ctx context.Context, user, provider string) error
	ListProviders(ctx context.Context, user string) ([]string, error)
	Close() error
}

// --- SQLite implementation ---

const tokenSchema = `
CREATE TABLE IF NOT EXISTS user_oauth_tokens (
    user_name     TEXT NOT NULL,
    provider      TEXT NOT NULL,
    access_token  TEXT NOT NULL,
    refresh_token TEXT NOT NULL DEFAULT '',
    token_type    TEXT NOT NULL DEFAULT 'Bearer',
    expiry        DATETIME,
    scopes        TEXT NOT NULL DEFAULT '',
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_name, provider)
);
`

var _ TokenStore = (*SQLiteTokenStore)(nil)

// SQLiteTokenStore implements TokenStore backed by SQLite.
type SQLiteTokenStore struct {
	db *sql.DB
}

// NewSQLiteTokenStore opens a SQLite database and runs migrations.
func NewSQLiteTokenStore(dbPath string) (*SQLiteTokenStore, error) {
	dsn := dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(wal)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("tokenstore: open database: %w", err)
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)

	if _, err := db.Exec(tokenSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("tokenstore: run migrations: %w", err)
	}
	return &SQLiteTokenStore{db: db}, nil
}

// NewSQLiteTokenStoreFromDB creates a token store using an existing *sql.DB.
// The caller is responsible for closing the DB.
func NewSQLiteTokenStoreFromDB(db *sql.DB) (*SQLiteTokenStore, error) {
	if _, err := db.Exec(tokenSchema); err != nil {
		return nil, fmt.Errorf("tokenstore: run migrations: %w", err)
	}
	return &SQLiteTokenStore{db: db}, nil
}

func (s *SQLiteTokenStore) StoreToken(_ context.Context, user, provider string, token *OAuthToken) error {
	var expiry *time.Time
	if !token.Expiry.IsZero() {
		expiry = &token.Expiry
	}
	_, err := s.db.Exec(`
		INSERT INTO user_oauth_tokens (user_name, provider, access_token, refresh_token, token_type, expiry, scopes, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(user_name, provider) DO UPDATE SET
			access_token = excluded.access_token,
			refresh_token = excluded.refresh_token,
			token_type = excluded.token_type,
			expiry = excluded.expiry,
			scopes = excluded.scopes,
			updated_at = CURRENT_TIMESTAMP
	`, user, provider, token.AccessToken, token.RefreshToken, token.TokenType, expiry, token.Scopes)
	if err != nil {
		return fmt.Errorf("tokenstore: store token: %w", err)
	}
	return nil
}

func (s *SQLiteTokenStore) GetToken(_ context.Context, user, provider string) (*OAuthToken, error) {
	var token OAuthToken
	var expiry sql.NullTime
	err := s.db.QueryRow(`
		SELECT access_token, refresh_token, token_type, expiry, scopes
		FROM user_oauth_tokens
		WHERE user_name = ? AND provider = ?
	`, user, provider).Scan(&token.AccessToken, &token.RefreshToken, &token.TokenType, &expiry, &token.Scopes)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("tokenstore: token not found: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("tokenstore: get token: %w", err)
	}
	if expiry.Valid {
		token.Expiry = expiry.Time
	}
	return &token, nil
}

func (s *SQLiteTokenStore) DeleteToken(_ context.Context, user, provider string) error {
	result, err := s.db.Exec(`
		DELETE FROM user_oauth_tokens WHERE user_name = ? AND provider = ?
	`, user, provider)
	if err != nil {
		return fmt.Errorf("tokenstore: delete token: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("tokenstore: token not found: %w", ErrNotFound)
	}
	return nil
}

func (s *SQLiteTokenStore) ListProviders(_ context.Context, user string) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT provider FROM user_oauth_tokens WHERE user_name = ? ORDER BY provider
	`, user)
	if err != nil {
		return nil, fmt.Errorf("tokenstore: list providers: %w", err)
	}
	defer rows.Close()
	var providers []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("tokenstore: scan provider: %w", err)
		}
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

func (s *SQLiteTokenStore) Close() error {
	return s.db.Close()
}

// --- Postgres implementation ---

const pgTokenSchema = `
CREATE TABLE IF NOT EXISTS user_oauth_tokens (
    user_name     TEXT NOT NULL,
    provider      TEXT NOT NULL,
    access_token  TEXT NOT NULL,
    refresh_token TEXT NOT NULL DEFAULT '',
    token_type    TEXT NOT NULL DEFAULT 'Bearer',
    expiry        TIMESTAMPTZ,
    scopes        TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ DEFAULT NOW(),
    updated_at    TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (user_name, provider)
);
`

var _ TokenStore = (*PostgresTokenStore)(nil)

// PostgresTokenStore implements TokenStore backed by Postgres.
type PostgresTokenStore struct {
	db *sql.DB
}

// NewPostgresTokenStore connects to Postgres and runs migrations.
func NewPostgresTokenStore(dsn string) (*PostgresTokenStore, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("tokenstore: open postgres: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("tokenstore: ping postgres: %w", err)
	}

	// Use advisory lock 43 (42 is used by auth store).
	if _, err := db.Exec("SELECT pg_advisory_lock(43)"); err != nil {
		db.Close()
		return nil, fmt.Errorf("tokenstore: acquire migration lock: %w", err)
	}
	_, migErr := db.Exec(pgTokenSchema)
	db.Exec("SELECT pg_advisory_unlock(43)")
	if migErr != nil {
		db.Close()
		return nil, fmt.Errorf("tokenstore: run migrations: %w", migErr)
	}

	return &PostgresTokenStore{db: db}, nil
}

// NewPostgresTokenStoreFromDB creates a token store using an existing *sql.DB.
func NewPostgresTokenStoreFromDB(db *sql.DB) (*PostgresTokenStore, error) {
	if _, err := db.Exec("SELECT pg_advisory_lock(43)"); err != nil {
		return nil, fmt.Errorf("tokenstore: acquire migration lock: %w", err)
	}
	_, migErr := db.Exec(pgTokenSchema)
	db.Exec("SELECT pg_advisory_unlock(43)")
	if migErr != nil {
		return nil, fmt.Errorf("tokenstore: run migrations: %w", migErr)
	}
	return &PostgresTokenStore{db: db}, nil
}

func (s *PostgresTokenStore) StoreToken(_ context.Context, user, provider string, token *OAuthToken) error {
	var expiry *time.Time
	if !token.Expiry.IsZero() {
		expiry = &token.Expiry
	}
	_, err := s.db.Exec(`
		INSERT INTO user_oauth_tokens (user_name, provider, access_token, refresh_token, token_type, expiry, scopes, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		ON CONFLICT(user_name, provider) DO UPDATE SET
			access_token = EXCLUDED.access_token,
			refresh_token = EXCLUDED.refresh_token,
			token_type = EXCLUDED.token_type,
			expiry = EXCLUDED.expiry,
			scopes = EXCLUDED.scopes,
			updated_at = NOW()
	`, user, provider, token.AccessToken, token.RefreshToken, token.TokenType, expiry, token.Scopes)
	if err != nil {
		return fmt.Errorf("tokenstore: store token: %w", err)
	}
	return nil
}

func (s *PostgresTokenStore) GetToken(_ context.Context, user, provider string) (*OAuthToken, error) {
	var token OAuthToken
	var expiry sql.NullTime
	err := s.db.QueryRow(`
		SELECT access_token, refresh_token, token_type, expiry, scopes
		FROM user_oauth_tokens
		WHERE user_name = $1 AND provider = $2
	`, user, provider).Scan(&token.AccessToken, &token.RefreshToken, &token.TokenType, &expiry, &token.Scopes)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("tokenstore: token not found: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("tokenstore: get token: %w", err)
	}
	if expiry.Valid {
		token.Expiry = expiry.Time
	}
	return &token, nil
}

func (s *PostgresTokenStore) DeleteToken(_ context.Context, user, provider string) error {
	result, err := s.db.Exec(`
		DELETE FROM user_oauth_tokens WHERE user_name = $1 AND provider = $2
	`, user, provider)
	if err != nil {
		return fmt.Errorf("tokenstore: delete token: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("tokenstore: token not found: %w", ErrNotFound)
	}
	return nil
}

func (s *PostgresTokenStore) ListProviders(_ context.Context, user string) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT provider FROM user_oauth_tokens WHERE user_name = $1 ORDER BY provider
	`, user)
	if err != nil {
		return nil, fmt.Errorf("tokenstore: list providers: %w", err)
	}
	defer rows.Close()
	var providers []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("tokenstore: scan provider: %w", err)
		}
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

func (s *PostgresTokenStore) Close() error {
	return s.db.Close()
}

// OpenTokenStore creates a TokenStore from the given database config.
func OpenTokenStore(cfg config.DatabaseConfig) (TokenStore, error) {
	switch cfg.Driver() {
	case "sqlite", "":
		return NewSQLiteTokenStore(cfg.DSN())
	case "postgres":
		return NewPostgresTokenStore(cfg.DSN())
	default:
		return nil, fmt.Errorf("tokenstore: unsupported database driver %q", cfg.Driver())
	}
}
