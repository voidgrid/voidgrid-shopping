package storage

import (
	"context"
	"time"
)

// ListRevision is a number that goes up whenever the shopping list, its
// purchases, stores or aisles change.
func (db *DB) ListRevision(ctx context.Context) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx, `SELECT n FROM list_rev WHERE id = 1`).Scan(&n)
	return n, err
}

// ClaimOp records an offline change's id. It returns false if that id was
// already applied, so a replayed request does nothing the second time.
func (db *DB) ClaimOp(ctx context.Context, op string, now time.Time) (bool, error) {
	res, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO applied_ops (op, at) VALUES (?1, ?2)`, op, now.Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ReleaseOp forgets an id whose change failed, so it can be tried again.
func (db *DB) ReleaseOp(ctx context.Context, op string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM applied_ops WHERE op = ?1`, op)
	return err
}

// PurgeOps forgets ids older than 14 days.
func (db *DB) PurgeOps(ctx context.Context, now time.Time) error {
	_, err := db.ExecContext(ctx, `DELETE FROM applied_ops WHERE at < ?1`, now.Add(-14*24*time.Hour).Unix())
	return err
}
