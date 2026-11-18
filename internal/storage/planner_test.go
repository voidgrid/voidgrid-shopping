package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func planDB(t *testing.T) (*DB, int64, int64) {
	t.Helper()
	db := recipeDB(t)
	ctx := context.Background()
	a, err := db.CreateRecipe(ctx, &Recipe{Title: "Soup", Servings: 4}, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.CreateRecipe(ctx, &Recipe{Title: "Stew", Servings: 6}, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	return db, a, b
}

func TestPlanEntriesAndOrder(t *testing.T) {
	db, soup, stew := planDB(t)
	ctx, now := context.Background(), time.Unix(100, 0)
	add := func(e PlanEntry) int64 {
		t.Helper()
		id, err := db.AddEntry(ctx, e, 0, now)
		if err != nil {
			t.Fatalf("%+v: %v", e, err)
		}
		return id
	}
	d1 := add(PlanEntry{Date: "2026-11-18", Slot: "dinner", Kind: KindRecipe, RecipeID: soup, Servings: 8})
	add(PlanEntry{Date: "2026-11-18", Slot: "dinner", Kind: KindRecipe, RecipeID: stew})
	add(PlanEntry{Date: "2026-11-18", Slot: "breakfast", Kind: KindLeftovers})
	add(PlanEntry{Date: "2026-11-19", Slot: "lunch", Kind: KindNote, Note: " eating out "})
	add(PlanEntry{Date: "2026-11-30", Slot: "lunch", Kind: KindLeftovers})

	got, err := db.PlanEntries(ctx, "2026-11-18", "2026-11-19")
	if err != nil || len(got) != 4 {
		t.Fatalf("entries %v %v", got, err)
	}
	if got[0].Slot != "breakfast" || got[0].Kind != KindLeftovers {
		t.Errorf("breakfast should come first: %+v", got[0])
	}
	if got[1].Title != "Soup" || got[1].Servings != 8 || got[1].RecipeServings != 4 || got[2].Title != "Stew" {
		t.Errorf("dinner order/titles: %+v %+v", got[1], got[2])
	}
	if got[3].Note != "eating out" {
		t.Errorf("note not trimmed: %q", got[3].Note)
	}

	// moving puts it at the end of the new slot; a drag keeps servings
	if err := db.MoveEntry(ctx, d1, "2026-11-19", "lunch", 8); err != nil {
		t.Fatal(err)
	}
	e, _ := db.PlanEntry(ctx, d1)
	if e.Date != "2026-11-19" || e.Slot != "lunch" || e.Position < 2 {
		t.Errorf("moved entry %+v", e)
	}
	if err := db.MoveEntry(ctx, d1, "2026-11-19", "lunch", 2); err != nil {
		t.Fatal(err)
	}
	if e, _ = db.PlanEntry(ctx, d1); e.Servings != 2 {
		t.Errorf("servings %d", e.Servings)
	}
	if err := db.DeleteEntry(ctx, d1); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteEntry(ctx, d1); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestPlanEntryValidation(t *testing.T) {
	db, soup, _ := planDB(t)
	ctx, now := context.Background(), time.Unix(100, 0)
	for name, e := range map[string]PlanEntry{
		"bad date":      {Date: "2026-13-01", Slot: "dinner", Kind: KindLeftovers},
		"text date":     {Date: "tomorrow", Slot: "dinner", Kind: KindLeftovers},
		"bad slot":      {Date: "2026-11-18", Slot: "brunch", Kind: KindLeftovers},
		"bad kind":      {Date: "2026-11-18", Slot: "dinner", Kind: "snack"},
		"recipe no id":  {Date: "2026-11-18", Slot: "dinner", Kind: KindRecipe},
		"empty note":    {Date: "2026-11-18", Slot: "dinner", Kind: KindNote, Note: "  "},
		"huge servings": {Date: "2026-11-18", Slot: "dinner", Kind: KindRecipe, RecipeID: soup, Servings: 5000},
		"long note":     {Date: "2026-11-18", Slot: "dinner", Kind: KindNote, Note: string(make([]byte, 501))},
	} {
		if _, err := db.AddEntry(ctx, e, 0, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := db.AddEntry(ctx, PlanEntry{Date: "2026-11-18", Slot: "dinner", Kind: KindRecipe, RecipeID: 9999}, 0, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing recipe: %v", err)
	}
	if err := db.MoveEntry(ctx, 1, "2026-02-30", "dinner", 0); !errors.Is(err, ErrInvalid) {
		t.Errorf("impossible date: %v", err)
	}
}

func TestDeletingARecipeRemovesItFromThePlan(t *testing.T) {
	db, soup, _ := planDB(t)
	ctx := context.Background()
	if _, err := db.AddEntry(ctx, PlanEntry{Date: "2026-11-18", Slot: "dinner", Kind: KindRecipe, RecipeID: soup}, 0, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DeleteRecipe(ctx, soup); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.PlanEntries(ctx, "2026-11-01", "2026-11-30"); len(got) != 0 {
		t.Errorf("entries left: %v", got)
	}
}

func TestDayNotes(t *testing.T) {
	db, _, _ := planDB(t)
	ctx, now := context.Background(), time.Unix(100, 0)
	id, err := db.AddDayNote(ctx, "2026-11-17", "Thaw the chicken", "2026-11-19", "dinner", 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddDayNote(ctx, "2026-11-18", "Market day", "", "", 0, now); err != nil {
		t.Fatal(err)
	}
	// a note shows for the week it was written in and for the week of the meal it is tied to
	got, _ := db.DayNotes(ctx, "2026-11-19", "2026-11-25")
	if len(got) != 1 || got[0].Text != "Thaw the chicken" || got[0].ForSlot != "dinner" {
		t.Errorf("notes tied to a later week: %v", got)
	}
	for name, args := range map[string][4]string{
		"one half": {"2026-11-17", "x", "2026-11-19", ""},
		"bad slot": {"2026-11-17", "x", "2026-11-19", "brunch"},
		"no text":  {"2026-11-17", " ", "", ""},
		"bad date": {"nope", "x", "", ""},
	} {
		if _, err := db.AddDayNote(ctx, args[0], args[1], args[2], args[3], 0, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := db.DeleteDayNote(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteDayNote(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}
