package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Token kinds.
const (
	TokenInvite = "invite"
	TokenReset  = "reset"
)

// UserToken is a single-use invite or password-reset link.
type UserToken struct {
	Hash        string
	Kind        string
	UserID      int64 // reset only
	Email       string
	DisplayName string
	IsAdmin     bool
	CreatedAt   int64
	ExpiresAt   int64
}

// ListUsers returns every account, oldest first.
func (db *DB) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetUserDisabled blocks or allows sign-in and ends the user's sessions when blocking.
func (db *DB) SetUserDisabled(ctx context.Context, id int64, disabled bool) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE users SET disabled = ?1 WHERE id = ?2`, boolInt(disabled), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if disabled {
			_, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?1`, id)
		}
		return err
	})
}

// SetUserAdmin changes the admin flag.
func (db *DB) SetUserAdmin(ctx context.Context, id int64, admin bool) error {
	_, err := db.ExecContext(ctx, `UPDATE users SET is_admin = ?1 WHERE id = ?2`, boolInt(admin), id)
	return err
}

// ---- invite and reset links

// CreateInvite stores an invite for a new account. Older open invites for the
// same email are removed. It refuses an email that already has an account.
func (db *DB) CreateInvite(ctx context.Context, hash, email, name string, admin bool, by int64, now time.Time, ttl time.Duration) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE email = ?1`, email).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrEmailUsed
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_tokens WHERE kind = 'invite' AND email = ?1 COLLATE NOCASE`, email); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO user_tokens (token_hash, kind, email, display_name, is_admin, created_by, created_at, expires_at)
			 VALUES (?1, 'invite', ?2, ?3, ?4, ?5, ?6, ?7)`,
			hash, email, name, boolInt(admin), by, now.Unix(), now.Add(ttl).Unix())
		return err
	})
}

// CreateReset stores a password-reset link for a user, replacing any open one.
func (db *DB) CreateReset(ctx context.Context, hash string, userID, by int64, now time.Time, ttl time.Duration) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_tokens WHERE kind = 'reset' AND user_id = ?1`, userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO user_tokens (token_hash, kind, user_id, created_by, created_at, expires_at)
			 VALUES (?1, 'reset', ?2, ?3, ?4, ?5)`, hash, userID, by, now.Unix(), now.Add(ttl).Unix())
		return err
	})
}

// Token returns an unused, unexpired link of the given kind.
func (db *DB) Token(ctx context.Context, kind, hash string, now time.Time) (UserToken, error) {
	var t UserToken
	var uid sql.NullInt64
	var admin int
	err := db.QueryRowContext(ctx,
		`SELECT token_hash, kind, user_id, email, display_name, is_admin, created_at, expires_at
		   FROM user_tokens WHERE token_hash = ?1 AND kind = ?2 AND used_at IS NULL AND expires_at > ?3`,
		hash, kind, now.Unix()).Scan(&t.Hash, &t.Kind, &uid, &t.Email, &t.DisplayName, &admin, &t.CreatedAt, &t.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return UserToken{}, ErrNotFound
	}
	if err != nil {
		return UserToken{}, err
	}
	t.UserID, t.IsAdmin = uid.Int64, admin != 0
	return t, nil
}

// OpenTokens lists links that can still be used, newest first.
func (db *DB) OpenTokens(ctx context.Context, now time.Time) ([]UserToken, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT t.token_hash, t.kind, coalesce(t.user_id, 0), CASE WHEN t.kind = 'reset' THEN u.email ELSE t.email END,
		        t.display_name, t.is_admin, t.created_at, t.expires_at
		   FROM user_tokens t LEFT JOIN users u ON u.id = t.user_id
		  WHERE t.used_at IS NULL AND t.expires_at > ?1 ORDER BY t.created_at DESC`, now.Unix())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []UserToken
	for rows.Next() {
		var t UserToken
		var admin int
		if err := rows.Scan(&t.Hash, &t.Kind, &t.UserID, &t.Email, &t.DisplayName, &admin, &t.CreatedAt, &t.ExpiresAt); err != nil {
			return nil, err
		}
		t.IsAdmin = admin != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteToken withdraws a link.
func (db *DB) DeleteToken(ctx context.Context, hash string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM user_tokens WHERE token_hash = ?1`, hash)
	return err
}

// AcceptInvite uses an invite once and creates the account.
func (db *DB) AcceptInvite(ctx context.Context, hash, name, passwordHash string, now time.Time) (int64, error) {
	var id int64
	err := db.tx(ctx, func(tx *sql.Tx) error {
		var email, dname string
		var admin int
		err := tx.QueryRowContext(ctx,
			`SELECT email, display_name, is_admin FROM user_tokens
			  WHERE token_hash = ?1 AND kind = 'invite' AND used_at IS NULL AND expires_at > ?2`,
			hash, now.Unix()).Scan(&email, &dname, &admin)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if res, err := tx.ExecContext(ctx, `UPDATE user_tokens SET used_at = ?1 WHERE token_hash = ?2 AND used_at IS NULL`, now.Unix(), hash); err != nil {
			return err
		} else if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if strings.TrimSpace(name) == "" {
			name = dname
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO users (email, display_name, password_hash, is_admin, created_at) VALUES (?1, ?2, ?3, ?4, ?5)`,
			email, strings.TrimSpace(name), passwordHash, admin, now.Unix())
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed: users.email") {
				return ErrEmailUsed
			}
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// AcceptReset uses a reset link once: it sets the password and ends the
// user's sessions. It returns the user's id.
func (db *DB) AcceptReset(ctx context.Context, hash, passwordHash string, now time.Time) (int64, error) {
	var id int64
	err := db.tx(ctx, func(tx *sql.Tx) error {
		var uid sql.NullInt64
		err := tx.QueryRowContext(ctx,
			`SELECT user_id FROM user_tokens WHERE token_hash = ?1 AND kind = 'reset' AND used_at IS NULL AND expires_at > ?2`,
			hash, now.Unix()).Scan(&uid)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !uid.Valid) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if res, err := tx.ExecContext(ctx, `UPDATE user_tokens SET used_at = ?1 WHERE token_hash = ?2 AND used_at IS NULL`, now.Unix(), hash); err != nil {
			return err
		} else if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?1 WHERE id = ?2`, passwordHash, uid.Int64); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?1`, uid.Int64); err != nil {
			return err
		}
		id = uid.Int64
		return nil
	})
	return id, err
}

