// Package store owns the SQLite database: schema, migrations, and queries.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Store wraps the SQLite handle. All methods are safe for concurrent use.
type Store struct{ db *sql.DB }

// Open creates (if needed) and migrates the database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating db dir: %w", err)
	}
	dsn := path + "?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on&_synchronous=NORMAL"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening db: %w", err)
	}
	// SQLite serialises writers; a single connection avoids SQLITE_BUSY storms.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	email       TEXT NOT NULL UNIQUE,
	name        TEXT NOT NULL DEFAULT '',
	picture     TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	last_seen   INTEGER NOT NULL,
	is_seed     INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS sessions (
	token_hash  TEXT PRIMARY KEY,
	user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at  INTEGER NOT NULL,
	expires_at  INTEGER NOT NULL,
	user_agent  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);

CREATE TABLE IF NOT EXISTS oauth_states (
	state       TEXT PRIMARY KEY,
	created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS projects (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	owner        TEXT NOT NULL,
	repo         TEXT NOT NULL,
	path         TEXT NOT NULL,
	model        TEXT NOT NULL DEFAULT '',
	agent        TEXT NOT NULL DEFAULT '',
	role         TEXT NOT NULL DEFAULT '',
	cloned_at    INTEGER NOT NULL,
	last_pull_at INTEGER,
	UNIQUE(owner, repo)
);

CREATE TABLE IF NOT EXISTS repo_cache (
	owner        TEXT NOT NULL,
	repo         TEXT NOT NULL,
	private      INTEGER NOT NULL DEFAULT 0,
	description  TEXT NOT NULL DEFAULT '',
	language     TEXT NOT NULL DEFAULT '',
	size_kb      INTEGER NOT NULL DEFAULT 0,
	pushed_at    TEXT NOT NULL DEFAULT '',
	archived     INTEGER NOT NULL DEFAULT 0,
	fetched_at   INTEGER NOT NULL,
	PRIMARY KEY (owner, repo)
);

CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("applying schema: %w", err)
	}
	return nil
}

// User is an application user.
type User struct {
	ID       int64
	Email    string
	Name     string
	Picture  string
	Created  time.Time
	LastSeen time.Time
	IsSeed   bool
}

// UpsertUser records a successful sign-in, creating the row when absent.
func (s *Store) UpsertUser(ctx context.Context, email, name, picture string) (*User, error) {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (email, name, picture, created_at, last_seen)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(email) DO UPDATE SET
			name = excluded.name,
			picture = excluded.picture,
			last_seen = excluded.last_seen`,
		email, name, picture, now, now)
	if err != nil {
		return nil, fmt.Errorf("upserting user: %w", err)
	}
	return s.UserByEmail(ctx, email)
}

// UserByEmail returns the user with this email, or nil when absent.
func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, email, name, picture, created_at, last_seen, is_seed FROM users WHERE email = ?`, email)
	return scanUser(row)
}

// CountUsers reports how many users exist.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateSession stores a hashed session token.
func (s *Store) CreateSession(ctx context.Context, tokenHash string, userID int64, ttl time.Duration, ua string) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, created_at, expires_at, user_agent) VALUES (?, ?, ?, ?, ?)`,
		tokenHash, userID, now.Unix(), now.Add(ttl).Unix(), ua)
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	return nil
}

// UserForSession resolves a hashed token to its owner, or nil when expired.
func (s *Store) UserForSession(ctx context.Context, tokenHash string) (*User, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.email, u.name, u.picture, u.created_at, u.last_seen, u.is_seed
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, tokenHash, time.Now().Unix())
	return scanUser(row)
}

// DeleteSession revokes a session.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// PurgeExpiredSessions removes sessions past their expiry.
func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix())
	return err
}

// PutOAuthState records a pending OAuth state value.
func (s *Store) PutOAuthState(ctx context.Context, state string) error {
	cutoff := time.Now().Add(-15 * time.Minute).Unix()
	s.db.ExecContext(ctx, `DELETE FROM oauth_states WHERE created_at < ?`, cutoff)
	_, err := s.db.ExecContext(ctx, `INSERT INTO oauth_states (state, created_at) VALUES (?, ?)`,
		state, time.Now().Unix())
	return err
}

// ConsumeOAuthState deletes and reports whether state existed.
func (s *Store) ConsumeOAuthState(ctx context.Context, state string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM oauth_states WHERE state = ?`, state)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func scanUser(row *sql.Row) (*User, error) {
	var u User
	var created, seen int64
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Picture, &created, &seen, &u.IsSeed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scanning user: %w", err)
	}
	u.Created = time.Unix(created, 0)
	u.LastSeen = time.Unix(seen, 0)
	return &u, nil
}
