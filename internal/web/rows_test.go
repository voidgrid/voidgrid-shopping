package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/voidgrid/voidgrid-shopping/internal/storage"
)

func TestSplitAndJoinRow(t *testing.T) {
	for line, want := range map[string]formRow{
		"1-1/2 cups flour, sifted":  {Amount: "1-1/2", Unit: "cups", Name: "flour", Note: "sifted"},
		"4 clove Minced garlic":     {Amount: "4", Unit: "clove", Name: "Minced garlic"},
		"salt to taste":             {Name: "salt to taste"},
		"2 eggs":                    {Amount: "2", Name: "eggs"},
		"½ tsp salt":                {Amount: "½", Unit: "tsp", Name: "salt"},
		"For the sauce:":            {Sep: true, Name: "For the sauce"},
		"1 (8 oz) can tomato sauce": {Amount: "1", Name: "(8 oz) can tomato sauce"},
	} {
		got := splitRow(line)
		if got != want {
			t.Errorf("%q: %+v want %+v", line, got, want)
		}
		if l := got.line(); l != line {
			t.Errorf("%q round-trips to %q", line, l)
		}
	}
}

func TestRowsFromPost(t *testing.T) {
	form := url.Values{
		"ing_k": {"i", "s", "i", "i"}, "ing_a": {"2", "", "", ""}, "ing_u": {"cups", "", "", ""},
		"ing_n": {"flour", "For the sauce", "salt", ""}, "ing_x": {"sifted", "", "", ""},
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = r.ParseForm()
	got, ok := rowsFromPost(r)
	if !ok || got != "2 cups flour, sifted\nFor the sauce:\nsalt" {
		t.Errorf("%q %v", got, ok)
	}
	if rows := rowsFromText("1 egg\nFor x:"); len(rows) != 3 || !rows[1].Sep || rows[2] != (formRow{}) {
		t.Errorf("rows %+v", rows)
	}
}

func TestImportDraftFromText(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	rec := e.do(http.MethodPost, "/recipes/import/draft", url.Values{
		"kind": {"text"}, "text": {"Pancakes\n\nIngredients\n1 cup flour, sifted\nFor the syrup:\n1 cup sugar\n\nDirections\n1. Mix.\n"},
	}, c)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `value="Pancakes"`) || !strings.Contains(body, `value="sifted"`) || !strings.Contains(body, `class="row sep"`) {
		t.Fatalf("%d %s", rec.Code, body)
	}
	if n := len(must(e.db.ListRecipes(t.Context(), storage.Filter{}))); n != 0 {
		t.Errorf("draft saved %d recipes", n)
	}
	for _, bad := range []url.Values{
		{"kind": {"url"}, "address": {"http://127.0.0.1:1/"}},
		{"kind": {"tiktok"}, "tiktok": {"https://example.com/x"}},
		{"kind": {"text"}, "text": {"  "}},
	} {
		if r := e.do(http.MethodPost, "/recipes/import/draft", bad, c); r.Code != http.StatusUnprocessableEntity {
			t.Errorf("%v: %d", bad, r.Code)
		}
	}
}
