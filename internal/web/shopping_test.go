package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func (e *env) list(c *http.Cookie, query string) string {
	e.t.Helper()
	rec := e.do("GET", "/list"+query, nil, c)
	if rec.Code != 200 {
		e.t.Fatalf("GET /list: %d", rec.Code)
	}
	return rec.Body.String()
}

func TestGenerateListFromThePlan(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	a := recipeIDFrom(e.newRecipe(c, "Pancakes", "For the batter\n1 1/2 cups flour\n2 eggs\n1 tbsp sugar\nsalt, (to taste)"))
	b := recipeIDFrom(e.newRecipe(c, "Waffles", "2 cups flour\n3 eggs\n1 (15 oz) can pumpkin\nsalt"))
	e.post("/planner/entries", url.Values{"date": {"2026-11-17"}, "slot": {"breakfast"}, "recipe_id": {a}, "servings": {"8"}}, c) // recipe serves 4: double
	e.post("/planner/entries", url.Values{"date": {"2026-11-18"}, "slot": {"breakfast"}, "recipe_id": {b}}, c)

	form := url.Values{"from": {"2026-11-15"}, "to": {"2026-11-21"}}
	if code := e.post("/list/generate", form, c); code != 303 {
		t.Fatalf("generate: %d", code)
	}
	items, _ := e.db.ListItems(t.Context())
	got := map[string]string{}
	for _, it := range items {
		got[it.Key] = amountText(it.Quantity, it.Unit, it.Pack)
	}
	want := map[string]string{
		"flour":   "5 cups", // 1½ cups doubled (3) + 2 cups
		"eggs":    "7",      // 2 doubled (4) + 3
		"sugar":   "2 tbsp",
		"salt":    "", // once, however many recipes
		"pumpkin": "1 can (15 oz each)",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["for the batter"]; ok || len(items) != len(want) {
		t.Errorf("items: %v", got)
	}

	// adding the same days again adds nothing
	e.post("/list/generate", form, c)
	again, _ := e.db.ListItems(t.Context())
	for _, it := range again {
		if it.Key == "flour" && it.Quantity != 5 {
			t.Errorf("flour doubled up: %v", it.Quantity)
		}
	}
	if len(again) != len(items) {
		t.Errorf("%d items after the second run, %d before", len(again), len(items))
	}
	for _, bad := range []url.Values{
		{"from": {"2026-11-21"}, "to": {"2026-11-15"}}, {"from": {"x"}, "to": {"y"}}, {"from": {"2026-01-01"}, "to": {"2026-12-31"}},
	} {
		if code := e.post("/list/generate", bad, c); code != http.StatusBadRequest {
			t.Errorf("bad range %v: %d", bad, code)
		}
	}
}

func TestListByAisleAndStates(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	e.post("/stores", url.Values{"name": {"HEB"}}, c)
	stores, _ := e.db.Stores(t.Context())
	sid := itoa(stores[0].ID)
	e.post("/stores/"+sid+"/aisles", url.Values{"name": {"Produce"}, "position": {"1"}}, c)
	e.post("/stores/"+sid+"/aisles", url.Values{"name": {"Dairy"}, "position": {"7"}}, c)

	e.post("/list/items", url.Values{"name": {"milk"}, "quantity": {"1"}, "unit": {"gallon"}, "store_id": {sid}}, c)
	e.post("/list/items", url.Values{"name": {"apples"}, "quantity": {"6"}, "store_id": {sid}}, c)
	e.post("/list/items", url.Values{"name": {"mystery"}}, c)
	items, _ := e.db.ListItems(t.Context())
	var milk, apples int64
	for _, it := range items {
		switch it.Key {
		case "milk":
			milk = it.ID
		case "apples":
			apples = it.ID
		}
	}
	// assign aisles by editing (learned from here on)
	e.post("/list/items/"+itoa(milk), url.Values{"name": {"milk"}, "quantity": {"1"}, "unit": {"gallon"}, "store_id": {sid}, "new_aisle": {"Dairy"}}, c)
	e.post("/list/items/"+itoa(apples), url.Values{"name": {"apples"}, "quantity": {"6"}, "store_id": {sid}, "new_aisle": {"Produce"}}, c)

	body := e.list(c, "")
	iProduce, iDairy, iNoStore := strings.Index(body, "Produce"), strings.Index(body, "Dairy"), strings.Index(body, "No store yet")
	if iProduce <= 0 || iProduce >= iDairy || iDairy >= iNoStore {
		t.Errorf("walking order wrong: produce %d dairy %d nostore %d", iProduce, iDairy, iNoStore)
	}
	if !strings.Contains(e.list(c, "?store="+sid), "milk") || strings.Contains(e.list(c, "?store="+sid), "mystery") {
		t.Error("store filter")
	}

	e.post("/list/items/"+itoa(apples)+"/state", url.Values{"state": {"have"}}, c)
	body = e.list(c, "")
	if !strings.Contains(body, "Already have") {
		t.Error("have section missing")
	}
	e.post("/list/items/"+itoa(apples)+"/state", url.Values{"state": {"need"}}, c)
	if strings.Contains(e.list(c, ""), "Already have") {
		t.Error("item did not move back")
	}
	if code := e.post("/list/items/"+itoa(apples)+"/state", url.Values{"state": {"lost"}}, c); code != http.StatusBadRequest {
		t.Errorf("bad state: %d", code)
	}
}

func TestEditingListItemQuantityForAStaple(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	e.post("/staples", url.Values{"name": {"Milk"}, "quantity": {"2"}, "unit": {"gallons"}, "note": {"Buy when less than 1"}}, c)
	if code := e.post("/staples/copy", url.Values{"all": {"1"}}, c); code != 303 {
		t.Fatalf("copy: %d", code)
	}
	body := e.list(c, "")
	if !strings.Contains(body, "2 gallons") || !strings.Contains(body, "Staple: check there is enough") || !strings.Contains(body, "Buy when less than 1") {
		t.Fatalf("staple on the list: %s", body)
	}
	items, _ := e.db.ListItems(t.Context())
	e.post("/list/items/"+itoa(items[0].ID), url.Values{"name": {"Milk"}, "quantity": {"1"}, "unit": {"gallon"}, "store_id": {"0"}}, c)
	body = e.list(c, "")
	if !strings.Contains(body, "1 gallon") || strings.Contains(body, "2 gallons") {
		t.Errorf("edit did not apply: %s", body)
	}
	if st := e.do("GET", "/staples", nil, c).Body.String(); !strings.Contains(st, "2 gallons") {
		t.Error("the staple itself changed")
	}
	// copying again does not add a second milk
	e.post("/staples/copy", url.Values{"all": {"1"}}, c)
	if n, _ := e.db.ListItems(t.Context()); len(n) != 1 {
		t.Errorf("%d items after copying twice", len(n))
	}
}

func TestPurchasesFromThePage(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	e.post("/list/items", url.Values{"name": {"Sliced cheese"}, "quantity": {"3"}, "unit": {"package"}}, c)
	e.post("/list/items", url.Values{"name": {"bananas"}, "quantity": {"2"}, "unit": {"lb"}}, c)
	items, _ := e.db.ListItems(t.Context())
	var cheese, bananas int64
	for _, it := range items {
		if it.Key == "sliced cheese" {
			cheese = it.ID
		} else {
			bananas = it.ID
		}
	}
	e.post("/list/items/"+itoa(cheese)+"/purchase", url.Values{"quantity": {"1"}, "price": {"$3.49"}, "brand": {"Brand A"}}, c)
	body := e.list(c, "")
	if !strings.Contains(body, "Spent so far <strong>$3.49</strong>") || !strings.Contains(body, "Brand A") {
		t.Errorf("after the first purchase: %s", body)
	}
	e.post("/list/items/"+itoa(cheese)+"/purchase", url.Values{"quantity": {"2"}, "price": {"2.99"}, "brand": {"Brand B"}}, c)
	e.post("/list/items/"+itoa(bananas)+"/purchase", url.Values{"quantity": {"1"}, "price": {"0.69"}, "per": {"lb"}, "weight": {"2.5"}}, c)
	body = e.list(c, "")
	if !strings.Contains(body, "Spent so far <strong>$11.20</strong>") { // 3.49 + 2*2.99 + 2.5*0.69 = 11.205 -> 349+598+173
		t.Errorf("total: %s", regexp.MustCompile(`Spent so far[^<]*<strong>[^<]*`).FindString(body))
	}
	if !strings.Contains(body, "Purchased") || strings.Contains(body, "Nothing left to buy") == false {
		t.Error("everything bought should be under Purchased with nothing left to buy")
	}
	// moving back removes the item's purchases from the total
	e.post("/list/items/"+itoa(cheese)+"/state", url.Values{"state": {"need"}}, c)
	if !strings.Contains(e.list(c, ""), "Spent so far <strong>$1.73</strong>") {
		t.Error("undo did not take the purchases out of the total")
	}
	for name, f := range map[string]url.Values{
		"no price":       {"quantity": {"1"}},
		"bad price":      {"quantity": {"1"}, "price": {"cheap"}},
		"weight missing": {"quantity": {"1"}, "price": {"1"}, "per": {"lb"}},
		"bad unit":       {"quantity": {"1"}, "price": {"1"}, "per": {"stone"}, "weight": {"2"}},
		"negative price": {"quantity": {"1"}, "price": {"-1"}},
	} {
		if code := e.post("/list/items/"+itoa(cheese)+"/purchase", f, c); code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, code)
		}
	}
	// the last price is offered next time
	if !strings.Contains(e.list(c, ""), `value="2.99"`) {
		t.Error("last price not prefilled")
	}
}

