package storage

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/voidgrid/voidgrid-shopping/internal/units"
)

// Item states.
const (
	StateNeed   = "need"
	StateHave   = "have"
	StateBought = "bought"
)

// Need is something to put on the list.
type Need struct {
	Name     string
	Quantity float64 // 0 means no amount ("salt")
	Unit     string
	Pack     string // "12 oz": the size of each can or package
	Note     string
	StoreID  int64 // 0: use what was learned for this item
}

// Purchase is one buy of an item.
type Purchase struct {
	ID         int64
	ItemID     int64
	Quantity   float64
	PriceCents int64 // per package, or per WeightUnit when that is set
	WeightUnit string
	Weight     float64
	Brand      string
	TotalCents int64
}

// ListItem is a line of the shopping list.
type ListItem struct {
	ID         int64
	Name       string
	Key        string
	Quantity   float64
	Unit       string
	Pack       string
	Note       string
	StoreID    int64
	StoreName  string
	AisleID    int64
	AisleName  string
	AislePos   int
	State      string
	FromStaple bool
	Purchases  []Purchase
}

// counted is how much of an item's amount a purchase covers: its weight when
// it was bought by weight and the item is listed by weight, else its quantity.
func counted(p Purchase, unit string) float64 {
	if p.WeightUnit != "" {
		if q, ok := convertAmount(p.Weight, p.WeightUnit, unit); ok {
			return q
		}
	}
	return p.Quantity
}

// Bought is the amount purchased so far, in the item's own unit.
func (it ListItem) Bought() float64 {
	var q float64
	for _, p := range it.Purchases {
		q += counted(p, it.Unit)
	}
	return q
}

// Remaining is what is still to buy (1 for an item with no set amount).
func (it ListItem) Remaining() float64 {
	if it.Quantity == 0 {
		return 1
	}
	return math.Max(it.Quantity-it.Bought(), 0)
}

// Spent is the total paid for this item.
func (it ListItem) Spent() int64 {
	var c int64
	for _, p := range it.Purchases {
		c += p.TotalCents
	}
	return c
}

func checkNeed(n *Need) error {
	name, err := cleanName(n.Name, 200)
	if err != nil || n.Quantity < 0 || n.Quantity > 100000 || len(n.Unit) > 30 || len(n.Pack) > 40 || len(n.Note) > 500 {
		return ErrInvalid
	}
	n.Name, n.Unit, n.Pack, n.Note = name, strings.TrimSpace(n.Unit), strings.TrimSpace(n.Pack), strings.TrimSpace(n.Note)
	return nil
}

// convertAmount turns qty of unit from into unit to when both are the same kind of measure.
func convertAmount(qty float64, from, to string) (float64, bool) {
	a, ok1 := units.Lookup(from)
	b, ok2 := units.Lookup(to)
	if !ok1 || !ok2 {
		return 0, false
	}
	return units.Convert(qty, a, b)
}