// PurgeTokens removes used and expired links.
func (db *DB) PurgeTokens(ctx context.Context, now time.Time) error {
	_, err := db.ExecContext(ctx, `DELETE FROM user_tokens WHERE used_at IS NOT NULL OR expires_at <= ?1`, now.Unix())
	return err
}

// ---- TOTP

// SetTOTPSecret stores a new (not yet confirmed) encrypted secret.
func (db *DB) SetTOTPSecret(ctx context.Context, id int64, sealed string) error {
	_, err := db.ExecContext(ctx, `UPDATE users SET totp_secret = ?1, totp_enabled = 0, totp_last_step = 0 WHERE id = ?2`, sealed, id)
	return err
}

// TOTPSecret returns the stored encrypted secret and whether TOTP is on.
func (db *DB) TOTPSecret(ctx context.Context, id int64) (sealed string, enabled bool, err error) {
	var e int
	err = db.QueryRowContext(ctx, `SELECT totp_secret, totp_enabled FROM users WHERE id = ?1`, id).Scan(&sealed, &e)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ErrNotFound
	}
	return sealed, e != 0, err
}

// EnableTOTP turns TOTP on and stores the hashed recovery codes.
func (db *DB) EnableTOTP(ctx context.Context, id int64, codeHashes []string) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET totp_enabled = 1 WHERE id = ?1 AND totp_secret <> ''`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?1`, id); err != nil {
			return err
		}
		for _, h := range codeHashes {
			if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_codes (user_id, code_hash) VALUES (?1, ?2)`, id, h); err != nil {
				return err
			}
		}
		return nil
	})
}

// DisableTOTP removes the secret and recovery codes.
func (db *DB) DisableTOTP(ctx context.Context, id int64) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET totp_secret = '', totp_enabled = 0, totp_last_step = 0 WHERE id = ?1`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?1`, id)
		return err
	})
}

// AcceptTOTPStep records step as used, only if it is newer than the last one
// accepted, so a code works once.
func (db *DB) AcceptTOTPStep(ctx context.Context, id, step int64) (bool, error) {
	res, err := db.ExecContext(ctx, `UPDATE users SET totp_last_step = ?1 WHERE id = ?2 AND totp_last_step < ?1`, step, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// UseRecoveryCode deletes a recovery code if it exists; true means it was valid.
func (db *DB) UseRecoveryCode(ctx context.Context, id int64, hash string) (bool, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?1 AND code_hash = ?2`, id, hash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// RecoveryCodeCount is how many unused codes remain.
func (db *DB) RecoveryCodeCount(ctx context.Context, id int64) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM recovery_codes WHERE user_id = ?1`, id).Scan(&n)
	return n, err
}

// ---- passkeys

// Passkey is one registered WebAuthn credential.
type Passkey struct {
	ID           int64
	UserID       int64
	CredentialID []byte
	Data         string
	Name         string
	CreatedAt    int64
	LastUsedAt   int64
}

// AddPasskey stores a credential.
func (db *DB) AddPasskey(ctx context.Context, userID int64, credID []byte, data, name string, now time.Time) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO passkeys (user_id, credential_id, data, name, created_at) VALUES (?1, ?2, ?3, ?4, ?5)`,
		userID, credID, data, name, now.Unix())
	if err != nil {
		return fmt.Errorf("storage: add passkey: %w", err)
	}
	return nil
}

