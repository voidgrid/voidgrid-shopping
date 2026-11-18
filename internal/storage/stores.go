package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// ErrDuplicate means the name is already taken.
var ErrDuplicate = errors.New("storage: already exists")

// ItemKey is how items are told apart: lower case, single spaces. Matching is
// exact on purpose; two spellings are two items until a person says otherwise.
func ItemKey(name string) string {
	return strings.Trim(strings.Join(strings.Fields(strings.ToLower(name)), " "), ".,;:")
}

// Aisle is a numbered section of a store; the number is the walking order.
type Aisle struct {
	ID       int64
	StoreID  int64
	Name     string
	Position int
}

// Store is a shop with its aisles.
type Store struct {
	ID       int64
	Name     string
	Position int
	Aisles   []Aisle
}

func isUnique(err error) bool { return err != nil && strings.Contains(err.Error(), "UNIQUE") }

// Stores lists every store with its aisles in walking order.
func (db *DB) Stores(ctx context.Context) ([]Store, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, name, position FROM stores ORDER BY position, lower(name)`)
	if err != nil {
		return nil, err
	}
	var out []Store
	idx := map[int64]int{}
	for rows.Next() {
		var s Store
		if err := rows.Scan(&s.ID, &s.Name, &s.Position); err != nil {
			_ = rows.Close()
			return nil, err
		}
		idx[s.ID] = len(out)
		out = append(out, s)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	ar, err := db.QueryContext(ctx, `SELECT id, store_id, name, position FROM aisles ORDER BY position, lower(name)`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = ar.Close() }()
	for ar.Next() {
		var a Aisle
		if err := ar.Scan(&a.ID, &a.StoreID, &a.Name, &a.Position); err != nil {
			return nil, err
		}
		if i, ok := idx[a.StoreID]; ok {
			out[i].Aisles = append(out[i].Aisles, a)
		}
	}
	return out, ar.Err()
}

func cleanName(s string, max int) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" || len(s) > max {
		return "", ErrInvalid
	}
	return s, nil
}

// AddStore creates a store.
func (db *DB) AddStore(ctx context.Context, name string) (int64, error) {
	name, err := cleanName(name, 80)
	if err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, `INSERT INTO stores (name, position) VALUES (?, (SELECT coalesce(max(position), 0) + 1 FROM stores))`, name)
	if isUnique(err) {
		return 0, ErrDuplicate
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateStore renames a store and sets its order.
func (db *DB) UpdateStore(ctx context.Context, id int64, name string, position int) error {
	name, err := cleanName(name, 80)
	if err != nil || position < 0 || position > 9999 {
		return ErrInvalid
	}
	res, err := db.ExecContext(ctx, `UPDATE stores SET name = ?, position = ? WHERE id = ?`, name, position, id)
	if isUnique(err) {
		return ErrDuplicate
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteStore removes a store, its aisles and what was learned for it.
func (db *DB) DeleteStore(ctx context.Context, id int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM stores WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddAisle adds an aisle; position 0 puts it last.
func (db *DB) AddAisle(ctx context.Context, storeID int64, name string, position int) (int64, error) {
	name, err := cleanName(name, 80)
	if err != nil || position < 0 || position > 9999 {
		return 0, ErrInvalid
	}
	res, err := db.ExecContext(ctx, `INSERT INTO aisles (store_id, name, position)
		VALUES (?, ?, CASE WHEN ?3 > 0 THEN ?3 ELSE (SELECT coalesce(max(position), 0) + 1 FROM aisles WHERE store_id = ?1) END)`,
		storeID, name, position)
	switch {
	case isUnique(err):
		return 0, ErrDuplicate
	case err != nil && strings.Contains(err.Error(), "FOREIGN KEY"):
		return 0, ErrNotFound
	case err != nil:
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateAisle renames an aisle and sets its number.
func (db *DB) UpdateAisle(ctx context.Context, id int64, name string, position int) error {
	name, err := cleanName(name, 80)
	if err != nil || position < 0 || position > 9999 {
		return ErrInvalid
	}
	res, err := db.ExecContext(ctx, `UPDATE aisles SET name = ?, position = ? WHERE id = ?`, name, position, id)
	if isUnique(err) {
		return ErrDuplicate
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAisle removes an aisle; items in it become unassigned.
func (db *DB) DeleteAisle(ctx context.Context, id int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM aisles WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// placeFor returns where an item key was last assigned: its store and that store's aisle (0 if none).
func placeFor(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, key string, storeID int64,
) (store, aisle int64, err error) {
	store = storeID
	if store == 0 {
		err = q.QueryRowContext(ctx, `SELECT store_id FROM item_places WHERE key = ?`, key).Scan(&store)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, nil
		}
		if err != nil {
			return 0, 0, err
		}
	}
	err = q.QueryRowContext(ctx, `SELECT aisle_id FROM item_aisles WHERE store_id = ? AND key = ?`, store, key).Scan(&aisle)
	if errors.Is(err, sql.ErrNoRows) {
		return store, 0, nil
	}
	return store, aisle, err
}

// learnPlace remembers where an item was assigned.
func learnPlace(ctx context.Context, tx *sql.Tx, key string, storeID, aisleID int64) error {
	if key == "" || storeID == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO item_places (key, store_id) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET store_id = excluded.store_id`, key, storeID); err != nil {
		return err
	}
	if aisleID != 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO item_aisles (store_id, key, aisle_id) VALUES (?, ?, ?)
			ON CONFLICT(store_id, key) DO UPDATE SET aisle_id = excluded.aisle_id`, storeID, key, aisleID); err != nil {
			return err
		}
	}
	return nil
}