// addNeed puts a need on the list inside tx, merging it into an item that is
// still to buy when that is exact: same name, same pack, the same kind of unit.
// A staple's amount is never added to; an item that is also a staple appears once.
func addNeed(ctx context.Context, tx *sql.Tx, n Need, fromStaple bool, now time.Time) (id int64, merged bool, err error) {
	if err := checkNeed(&n); err != nil {
		return 0, false, err
	}
	key := ItemKey(n.Name)
	rows, err := tx.QueryContext(ctx, `SELECT id, quantity, unit, pack, from_staple FROM list_items WHERE key = ? AND state = 'need' ORDER BY id`, key)
	if err != nil {
		return 0, false, err
	}
	type row struct {
		id         int64
		qty        float64
		unit, pack string
		staple     bool
	}
	var existing []row
	for rows.Next() {
		var r row
		var st int
		if err := rows.Scan(&r.id, &r.qty, &r.unit, &r.pack, &st); err != nil {
			_ = rows.Close()
			return 0, false, err
		}
		r.staple = st != 0
		existing = append(existing, r)
	}
	if err := rows.Close(); err != nil {
		return 0, false, err
	}
	for _, r := range existing {
		switch {
		case fromStaple || r.staple || n.Quantity == 0:
			return r.id, true, nil // already there: it appears once
		case r.pack != n.Pack:
			continue
		case r.qty == 0:
			_, err = tx.ExecContext(ctx, `UPDATE list_items SET quantity = ?, unit = ?, changed_at = ? WHERE id = ?`, n.Quantity, n.Unit, now.Unix(), r.id)
			return r.id, true, err
		case r.unit == n.Unit:
			_, err = tx.ExecContext(ctx, `UPDATE list_items SET quantity = quantity + ?, changed_at = ? WHERE id = ?`, n.Quantity, now.Unix(), r.id)
			return r.id, true, err
		}
		if q, ok := convertAmount(n.Quantity, n.Unit, r.unit); ok {
			_, err = tx.ExecContext(ctx, `UPDATE list_items SET quantity = quantity + ?, changed_at = ? WHERE id = ?`, q, now.Unix(), r.id)
			return r.id, true, err
		}
	}
	store, aisle, err := placeFor(ctx, tx, key, n.StoreID)
	if err != nil {
		return 0, false, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO list_items (name, key, quantity, unit, pack, note, store_id, aisle_id, state, from_staple, created_at, changed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'need', ?, ?, ?)`,
		n.Name, key, n.Quantity, n.Unit, n.Pack, n.Note, storeRef(store), storeRef(aisle), boolInt(fromStaple), now.Unix(), now.Unix())
	if err != nil {
		return 0, false, err
	}
	id, err = res.LastInsertId()
	return id, false, err
}

// AddListItem adds one item by hand.
func (db *DB) AddListItem(ctx context.Context, n Need, now time.Time) (id int64, merged bool, err error) {
	err = db.tx(ctx, func(tx *sql.Tx) error {
		id, merged, err = addNeed(ctx, tx, n, false, now)
		return err
	})
	return id, merged, err
}

// AddNeeds adds the needs of planned meals and records those meals as added.
func (db *DB) AddNeeds(ctx context.Context, needs []Need, entryIDs []int64, now time.Time) (added, merged int, err error) {
	err = db.tx(ctx, func(tx *sql.Tx) error {
		for _, n := range needs {
			_, m, err := addNeed(ctx, tx, n, false, now)
			if errors.Is(err, ErrInvalid) {
				continue
			}
			if err != nil {
				return err
			}
			if m {
				merged++
			} else {
				added++
			}
		}
		for _, id := range entryIDs {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO list_added_entries (plan_entry_id) VALUES (?)`, id); err != nil {
				return err
			}
		}
		return nil
	})
	return added, merged, err
}

// AddedEntries is the set of planned meals already on the list.
func (db *DB) AddedEntries(ctx context.Context) (map[int64]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT plan_entry_id FROM list_added_entries`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// CopyStaples puts the given staples (all of them if ids is empty) on the list in their aisles.
// Staples already on the list, as a staple or as a recipe ingredient, are not added twice.
func (db *DB) CopyStaples(ctx context.Context, ids []int64, now time.Time) (added, already int, err error) {
	all, err := db.Staples(ctx)
	if err != nil {
		return 0, 0, err
	}
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	err = db.tx(ctx, func(tx *sql.Tx) error {
		for _, s := range all {
			if len(want) > 0 && !want[s.ID] {
				continue
			}
			_, merged, err := addNeed(ctx, tx, Need{Name: s.Name, Quantity: s.Quantity, Unit: s.Unit, Note: s.Note, StoreID: s.StoreID}, true, now)
			if err != nil {
				return err
			}
			if merged {
				already++
			} else {
				added++
			}
		}
		return nil
	})
	return added, already, err
}

// ListItems returns every item with its purchases, store and aisle names.
func (db *DB) ListItems(ctx context.Context) ([]ListItem, error) {
	rows, err := db.QueryContext(ctx, `SELECT i.id, i.name, i.key, i.quantity, i.unit, i.pack, i.note,
		coalesce(i.store_id, 0), coalesce(s.name, ''), coalesce(i.aisle_id, 0), coalesce(a.name, ''), coalesce(a.position, 0),
		i.state, i.from_staple
		FROM list_items i LEFT JOIN stores s ON s.id = i.store_id LEFT JOIN aisles a ON a.id = i.aisle_id
		ORDER BY lower(i.name), i.id`)
	if err != nil {
		return nil, err
	}
	var items []ListItem
	idx := map[int64]int{}
	for rows.Next() {
		var it ListItem
		var staple int
		if err := rows.Scan(&it.ID, &it.Name, &it.Key, &it.Quantity, &it.Unit, &it.Pack, &it.Note, &it.StoreID, &it.StoreName,
			&it.AisleID, &it.AisleName, &it.AislePos, &it.State, &staple); err != nil {
			_ = rows.Close()
			return nil, err
		}
		it.FromStaple = staple != 0
		idx[it.ID] = len(items)
		items = append(items, it)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	pr, err := db.QueryContext(ctx, `SELECT id, item_id, quantity, price_cents, weight_unit, weight, brand, total_cents FROM purchases ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = pr.Close() }()
	for pr.Next() {
		var p Purchase
		if err := pr.Scan(&p.ID, &p.ItemID, &p.Quantity, &p.PriceCents, &p.WeightUnit, &p.Weight, &p.Brand, &p.TotalCents); err != nil {
			return nil, err
		}
		if i, ok := idx[p.ItemID]; ok {
			items[i].Purchases = append(items[i].Purchases, p)
		}
	}
	return items, pr.Err()
}

