package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/voidgrid/voidgrid-shopping/internal/openfoodfacts"
	"github.com/voidgrid/voidgrid-shopping/internal/storage"
)

func (e *env) loadUSDA() {
	e.t.Helper()
	foods := []storage.USDAFood{{FdcID: 1, Description: "Wheat flour, white, all-purpose", DataType: "sr_legacy_food"}, {FdcID: 2, Description: "Egg, whole, raw", DataType: "sr_legacy_food"}}
	portions := []storage.USDAPortion{
		{FdcID: 1, Amount: 1, Label: "cup", Grams: 125, ML: 236.5882365},
		{FdcID: 2, Amount: 1, Label: "large", Grams: 50},
		{FdcID: 2, Amount: 1, Label: "medium", Grams: 44},
	}
	nut := []storage.USDANutrients{{FdcID: 1, Kcal: 364, Protein: 10, Carbs: 76, Fat: 1}, {FdcID: 2, Kcal: 143, Protein: 12.6, Carbs: 0.7, Fat: 9.5}}
	if err := e.db.ReplaceUSDA(e.t.Context(), foods, portions, nut); err != nil {
		e.t.Fatal(err)
	}
}

func TestNutritionFromLinkedFoods(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	e.loadUSDA()
	loc := e.newRecipe(c, "Crepes", "2 cups flour\n2 eggs\n1 tbsp mystery\nsalt")
	id := recipeIDFrom(loc)

	if body := e.do("GET", loc, nil, c).Body.String(); !strings.Contains(body, "No nutrition yet") {
		t.Error("a recipe with nothing linked should say so")
	}
	// search the USDA data and choose foods: nothing is guessed
	res := e.do("GET", "/nutrition/link?key=flour&recipe="+id+"&q=wheat+flour", nil, c).Body.String()
	if !strings.Contains(res, "Wheat flour, white, all-purpose") || !strings.Contains(res, "364 kcal") {
		t.Fatalf("search results: %s", res)
	}
	if strings.Contains(e.do("GET", "/nutrition/link?key=flour&q=marsala", nil, c).Body.String(), "Wheat flour") {
		t.Error("an unrelated search returned a food")
	}
	if code := e.post("/nutrition/link", url.Values{"key": {"flour"}, "recipe": {id}, "source": {"usda"}, "fdc_id": {"1"}}, c); code != 303 {
		t.Fatalf("link flour: %d", code)
	}
	if code := e.post("/nutrition/link", url.Values{"key": {"eggs"}, "recipe": {id}, "source": {"usda"}, "fdc_id": {"2"}}, c); code != 303 {
		t.Fatalf("link eggs: %d", code)
	}

	page := e.do("GET", "/recipes/"+id+"/nutrition", nil, c).Body.String()
	// 2 cups flour = 250 g (910 kcal); 2 medium eggs = 88 g (125.84 kcal); the recipe serves 4
	for _, want := range []string{"Calculated from 2 of 3 ingredients", "<strong>259</strong> kcal", "Choose food"} {
		if !strings.Contains(page, want) {
			t.Errorf("nutrition page missing %q\n%s", want, page)
		}
	}
	if !strings.Contains(e.do("GET", loc, nil, c).Body.String(), "Nutrition per serving") {
		t.Error("recipe page has no nutrition")
	}

	// a person's own figure for one each beats the USDA measure
	e.post("/nutrition/link", url.Values{"key": {"eggs"}, "recipe": {id}, "source": {"usda"}, "fdc_id": {"2"}, "unit_g": {"60"}}, c)
	if page = e.do("GET", "/recipes/"+id+"/nutrition", nil, c).Body.String(); !strings.Contains(page, "120 g") {
		t.Errorf("one-each weight ignored: %s", page)
	}

	// entering nutrition by hand for the third ingredient, per serving of 30 g
	form := url.Values{"key": {"mystery"}, "recipe": {id}, "name": {"Mystery powder"}, "basis": {"serving"}, "serving_g": {"30"}, "kcal": {"120"}, "protein": {"3"}, "carbs": {"20"}, "fat": {"3"}, "g_per_ml": {"0.5"}}
	if code := e.post("/nutrition/foods", form, c); code != 303 {
		t.Fatalf("manual food: %d", code)
	}
	recs, _ := e.db.FoodRecords(t.Context())
	if len(recs) != 1 || recs[0].Per100.Kcal != 400 || recs[0].UnitG != 30 {
		t.Fatalf("record %+v (120 kcal per 30 g is 400 per 100 g)", recs)
	}
	if page = e.do("GET", "/recipes/"+id+"/nutrition", nil, c).Body.String(); !strings.Contains(page, "Calculated from all ingredients") && !strings.Contains(page, "from 3 of 3") {
		t.Errorf("after the third link: %s", page)
	}

	// hand-entered figures replace the calculated ones, and can be removed
	if code := e.post("/recipes/"+id+"/nutrition", url.Values{"kcal": {"500"}, "protein": {"30"}, "carbs": {"40"}, "fat": {"20"}}, c); code != 303 {
		t.Fatalf("override: %d", code)
	}
	if body := e.do("GET", loc, nil, c).Body.String(); !strings.Contains(body, "<strong>500</strong> kcal") || !strings.Contains(body, "Entered by hand") {
		t.Error("override not shown")
	}
	e.post("/recipes/"+id+"/nutrition", url.Values{"clear": {"1"}}, c)
	if strings.Contains(e.do("GET", loc, nil, c).Body.String(), "Entered by hand") {
		t.Error("override not cleared")
	}
	if code := e.post("/recipes/"+id+"/nutrition", url.Values{"kcal": {"lots"}}, c); code != http.StatusBadRequest {
		t.Errorf("bad override: %d", code)
	}
	if code := e.post("/nutrition/unlink", url.Values{"key": {"eggs"}, "recipe": {id}}, c); code != 303 {
		t.Errorf("unlink: %d", code)
	}
	if links, _ := e.db.IngredientLinks(t.Context()); len(links) != 2 {
		t.Errorf("links after unlink: %v", links)
	}
	for name, f := range map[string]url.Values{
		"bad macros":   {"key": {"x"}, "name": {"x"}, "kcal": {"abc"}},
		"serving no g": {"key": {"x"}, "name": {"x"}, "basis": {"serving"}, "kcal": {"1"}},
		"negative":     {"key": {"x"}, "name": {"x"}, "kcal": {"-5"}},
		"nameless":     {"key": {"x"}, "name": {""}, "kcal": {"5"}},
	} {
		if code := e.post("/nutrition/foods", f, c); code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, code)
		}
	}
}

