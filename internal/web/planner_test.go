package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The test clock is Tuesday 2026-11-17; weeks start on Sunday, so that week is Nov 15 to 21.

func (e *env) post(path string, v url.Values, c *http.Cookie) int {
	e.t.Helper()
	return e.do("POST", path, v, c).Code
}

func recipeIDFrom(loc string) string { return regexp.MustCompile(`\d+$`).FindString(loc) }

func TestPlannerWeekAndEntries(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	id := recipeIDFrom(e.newRecipe(c, "Chili", "1 can beans"))

	page := e.do("GET", "/planner", nil, c).Body.String()
	for _, want := range []string{"Nov 15 – Nov 21, 2026", "Tuesday", `data-date="2026-11-17" data-slot="dinner"`, "Chili"} {
		if !strings.Contains(page, want) {
			t.Errorf("empty week missing %q", want)
		}
	}
	if !strings.Contains(page, `class="day today"`) {
		t.Error("today not marked")
	}

	if code := e.post("/planner/entries", url.Values{"date": {"2026-11-17"}, "slot": {"dinner"}, "recipe_id": {id}, "servings": {"6"}}, c); code != 303 {
		t.Fatalf("add recipe: %d", code)
	}
	e.post("/planner/entries", url.Values{"date": {"2026-11-17"}, "slot": {"lunch"}, "kind": {"leftovers"}}, c)
	e.post("/planner/entries", url.Values{"date": {"2026-11-18"}, "slot": {"dinner"}, "kind": {"note"}, "note": {"Pizza night <b>"}}, c)
	body := e.do("GET", "/planner?start=2026-11-19", nil, c).Body.String() // any day of the week works
	for _, want := range []string{"6 servings", "<strong>Leftovers</strong>", "Pizza night &lt;b&gt;"} {
		if !strings.Contains(body, want) {
			t.Errorf("week missing %q", want)
		}
	}

	// next week is empty
	if next := e.do("GET", "/planner?start=2026-11-22", nil, c).Body.String(); strings.Contains(next, "Chili</a>") && strings.Contains(next, "6 servings") {
		t.Error("entries leaked into the next week")
	}
}

func TestPlannerMoveAndRemove(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	id := recipeIDFrom(e.newRecipe(c, "Tacos", "1 lb beef"))
	e.post("/planner/entries", url.Values{"date": {"2026-11-16"}, "slot": {"dinner"}, "recipe_id": {id}}, c)
	entries, _ := e.db.PlanEntries(t.Context(), "2026-11-15", "2026-11-21")
	if len(entries) != 1 {
		t.Fatalf("entries: %v", entries)
	}
	path := "/planner/entries/" + itoa(entries[0].ID)

	// the drag-and-drop script sends X-Fetch and gets 204
	req := e.fetchPost(path, url.Values{"date": {"2026-11-19"}, "slot": {"lunch"}}, c)
	if req.Code != http.StatusNoContent {
		t.Fatalf("drag move: %d", req.Code)
	}
	moved, _ := e.db.PlanEntry(t.Context(), entries[0].ID)
	if moved.Date != "2026-11-19" || moved.Slot != "lunch" {
		t.Errorf("after move %+v", moved)
	}
	if code := e.post(path, url.Values{"date": {"2026-02-30"}, "slot": {"lunch"}}, c); code != http.StatusBadRequest {
		t.Errorf("impossible date: %d", code)
	}
	if code := e.post(path, url.Values{"date": {"2026-11-19"}, "slot": {"brunch"}}, c); code != http.StatusBadRequest {
		t.Errorf("bad slot: %d", code)
	}
	if code := e.post("/planner/entries/999", url.Values{"date": {"2026-11-19"}}, c); code != http.StatusNotFound {
		t.Errorf("missing entry: %d", code)
	}
	if code := e.post(path+"/delete", url.Values{}, c); code != 303 {
		t.Errorf("remove: %d", code)
	}
	if left, _ := e.db.PlanEntries(t.Context(), "2026-11-01", "2026-11-30"); len(left) != 0 {
		t.Errorf("entry not removed: %v", left)
	}
}

func TestPlannerPrepNotes(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	if code := e.post("/planner/notes", url.Values{"date": {"2026-11-16"}, "text": {"Thaw the chicken"}, "for": {"2026-11-18|dinner"}}, c); code != 303 {
		t.Fatalf("add note: %d", code)
	}
	body := e.do("GET", "/planner?start=2026-11-16", nil, c).Body.String()
	if strings.Count(body, "Thaw the chicken") < 2 || !strings.Contains(body, "for Wed Dinner") || !strings.Contains(body, "Prep: Thaw the chicken") {
		t.Errorf("prep note not shown on the day and the meal: %s", body)
	}
	if code := e.post("/planner/notes", url.Values{"date": {"2026-11-16"}, "text": {"x"}, "for": {"2026-11-18|brunch"}}, c); code != http.StatusBadRequest {
		t.Errorf("bad meal: %d", code)
	}
	notes, _ := e.db.DayNotes(t.Context(), "2026-11-15", "2026-11-21")
	if code := e.post("/planner/notes/"+itoa(notes[0].ID)+"/delete", url.Values{"back": {"2026-11-16"}}, c); code != 303 {
		t.Errorf("delete note: %d", code)
	}
}

func TestPlannerAddPages(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	id := recipeIDFrom(e.newRecipe(c, "Pad Thai", "noodles"))
	if r := e.do("GET", "/planner/add?date=2026-11-18&slot=dinner&q=pad", nil, c); r.Code != 200 || !strings.Contains(r.Body.String(), "Pad Thai") {
		t.Errorf("slot picker: %d", r.Code)
	}
	if r := e.do("GET", "/planner/add?recipe_id="+id, nil, c); r.Code != 200 || !strings.Contains(r.Body.String(), `name="recipe_id" value="`+id+`"`) {
		t.Errorf("recipe picker: %d", r.Code)
	}
	if r := e.do("GET", "/planner/add?date=bad&slot=dinner", nil, c); r.Code != http.StatusNotFound {
		t.Errorf("bad slot picker: %d", r.Code)
	}
	if r := e.do("GET", "/planner/add?recipe_id=9999", nil, c); r.Code != http.StatusNotFound {
		t.Errorf("missing recipe: %d", r.Code)
	}
}

func TestPlannerWeekStartSetting(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	if err := e.db.SetSettings(t.Context(), map[string]string{"week_start": "1", "timezone": "America/Denver"}); err != nil {
		t.Fatal(err)
	}
	if body := e.do("GET", "/planner", nil, c).Body.String(); !strings.Contains(body, "Nov 16 – Nov 22, 2026") {
		t.Errorf("Monday-start week: %s", body)
	}
}

func TestPlannerNeedsSignIn(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/planner", "/planner/add?recipe_id=1"} {
		if r := e.do("GET", p, nil, nil); r.Code != http.StatusSeeOther {
			t.Errorf("GET %s: %d", p, r.Code)
		}
	}
	for _, p := range []string{"/planner/entries", "/planner/entries/1", "/planner/entries/1/delete", "/planner/notes", "/planner/notes/1/delete"} {
		if r := e.do("POST", p, url.Values{"date": {"2026-11-17"}}, nil); r.Code != http.StatusSeeOther {
			t.Errorf("POST %s: %d", p, r.Code)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func (e *env) fetchPost(path string, v url.Values, c *http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Fetch", "1")
	req.RemoteAddr = "192.0.2.10:5555"
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}