func TestStaplesImportAndNoStoreFromTheFile(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	csv := "Store,Category,Title,Quantity,Unit,Ingredient Notes,List Notes\nHEB,Dairy,Milk,2,gallons,,Buy when less than 1\nHEB,Misc,Reese&#39;s,,,,\n"
	if rec := e.multipart("/staples/import", nil, "file", "s.csv", []byte(csv), c); rec.Code != 303 {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	page := e.do("GET", "/staples", nil, c).Body.String()
	for _, want := range []string{"Milk", "2 gallons", "Buy when less than 1", "Reese&#39;s", "No store yet"} {
		if !strings.Contains(page, want) {
			t.Errorf("staples page missing %q", want)
		}
	}
	if st, _ := e.db.Stores(t.Context()); len(st) != 0 {
		t.Errorf("a store was created from the file: %v", st)
	}
	e.multipart("/staples/import", nil, "file", "s.csv", []byte(csv), c) // again: nothing new
	if all, _ := e.db.Staples(t.Context()); len(all) != 2 {
		t.Errorf("%d staples after a second import", len(all))
	}
	if code := e.post("/staples/copy", url.Values{}, c); code != 303 {
		t.Errorf("copy with nothing ticked: %d", code)
	}
	if n, _ := e.db.ListItems(t.Context()); len(n) != 0 {
		t.Error("copy with nothing ticked copied something")
	}
}

func TestShoppingPagesNeedSignIn(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/list", "/list/items/1/edit", "/stores", "/staples", "/staples/1/edit"} {
		if r := e.do("GET", p, nil, nil); r.Code != http.StatusSeeOther {
			t.Errorf("GET %s: %d", p, r.Code)
		}
	}
	for _, p := range []string{
		"/list/generate", "/list/items", "/list/items/1", "/list/items/1/delete", "/list/items/1/state", "/list/items/1/purchase",
		"/list/purchases/1/delete", "/list/clear", "/stores", "/stores/1", "/stores/1/delete", "/stores/1/aisles", "/aisles/1", "/aisles/1/delete",
		"/staples", "/staples/1", "/staples/1/delete", "/staples/copy", "/staples/import",
	} {
		if r := e.do("POST", p, url.Values{"name": {"x"}}, nil); r.Code != http.StatusSeeOther {
			t.Errorf("POST %s: %d", p, r.Code)
		}
	}
}

func TestFlashMessageIsNotInTheAddress(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	rec := e.do("POST", "/list/clear", url.Values{"which": {"all"}}, c)
	if loc := rec.Header().Get("Location"); loc != "/list" {
		t.Errorf("redirect carries more than the page: %q", loc)
	}
	var flash *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == flashCookie {
			flash = ck
		}
	}
	if flash == nil {
		t.Fatal("no flash cookie")
	}
	req := e.doWithCookies("GET", "/list", c, flash)
	if !strings.Contains(req, "Removed 0 items.") {
		t.Error("flash not shown")
	}
	if strings.Contains(e.do("GET", "/list?msg=Your+account+is+locked", nil, c).Body.String(), "account is locked") {
		t.Error("a message in the address was shown")
	}
}

func (e *env) doWithCookies(method, path string, cookies ...*http.Cookie) string {
	e.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "192.0.2.10:5555"
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec.Body.String()
}