func fakeOFF(t *testing.T, body string, status int) (*openfoodfacts.Client, *int) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return openfoodfacts.New(srv.URL, 5), &hits
}

const cheerios = `{"status":1,"product":{"code":"0016000275287","product_name":"Cheerios","brands":"General Mills","nutriments":{"energy-kcal_100g":359,"proteins_100g":12.8,"carbohydrates_100g":39,"fat_100g":6},"serving_quantity":"39"}}`

func TestBarcodeLookupIsCachedAndLinked(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	client, hits := fakeOFF(t, cheerios, 200)
	e.srv.off = client
	id := recipeIDFrom(e.newRecipe(c, "Cereal bowl", "2 cups cheerios\n1 cup milk"))

	rec := e.do("POST", "/nutrition/barcode", url.Values{"key": {"cheerios"}, "recipe": {id}, "barcode": {"0016000275287"}}, c)
	loc := rec.Header().Get("Location")
	if rec.Code != 303 || !strings.HasPrefix(loc, "/nutrition/product?barcode=0016000275287") {
		t.Fatalf("lookup: %d %q", rec.Code, loc)
	}
	body := e.do("GET", loc, nil, c).Body.String()
	for _, want := range []string{"General Mills Cheerios", "359</strong> kcal", "Open Food Facts", "Use this for"} {
		if !strings.Contains(body, want) {
			t.Errorf("product page missing %q", want)
		}
	}
	e.do("POST", "/nutrition/barcode", url.Values{"key": {"cheerios"}, "recipe": {id}, "barcode": {"0016000275287"}}, c)
	if *hits != 1 {
		t.Errorf("%d network lookups for one barcode, want 1 (cached)", *hits)
	}
	if code := e.post("/nutrition/link", url.Values{"key": {"cheerios"}, "recipe": {id}, "source": {"off"}, "barcode": {"0016000275287"}, "unit_g": {"39"}}, c); code != 303 {
		t.Fatalf("link product: %d", code)
	}
	if links, _ := e.db.IngredientLinks(t.Context()); links["cheerios"].Source != storage.SourceOFF {
		t.Errorf("links %+v", links)
	}
	// 2 cups of a volume-measured product has no density: said so, not guessed
	if page := e.do("GET", "/recipes/"+id+"/nutrition", nil, c).Body.String(); !strings.Contains(page, "weight per cup") {
		t.Errorf("volume amount of a product with no density should be explained: %s", page)
	}
}

func TestBarcodeProblems(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	client, hits := fakeOFF(t, `{"status":0}`, 200)
	e.srv.off = client
	flash := func(rec *httptest.ResponseRecorder) string {
		for _, ck := range rec.Result().Cookies() {
			if ck.Name == flashCookie {
				v, _ := url.QueryUnescape(ck.Value)
				return v
			}
		}
		return ""
	}
	for _, bad := range []string{"", "12ab", "123", "../../x"} {
		rec := e.do("POST", "/nutrition/barcode", url.Values{"key": {"x"}, "barcode": {bad}}, c)
		if rec.Code != 303 || !strings.Contains(flash(rec), "8 to 14 digits") {
			t.Errorf("%q: %d %q", bad, rec.Code, flash(rec))
		}
	}
	if *hits != 0 {
		t.Error("an invalid barcode reached Open Food Facts")
	}
	rec := e.do("POST", "/nutrition/barcode", url.Values{"key": {"x"}, "barcode": {"12345670"}}, c)
	if !strings.Contains(flash(rec), "doesn't know that barcode") {
		t.Errorf("unknown barcode: %q", flash(rec))
	}
	down, _ := fakeOFF(t, `oops`, 500)
	e.srv.off = down
	rec = e.do("POST", "/nutrition/barcode", url.Values{"key": {"x"}, "barcode": {"12345670"}}, c)
	if !strings.Contains(flash(rec), "could not be reached") {
		t.Errorf("service down: %q", flash(rec))
	}
	if code := e.do("GET", "/nutrition/product?barcode=12345670&key=x", nil, c).Code; code != http.StatusNotFound {
		t.Errorf("product page for an unseen barcode: %d", code)
	}
}

func TestNutritionNeedsSignIn(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/recipes/1/nutrition", "/nutrition/link?key=x", "/nutrition/product?barcode=12345670&key=x"} {
		if r := e.do("GET", p, nil, nil); r.Code != http.StatusSeeOther {
			t.Errorf("GET %s: %d", p, r.Code)
		}
	}
	for _, p := range []string{"/recipes/1/nutrition", "/nutrition/link", "/nutrition/unlink", "/nutrition/foods", "/nutrition/barcode"} {
		if r := e.do("POST", p, url.Values{"key": {"x"}}, nil); r.Code != http.StatusSeeOther {
			t.Errorf("POST %s: %d", p, r.Code)
		}
	}
}
