package storage

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Recipe is one recipe. Ingredients and Directions are plain text, one line
// each; the ingredient parser (a later step) reads the lines.
type Recipe struct {
	ID             int64
	Title          string
	Course         string
	Cuisine        string
	MainIngredient string
	Description    string
	Source         string
	URL            string
	PrepMin        int
	CookMin        int
	TotalMin       int
	Servings       int
	YieldText      string
	Ingredients    string
	Directions     string
	Rating         int
	Photo          string // file name under the photo directory
	PhotoSrc       string // remote address still to be downloaded
	Tags           []string
	CreatedBy      int64
	CreatedAt      int64
	UpdatedAt      int64
}

// RecipeCard is what the recipe grid needs.
type RecipeCard struct {
	ID       int64
	Title    string
	Course   string
	Photo    string
	Rating   int
	TotalMin int
	Source   string
}

// Filter narrows the recipe list.
type Filter struct {
	Q         string
	Course    string
	Cuisine   string
	Main      string
	Tag       string
	MinRating int
	MaxTotal  int      // minutes; 0 means no limit
	Any       []string // at least one of these ingredient words
	All       []string // every one of these
	Without   []string // none of these
	Sort      string   // title, newest, rating, time, random
	Limit     int
}

const recipeColumns = `id, title, course, cuisine, main_ingredient, description, source, url, prep_min, cook_min,
	total_min, servings, yield_text, ingredients, directions, rating, photo, photo_src,
	coalesce(created_by, 0), created_at, updated_at`

func scanRecipe(row interface{ Scan(...any) error }) (Recipe, error) {
	var r Recipe
	err := row.Scan(&r.ID, &r.Title, &r.Course, &r.Cuisine, &r.MainIngredient, &r.Description, &r.Source, &r.URL,
		&r.PrepMin, &r.CookMin, &r.TotalMin, &r.Servings, &r.YieldText, &r.Ingredients, &r.Directions,
		&r.Rating, &r.Photo, &r.PhotoSrc, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Recipe{}, ErrNotFound
	}
	return r, err
}

// CleanTags trims, drops empties and duplicates (ignoring case), and sorts.
func CleanTags(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" || len(t) > 60 || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

func writeIndex(ctx context.Context, tx *sql.Tx, r *Recipe) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM recipe_tags WHERE recipe_id = ?`, r.ID); err != nil {
		return err
	}
	for _, t := range r.Tags {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO recipe_tags (recipe_id, tag) VALUES (?, ?)`, r.ID, t); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM recipes_fts WHERE rowid = ?`, r.ID); err != nil {
		return err
	}
	body := strings.Join([]string{r.Description, r.MainIngredient, r.Cuisine, r.Course, r.Ingredients, r.Directions}, "\n")
	_, err := tx.ExecContext(ctx, `INSERT INTO recipes_fts (rowid, title, body, tags) VALUES (?, ?, ?, ?)`,
		r.ID, r.Title, body, strings.Join(r.Tags, " "))
	return err
}

// CreateRecipe stores r (its ID is set) and returns the new ID.
func (db *DB) CreateRecipe(ctx context.Context, r *Recipe, now time.Time) (int64, error) {
	r.Tags = CleanTags(r.Tags)
	err := db.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO recipes
			(title, course, cuisine, main_ingredient, description, source, url, prep_min, cook_min, total_min,
			 servings, yield_text, ingredients, directions, rating, photo, photo_src, created_by, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.Title, r.Course, r.Cuisine, r.MainIngredient, r.Description, r.Source, r.URL, r.PrepMin, r.CookMin,
			r.TotalMin, r.Servings, r.YieldText, r.Ingredients, r.Directions, r.Rating, r.Photo, r.PhotoSrc,
			nullID(r.CreatedBy), now.Unix(), now.Unix())
		if err != nil {
			return err
		}
		if r.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		return writeIndex(ctx, tx, r)
	})
	return r.ID, err
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// UpdateRecipe replaces every editable field of r (matched by ID). The photo
// and rating are changed through their own methods.
func (db *DB) UpdateRecipe(ctx context.Context, r *Recipe, now time.Time) error {
	r.Tags = CleanTags(r.Tags)
	return db.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE recipes SET title=?, course=?, cuisine=?, main_ingredient=?,
			description=?, source=?, url=?, prep_min=?, cook_min=?, total_min=?, servings=?, yield_text=?,
			ingredients=?, directions=?, updated_at=? WHERE id=?`,
			r.Title, r.Course, r.Cuisine, r.MainIngredient, r.Description, r.Source, r.URL, r.PrepMin, r.CookMin,
			r.TotalMin, r.Servings, r.YieldText, r.Ingredients, r.Directions, now.Unix(), r.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return writeIndex(ctx, tx, r)
	})
}

