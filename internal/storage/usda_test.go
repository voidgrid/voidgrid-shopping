package storage

import (
	"context"
	"math"
	"testing"
)

func usdaDB(t *testing.T) *DB {
	t.Helper()
	db := recipeDB(t)
	foods := []USDAFood{
		{1, "Wheat flour, white, all-purpose, enriched, bleached", "sr_legacy_food"},
		{2, "Sugars, granulated", "sr_legacy_food"},
		{3, "Sugars, brown", "sr_legacy_food"},
		{4, "Cereals ready-to-eat, QUAKER, Maple Brown Sugar LIFE", "sr_legacy_food"},
		{5, "Butter, salted", "sr_legacy_food"},
		{6, "Onions, raw", "sr_legacy_food"},
		{7, "Cheese, cheddar", "sr_legacy_food"},
		{8, "Cheese, american cheddar, imitation", "sr_legacy_food"},
		{9, "Egg, whole, raw, fresh", "sr_legacy_food"},
		{10, "Vinegar, red wine", "sr_legacy_food"},
		{11, "Mustard greens, raw", "sr_legacy_food"},
		{12, "Soup, tomato, canned, prepared with equal volume lowfat milk", "sr_legacy_food"},
		{13, "Spices, cinnamon, ground", "sr_legacy_food"},
	}
	cup := 236.5882365
	portions := []USDAPortion{
		{1, 1, "cup", 125, cup},
		{2, 1, "cup", 200, cup},
		{3, 1, "cup", 213, cup},
		{4, 1, "cup", 43, cup},
		{5, 1, "cup", 227, cup},
		{6, 1, "cup, chopped", 160, cup},
		{6, 1, "cup", 150, cup},
		{7, 1, "cup, shredded", 113, cup},
		{8, 1, "cup", 224, cup},
		{9, 1, "large", 50, 0},
		{10, 1, "cup", 238, cup},
		{11, 1, "cup", 56, cup},
		{12, 1, "cup", 252, cup},
		{13, 1, "cup", 125, cup},
	}
	if err := db.ReplaceUSDA(context.Background(), foods, portions, nil); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestUSDAStatusAndReplace(t *testing.T) {
	db, ctx := usdaDB(t), context.Background()
	st, err := db.USDAStatus(ctx)
	if err != nil || st.Foods != 13 || st.Portions != 14 || st.Densities != 12 {
		t.Fatalf("status %+v %v", st, err)
	}
	if err := db.ReplaceUSDA(ctx, []USDAFood{{1, "Salt, table", "sr_legacy_food"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if st, _ := db.USDAStatus(ctx); st.Foods != 1 || st.Portions != 0 {
		t.Errorf("replace left old data: %+v", st)
	}
	if got, _ := db.SearchDensities(ctx, "flour", 5); len(got) != 0 {
		t.Error("old foods still searchable after replace")
	}
}

func TestSearchDensities(t *testing.T) {
	db, ctx := usdaDB(t), context.Background()
	cup := 236.5882365
	got, err := db.SearchDensities(ctx, "brown sugar", 10)
	if err != nil || len(got) != 2 || got[0].Description != "Sugars, brown" {
		t.Fatalf("brown sugar: %v %v", got, err)
	}
	if g := got[0].GPerMl * cup; math.Abs(g-213) > 0.01 {
		t.Errorf("brown sugar %.1f g/cup, want 213", g)
	}
	// plainest measure wins: "cup" (150) over "cup, chopped" (160)
	if o, _ := db.SearchDensities(ctx, "onions", 5); len(o) != 1 || math.Abs(o[0].GPerMl*cup-150) > 0.01 {
		t.Errorf("onions: %v", o)
	}
	if one, _ := db.SearchDensities(ctx, "cheddar", 1); len(one) != 1 || one[0].Description != "Cheese, cheddar" {
		t.Errorf("limit/order: %v", one)
	}
}

func TestSearchDensitiesGuessesNothing(t *testing.T) {
	db, ctx := usdaDB(t), context.Background()
	for _, q := range []string{"", "   ", "xyzzy", "marsala wine", "egg", `"; DROP TABLE usda_foods; --`} {
		if got, err := db.SearchDensities(ctx, q, 10); err != nil || len(got) != 0 {
			t.Errorf("%q matched %v (err %v)", q, got, err)
		}
	}
}
