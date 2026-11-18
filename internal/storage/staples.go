package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Staple is something bought regularly, food or not.
type Staple struct {
	ID        int64
	Name      string
	Key       string
	Quantity  float64 // 0 means no set amount
	Unit      string
	Note      string
	StoreID   int64
	StoreName string
}

func checkStaple(s *Staple) error {
	name, err := cleanName(s.Name, 200)
	if err != nil || s.Quantity < 0 || s.Quantity > 100000 || len(s.Unit) > 30 || len(s.Note) > 500 {
		return ErrInvalid
	}
	s.Name, s.Key = name, ItemKey(name)
	s.Unit, s.Note = strings.TrimSpace(s.Unit), strings.TrimSpace(s.Note)
	return nil
}

// Staples lists the staples by store, then name; ones with no store come last.
func (db *DB) Staples(ctx context.Context) ([]Staple, error) {
	rows, err := db.QueryContext(ctx, `SELECT s.id, s.name, s.key, s.quantity, s.unit, s.note, coalesce(s.store_id, 0), coalesce(st.name, '')
		FROM staples s LEFT JOIN stores st ON st.id = s.store_id
		ORDER BY st.id IS NULL, st.position, lower(st.name), lower(s.name)`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Staple
	for rows.Next() {
		var s Staple
		if err := rows.Scan(&s.ID, &s.Name, &s.Key, &s.Quantity, &s.Unit, &s.Note, &s.StoreID, &s.StoreName); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// StapleKeys is the set of item keys that are staples.
func (db *DB) StapleKeys(ctx context.Context) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT key FROM staples`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

func storeRef(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// AddStaple adds a staple; a staple with the same name already there is ErrDuplicate.
func (db *DB) AddStaple(ctx context.Context, s Staple, now time.Time) (int64, error) {
	if err := checkStaple(&s); err != nil {
		return 0, err
	}
	var id int64
	err := db.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO staples (name, key, quantity, unit, note, store_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			s.Name, s.Key, s.Quantity, s.Unit, s.Note, storeRef(s.StoreID), now.Unix())
		if isUnique(err) {
			return ErrDuplicate
		}
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		return learnPlace(ctx, tx, s.Key, s.StoreID, 0)
	})
	return id, err
}

// UpdateStaple changes a staple. Giving it a store teaches that store as the item's usual one.
func (db *DB) UpdateStaple(ctx context.Context, s Staple) error {
	if err := checkStaple(&s); err != nil {
		return err
	}
	return db.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE staples SET name = ?, key = ?, quantity = ?, unit = ?, note = ?, store_id = ? WHERE id = ?`,
			s.Name, s.Key, s.Quantity, s.Unit, s.Note, storeRef(s.StoreID), s.ID)
		if isUnique(err) {
			return ErrDuplicate
		}
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return learnPlace(ctx, tx, s.Key, s.StoreID, 0)
	})
}

// DeleteStaple removes a staple.
func (db *DB) DeleteStaple(ctx context.Context, id int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM staples WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ImportStaples adds staples whose names are not there yet and reports how many were new.
func (db *DB) ImportStaples(ctx context.Context, in []Staple, now time.Time) (added, skipped int, err error) {
	for _, s := range in {
		s.StoreID = 0
		if _, e := db.AddStaple(ctx, s, now); e != nil {
			if errors.Is(e, ErrDuplicate) || errors.Is(e, ErrInvalid) {
				skipped++
				continue
			}
			return added, skipped, e
		}
		added++
	}
	return added, skipped, nil
}