// DeleteRecipe removes a recipe and returns its photo file name.
func (db *DB) DeleteRecipe(ctx context.Context, id int64) (string, error) {
	var photo string
	err := db.tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT photo FROM recipes WHERE id = ?`, id).Scan(&photo); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM recipes_fts WHERE rowid = ?`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM recipes WHERE id = ?`, id)
		return err
	})
	return photo, err
}

// Recipe returns one recipe with its tags.
func (db *DB) Recipe(ctx context.Context, id int64) (Recipe, error) {
	r, err := scanRecipe(db.QueryRowContext(ctx, `SELECT `+recipeColumns+` FROM recipes WHERE id = ?`, id))
	if err != nil {
		return Recipe{}, err
	}
	rows, err := db.QueryContext(ctx, `SELECT tag FROM recipe_tags WHERE recipe_id = ?`, id)
	if err != nil {
		return Recipe{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return Recipe{}, err
		}
		r.Tags = append(r.Tags, t)
	}
	r.Tags = CleanTags(r.Tags)
	return r, rows.Err()
}

// SetRating sets the one family rating (0 clears it).
func (db *DB) SetRating(ctx context.Context, id int64, rating int) error {
	if rating < 0 || rating > 5 {
		return errors.New("storage: rating must be 0 to 5")
	}
	res, err := db.ExecContext(ctx, `UPDATE recipes SET rating = ? WHERE id = ?`, rating, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPhoto records a stored photo file and clears the pending remote address.
func (db *DB) SetPhoto(ctx context.Context, id int64, name string) error {
	res, err := db.ExecContext(ctx, `UPDATE recipes SET photo = ?, photo_src = '' WHERE id = ?`, name, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PendingPhoto is a recipe whose photo still has to be downloaded.
type PendingPhoto struct {
	ID  int64
	Src string
}

// PendingPhotos lists recipes with a remote photo not yet stored.
func (db *DB) PendingPhotos(ctx context.Context) ([]PendingPhoto, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, photo_src FROM recipes WHERE photo_src <> '' AND photo = '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PendingPhoto
	for rows.Next() {
		var p PendingPhoto
		if err := rows.Scan(&p.ID, &p.Src); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GiveUpPhoto stops retrying a photo that can't be fetched.
func (db *DB) GiveUpPhoto(ctx context.Context, id int64) error {
	_, err := db.ExecContext(ctx, `UPDATE recipes SET photo_src = '' WHERE id = ? AND photo = ''`, id)
	return err
}

// HasRecipe reports whether a recipe with this title, address and ingredients
// exists. Two different recipes may share a title, so the title alone is not enough.
func (db *DB) HasRecipe(ctx context.Context, title, url, ingredients string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM recipes WHERE lower(title) = lower(?) AND url = ? AND ingredients = ?`,
		title, url, ingredients).Scan(&n)
	return n > 0, err
}

// ftsQuery turns free text into an FTS5 query: every word must match as a prefix.
func ftsQuery(q string) string {
	words := strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for i, w := range words {
		words[i] = `"` + w + `"*`
	}
	return strings.Join(words, " ")
}

