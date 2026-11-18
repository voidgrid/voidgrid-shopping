package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/voidgrid/voidgrid-shopping/internal/storage"
)

var shareRE = regexp.MustCompile(`https://shop\.example\.com(/s/[A-Za-z0-9_-]+)`)

func (e *env) shareRecipe(c *http.Cookie, days string) (id, path string) {
	e.t.Helper()
	e.baseURL()
	cards, _ := e.db.ListRecipes(context.Background(), storage.Filter{})
	var rid int64
	if len(cards) > 0 {
		rid = cards[0].ID
	} else {
		rid, _ = e.db.CreateRecipe(context.Background(), &storage.Recipe{
			Title: "Secret Soup", Description: "Warm.", Ingredients: "2 cups stock\nFor the garnish:\n1 sprig thyme", Directions: "Heat.\nServe.",
			Source: "Grandma", URL: "https://example.com/soup", Servings: 4, CreatedBy: 1, Rating: 5,
		}, e.now)
	}
	rec := e.post2("/recipes/"+itoa(rid)+"/share", url.Values{"days": {days}}, c)
	m := shareRE.FindStringSubmatch(rec.Body.String())
	if m == nil {
		e.t.Fatalf("no link (%d): %s", rec.Code, rec.Body.String())
	}
	return itoa(rid), m[1]
}

func TestShareLinkLifecycle(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	id, path := e.shareRecipe(admin, "30")

	// public: no cookie, works, read-only content only
	rec := e.get2(path)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "Secret Soup") || !strings.Contains(body, "2 cups stock") {
		t.Fatalf("shared page: %d %s", rec.Code, body)
	}
	for _, leak := range []string{"ann@example.com", "Ann", "/recipes", "/planner", "Sign out", "rating", "Edit", "/settings", "form"} {
		if strings.Contains(body, leak) {
			t.Errorf("shared page contains %q", leak)
		}
	}
	h := rec.Header()
	if h.Get("Referrer-Policy") != "no-referrer" || !strings.Contains(h.Get("X-Robots-Tag"), "noindex") || h.Get("Cache-Control") != "no-store" {
		t.Errorf("headers %v", h)
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") || h.Get("Strict-Transport-Security") == "" {
		t.Error("security headers missing")
	}
	// being signed in changes nothing: still the public page
	if b := e.get2(path, admin).Body.String(); strings.Contains(b, "Sign out") {
		t.Error("signed-in cookie leaks into the shared page")
	}
	// only GET
	if r := e.post2(path, url.Values{}); r.Code == 200 {
		t.Errorf("POST to a share: %d", r.Code)
	}

	// expiry is enforced by the server clock
	e.now = e.now.Add(29 * 24 * time.Hour)
	if e.get2(path).Code != 200 {
		t.Error("link died early")
	}
	e.now = e.now.Add(2 * 24 * time.Hour)
	gone := e.get2(path)
	if gone.Code != http.StatusNotFound || !strings.Contains(gone.Body.String(), "no longer available") || strings.Contains(gone.Body.String(), "Secret Soup") {
		t.Errorf("expired: %d", gone.Code)
	}

	// revoke
	e.now = e.now.Add(-2 * 24 * time.Hour)
	_, path2 := e.shareRecipe(admin, "10")
	hash := ""
	shares, _ := e.db.SharesFor(context.Background(), 1, e.now)
	for _, s := range shares {
		if s.ExpiresAt == e.now.Add(10*24*time.Hour).Unix() {
			hash = s.Hash
		}
	}
	if hash == "" {
		t.Fatal("share not listed")
	}
	if r := e.post2("/recipes/"+id+"/share/revoke", url.Values{"hash": {hash}}, admin); r.Code != 200 {
		t.Fatalf("revoke: %d", r.Code)
	}
	if r := e.get2(path2); r.Code != http.StatusNotFound || !strings.Contains(r.Body.String(), "no longer available") {
		t.Errorf("revoked link still works: %d", r.Code)
	}
	// unknown tokens look exactly like revoked ones
	unk := e.get2("/s/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if unk.Code != http.StatusNotFound || unk.Body.String() != e.get2(path2).Body.String() {
		t.Error("unknown and revoked tokens look different")
	}
}

func TestShareExpiryRules(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	e.baseURL()
	rid, _ := e.db.CreateRecipe(context.Background(), &storage.Recipe{Title: "A"}, e.now)
	for _, d := range []string{"", "0", "-1", "366", "9999", "abc", "1e3"} {
		if r := e.post2("/recipes/"+itoa(rid)+"/share", url.Values{"days": {d}}, admin); r.Code != http.StatusUnprocessableEntity {
			t.Errorf("days %q: %d", d, r.Code)
		}
	}
	if r := e.post2("/recipes/"+itoa(rid)+"/share", url.Values{"days": {"365"}}, admin); r.Code != 200 {
		t.Errorf("365 days refused: %d", r.Code)
	}
	// the storage layer enforces it too, whatever the caller passes
	now := e.now
	if err := e.db.CreateShare(context.Background(), "h1", rid, 1, now, now.Add(366*24*time.Hour)); err == nil {
		t.Error("storage accepted 366 days")
	}
	if err := e.db.CreateShare(context.Background(), "h2", rid, 1, now, now.Add(-time.Hour)); err == nil {
		t.Error("storage accepted an expiry in the past")
	}
	// no public address: no link
	e2 := newEnv(t)
	c2 := e2.setup()
	rid2, _ := e2.db.CreateRecipe(context.Background(), &storage.Recipe{Title: "A"}, e2.now)
	if r := e2.post2("/recipes/"+itoa(rid2)+"/share", url.Values{"days": {"7"}}, c2); r.Code != http.StatusUnprocessableEntity {
		t.Errorf("without public address: %d", r.Code)
	}
}