// ListItem returns one item.
func (db *DB) ListItem(ctx context.Context, id int64) (ListItem, error) {
	all, err := db.ListItems(ctx)
	if err != nil {
		return ListItem{}, err
	}
	for _, it := range all {
		if it.ID == id {
			return it, nil
		}
	}
	return ListItem{}, ErrNotFound
}

// ItemEdit is the editable part of a list item. NewAisle creates an aisle in StoreID.
type ItemEdit struct {
	Name     string
	Quantity float64
	Unit     string
	Note     string
	StoreID  int64
	AisleID  int64
	NewAisle string
}

// UpdateListItem edits an item and teaches the store and aisle it was given.
func (db *DB) UpdateListItem(ctx context.Context, id int64, e ItemEdit, now time.Time) error {
	n := Need{Name: e.Name, Quantity: e.Quantity, Unit: e.Unit, Note: e.Note}
	if err := checkNeed(&n); err != nil {
		return err
	}
	return db.tx(ctx, func(tx *sql.Tx) error {
		store, aisle := e.StoreID, e.AisleID
		if aisle != 0 { // an aisle belongs to a store: it decides the store
			err := tx.QueryRowContext(ctx, `SELECT store_id FROM aisles WHERE id = ?`, aisle).Scan(&store)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrInvalid
			}
			if err != nil {
				return err
			}
		}
		if name := strings.Join(strings.Fields(e.NewAisle), " "); name != "" {
			if store == 0 || len(name) > 80 {
				return ErrInvalid
			}
			err := tx.QueryRowContext(ctx, `SELECT id FROM aisles WHERE store_id = ? AND name = ?`, store, name).Scan(&aisle)
			if errors.Is(err, sql.ErrNoRows) {
				res, err := tx.ExecContext(ctx, `INSERT INTO aisles (store_id, name, position)
					VALUES (?1, ?2, (SELECT coalesce(max(position), 0) + 1 FROM aisles WHERE store_id = ?1))`, store, name)
				if err != nil {
					return err
				}
				if aisle, err = res.LastInsertId(); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		}
		if store == 0 {
			aisle = 0
		}
		res, err := tx.ExecContext(ctx, `UPDATE list_items SET name = ?, key = ?, quantity = ?, unit = ?, note = ?, store_id = ?, aisle_id = ?, changed_at = ? WHERE id = ?`,
			n.Name, ItemKey(n.Name), n.Quantity, n.Unit, n.Note, storeRef(store), storeRef(aisle), now.Unix(), id)
		if err != nil {
			return err
		}
		if c, _ := res.RowsAffected(); c == 0 {
			return ErrNotFound
		}
		return learnPlace(ctx, tx, ItemKey(n.Name), store, aisle)
	})
}