// Passkeys lists a user's credentials.
func (db *DB) Passkeys(ctx context.Context, userID int64) ([]Passkey, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, user_id, credential_id, data, name, created_at, last_used_at FROM passkeys WHERE user_id = ?1 ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Passkey
	for rows.Next() {
		var p Passkey
		if err := rows.Scan(&p.ID, &p.UserID, &p.CredentialID, &p.Data, &p.Name, &p.CreatedAt, &p.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PasskeyByCredential finds a credential by its id.
func (db *DB) PasskeyByCredential(ctx context.Context, credID []byte) (Passkey, error) {
	var p Passkey
	err := db.QueryRowContext(ctx,
		`SELECT id, user_id, credential_id, data, name, created_at, last_used_at FROM passkeys WHERE credential_id = ?1`, credID).
		Scan(&p.ID, &p.UserID, &p.CredentialID, &p.Data, &p.Name, &p.CreatedAt, &p.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Passkey{}, ErrNotFound
	}
	return p, err
}

// TouchPasskey saves the updated credential (signature counter) after use.
func (db *DB) TouchPasskey(ctx context.Context, id int64, data string, now time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE passkeys SET data = ?1, last_used_at = ?2 WHERE id = ?3`, data, now.Unix(), id)
	return err
}

// DeletePasskey removes one of the user's credentials.
func (db *DB) DeletePasskey(ctx context.Context, userID, id int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM passkeys WHERE id = ?1 AND user_id = ?2`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAllPasskeys removes every credential of a user and reports how many.
func (db *DB) DeleteAllPasskeys(ctx context.Context, userID int64) (int64, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM passkeys WHERE user_id = ?1`, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- single sign-on

// IdentityUser finds the account linked to a provider subject.
func (db *DB) IdentityUser(ctx context.Context, issuer, subject string) (User, error) {
	return scanUser(db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = (SELECT user_id FROM oidc_identities WHERE issuer = ?1 AND subject = ?2)`,
		issuer, subject))
}

// LinkIdentity ties a provider subject to an account.
func (db *DB) LinkIdentity(ctx context.Context, issuer, subject string, userID int64) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO oidc_identities (issuer, subject, user_id) VALUES (?1, ?2, ?3)
		 ON CONFLICT (issuer, subject) DO UPDATE SET user_id = excluded.user_id`, issuer, subject, userID)
	return err
}