func TestShareManagementNeedsSignIn(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	id, path := e.shareRecipe(admin, "7")
	for _, p := range []string{"/recipes/" + id + "/share"} {
		if r := e.get2(p); r.Code != http.StatusSeeOther {
			t.Errorf("GET %s signed out: %d", p, r.Code)
		}
	}
	if r := e.post2("/recipes/"+id+"/share", url.Values{"days": {"7"}}); r.Code != http.StatusSeeOther {
		t.Errorf("create signed out: %d", r.Code)
	}
	if r := e.post2("/recipes/"+id+"/share/revoke", url.Values{"hash": {"x"}}); r.Code != http.StatusSeeOther {
		t.Errorf("revoke signed out: %d", r.Code)
	}
	// a member cannot withdraw the admin's link
	e.addUser("bob@example.com")
	bob := sessionOf(e.login("bob@example.com", testPass))
	shares, _ := e.db.SharesFor(context.Background(), 1, e.now)
	if r := e.post2("/recipes/"+id+"/share/revoke", url.Values{"hash": {shares[0].Hash}}, bob); r.Code != http.StatusNotFound {
		t.Errorf("member withdrew another's link: %d", r.Code)
	}
	if e.get2(path).Code != 200 {
		t.Error("link broke")
	}
	// deleting the recipe ends its links
	e.post2("/recipes/"+id+"/delete", url.Values{}, admin)
	if e.get2(path).Code != http.StatusNotFound {
		t.Error("link survived its recipe")
	}
}

func TestSharedPhotoOnlyThroughItsLink(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	e.baseURL()
	name, err := e.srv.photos.Save(strings.NewReader(string(png)))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := e.db.CreateRecipe(context.Background(), &storage.Recipe{Title: "A", Photo: name}, e.now)
	b, _ := e.db.CreateRecipe(context.Background(), &storage.Recipe{Title: "B"}, e.now)
	pathA := shareRE.FindStringSubmatch(e.post2("/recipes/"+itoa(a)+"/share", url.Values{"days": {"5"}}, admin).Body.String())[1]
	pathB := shareRE.FindStringSubmatch(e.post2("/recipes/"+itoa(b)+"/share", url.Values{"days": {"5"}}, admin).Body.String())[1]
	if r := e.get2(pathA + "/photo"); r.Code != 200 || !strings.HasPrefix(r.Header().Get("Content-Type"), "image/") {
		t.Errorf("shared photo: %d", r.Code)
	}
	if r := e.get2(pathB + "/photo"); r.Code != http.StatusNotFound {
		t.Errorf("recipe without photo: %d", r.Code)
	}
	if r := e.get2("/photos/" + name); r.Code != http.StatusSeeOther {
		t.Errorf("photo open to the public without a link: %d", r.Code)
	}
	e.now = e.now.Add(6 * 24 * time.Hour)
	if r := e.get2(pathA + "/photo"); r.Code != http.StatusNotFound {
		t.Errorf("photo after expiry: %d", r.Code)
	}
}

func TestShareGuessingIsLimited(t *testing.T) {
	e := newEnv(t)
	var last int
	for range 25 {
		last = e.get2("/s/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA").Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("guessing was not slowed: %d", last)
	}
	// short or odd tokens never reach the database
	e2 := newEnv(t)
	for _, tok := range []string{"x", "../../etc/passwd", strings.Repeat("a", 500)} {
		if r := e2.get2("/s/" + tok); r.Code == http.StatusOK {
			t.Errorf("token %q: %d", tok, r.Code)
		}
	}
}

func TestSharedPageIsEscaped(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	e.baseURL()
	id, _ := e.db.CreateRecipe(context.Background(), &storage.Recipe{
		Title: `<script>alert(1)</script>`, Description: `"><img src=x onerror=alert(1)>`, Ingredients: `<b>1 cup</b> x`, Directions: `<i>go</i>`,
		URL: "javascript:alert(1)",
	}, e.now)
	path := shareRE.FindStringSubmatch(e.post2("/recipes/"+itoa(id)+"/share", url.Values{"days": {"2"}}, admin).Body.String())[1]
	body := e.get2(path).Body.String()
	for _, bad := range []string{"<script>alert", "<img src=x", "<b>1 cup", "<i>go"} {
		if strings.Contains(body, bad) {
			t.Errorf("unescaped %q", bad)
		}
	}
	if strings.Contains(body, `href="javascript:`) {
		t.Error("javascript: link rendered")
	}
}