// DeleteListItem removes an item and its purchases.
func (db *DB) DeleteListItem(ctx context.Context, id int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM list_items WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetItemState moves an item between "to buy", "already have" and "bought".
// Moving back to "to buy" undoes its purchases.
func (db *DB) SetItemState(ctx context.Context, id int64, state string, now time.Time) error {
	if state != StateNeed && state != StateHave && state != StateBought {
		return ErrInvalid
	}
	return db.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE list_items SET state = ?, changed_at = ? WHERE id = ?`, state, now.Unix(), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if state != StateBought {
			_, err = tx.ExecContext(ctx, `DELETE FROM purchases WHERE item_id = ?`, id)
		}
		return err
	})
}

// ClearList removes finished items: "bought", "have", "done" (both) or "all".
func (db *DB) ClearList(ctx context.Context, which string) (int64, error) {
	var q string
	switch which {
	case "bought":
		q = `DELETE FROM list_items WHERE state = 'bought'`
	case "have":
		q = `DELETE FROM list_items WHERE state = 'have'`
	case "done":
		q = `DELETE FROM list_items WHERE state <> 'need'`
	case "all":
		q = `DELETE FROM list_items`
	default:
		return 0, ErrInvalid
	}
	res, err := db.ExecContext(ctx, q)
	if err != nil {
		return 0, err
	}
	if which == "all" {
		if _, err := db.ExecContext(ctx, `DELETE FROM list_added_entries`); err != nil {
			return 0, err
		}
	}
	return res.RowsAffected()
}

// Spent is the total of every purchase on the list, in cents.
func (db *DB) Spent(ctx context.Context) (int64, error) {
	var c int64
	err := db.QueryRowContext(ctx, `SELECT coalesce(sum(total_cents), 0) FROM purchases`).Scan(&c)
	return c, err
}

var weightUnits = map[string]float64{"lb": 453.59237, "oz": 28.349523125, "kg": 1000, "g": 1}

// PurchaseTotal is what a purchase costs: price times quantity, or price per
// weight unit times the weight for goods sold by weight.
func PurchaseTotal(p Purchase) int64 {
	if p.WeightUnit != "" {
		return int64(math.Round(float64(p.PriceCents) * p.Weight))
	}
	return int64(math.Round(float64(p.PriceCents) * p.Quantity))
}

// AddPurchase records a purchase of an item. The item is marked bought once
// the purchased quantity reaches what was needed, and the price is remembered
// for the item, store and brand.
func (db *DB) AddPurchase(ctx context.Context, itemID int64, p Purchase, now time.Time) error {
	p.Brand = strings.Join(strings.Fields(p.Brand), " ")
	_, wok := weightUnits[p.WeightUnit]
	if p.Quantity <= 0 || p.Quantity > 100000 || p.PriceCents < 0 || p.PriceCents > 100000000 || len(p.Brand) > 80 ||
		(p.WeightUnit != "" && (!wok || p.Weight <= 0 || p.Weight > 100000)) || (p.WeightUnit == "" && p.Weight != 0) {
		return ErrInvalid
	}
	p.TotalCents = PurchaseTotal(p)
	return db.tx(ctx, func(tx *sql.Tx) error {
		var key, unit string
		var store sql.NullInt64
		var need float64
		if err := tx.QueryRowContext(ctx, `SELECT key, store_id, quantity, unit FROM list_items WHERE id = ?`, itemID).Scan(&key, &store, &need, &unit); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO purchases (item_id, quantity, price_cents, weight_unit, weight, brand, total_cents, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, itemID, p.Quantity, p.PriceCents, p.WeightUnit, p.Weight, p.Brand, p.TotalCents, now.Unix()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO item_prices (key, store_id, brand, price_cents, weight_unit, updated_at) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(key, store_id, brand) DO UPDATE SET price_cents = excluded.price_cents, weight_unit = excluded.weight_unit, updated_at = excluded.updated_at`,
			key, store.Int64, p.Brand, p.PriceCents, p.WeightUnit, now.Unix()); err != nil {
			return err
		}
		got, err := purchasedIn(ctx, tx, itemID, unit)
		if err != nil {
			return err
		}
		if need == 0 || got >= need-1e-9 {
			_, err := tx.ExecContext(ctx, `UPDATE list_items SET state = 'bought', changed_at = ? WHERE id = ?`, now.Unix(), itemID)
			return err
		}
		return nil
	})
}

// purchasedIn sums what an item's purchases cover, in the item's unit.
func purchasedIn(ctx context.Context, tx *sql.Tx, itemID int64, unit string) (float64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT quantity, weight_unit, weight FROM purchases WHERE item_id = ?`, itemID)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var got float64
	for rows.Next() {
		var p Purchase
		if err := rows.Scan(&p.Quantity, &p.WeightUnit, &p.Weight); err != nil {
			return 0, err
		}
		got += counted(p, unit)
	}
	return got, rows.Err()
}

// DeletePurchase removes a purchase; an item that was bought goes back to "to buy" if it is short again.
func (db *DB) DeletePurchase(ctx context.Context, id int64, now time.Time) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		var item int64
		if err := tx.QueryRowContext(ctx, `SELECT item_id FROM purchases WHERE id = ?`, id).Scan(&item); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM purchases WHERE id = ?`, id); err != nil {
			return err
		}
		var need float64
		var unit, state string
		if err := tx.QueryRowContext(ctx, `SELECT quantity, unit, state FROM list_items WHERE id = ?`, item).Scan(&need, &unit, &state); err != nil {
			return err
		}
		got, err := purchasedIn(ctx, tx, item, unit)
		if err != nil {
			return err
		}
		if state == StateBought && need > 0 && got < need-1e-9 {
			_, err = tx.ExecContext(ctx, `UPDATE list_items SET state = 'need', changed_at = ? WHERE id = ?`, now.Unix(), item)
		}
		return err
	})
}

// PriceHint is the last price paid for an item.
type PriceHint struct {
	Brand      string
	PriceCents int64
	WeightUnit string
}

// PriceHints lists remembered prices for an item, newest first, preferring its own store.
func (db *DB) PriceHints(ctx context.Context, key string, storeID int64) ([]PriceHint, error) {
	rows, err := db.QueryContext(ctx, `SELECT brand, price_cents, weight_unit FROM item_prices WHERE key = ?
		ORDER BY store_id = ? DESC, updated_at DESC, rowid DESC LIMIT 6`, key, storeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PriceHint
	for rows.Next() {
		var h PriceHint
		if err := rows.Scan(&h.Brand, &h.PriceCents, &h.WeightUnit); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
