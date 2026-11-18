package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Share is a public link to one recipe.
type Share struct {
	Hash      string
	RecipeID  int64
	CreatedBy int64
	CreatedAt int64
	ExpiresAt int64
	RevokedAt int64 // 0 if not revoked
}

// MaxShareDays is the longest a public link may live.
const MaxShareDays = 365

// CreateShare stores a link that expires at expires. It refuses an expiry in
// the past or more than a year ahead, whatever the caller asked for.
func (db *DB) CreateShare(ctx context.Context, hash string, recipeID, by int64, now, expires time.Time) error {
	if !expires.After(now) || expires.After(now.Add(MaxShareDays*24*time.Hour+time.Minute)) {
		return errors.New("storage: share expiry must be within 1 to 365 days")
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO shares (token_hash, recipe_id, created_by, created_at, expires_at) VALUES (?1, ?2, ?3, ?4, ?5)`,
		hash, recipeID, by, now.Unix(), expires.Unix())
	return err
}

// LiveShare returns a link that is neither expired nor revoked.
func (db *DB) LiveShare(ctx context.Context, hash string, now time.Time) (Share, error) {
	var s Share
	var by, rev sql.NullInt64
	err := db.QueryRowContext(ctx,
		`SELECT token_hash, recipe_id, created_by, created_at, expires_at, revoked_at FROM shares
		  WHERE token_hash = ?1 AND revoked_at IS NULL AND expires_at > ?2`, hash, now.Unix()).
		Scan(&s.Hash, &s.RecipeID, &by, &s.CreatedAt, &s.ExpiresAt, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return Share{}, ErrNotFound
	}
	s.CreatedBy, s.RevokedAt = by.Int64, rev.Int64
	return s, err
}

// SharesFor lists a recipe's links that can still be used, newest first.
func (db *DB) SharesFor(ctx context.Context, recipeID int64, now time.Time) ([]Share, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT token_hash, recipe_id, coalesce(created_by, 0), created_at, expires_at FROM shares
		  WHERE recipe_id = ?1 AND revoked_at IS NULL AND expires_at > ?2 ORDER BY created_at DESC, rowid DESC`, recipeID, now.Unix())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Share
	for rows.Next() {
		var s Share
		if err := rows.Scan(&s.Hash, &s.RecipeID, &s.CreatedBy, &s.CreatedAt, &s.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RevokeShare ends a link at once.
func (db *DB) RevokeShare(ctx context.Context, hash string, recipeID int64, now time.Time) error {
	res, err := db.ExecContext(ctx,
		`UPDATE shares SET revoked_at = ?1 WHERE token_hash = ?2 AND recipe_id = ?3 AND revoked_at IS NULL`, now.Unix(), hash, recipeID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgeShares forgets links that ended more than 30 days ago.
func (db *DB) PurgeShares(ctx context.Context, now time.Time) error {
	cut := now.Add(-30 * 24 * time.Hour).Unix()
	_, err := db.ExecContext(ctx, `DELETE FROM shares WHERE expires_at < ?1 OR revoked_at < ?1`, cut)
	return err
}
