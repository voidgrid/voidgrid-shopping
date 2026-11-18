package storage

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Macros are calories (kcal) and grams of protein, carbohydrate and fat.
type Macros struct {
	Kcal, Protein, Carbs, Fat float64
}

func (m Macros) valid() bool {
	for _, v := range []float64{m.Kcal, m.Protein, m.Carbs, m.Fat} {
		if v < 0 || v > 100000 {
			return false
		}
	}
	return true
}

// Food sources.
const (
	SourceUSDA   = "usda"
	SourceOFF    = "off"
	SourceManual = "manual"
)

// IngredientLink says which food an ingredient (by list key) takes its nutrition from.
type IngredientLink struct {
	Key      string
	Source   string
	FdcID    int64
	Barcode  string
	RecordID int64
	UnitG    float64 // grams of one "each" of this ingredient; 0 means use the food's own
}

// Food is the nutrition of a linked food, ready for calculation.
type Food struct {
	Source      string
	Name        string
	Per100      Macros // per 100 g
	GPerMl      float64
	UnitG       float64
	Portions    []USDAPortion
	Attribution string
}

var barcodeRE = regexp.MustCompile(`^[0-9]{8,14}$`)

// ValidBarcode reports whether s looks like an EAN or UPC code.
func ValidBarcode(s string) bool { return barcodeRE.MatchString(s) }

// ---- USDA foods with nutrition

// USDAHit is a USDA food found by a search, with its nutrition per 100 g.
type USDAHit struct {
	FdcID       int64
	Description string
	Per100      Macros
}

