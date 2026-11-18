package storage

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"unicode"
)

// USDAFood is a USDA FoodData Central food.
type USDAFood struct {
	FdcID       int64
	Description string
	DataType    string
}

// USDAPortion is a household measure of a food: Amount of Label weighs Grams.
type USDAPortion struct {
	FdcID  int64
	Amount float64
	Label  string
	Grams  float64
	ML     float64 // volume of Amount of Label; 0 if it is not a volume
}

// USDANutrients are a food's macronutrients per 100 g.
type USDANutrients struct {
	FdcID                     int64
	Kcal, Protein, Carbs, Fat float64
}

// ReplaceUSDA swaps the stored USDA data for foods, portions and nutrients, in one transaction.
// Foods people linked ingredients to are lost with the old data, so their links go too.
func (db *DB) ReplaceUSDA(ctx context.Context, foods []USDAFood, portions []USDAPortion, nutrients []USDANutrients) error {
	return db.tx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{`DELETE FROM usda_nutrients`, `DELETE FROM usda_portions`, `DELETE FROM usda_foods_fts`, `DELETE FROM ingredient_foods WHERE source = 'usda'`, `DELETE FROM usda_foods`} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
		fs, err := tx.PrepareContext(ctx, `INSERT INTO usda_foods (fdc_id, description, data_type) VALUES (?, ?, ?)`)
		if err != nil {
			return err
		}
		defer func() { _ = fs.Close() }()
		ix, err := tx.PrepareContext(ctx, `INSERT INTO usda_foods_fts (rowid, description) VALUES (?, ?)`)
		if err != nil {
			return err
		}
		defer func() { _ = ix.Close() }()
		for _, f := range foods {
			if _, err := fs.ExecContext(ctx, f.FdcID, f.Description, f.DataType); err != nil {
				return err
			}
			if _, err := ix.ExecContext(ctx, f.FdcID, f.Description); err != nil {
				return err
			}
		}
		ps, err := tx.PrepareContext(ctx, `INSERT INTO usda_portions (fdc_id, amount, label, grams, ml) VALUES (?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer func() { _ = ps.Close() }()
		for _, p := range portions {
			var ml any
			if p.ML > 0 {
				ml = p.ML
			}
			if _, err := ps.ExecContext(ctx, p.FdcID, p.Amount, p.Label, p.Grams, ml); err != nil {
				return err
			}
		}
		ns, err := tx.PrepareContext(ctx, `INSERT INTO usda_nutrients (fdc_id, kcal, protein, carbs, fat) VALUES (?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer func() { _ = ns.Close() }()
		for _, n := range nutrients {
			if _, err := ns.ExecContext(ctx, n.FdcID, n.Kcal, n.Protein, n.Carbs, n.Fat); err != nil {
				return err
			}
		}
		return nil
	})
}

// USDAStatus counts what is stored.
type USDAStatus struct {
	Foods, Portions, Densities, Nutrients int
}

// USDAStatus reports how much USDA data is loaded.
func (db *DB) USDAStatus(ctx context.Context) (USDAStatus, error) {
	var s USDAStatus
	err := db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM usda_foods),
		(SELECT count(*) FROM usda_portions),
		(SELECT count(DISTINCT fdc_id) FROM usda_portions WHERE ml IS NOT NULL),
		(SELECT count(*) FROM usda_nutrients)`).Scan(&s.Foods, &s.Portions, &s.Densities, &s.Nutrients)
	return s, err
}

// Density is a USDA food with a weight per volume.
type Density struct {
	FdcID       int64
	Description string
	GPerMl      float64
}

// SearchDensities lists USDA foods that have a volume measure and whose name
// contains every word of query, shortest names first. It only finds
// candidates for a person to pick from: nothing here guesses which food an
// ingredient is.
func (db *DB) SearchDensities(ctx context.Context, query string, limit int) ([]Density, error) {
	words := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(words) == 0 {
		return nil, nil
	}
	if len(words) > 8 {
		words = words[:8]
	}
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = `"` + w + `"`
	}
	rows, err := db.QueryContext(ctx, `
		SELECT f.fdc_id, f.description, p.label, p.grams / p.ml
		FROM usda_foods_fts x
		JOIN usda_foods f ON f.fdc_id = x.rowid
		JOIN usda_portions p ON p.fdc_id = f.fdc_id AND p.ml IS NOT NULL
		WHERE usda_foods_fts MATCH ?
		ORDER BY f.fdc_id, p.id
		LIMIT 4000`, strings.Join(q, " "))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	type cand struct {
		d     Density
		plain bool
	}
	byFood := map[int64]*cand{}
	var order []int64
	for rows.Next() {
		var id int64
		var desc, label string
		var g float64
		if err := rows.Scan(&id, &desc, &label, &g); err != nil {
			return nil, err
		}
		// "cup" is a plainer measure than "cup, chopped" or "cup (1 NLEA serving)".
		plain := !strings.ContainsAny(label, ",(") && len(strings.Fields(label)) <= 2
		c, seen := byFood[id]
		switch {
		case !seen:
			byFood[id] = &cand{Density{id, desc, g}, plain}
			order = append(order, id)
		case plain && !c.plain:
			c.d, c.plain = Density{id, desc, g}, true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Density, 0, len(order))
	for _, id := range order {
		out = append(out, byFood[id].d)
	}
	sort.SliceStable(out, func(i, j int) bool {
		wi, wj := len(strings.Fields(out[i].Description)), len(strings.Fields(out[j].Description))
		if wi != wj {
			return wi < wj
		}
		return out[i].Description < out[j].Description
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
