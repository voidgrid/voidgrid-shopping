package storage

import (
	"context"
	"errors"
	"testing"
)

func TestSearchUSDAFoodsNeedsNutrition(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	foods := []USDAFood{{1, "Wheat flour, white, all-purpose", "sr_legacy_food"}, {2, "Wheat flour, whole-grain", "sr_legacy_food"}, {3, "Egg, whole, raw", "sr_legacy_food"}}
	nut := []USDANutrients{{FdcID: 1, Kcal: 364, Protein: 10.3, Carbs: 76.3, Fat: 1}, {FdcID: 3, Kcal: 143, Protein: 12.6, Carbs: 0.7, Fat: 9.5}}
	if err := db.ReplaceUSDA(ctx, foods, nil, nut); err != nil {
		t.Fatal(err)
	}
	got, err := db.SearchUSDAFoods(ctx, "flour", 10)
	if err != nil || len(got) != 1 || got[0].FdcID != 1 || got[0].Per100.Kcal != 364 {
		t.Fatalf("flour: %v %v", got, err)
	}
	if got, _ := db.SearchUSDAFoods(ctx, "marsala wine", 10); len(got) != 0 {
		t.Errorf("a search guessed: %v", got)
	}
	if got, _ := db.SearchUSDAFoods(ctx, `"; DROP TABLE usda_foods; --`, 10); len(got) != 0 {
		t.Errorf("odd query matched: %v", got)
	}
	if st, _ := db.USDAStatus(ctx); st.Nutrients != 2 {
		t.Errorf("status %+v", st)
	}
}

func TestLinksResolveToFoods(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	foods := []USDAFood{{1, "Egg, whole, raw", "sr_legacy_food"}}
	portions := []USDAPortion{{FdcID: 1, Amount: 1, Label: "large", Grams: 50}}
	if err := db.ReplaceUSDA(ctx, foods, portions, []USDANutrients{{FdcID: 1, Kcal: 143, Protein: 12.6, Carbs: 0.7, Fat: 9.5}}); err != nil {
		t.Fatal(err)
	}
	rec, err := db.AddFoodRecord(ctx, FoodRecord{Name: "My granola", Per100: Macros{Kcal: 450, Protein: 10, Carbs: 60, Fat: 18}, UnitG: 45}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveOFFProduct(ctx, OFFProduct{Barcode: "0016000275287", Name: "Cheerios", Brand: "General Mills", Per100: Macros{Kcal: 359, Protein: 12.8, Carbs: 39, Fat: 6}, HasNutrition: true, ServingG: 39}, t0); err != nil {
		t.Fatal(err)
	}
	for _, l := range []IngredientLink{
		{Key: " Eggs ", Source: SourceUSDA, FdcID: 1},
		{Key: "granola", Source: SourceManual, RecordID: rec, UnitG: 50},
		{Key: "cheerios", Source: SourceOFF, Barcode: "0016000275287"},
	} {
		if err := db.SetIngredientLink(ctx, l, t0); err != nil {
			t.Fatalf("%+v: %v", l, err)
		}
	}
	links, _ := db.IngredientLinks(ctx)
	if len(links) != 3 || links["eggs"].FdcID != 1 {
		t.Fatalf("links %+v", links)
	}
	egg, err := db.FoodFor(ctx, links["eggs"])
	if err != nil || egg.Per100.Kcal != 143 || len(egg.Portions) != 1 || egg.Attribution != "USDA FoodData Central" {
		t.Errorf("usda food %+v %v", egg, err)
	}
	gr, _ := db.FoodFor(ctx, links["granola"])
	if gr.UnitG != 50 || gr.Per100.Fat != 18 { // the link's own weight wins over the record's
		t.Errorf("manual food %+v", gr)
	}
	ch, _ := db.FoodFor(ctx, links["cheerios"])
	if ch.Name != "General Mills Cheerios" || ch.UnitG != 39 || ch.Attribution != "Open Food Facts" {
		t.Errorf("off food %+v", ch)
	}
	// re-linking replaces
	if err := db.SetIngredientLink(ctx, IngredientLink{Key: "eggs", Source: SourceManual, RecordID: rec}, t0); err != nil {
		t.Fatal(err)
	}
	if links, _ = db.IngredientLinks(ctx); links["eggs"].Source != SourceManual || links["eggs"].FdcID != 0 {
		t.Errorf("relink %+v", links["eggs"])
	}
	// a re-import of USDA data drops links into the old data; the others stay
	if err := db.SetIngredientLink(ctx, IngredientLink{Key: "egg whites", Source: SourceUSDA, FdcID: 1}, t0); err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceUSDA(ctx, foods, portions, nil); err != nil {
		t.Fatal(err)
	}
	links, _ = db.IngredientLinks(ctx)
	if _, ok := links["egg whites"]; ok || len(links) != 3 {
		t.Errorf("after re-import: %+v", links)
	}
	// deleting a record removes links to it
	if err := db.DeleteFoodRecord(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if links, _ = db.IngredientLinks(ctx); len(links) != 1 {
		t.Errorf("links to a deleted record remain: %+v", links)
	}
}

func TestLinkValidation(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	for name, l := range map[string]IngredientLink{
		"empty key":    {Key: " ", Source: SourceManual, RecordID: 1},
		"bad source":   {Key: "x", Source: "guess"},
		"usda no id":   {Key: "x", Source: SourceUSDA},
		"off bad code": {Key: "x", Source: SourceOFF, Barcode: "abc"},
		"neg weight":   {Key: "x", Source: SourceManual, RecordID: 1, UnitG: -1},
	} {
		if err := db.SetIngredientLink(ctx, l, t0); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := db.SetIngredientLink(ctx, IngredientLink{Key: "x", Source: SourceManual, RecordID: 999}, t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing record: %v", err)
	}
	if _, err := db.AddFoodRecord(ctx, FoodRecord{Name: "", Per100: Macros{Kcal: 1}}, t0); !errors.Is(err, ErrInvalid) {
		t.Errorf("nameless food: %v", err)
	}
	if _, err := db.AddFoodRecord(ctx, FoodRecord{Name: "x", Per100: Macros{Kcal: -5}}, t0); !errors.Is(err, ErrInvalid) {
		t.Errorf("negative kcal: %v", err)
	}
}

func TestRecipeNutritionOverride(t *testing.T) {
	db, id, _ := planDB(t)
	ctx := context.Background()
	if _, ok, _ := db.RecipeNutrition(ctx, id); ok {
		t.Error("override present from the start")
	}
	if err := db.SetRecipeNutrition(ctx, id, &Macros{Kcal: 500, Protein: 30, Carbs: 40, Fat: 20}); err != nil {
		t.Fatal(err)
	}
	m, ok, _ := db.RecipeNutrition(ctx, id)
	if !ok || m.Kcal != 500 {
		t.Errorf("override %+v %v", m, ok)
	}
	if err := db.SetRecipeNutrition(ctx, id, &Macros{Kcal: -1}); !errors.Is(err, ErrInvalid) {
		t.Errorf("negative: %v", err)
	}
	if err := db.SetRecipeNutrition(ctx, 9999, &Macros{Kcal: 1}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing recipe: %v", err)
	}
	if err := db.SetRecipeNutrition(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.RecipeNutrition(ctx, id); ok {
		t.Error("override not removed")
	}
}
