package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// AllSettings returns every stored setting.
func (db *DB) AllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("storage: read settings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	m := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("storage: read settings: %w", err)
		}
		m[k] = v
	}
	return m, rows.Err()
}

// SetSettings stores every pair in one transaction.
func (db *DB) SetSettings(ctx context.Context, m map[string]string) error {
	err := db.tx(ctx, func(tx *sql.Tx) error {
		for k, v := range m {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO settings (key, value) VALUES (?, ?)
				 ON CONFLICT (key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("storage: save settings: %w", err)
	}
	return nil
}
