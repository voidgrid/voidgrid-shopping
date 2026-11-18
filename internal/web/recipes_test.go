package web

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/voidgrid/voidgrid-shopping/internal/storage"
)

var png = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)

func (e *env) multipart(path string, fields map[string]string, file, filename string, data []byte, c *http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if file != "" {
		fw, _ := mw.CreateFormFile(file, filename)
		_, _ = fw.Write(data)
	}
	_ = mw.Close()
	req := httptest.NewRequest("POST", path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.RemoteAddr = "192.0.2.10:5555"
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *env) newRecipe(c *http.Cookie, title, ingredients string) string {
	e.t.Helper()
	rec := e.multipart("/recipes", map[string]string{
		"title": title, "ingredients": ingredients, "directions": "Cook.", "servings": "4", "tags": "Easy, Quick",
	}, "", "", nil, c)
	if rec.Code != http.StatusSeeOther {
		e.t.Fatalf("create %q: %d %s", title, rec.Code, rec.Body.String())
	}
	return rec.Header().Get("Location")
}

func TestRecipeLifecycle(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	loc := e.newRecipe(c, "Garlic <b>Bread</b>", "1 loaf bread\n2 clove garlic")
	if !regexp.MustCompile(`^/recipes/\d+$`).MatchString(loc) {
		t.Fatalf("redirect %q", loc)
	}
	rec := e.do("GET", loc, nil, c)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "1 loaf bread") || !strings.Contains(body, "Serves 4") {
		t.Fatalf("detail: %d %s", rec.Code, body)
	}
	if strings.Contains(body, "<b>Bread</b>") || !strings.Contains(body, "&lt;b&gt;Bread") {
		t.Error("title not escaped")
	}

	if r := e.do("POST", loc+"/rating", url.Values{"rating": {"4"}}, c); r.Code != http.StatusSeeOther {
		t.Fatalf("rating: %d", r.Code)
	}
	if r := e.do("POST", loc+"/rating", url.Values{"rating": {"9"}}, c); r.Code != http.StatusBadRequest {
		t.Errorf("rating 9: %d", r.Code)
	}
	if !strings.Contains(e.do("GET", "/recipes?rating=4", nil, c).Body.String(), "Garlic &lt;b&gt;Bread") {
		t.Error("rated recipe not in the 4-star filter")
	}
	if strings.Contains(e.do("GET", "/recipes?rating=5", nil, c).Body.String(), "Garlic &lt;b&gt;Bread") {
		t.Error("4-star recipe in the 5-star filter")
	}

	edit := e.multipart(loc, map[string]string{"title": "Cheese Bread", "ingredients": "1 loaf", "tags": "Bread"}, "", "", nil, c)
	if edit.Code != http.StatusSeeOther {
		t.Fatalf("edit: %d %s", edit.Code, edit.Body.String())
	}
	if !strings.Contains(e.do("GET", "/recipes?q=cheese", nil, c).Body.String(), "Cheese Bread") {
		t.Error("edited recipe not searchable")
	}

	if r := e.do("POST", loc+"/delete", url.Values{}, c); r.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d", r.Code)
	}
	if r := e.do("GET", loc, nil, c); r.Code != http.StatusNotFound {
		t.Errorf("deleted recipe: %d", r.Code)
	}
}

func TestRecipeFormValidation(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	for name, f := range map[string]map[string]string{
		"no title":     {"title": " "},
		"bad servings": {"title": "x", "servings": "lots"},
		"bad address":  {"title": "x", "url": "javascript:alert(1)"},
		"negative":     {"title": "x", "prep": "-5"},
	} {
		rec := e.multipart("/recipes", f, "", "", nil, c)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", name, rec.Code)
		}
	}
	if n := len(must(e.db.ListRecipes(t.Context(), storage.Filter{}))); n != 0 {
		t.Errorf("%d recipes saved from invalid forms", n)
	}
}

