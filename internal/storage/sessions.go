package storage

import (
	"context"
	"fmt"
	"time"
)

// CreateSession records a session by the hash of its cookie token.
func (db *DB) CreateSession(ctx context.Context, tokenHash string, userID int64, expires time.Time) error {
	if _, err := db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		tokenHash, userID, time.Now().Unix(), expires.Unix()); err != nil {
		return fmt.Errorf("storage: create session: %w", err)
	}
	return nil
}

// SessionUser returns the user a live session belongs to. Expired sessions
// and sessions of disabled users give ErrNotFound.
func (db *DB) SessionUser(ctx context.Context, tokenHash string, now time.Time) (User, error) {
	return scanUser(db.QueryRowContext(ctx,
		`SELECT u.id, u.email, u.display_name, u.password_hash, u.is_admin, u.disabled, u.created_at, u.totp_enabled
		   FROM sessions s JOIN users u ON u.id = s.user_id
		  WHERE s.token_hash = ? AND s.expires_at > ? AND u.disabled = 0`,
		tokenHash, now.Unix()))
}

// DeleteSession ends one session.
func (db *DB) DeleteSession(ctx context.Context, tokenHash string) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash); err != nil {
		return fmt.Errorf("storage: delete session: %w", err)
	}
	return nil
}

// PurgeSessions removes expired sessions.
func (db *DB) PurgeSessions(ctx context.Context, now time.Time) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.Unix()); err != nil {
		return fmt.Errorf("storage: purge sessions: %w", err)
	}
	return nil
}
