package storage

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

var t0 = time.Unix(1000, 0)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func items(t *testing.T, db *DB) map[string]ListItem {
	t.Helper()
	all, err := db.ListItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]ListItem{}
	for _, it := range all {
		m[it.Key] = it
	}
	return m
}

func TestMergeIsExact(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	add := func(n Need) bool {
		t.Helper()
		_, merged, err := db.AddListItem(ctx, n, t0)
		if err != nil {
			t.Fatal(err)
		}
		return merged
	}
	if add(Need{Name: "Flour", Quantity: 1, Unit: "cup"}) {
		t.Fatal("first add reported a merge")
	}
	if !add(Need{Name: "flour ", Quantity: 2, Unit: "tbsp"}) { // same name, same kind of unit: converted and summed
		t.Error("cup + tbsp did not merge")
	}
	if it := items(t, db)["flour"]; it.Unit != "cup" || !near(it.Quantity, 1.125) {
		t.Errorf("flour %v %s", it.Quantity, it.Unit)
	}
	if add(Need{Name: "flour", Quantity: 1, Unit: "lb"}) { // volume and weight never merge without a density
		t.Error("cup + lb merged")
	}
	if add(Need{Name: "eggs", Quantity: 2}); !add(Need{Name: "Eggs", Quantity: 3}) {
		t.Error("counts did not merge")
	}
	if it := items(t, db)["eggs"]; !near(it.Quantity, 5) {
		t.Errorf("eggs %v", it.Quantity)
	}
	if add(Need{Name: "egg", Quantity: 1}) { // exact only: no plural guessing
		t.Error("egg merged with eggs")
	}
	add(Need{Name: "tomato sauce", Quantity: 2, Unit: "can", Pack: "15 oz"})
	if add(Need{Name: "tomato sauce", Quantity: 1, Unit: "can", Pack: "8 oz"}) {
		t.Error("different pack sizes merged")
	}
	add(Need{Name: "salt"})
	if !add(Need{Name: "Salt"}) || !add(Need{Name: "salt", Quantity: 2, Unit: "tsp"}) {
		t.Error("salt should appear once")
	}
	if err := func() error { _, _, e := db.AddListItem(ctx, Need{Name: "  "}, t0); return e }(); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty name: %v", err)
	}
}

func TestStaplesAppearOnce(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	store, _ := db.AddStore(ctx, "HEB")
	milk, _ := db.AddStaple(ctx, Staple{Name: "Milk", Quantity: 2, Unit: "gallon", StoreID: store}, t0)
	_, _ = db.AddStaple(ctx, Staple{Name: "Toothpaste", Note: "Buy when less than 2"}, t0)
	if _, err := db.AddStaple(ctx, Staple{Name: "milk"}, t0); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate staple: %v", err)
	}
	// a recipe needs milk before staples are copied: the staple joins that line instead of a second one
	_, _, _ = db.AddListItem(ctx, Need{Name: "milk", Quantity: 1, Unit: "cup"}, t0)
	added, already, err := db.CopyStaples(ctx, nil, t0)
	if err != nil || added != 1 || already != 1 {
		t.Fatalf("copy: added %d already %d %v", added, already, err)
	}
	if it := items(t, db)["milk"]; !near(it.Quantity, 1) || it.Unit != "cup" {
		t.Errorf("milk changed: %+v", it)
	}
	if it := items(t, db)["toothpaste"]; it.Note != "Buy when less than 2" {
		t.Errorf("staple note lost: %+v", it)
	}
	keys, _ := db.StapleKeys(ctx)
	if !keys["milk"] || keys["bread"] {
		t.Errorf("staple keys %v", keys)
	}
	_ = milk

	// the other way round: staples first, then a recipe needing the same thing
	db2 := recipeDB(t)
	_, _ = db2.AddStaple(ctx, Staple{Name: "Milk", Quantity: 2, Unit: "gallon"}, t0)
	_, _, _ = db2.CopyStaples(ctx, nil, t0)
	if _, merged, _ := db2.AddListItem(ctx, Need{Name: "milk", Quantity: 1, Unit: "cup"}, t0); !merged {
		t.Error("a recipe added a second milk line")
	}
	if it := items(t, db2)["milk"]; !near(it.Quantity, 2) || it.Unit != "gallon" {
		t.Errorf("staple amount was changed by a recipe: %+v", it)
	}
}

func TestEditingAStapleItemLeavesTheStaple(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	_, _ = db.AddStaple(ctx, Staple{Name: "Milk", Quantity: 2, Unit: "gallon"}, t0)
	_, _, _ = db.CopyStaples(ctx, nil, t0)
	it := items(t, db)["milk"]
	if err := db.UpdateListItem(ctx, it.ID, ItemEdit{Name: "Milk", Quantity: 1, Unit: "gallon"}, t0); err != nil {
		t.Fatal(err)
	}
	if got := items(t, db)["milk"]; !near(got.Quantity, 1) {
		t.Errorf("list item %v", got.Quantity)
	}
	st, _ := db.Staples(ctx)
	if len(st) != 1 || !near(st[0].Quantity, 2) {
		t.Errorf("the staple changed: %+v", st)
	}
}

