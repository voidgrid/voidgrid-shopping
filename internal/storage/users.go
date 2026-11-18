package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Errors the callers branch on.
var (
	ErrNotFound  = errors.New("storage: not found")
	ErrEmailUsed = errors.New("storage: email already in use")
	ErrSetupDone = errors.New("storage: setup already done")
)

// User is one household account.
type User struct {
	ID           int64
	Email        string
	DisplayName  string
	PasswordHash string
	IsAdmin      bool
	Disabled     bool
	CreatedAt    int64
	TOTPEnabled  bool
}

const userColumns = `id, email, display_name, password_hash, is_admin, disabled, created_at, totp_enabled`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var admin, disabled, totp int
	if err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.PasswordHash, &admin, &disabled, &u.CreatedAt, &totp); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, err
	}
	u.IsAdmin = admin != 0
	u.Disabled = disabled != 0
	u.TOTPEnabled = totp != 0
	return u, nil
}

// CountUsers returns how many accounts exist.
func (db *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("storage: count users: %w", err)
	}
	return n, nil
}

// CreateFirstAdmin creates the first account as admin, atomically: it
// returns ErrSetupDone if any account already exists, so two racing setup
// requests can't both win.
func (db *DB) CreateFirstAdmin(ctx context.Context, email, name, hash string) (int64, error) {
	var id int64
	err := db.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrSetupDone
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO users (email, display_name, password_hash, is_admin, created_at) VALUES (?, ?, ?, 1, ?)`,
			strings.TrimSpace(email), strings.TrimSpace(name), hash, time.Now().Unix())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		if errors.Is(err, ErrSetupDone) {
			return 0, err
		}
		return 0, fmt.Errorf("storage: create first admin: %w", err)
	}
	return id, nil
}

// CreateUser creates an account.
func (db *DB) CreateUser(ctx context.Context, email, name, hash string, admin bool) (int64, error) {
	res, err := db.ExecContext(ctx,
		`INSERT INTO users (email, display_name, password_hash, is_admin, created_at) VALUES (?, ?, ?, ?, ?)`,
		strings.TrimSpace(email), strings.TrimSpace(name), hash, boolInt(admin), time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: users.email") {
			return 0, ErrEmailUsed
		}
		return 0, fmt.Errorf("storage: create user: %w", err)
	}
	return res.LastInsertId()
}

// UserByEmail finds an account by email, case-insensitively.
func (db *DB) UserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE email = ?`, strings.TrimSpace(email)))
}

// UserByID finds an account by ID.
func (db *DB) UserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// SetPassword replaces a user's password hash and ends all their sessions.
func (db *DB) SetPassword(ctx context.Context, id int64, hash string) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id)
		return err
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
