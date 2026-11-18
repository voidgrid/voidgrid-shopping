package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestListVersionTracksChanges(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	if r := e.get2("/list/version"); r.Code != http.StatusSeeOther {
		t.Errorf("version without sign-in: %d", r.Code)
	}
	ver := func() string { return strings.TrimSpace(e.get2("/list/version", c).Body.String()) }
	page := func() string {
		m := regexp.MustCompile(`data-version="(\d+)"`).FindStringSubmatch(e.get2("/list", c).Body.String())
		if m == nil {
			t.Fatal("no data-version on the list page")
		}
		return m[1]
	}
	v0 := ver()
	if page() != v0 {
		t.Error("page version differs from the endpoint")
	}
	e.post2("/list/items", url.Values{"name": {"milk"}}, c)
	v1 := ver()
	if v1 == v0 || page() != v1 {
		t.Errorf("adding an item: %s -> %s", v0, v1)
	}
	id := firstItemID(t, e)
	e.post2("/list/items/"+id+"/state", url.Values{"state": {"have"}}, c)
	v2 := ver()
	e.post2("/list/items/"+id+"/state", url.Values{"state": {"need"}}, c)
	if v3 := ver(); v1 == v2 || v2 == v3 {
		t.Errorf("state changes not seen: %s %s %s", v1, v2, v3)
	}
}

func firstItemID(t *testing.T, e *env) string {
	t.Helper()
	items, err := e.db.ListItems(context.Background())
	if err != nil || len(items) == 0 {
		t.Fatal("no items")
	}
	return itoa(items[0].ID)
}

func TestOfflineReplayDoesNotDoublePurchases(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	e.post2("/list/items", url.Values{"name": {"eggs"}, "quantity": {"12"}}, c)
	id := firstItemID(t, e)
	buy := url.Values{"quantity": {"6"}, "price": {"3.00"}, "op": {"abc123"}}
	for range 3 { // a dropped connection makes the phone send it again
		e.post2("/list/items/"+id+"/purchase", buy, c)
	}
	items, _ := e.db.ListItems(context.Background())
	if n := len(items[0].Purchases); n != 1 {
		t.Fatalf("%d purchases after replays, want 1", n)
	}
	// a different change with its own id is applied
	buy.Set("op", "def456")
	e.post2("/list/items/"+id+"/purchase", buy, c)
	if items, _ = e.db.ListItems(context.Background()); len(items[0].Purchases) != 2 {
		t.Error("second purchase not recorded")
	}
	// a failed one can be retried with the same id
	bad := url.Values{"quantity": {"x"}, "price": {"1"}, "op": {"zzz"}}
	if r := e.post2("/list/items/"+id+"/purchase", bad, c); r.Code != http.StatusBadRequest {
		t.Errorf("bad purchase: %d", r.Code)
	}
	ok := url.Values{"quantity": {"1"}, "price": {"1"}, "op": {"zzz"}}
	e.post2("/list/items/"+id+"/purchase", ok, c)
	if items, _ = e.db.ListItems(context.Background()); len(items[0].Purchases) != 3 {
		t.Error("retry with the same id was blocked")
	}
	// odd ids are refused
	if r := e.post2("/list/items/"+id+"/purchase", url.Values{"quantity": {"1"}, "price": {"1"}, "op": {"a b;"}}, c); r.Code != http.StatusBadRequest {
		t.Errorf("odd op id: %d", r.Code)
	}
}

func TestServiceWorkerIsServed(t *testing.T) {
	e := newEnv(t)
	r := e.get2("/sw.js")
	if r.Code != 200 || !strings.Contains(r.Header().Get("Content-Type"), "javascript") || !strings.Contains(r.Body.String(), "addEventListener('fetch'") {
		t.Fatalf("sw.js: %d %q", r.Code, r.Header().Get("Content-Type"))
	}
	// it only ever caches the list, never other pages
	body := r.Body.String()
	for _, p := range []string{"/settings", "/users", "/account", "/recipes"} {
		if strings.Contains(body, "'"+p) {
			t.Errorf("service worker mentions %s", p)
		}
	}
}