func TestAislesAreLearned(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	heb, _ := db.AddStore(ctx, "HEB")
	aldi, _ := db.AddStore(ctx, "Aldi")
	id, _, _ := db.AddListItem(ctx, Need{Name: "Flour", Quantity: 1, Unit: "cup"}, t0)
	if err := db.UpdateListItem(ctx, id, ItemEdit{Name: "Flour", Quantity: 1, Unit: "cup", StoreID: heb, NewAisle: "Baking"}, t0); err != nil {
		t.Fatal(err)
	}
	it := items(t, db)["flour"]
	if it.StoreName != "HEB" || it.AisleName != "Baking" {
		t.Fatalf("assigned %+v", it)
	}
	// the next time flour comes up it lands in HEB's baking aisle by itself
	_, _ = db.ClearList(ctx, "all")
	_, _, _ = db.AddListItem(ctx, Need{Name: "flour", Quantity: 2, Unit: "cup"}, t0)
	if it := items(t, db)["flour"]; it.StoreName != "HEB" || it.AisleName != "Baking" {
		t.Errorf("not learned: %+v", it)
	}
	// aisles are per store: another store knows nothing until flour is assigned there
	id2, _, _ := db.AddListItem(ctx, Need{Name: "sugar", Quantity: 1, Unit: "cup", StoreID: aldi}, t0)
	if it, _ := db.ListItem(ctx, id2); it.AisleName != "" || it.StoreName != "Aldi" {
		t.Errorf("sugar at Aldi: %+v", it)
	}
	// an aisle chosen by id sets the store
	stores, _ := db.Stores(ctx)
	var bakingID int64
	for _, s := range stores {
		for _, a := range s.Aisles {
			bakingID = a.ID
		}
	}
	if err := db.UpdateListItem(ctx, id2, ItemEdit{Name: "sugar", Quantity: 1, Unit: "cup", AisleID: bakingID}, t0); err != nil {
		t.Fatal(err)
	}
	if it, _ := db.ListItem(ctx, id2); it.StoreName != "HEB" {
		t.Errorf("aisle did not set the store: %+v", it)
	}
	if err := db.UpdateListItem(ctx, id2, ItemEdit{Name: "x", NewAisle: "Orphan"}, t0); !errors.Is(err, ErrInvalid) {
		t.Errorf("new aisle with no store: %v", err)
	}
}

func TestPurchasesAndTotals(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	store, _ := db.AddStore(ctx, "HEB")
	id, _, _ := db.AddListItem(ctx, Need{Name: "Sliced cheese", Quantity: 3, Unit: "package", StoreID: store}, t0)

	if err := db.AddPurchase(ctx, id, Purchase{Quantity: 1, PriceCents: 349, Brand: "Brand A"}, t0); err != nil {
		t.Fatal(err)
	}
	it, _ := db.ListItem(ctx, id)
	if it.State != StateNeed || !near(it.Remaining(), 2) || it.Spent() != 349 {
		t.Errorf("after 1 of 3: %+v", it)
	}
	if err := db.AddPurchase(ctx, id, Purchase{Quantity: 2, PriceCents: 299, Brand: "Brand B"}, t0); err != nil {
		t.Fatal(err)
	}
	it, _ = db.ListItem(ctx, id)
	if it.State != StateBought || it.Spent() != 349+2*299 || len(it.Purchases) != 2 {
		t.Errorf("after 3 of 3: %+v", it)
	}
	if spent, _ := db.Spent(ctx); spent != 947 {
		t.Errorf("spent %d", spent)
	}

	// by weight: $2.49 a pound, 1.35 lb
	pid, _, _ := db.AddListItem(ctx, Need{Name: "bananas", Quantity: 2, Unit: "lb"}, t0)
	if err := db.AddPurchase(ctx, pid, Purchase{Quantity: 1, PriceCents: 249, WeightUnit: "lb", Weight: 1.35}, t0); err != nil {
		t.Fatal(err)
	}
	b, _ := db.ListItem(ctx, pid)
	if b.Purchases[0].TotalCents != 336 {
		t.Errorf("weight price total %d, want 336", b.Purchases[0].TotalCents)
	}
	if spent, _ := db.Spent(ctx); spent != 947+336 {
		t.Errorf("spent %d", spent)
	}

	// remembered prices, own store first
	hints, _ := db.PriceHints(ctx, "sliced cheese", store)
	if len(hints) != 2 {
		t.Fatalf("hints %v", hints)
	}
	// a purchase removed puts a short item back to need
	if err := db.DeletePurchase(ctx, it.Purchases[1].ID, t0); err != nil {
		t.Fatal(err)
	}
	if it, _ = db.ListItem(ctx, id); it.State != StateNeed || !near(it.Remaining(), 2) {
		t.Errorf("after undoing a purchase: %+v", it)
	}
	// moving back undoes all purchases
	if err := db.SetItemState(ctx, id, StateNeed, t0); err != nil {
		t.Fatal(err)
	}
	if it, _ = db.ListItem(ctx, id); len(it.Purchases) != 0 {
		t.Errorf("purchases kept: %+v", it)
	}
	if spent, _ := db.Spent(ctx); spent != 336 {
		t.Errorf("spent after undo %d", spent)
	}
}