func TestRecipePhotoUploadAndServe(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	rec := e.multipart("/recipes", map[string]string{"title": "Pic"}, "photo", "a.png", png, c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	detail := e.do("GET", rec.Header().Get("Location"), nil, c).Body.String()
	m := regexp.MustCompile(`/photos/[0-9a-f]{32}\.png`).FindString(detail)
	if m == "" {
		t.Fatalf("no photo on the page: %s", detail)
	}
	if r := e.do("GET", m, nil, c); r.Code != 200 || !bytes.Equal(r.Body.Bytes(), png) {
		t.Errorf("photo served: %d", r.Code)
	}
	if r := e.do("GET", m, nil, nil); r.Code != http.StatusSeeOther {
		t.Errorf("photo without sign-in: %d, want redirect to login", r.Code)
	}
	if r := e.do("GET", "/photos/..%2f..%2fetc%2fpasswd", nil, c); r.Code != http.StatusNotFound {
		t.Errorf("traversal: %d", r.Code)
	}
	bad := e.multipart("/recipes", map[string]string{"title": "Evil"}, "photo", "a.png", []byte("<script>alert(1)</script>"), c)
	if bad.Code != http.StatusUnprocessableEntity {
		t.Errorf("non-image photo: %d", bad.Code)
	}
}

func TestRecipeRandomAndFilters(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	e.newRecipe(c, "Beef Chili", "1 lb beef\n1 onion")
	e.newRecipe(c, "Veggie Chili", "1 can beans\n1 onion")
	if r := e.do("GET", "/recipes/random?without=beef", nil, c); r.Code != http.StatusSeeOther ||
		!regexp.MustCompile(`^/recipes/\d+$`).MatchString(r.Header().Get("Location")) {
		t.Fatalf("random: %d %q", r.Code, r.Header().Get("Location"))
	}
	body := e.do("GET", "/recipes?without=beef", nil, c).Body.String()
	if strings.Contains(body, "Beef Chili") || !strings.Contains(body, "Veggie Chili") {
		t.Errorf("without=beef: %s", body)
	}
	if r := e.do("GET", "/recipes/random?q=nothingmatches", nil, c); !strings.HasPrefix(r.Header().Get("Location"), "/recipes?") {
		t.Errorf("random with no match: %q", r.Header().Get("Location"))
	}
}

func TestImportPlanToEat(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	csv := "Title,Url,Ingredients,Directions,Tags,Photo Url,Servings\n" +
		"Soup,https://example.com/s,\"1 cup broth\",Heat.,\"Soup, Easy\",https://images.plantoeat.com/recipes/1/a,4\n"
	rec := e.multipart("/recipes/import", nil, "file", "x.csv", []byte(csv), c)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "1 recipe added") {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	again := e.multipart("/recipes/import", nil, "file", "x.csv", []byte(csv), c)
	if !strings.Contains(again.Body.String(), "0 recipes added") || !strings.Contains(again.Body.String(), "already here") {
		t.Errorf("second import: %s", again.Body.String())
	}
	if !strings.Contains(e.do("GET", "/recipes?tag=Easy", nil, c).Body.String(), "Soup") {
		t.Error("imported recipe not found by tag")
	}
	if r := e.multipart("/recipes/import", nil, "file", "x.csv", []byte("nope"), c); r.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad file: %d", r.Code)
	}
}

func TestRecipesNeedSignIn(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/recipes/new", "/recipes/1", "/recipes/1/edit", "/recipes/import", "/recipes/random", "/photos/x"} {
		if r := e.do("GET", p, nil, nil); r.Code != http.StatusSeeOther {
			t.Errorf("GET %s signed out: %d", p, r.Code)
		}
	}
	for _, p := range []string{"/recipes", "/recipes/1", "/recipes/1/rating", "/recipes/1/delete", "/recipes/import"} {
		if r := e.do("POST", p, url.Values{"rating": {"1"}}, nil); r.Code != http.StatusSeeOther {
			t.Errorf("POST %s signed out: %d", p, r.Code)
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestRecipeScalingAndUnits(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	rec := e.multipart("/recipes", map[string]string{
		"title": "Pancakes", "servings": "4",
		"ingredients": "For the batter\n1 1/2 cups flour\n250 ml milk\n1 egg\n2 tbsp butter, melted\nsalt",
	}, "", "", nil, c)
	loc := rec.Header().Get("Location")
	body := e.do("GET", loc, nil, c).Body.String()
	for _, want := range []string{"1½ cups flour", "250 ml milk", "2 tbsp butter, melted", `class="heading"`} {
		if !strings.Contains(body, want) {
			t.Errorf("default view missing %q", want)
		}
	}
	body = e.do("GET", loc+"?servings=8", nil, c).Body.String()
	for _, want := range []string{"3 cups flour", "500 ml milk", "4 tbsp butter, melted", "Scaled from 4 servings"} {
		if !strings.Contains(body, want) {
			t.Errorf("8 servings missing %q in %s", want, body)
		}
	}
	body = e.do("GET", loc+"?units=metric", nil, c).Body.String()
	if !strings.Contains(body, "355 ml flour") && !strings.Contains(body, "350 ml flour") {
		t.Errorf("metric view: %s", body)
	}
	if !strings.Contains(body, "250 ml milk") {
		t.Error("metric milk changed")
	}
	body = e.do("GET", loc+"?units=us", nil, c).Body.String()
	if !strings.Contains(body, "1 cup milk") || !strings.Contains(body, "1½ cups flour") {
		t.Errorf("US view: %s", body)
	}
	if r := e.do("GET", loc+"?servings=-3&units=<script>", nil, c); r.Code != 200 || strings.Contains(r.Body.String(), "<script>") {
		t.Errorf("bad parameters: %d", r.Code)
	}
}