// SearchUSDAFoods lists USDA foods with nutrition whose name has every word of
// query, shortest names first. It finds candidates only: a person picks one.
func (db *DB) SearchUSDAFoods(ctx context.Context, query string, limit int) ([]USDAHit, error) {
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
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := db.QueryContext(ctx, `
		SELECT f.fdc_id, f.description, n.kcal, n.protein, n.carbs, n.fat
		FROM usda_foods_fts x
		JOIN usda_foods f ON f.fdc_id = x.rowid
		JOIN usda_nutrients n ON n.fdc_id = f.fdc_id
		WHERE usda_foods_fts MATCH ?
		ORDER BY length(f.description), f.description
		LIMIT ?`, strings.Join(q, " "), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []USDAHit
	for rows.Next() {
		var h USDAHit
		if err := rows.Scan(&h.FdcID, &h.Description, &h.Per100.Kcal, &h.Per100.Protein, &h.Per100.Carbs, &h.Per100.Fat); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ---- manual food records

// FoodRecord is a food typed in by hand. Values are per 100 g.
type FoodRecord struct {
	ID     int64
	Name   string
	Per100 Macros
	UnitG  float64 // grams of one "each"
	GPerMl float64
}

func checkRecord(r *FoodRecord) error {
	name, err := cleanName(r.Name, 120)
	if err != nil || !r.Per100.valid() || r.UnitG < 0 || r.UnitG > 100000 || r.GPerMl < 0 || r.GPerMl > 30 {
		return ErrInvalid
	}
	r.Name = name
	return nil
}

// AddFoodRecord stores a hand-entered food.
func (db *DB) AddFoodRecord(ctx context.Context, r FoodRecord, now time.Time) (int64, error) {
	if err := checkRecord(&r); err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, `INSERT INTO food_records (name, kcal, protein, carbs, fat, unit_g, g_per_ml, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Name, r.Per100.Kcal, r.Per100.Protein, r.Per100.Carbs, r.Per100.Fat, r.UnitG, r.GPerMl, now.Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateFoodRecord edits a hand-entered food.
func (db *DB) UpdateFoodRecord(ctx context.Context, r FoodRecord) error {
	if err := checkRecord(&r); err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `UPDATE food_records SET name = ?, kcal = ?, protein = ?, carbs = ?, fat = ?, unit_g = ?, g_per_ml = ? WHERE id = ?`,
		r.Name, r.Per100.Kcal, r.Per100.Protein, r.Per100.Carbs, r.Per100.Fat, r.UnitG, r.GPerMl, r.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// FoodRecords lists the hand-entered foods by name.
func (db *DB) FoodRecords(ctx context.Context) ([]FoodRecord, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, name, kcal, protein, carbs, fat, unit_g, g_per_ml FROM food_records ORDER BY lower(name), id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []FoodRecord
	for rows.Next() {
		var r FoodRecord
		if err := rows.Scan(&r.ID, &r.Name, &r.Per100.Kcal, &r.Per100.Protein, &r.Per100.Carbs, &r.Per100.Fat, &r.UnitG, &r.GPerMl); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteFoodRecord removes a food and the ingredient links that used it.
func (db *DB) DeleteFoodRecord(ctx context.Context, id int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM food_records WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- Open Food Facts cache

// OFFProduct is an Open Food Facts product (data under the ODbL licence).
type OFFProduct struct {
	Barcode      string
	Name         string
	Brand        string
	Per100       Macros
	HasNutrition bool
	ServingG     float64
	PackageG     float64
	FetchedAt    int64
}

// OFFProduct returns a cached product.
func (db *DB) OFFProduct(ctx context.Context, barcode string) (OFFProduct, error) {
	var p OFFProduct
	var has int
	err := db.QueryRowContext(ctx, `SELECT barcode, name, brand, kcal, protein, carbs, fat, has_nutrition, serving_g, package_g, fetched_at
		FROM off_products WHERE barcode = ?`, barcode).Scan(&p.Barcode, &p.Name, &p.Brand, &p.Per100.Kcal, &p.Per100.Protein, &p.Per100.Carbs, &p.Per100.Fat,
		&has, &p.ServingG, &p.PackageG, &p.FetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return OFFProduct{}, ErrNotFound
	}
	p.HasNutrition = has != 0
	return p, err
}

// SaveOFFProduct caches a product by barcode.
func (db *DB) SaveOFFProduct(ctx context.Context, p OFFProduct, now time.Time) error {
	if !ValidBarcode(p.Barcode) || !p.Per100.valid() {
		return ErrInvalid
	}
	_, err := db.ExecContext(ctx, `INSERT INTO off_products (barcode, name, brand, kcal, protein, carbs, fat, has_nutrition, serving_g, package_g, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(barcode) DO UPDATE SET name = excluded.name, brand = excluded.brand, kcal = excluded.kcal, protein = excluded.protein,
			carbs = excluded.carbs, fat = excluded.fat, has_nutrition = excluded.has_nutrition, serving_g = excluded.serving_g,
			package_g = excluded.package_g, fetched_at = excluded.fetched_at`,
		p.Barcode, truncate(p.Name, 200), truncate(p.Brand, 200), p.Per100.Kcal, p.Per100.Protein, p.Per100.Carbs, p.Per100.Fat,
		boolInt(p.HasNutrition), p.ServingG, p.PackageG, now.Unix())
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// ---- links

// IngredientLinks returns every link by ingredient key.
func (db *DB) IngredientLinks(ctx context.Context) (map[string]IngredientLink, error) {
	rows, err := db.QueryContext(ctx, `SELECT key, source, coalesce(fdc_id, 0), coalesce(barcode, ''), coalesce(record_id, 0), unit_g FROM ingredient_foods`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]IngredientLink{}
	for rows.Next() {
		var l IngredientLink
		if err := rows.Scan(&l.Key, &l.Source, &l.FdcID, &l.Barcode, &l.RecordID, &l.UnitG); err != nil {
			return nil, err
		}
		out[l.Key] = l
	}
	return out, rows.Err()
}

// SetIngredientLink points an ingredient at a food.
func (db *DB) SetIngredientLink(ctx context.Context, l IngredientLink, now time.Time) error {
	l.Key = ItemKey(l.Key)
	if l.Key == "" || len(l.Key) > 200 || l.UnitG < 0 || l.UnitG > 100000 {
		return ErrInvalid
	}
	var fdc, rec, bar any
	switch l.Source {
	case SourceUSDA:
		if l.FdcID <= 0 {
			return ErrInvalid
		}
		fdc = l.FdcID
	case SourceManual:
		if l.RecordID <= 0 {
			return ErrInvalid
		}
		rec = l.RecordID
	case SourceOFF:
		if !ValidBarcode(l.Barcode) {
			return ErrInvalid
		}
		bar = l.Barcode
	default:
		return ErrInvalid
	}
	_, err := db.ExecContext(ctx, `INSERT INTO ingredient_foods (key, source, fdc_id, barcode, record_id, unit_g, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET source = excluded.source, fdc_id = excluded.fdc_id, barcode = excluded.barcode,
			record_id = excluded.record_id, unit_g = excluded.unit_g, updated_at = excluded.updated_at`,
		l.Key, l.Source, fdc, bar, rec, l.UnitG, now.Unix())
	if err != nil && strings.Contains(err.Error(), "FOREIGN KEY") {
		return ErrNotFound
	}
	return err
}

// DeleteIngredientLink forgets an ingredient's food.
func (db *DB) DeleteIngredientLink(ctx context.Context, key string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM ingredient_foods WHERE key = ?`, ItemKey(key))
	return err
}

// FoodFor loads the food a link points at.
func (db *DB) FoodFor(ctx context.Context, l IngredientLink) (Food, error) {
	f := Food{Source: l.Source, UnitG: l.UnitG}
	switch l.Source {
	case SourceManual:
		var r FoodRecord
		err := db.QueryRowContext(ctx, `SELECT id, name, kcal, protein, carbs, fat, unit_g, g_per_ml FROM food_records WHERE id = ?`, l.RecordID).
			Scan(&r.ID, &r.Name, &r.Per100.Kcal, &r.Per100.Protein, &r.Per100.Carbs, &r.Per100.Fat, &r.UnitG, &r.GPerMl)
		if errors.Is(err, sql.ErrNoRows) {
			return Food{}, ErrNotFound
		}
		if err != nil {
			return Food{}, err
		}
		f.Name, f.Per100, f.GPerMl = r.Name, r.Per100, r.GPerMl
		if f.UnitG == 0 {
			f.UnitG = r.UnitG
		}
	case SourceOFF:
		p, err := db.OFFProduct(ctx, l.Barcode)
		if err != nil {
			return Food{}, err
		}
		f.Name, f.Per100, f.Attribution = strings.TrimSpace(p.Brand+" "+p.Name), p.Per100, "Open Food Facts"
		if f.UnitG == 0 {
			f.UnitG = p.ServingG
		}
	case SourceUSDA:
		err := db.QueryRowContext(ctx, `SELECT f.description, n.kcal, n.protein, n.carbs, n.fat FROM usda_foods f JOIN usda_nutrients n ON n.fdc_id = f.fdc_id WHERE f.fdc_id = ?`,
			l.FdcID).Scan(&f.Name, &f.Per100.Kcal, &f.Per100.Protein, &f.Per100.Carbs, &f.Per100.Fat)
		if errors.Is(err, sql.ErrNoRows) {
			return Food{}, ErrNotFound
		}
		if err != nil {
			return Food{}, err
		}
		f.Attribution = "USDA FoodData Central"
		rows, err := db.QueryContext(ctx, `SELECT fdc_id, amount, label, grams, coalesce(ml, 0) FROM usda_portions WHERE fdc_id = ? ORDER BY id`, l.FdcID)
		if err != nil {
			return Food{}, err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var p USDAPortion
			if err := rows.Scan(&p.FdcID, &p.Amount, &p.Label, &p.Grams, &p.ML); err != nil {
				return Food{}, err
			}
			f.Portions = append(f.Portions, p)
		}
		if err := rows.Err(); err != nil {
			return Food{}, err
		}
	default:
		return Food{}, ErrInvalid
	}
	return f, nil
}

// ---- recipe nutrition set by hand

// RecipeNutrition returns the hand-entered nutrition per serving, if any.
func (db *DB) RecipeNutrition(ctx context.Context, recipeID int64) (Macros, bool, error) {
	var m Macros
	err := db.QueryRowContext(ctx, `SELECT kcal, protein, carbs, fat FROM recipe_nutrition WHERE recipe_id = ?`, recipeID).Scan(&m.Kcal, &m.Protein, &m.Carbs, &m.Fat)
	if errors.Is(err, sql.ErrNoRows) {
		return Macros{}, false, nil
	}
	return m, err == nil, err
}

// SetRecipeNutrition stores hand-entered nutrition per serving; nil removes it.
func (db *DB) SetRecipeNutrition(ctx context.Context, recipeID int64, m *Macros) error {
	if m == nil {
		_, err := db.ExecContext(ctx, `DELETE FROM recipe_nutrition WHERE recipe_id = ?`, recipeID)
		return err
	}
	if !m.valid() {
		return ErrInvalid
	}
	_, err := db.ExecContext(ctx, `INSERT INTO recipe_nutrition (recipe_id, kcal, protein, carbs, fat) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(recipe_id) DO UPDATE SET kcal = excluded.kcal, protein = excluded.protein, carbs = excluded.carbs, fat = excluded.fat`,
		recipeID, m.Kcal, m.Protein, m.Carbs, m.Fat)
	if err != nil && strings.Contains(err.Error(), "FOREIGN KEY") {
		return ErrNotFound
	}
	return err
}