func TestPurchaseValidation(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	id, _, _ := db.AddListItem(ctx, Need{Name: "x", Quantity: 1}, t0)
	for name, p := range map[string]Purchase{
		"zero quantity":   {Quantity: 0, PriceCents: 100},
		"negative price":  {Quantity: 1, PriceCents: -1},
		"weight no unit":  {Quantity: 1, PriceCents: 100, Weight: 2},
		"unit no weight":  {Quantity: 1, PriceCents: 100, WeightUnit: "lb"},
		"bad weight unit": {Quantity: 1, PriceCents: 100, WeightUnit: "stone", Weight: 1},
		"giant price":     {Quantity: 1, PriceCents: 1 << 40},
	} {
		if err := db.AddPurchase(ctx, id, p, t0); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := db.AddPurchase(ctx, 999, Purchase{Quantity: 1, PriceCents: 1}, t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing item: %v", err)
	}
	// an item with no set amount is bought by any purchase
	if err := db.AddPurchase(ctx, id, Purchase{Quantity: 1, PriceCents: 100}, t0); err != nil {
		t.Fatal(err)
	}
}

func TestHaveBoughtAndClear(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	a, _, _ := db.AddListItem(ctx, Need{Name: "a"}, t0)
	b, _, _ := db.AddListItem(ctx, Need{Name: "b"}, t0)
	c, _, _ := db.AddListItem(ctx, Need{Name: "c"}, t0)
	_ = db.SetItemState(ctx, a, StateHave, t0)
	_ = db.SetItemState(ctx, b, StateBought, t0)
	if err := db.SetItemState(ctx, c, "lost", t0); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad state: %v", err)
	}
	_ = db.SetItemState(ctx, a, StateNeed, t0) // unchecking moves it back
	if items(t, db)["a"].State != StateNeed {
		t.Error("item did not move back")
	}
	if n, _ := db.ClearList(ctx, "bought"); n != 1 {
		t.Errorf("cleared %d bought", n)
	}
	if _, err := db.ClearList(ctx, "everything"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad clear: %v", err)
	}
}

func TestStoresAndAisles(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	s, err := db.AddStore(ctx, "HEB")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddStore(ctx, "heb"); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate store: %v", err)
	}
	produce, _ := db.AddAisle(ctx, s, "Produce", 0)
	_, _ = db.AddAisle(ctx, s, "Dairy", 5)
	bakery, _ := db.AddAisle(ctx, s, "Bakery", 2)
	if _, err := db.AddAisle(ctx, s, "produce", 0); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate aisle: %v", err)
	}
	if _, err := db.AddAisle(ctx, 999, "X", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("aisle in a missing store: %v", err)
	}
	st, _ := db.Stores(ctx)
	if len(st) != 1 || st[0].Aisles[0].Name != "Produce" || st[0].Aisles[1].Name != "Bakery" || st[0].Aisles[2].Name != "Dairy" {
		t.Errorf("walking order %+v", st[0].Aisles)
	}
	if err := db.UpdateAisle(ctx, bakery, "Bakery & bread", 1); err != nil {
		t.Fatal(err)
	}
	_ = produce
	if err := db.DeleteStore(ctx, s); err != nil {
		t.Fatal(err)
	}
	if st, _ := db.Stores(ctx); len(st) != 0 {
		t.Errorf("store left: %v", st)
	}
}

func TestAddNeedsRecordsMeals(t *testing.T) {
	db, rid, _ := planDB(t)
	ctx := context.Background()
	e, _ := db.AddEntry(ctx, PlanEntry{Date: "2026-11-18", Slot: "dinner", Kind: KindRecipe, RecipeID: rid}, 0, t0)
	added, merged, err := db.AddNeeds(ctx, []Need{{Name: "beans", Quantity: 1, Unit: "can"}, {Name: "beans", Quantity: 1, Unit: "can"}}, []int64{e}, t0)
	if err != nil || added != 1 || merged != 1 {
		t.Fatalf("added %d merged %d %v", added, merged, err)
	}
	if got, _ := db.AddedEntries(ctx); !got[e] {
		t.Error("meal not recorded as added")
	}
}
