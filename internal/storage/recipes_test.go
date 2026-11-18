package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func recipeDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}

func seed(t *testing.T, db *DB) {
	t.Helper()
	now := time.Unix(1000, 0)
	for _, r := range []Recipe{
		{
			Title: "Beef Stew", Course: "Dinner", Cuisine: "American", MainIngredient: "Beef", TotalMin: 120, Rating: 4,
			Ingredients: "2 lbs beef\n1 onion\n2 clove garlic", Directions: "Simmer slowly.", Tags: []string{"Slow cooker", "Winter"},
		},
		{
			Title: "Garlic Chicken", Course: "Dinner", Cuisine: "Italian", MainIngredient: "Chicken", TotalMin: 30, Rating: 5,
			Ingredients: "4 chicken thighs\n6 clove garlic\n1 lemon", Directions: "Roast.", Tags: []string{"Quick"},
		},
		{Title: "Pancakes", Course: "Breakfast", TotalMin: 0, Ingredients: "1 cup flour\n1 egg\n1 cup milk", Directions: "Fry."},
	} {
		if _, err := db.CreateRecipe(context.Background(), &r, now); err != nil {
			t.Fatal(err)
		}
	}
}

func titles(cards []RecipeCard) []string {
	var out []string
	for _, c := range cards {
		out = append(out, c.Title)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRecipeFilters(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	seed(t, db)
	cases := map[string]struct {
		f    Filter
		want []string
	}{
		"everything":      {Filter{}, []string{"Beef Stew", "Garlic Chicken", "Pancakes"}},
		"search title":    {Filter{Q: "stew"}, []string{"Beef Stew"}},
		"search prefix":   {Filter{Q: "panc"}, []string{"Pancakes"}},
		"search stemmed":  {Filter{Q: "simmering"}, []string{"Beef Stew"}},
		"search two":      {Filter{Q: "garlic roast"}, []string{"Garlic Chicken"}},
		"search quotes":   {Filter{Q: `"; DROP TABLE recipes; --`}, nil},
		"course":          {Filter{Course: "breakfast"}, []string{"Pancakes"}},
		"cuisine":         {Filter{Cuisine: "Italian"}, []string{"Garlic Chicken"}},
		"main":            {Filter{Main: "Beef"}, []string{"Beef Stew"}},
		"tag":             {Filter{Tag: "quick"}, []string{"Garlic Chicken"}},
		"rating":          {Filter{MinRating: 5}, []string{"Garlic Chicken"}},
		"time":            {Filter{MaxTotal: 60}, []string{"Garlic Chicken"}},
		"any":             {Filter{Any: []string{"beef", "milk"}}, []string{"Beef Stew", "Pancakes"}},
		"all":             {Filter{All: []string{"garlic", "lemon"}}, []string{"Garlic Chicken"}},
		"without":         {Filter{Without: []string{"garlic"}}, []string{"Pancakes"}},
		"all and without": {Filter{All: []string{"garlic"}, Without: []string{"lemon"}}, []string{"Beef Stew"}},
		"rating order":    {Filter{Sort: "rating"}, []string{"Garlic Chicken", "Beef Stew", "Pancakes"}},
		"time order":      {Filter{Sort: "time"}, []string{"Garlic Chicken", "Beef Stew", "Pancakes"}},
		"newest limit":    {Filter{Sort: "newest", Limit: 1}, []string{"Pancakes"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := db.ListRecipes(ctx, c.f)
			if err != nil {
				t.Fatal(err)
			}
			if !eq(titles(got), c.want) {
				t.Errorf("got %v, want %v", titles(got), c.want)
			}
		})
	}
}

func TestRecipeEditKeepsSearchAndTagsInStep(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	seed(t, db)
	cards, _ := db.ListRecipes(ctx, Filter{Q: "stew"})
	r, err := db.Recipe(ctx, cards[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tags) != 2 {
		t.Fatalf("tags %v", r.Tags)
	}
	r.Title, r.Tags = "Chili", []string{"Spicy"}
	if err := db.UpdateRecipe(ctx, &r, time.Unix(2000, 0)); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.ListRecipes(ctx, Filter{Q: "stew"}); len(got) != 0 {
		t.Errorf("old title still found: %v", titles(got))
	}
	if got, _ := db.ListRecipes(ctx, Filter{Q: "chili"}); len(got) != 1 {
		t.Errorf("new title not found")
	}
	if got, _ := db.ListRecipes(ctx, Filter{Tag: "Winter"}); len(got) != 0 {
		t.Errorf("old tag still found")
	}
	if got, _ := db.ListRecipes(ctx, Filter{Tag: "Spicy"}); len(got) != 1 {
		t.Errorf("new tag not found")
	}
}

func TestRecipeRatingPhotoDelete(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	seed(t, db)
	cards, _ := db.ListRecipes(ctx, Filter{Q: "pancakes"})
	id := cards[0].ID
	if err := db.SetRating(ctx, id, 3); err != nil {
		t.Fatal(err)
	}
	if err := db.SetRating(ctx, id, 6); err == nil {
		t.Error("rating 6 accepted")
	}
	if err := db.SetRating(ctx, 9999, 3); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing recipe: %v", err)
	}
	if err := db.SetPhoto(ctx, id, "x.jpg"); err != nil {
		t.Fatal(err)
	}
	if r, _ := db.Recipe(ctx, id); r.Rating != 3 || r.Photo != "x.jpg" {
		t.Errorf("recipe %+v", r)
	}
	photo, err := db.DeleteRecipe(ctx, id)
	if err != nil || photo != "x.jpg" {
		t.Fatalf("delete: %q %v", photo, err)
	}
	if _, err := db.Recipe(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("recipe still there: %v", err)
	}
	if got, _ := db.ListRecipes(ctx, Filter{Q: "pancakes"}); len(got) != 0 {
		t.Error("deleted recipe still searchable")
	}
}

func TestPendingPhotos(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	id, _ := db.CreateRecipe(ctx, &Recipe{Title: "A", PhotoSrc: "https://images.plantoeat.com/x"}, time.Now())
	p, err := db.PendingPhotos(ctx)
	if err != nil || len(p) != 1 || p[0].ID != id {
		t.Fatalf("pending %v %v", p, err)
	}
	if err := db.SetPhoto(ctx, id, "f.jpg"); err != nil {
		t.Fatal(err)
	}
	if p, _ := db.PendingPhotos(ctx); len(p) != 0 {
		t.Errorf("still pending: %v", p)
	}
}

func TestHasRecipeAndFacets(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	seed(t, db)
	if ok, _ := db.HasRecipe(ctx, "beef stew", "", "2 lbs beef\n1 onion\n2 clove garlic"); !ok {
		t.Error("duplicate not seen (case-insensitive title)")
	}
	if ok, _ := db.HasRecipe(ctx, "Beef Stew", "https://x.example", "2 lbs beef\n1 onion\n2 clove garlic"); ok {
		t.Error("different address counted as duplicate")
	}
	if ok, _ := db.HasRecipe(ctx, "Beef Stew", "", "something else"); ok {
		t.Error("same title with different ingredients counted as duplicate")
	}
	f, err := db.RecipeFacets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !eq(f.Courses, []string{"Breakfast", "Dinner"}) || !eq(f.Tags, []string{"Quick", "Slow cooker", "Winter"}) {
		t.Errorf("facets %+v", f)
	}
}
