package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ErrInvalid means a value failed validation (bad date, slot, kind ...).
var ErrInvalid = errors.New("storage: invalid value")

// Slots are the meals of a day, in order.
var Slots = []string{"breakfast", "lunch", "snack", "dinner"}

// Entry kinds.
const (
	KindRecipe    = "recipe"
	KindLeftovers = "leftovers"
	KindNote      = "note"
)

// ValidSlot reports whether s is a meal slot.
func ValidSlot(s string) bool {
	for _, v := range Slots {
		if v == s {
			return true
		}
	}
	return false
}

// ValidDate reports whether s is a calendar date written YYYY-MM-DD.
func ValidDate(s string) bool {
	if len(s) != 10 {
		return false
	}
	t, err := time.Parse("2006-01-02", s)
	return err == nil && t.Format("2006-01-02") == s
}

// PlanEntry is one thing planned for a slot of a day.
type PlanEntry struct {
	ID             int64
	Date           string
	Slot           string
	Kind           string
	RecipeID       int64
	Title          string
	Photo          string
	Servings       int // planned servings; 0 means the recipe's own
	RecipeServings int
	Note           string
	Position       int
}

// DayNote is a note in a day's Notes row, optionally tied to a later meal.
type DayNote struct {
	ID      int64
	Date    string
	Text    string
	ForDate string
	ForSlot string
}

func checkEntry(e PlanEntry) error {
	if !ValidDate(e.Date) || !ValidSlot(e.Slot) || e.Servings < 0 || e.Servings > 1000 || len(e.Note) > 500 {
		return ErrInvalid
	}
	switch e.Kind {
	case KindRecipe:
		if e.RecipeID <= 0 {
			return ErrInvalid
		}
	case KindNote:
		if strings.TrimSpace(e.Note) == "" {
			return ErrInvalid
		}
	case KindLeftovers:
	default:
		return ErrInvalid
	}
	return nil
}

func nextPosition(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, date, slot string,
) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT coalesce(max(position), 0) + 1 FROM plan_entries WHERE date = ? AND slot = ?`, date, slot).Scan(&n)
	return n, err
}

// AddEntry puts e at the end of its slot and returns the new ID.
func (db *DB) AddEntry(ctx context.Context, e PlanEntry, userID int64, now time.Time) (int64, error) {
	if err := checkEntry(e); err != nil {
		return 0, err
	}
	var id int64
	err := db.tx(ctx, func(tx *sql.Tx) error {
		pos, err := nextPosition(ctx, tx, e.Date, e.Slot)
		if err != nil {
			return err
		}
		var rid any
		if e.Kind == KindRecipe {
			rid = e.RecipeID
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO plan_entries (date, slot, kind, recipe_id, servings, note, position, created_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, e.Date, e.Slot, e.Kind, rid, e.Servings, strings.TrimSpace(e.Note), pos, nullID(userID), now.Unix())
		if err != nil {
			if strings.Contains(err.Error(), "FOREIGN KEY") {
				return ErrNotFound
			}
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

const entryColumns = `e.id, e.date, e.slot, e.kind, coalesce(e.recipe_id, 0), coalesce(r.title, ''), coalesce(r.photo, ''),
	e.servings, coalesce(r.servings, 0), e.note, e.position`

func scanEntry(row interface{ Scan(...any) error }) (PlanEntry, error) {
	var e PlanEntry
	err := row.Scan(&e.ID, &e.Date, &e.Slot, &e.Kind, &e.RecipeID, &e.Title, &e.Photo, &e.Servings, &e.RecipeServings, &e.Note, &e.Position)
	if errors.Is(err, sql.ErrNoRows) {
		return PlanEntry{}, ErrNotFound
	}
	return e, err
}

// PlanEntry returns one entry.
func (db *DB) PlanEntry(ctx context.Context, id int64) (PlanEntry, error) {
	return scanEntry(db.QueryRowContext(ctx,
		`SELECT `+entryColumns+` FROM plan_entries e LEFT JOIN recipes r ON r.id = e.recipe_id WHERE e.id = ?`, id))
}

// PlanEntries lists entries from first to last date (inclusive), in slot then position order.
func (db *DB) PlanEntries(ctx context.Context, first, last string) ([]PlanEntry, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+entryColumns+` FROM plan_entries e LEFT JOIN recipes r ON r.id = e.recipe_id
		WHERE e.date BETWEEN ? AND ?
		ORDER BY e.date, CASE e.slot WHEN 'breakfast' THEN 1 WHEN 'lunch' THEN 2 WHEN 'snack' THEN 3 ELSE 4 END, e.position, e.id`, first, last)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PlanEntry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MoveEntry changes an entry's day, slot and (for recipes) servings. Moving
// to another slot puts it at the end of that slot.
func (db *DB) MoveEntry(ctx context.Context, id int64, date, slot string, servings int) error {
	if !ValidDate(date) || !ValidSlot(slot) || servings < 0 || servings > 1000 {
		return ErrInvalid
	}
	return db.tx(ctx, func(tx *sql.Tx) error {
		var oldDate, oldSlot string
		if err := tx.QueryRowContext(ctx, `SELECT date, slot FROM plan_entries WHERE id = ?`, id).Scan(&oldDate, &oldSlot); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if oldDate != date || oldSlot != slot {
			pos, err := nextPosition(ctx, tx, date, slot)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE plan_entries SET date = ?, slot = ?, position = ?, servings = ? WHERE id = ?`, date, slot, pos, servings, id)
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE plan_entries SET servings = ? WHERE id = ?`, servings, id)
		return err
	})
}

// EditEntryNote changes the text of a note entry.
func (db *DB) EditEntryNote(ctx context.Context, id int64, text string) error {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 500 {
		return ErrInvalid
	}
	res, err := db.ExecContext(ctx, `UPDATE plan_entries SET note = ? WHERE id = ? AND kind = 'note'`, text, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteEntry removes an entry.
func (db *DB) DeleteEntry(ctx context.Context, id int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM plan_entries WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddDayNote adds a note to a day, optionally tied to a meal (forDate and forSlot both set or both empty).
func (db *DB) AddDayNote(ctx context.Context, date, text, forDate, forSlot string, userID int64, now time.Time) (int64, error) {
	text = strings.TrimSpace(text)
	if !ValidDate(date) || text == "" || len(text) > 500 || (forDate == "") != (forSlot == "") ||
		(forDate != "" && (!ValidDate(forDate) || !ValidSlot(forSlot))) {
		return 0, ErrInvalid
	}
	var fd, fs any
	if forDate != "" {
		fd, fs = forDate, forSlot
	}
	res, err := db.ExecContext(ctx, `INSERT INTO day_notes (date, text, for_date, for_slot, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		date, text, fd, fs, nullID(userID), now.Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DayNotes lists notes written on days in the range, or tied to meals in it.
func (db *DB) DayNotes(ctx context.Context, first, last string) ([]DayNote, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, date, text, coalesce(for_date, ''), coalesce(for_slot, '') FROM day_notes
		WHERE date BETWEEN ?1 AND ?2 OR for_date BETWEEN ?1 AND ?2 ORDER BY date, id`, first, last)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []DayNote
	for rows.Next() {
		var n DayNote
		if err := rows.Scan(&n.ID, &n.Date, &n.Text, &n.ForDate, &n.ForSlot); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// DeleteDayNote removes a day note.
func (db *DB) DeleteDayNote(ctx context.Context, id int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM day_notes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