// ListRecipes returns the cards matching f.
func (db *DB) ListRecipes(ctx context.Context, f Filter) ([]RecipeCard, error) {
	var where []string
	var args []any
	if fq := ftsQuery(f.Q); fq != "" {
		where = append(where, `id IN (SELECT rowid FROM recipes_fts WHERE recipes_fts MATCH ?)`)
		args = append(args, fq)
	}
	eq := func(col, v string) {
		if v != "" {
			where = append(where, col+` = ? COLLATE NOCASE`)
			args = append(args, v)
		}
	}
	eq("course", f.Course)
	eq("cuisine", f.Cuisine)
	eq("main_ingredient", f.Main)
	if f.Tag != "" {
		where = append(where, `id IN (SELECT recipe_id FROM recipe_tags WHERE tag = ?)`)
		args = append(args, f.Tag)
	}
	if f.MinRating > 0 {
		where = append(where, `rating >= ?`)
		args = append(args, f.MinRating)
	}
	if f.MaxTotal > 0 {
		where = append(where, `total_min > 0 AND total_min <= ?`)
		args = append(args, f.MaxTotal)
	}
	has := `instr(lower(ingredients), lower(?)) > 0`
	if len(f.Any) > 0 {
		var parts []string
		for _, w := range f.Any {
			parts = append(parts, has)
			args = append(args, w)
		}
		where = append(where, "("+strings.Join(parts, " OR ")+")")
	}
	for _, w := range f.All {
		where = append(where, has)
		args = append(args, w)
	}
	for _, w := range f.Without {
		where = append(where, `NOT `+has)
		args = append(args, w)
	}

	order := map[string]string{
		"":       `lower(title)`,
		"title":  `lower(title)`,
		"newest": `id DESC`,
		"rating": `rating DESC, lower(title)`,
		"time":   `(total_min = 0), total_min, lower(title)`,
		"random": `random()`,
	}[f.Sort]
	if order == "" {
		order = `lower(title)`
	}
	q := `SELECT id, title, course, photo, rating, total_min, source FROM recipes`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	q += ` ORDER BY ` + order
	if f.Limit > 0 {
		q += ` LIMIT ` + strconv.Itoa(f.Limit)
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RecipeCard
	for rows.Next() {
		var c RecipeCard
		if err := rows.Scan(&c.ID, &c.Title, &c.Course, &c.Photo, &c.Rating, &c.TotalMin, &c.Source); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Facets are the values the filter drop-downs offer.
type Facets struct {
	Courses, Cuisines, Mains, Tags []string
}

// RecipeFacets lists the distinct values in use.
func (db *DB) RecipeFacets(ctx context.Context) (Facets, error) {
	col := func(q string) ([]string, error) {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, rows.Err()
	}
	var f Facets
	var err error
	if f.Courses, err = col(`SELECT DISTINCT course FROM recipes WHERE course <> '' ORDER BY lower(course)`); err != nil {
		return f, err
	}
	if f.Cuisines, err = col(`SELECT DISTINCT cuisine FROM recipes WHERE cuisine <> '' ORDER BY lower(cuisine)`); err != nil {
		return f, err
	}
	if f.Mains, err = col(`SELECT DISTINCT main_ingredient FROM recipes WHERE main_ingredient <> '' ORDER BY lower(main_ingredient)`); err != nil {
		return f, err
	}
	f.Tags, err = col(`SELECT DISTINCT tag FROM recipe_tags ORDER BY lower(tag)`)
	return f, err
}

// SetPhotoSource queues a photo to be downloaded from src for the recipe.
func (db *DB) SetPhotoSource(ctx context.Context, id int64, src string) error {
	_, err := db.ExecContext(ctx, `UPDATE recipes SET photo_src = ?1 WHERE id = ?2`, src, id)
	return err
}
