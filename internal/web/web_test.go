package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voidgrid/voidgrid-shopping/internal/auth"
	"github.com/voidgrid/voidgrid-shopping/internal/storage"
)

const (
	testToken = "test-setup-token"
	testPass  = "a long enough password"
)

type env struct {
	t   *testing.T
	db  *storage.DB
	srv *Server
	h   http.Handler
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, db: db, now: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	srv, err := New(context.Background(), db, Options{
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:        func() time.Time { return e.now },
		SetupToken: testToken,
		PhotoDir:   filepath.Join(t.TempDir(), "photos"),
	})
	if err != nil {
		t.Fatal(err)
	}
	e.srv, e.h = srv, srv.Handler()
	return e
}

func (e *env) do(method, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.RemoteAddr = "192.0.2.10:5555"
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func sessionOf(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c
		}
	}
	return nil
}

func (e *env) setup() *http.Cookie {
	e.t.Helper()
	rec := e.do("POST", "/setup", url.Values{
		"token": {testToken}, "name": {"Ann"}, "email": {"ann@example.com"},
		"password": {testPass}, "confirm": {testPass},
	}, nil)
	if rec.Code != http.StatusSeeOther {
		e.t.Fatalf("setup: status %d: %s", rec.Code, rec.Body.String())
	}
	c := sessionOf(rec)
	if c == nil {
		e.t.Fatal("setup did not sign the admin in")
	}
	return c
}

func (e *env) addUser(email string) {
	e.t.Helper()
	h, _ := auth.HashPassword(testPass)
	if _, err := e.db.CreateUser(context.Background(), email, "Member", h, false); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) login(email, pw string) *httptest.ResponseRecorder {
	return e.do("POST", "/login", url.Values{"email": {email}, "password": {pw}}, nil)
}

func TestHealth(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("GET", "/healthz", nil, nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("healthz: %d %q", rec.Code, rec.Body.String())
	}
}

func TestPagesNeedSignIn(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/", "/recipes", "/planner", "/list", "/staples", "/settings"} {
		rec := e.do("GET", p, nil, nil)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
			t.Errorf("GET %s signed out: %d -> %q", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	if rec := e.do("POST", "/settings", url.Values{"units": {"us"}}, nil); rec.Code != http.StatusSeeOther {
		t.Errorf("POST /settings signed out: %d", rec.Code)
	}
}

func TestSetupRequiresTokenAndWorksOnce(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("GET", "/setup", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("setup without token: %d, want 404", rec.Code)
	}
	if rec := e.do("GET", "/setup?token=wrong", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("setup with wrong token: %d, want 404", rec.Code)
	}
	if rec := e.do("GET", "/setup?token="+testToken, nil, nil); rec.Code != 200 {
		t.Errorf("setup with token: %d, want 200", rec.Code)
	}
	if rec := e.do("POST", "/setup", url.Values{
		"token": {"wrong"}, "name": {"Mal"}, "email": {"mal@example.com"},
		"password": {testPass}, "confirm": {testPass},
	}, nil); rec.Code != http.StatusNotFound {
		t.Errorf("setup post with wrong token: %d, want 404", rec.Code)
	}
	if n, _ := e.db.CountUsers(context.Background()); n != 0 {
		t.Fatalf("wrong-token setup created %d users", n)
	}

	c := e.setup()
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie flags: secure=%v httponly=%v samesite=%v", c.Secure, c.HttpOnly, c.SameSite)
	}
	u, err := e.db.UserByEmail(context.Background(), "ann@example.com")
	if err != nil || !u.IsAdmin {
		t.Fatalf("admin account: %+v %v", u, err)
	}
	if u.PasswordHash == testPass || !auth.VerifyPassword(testPass, u.PasswordHash) {
		t.Error("password not stored as a verifiable hash")
	}

	// The link is dead once used.
	if rec := e.do("GET", "/setup?token="+testToken, nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("setup reused: %d, want 404", rec.Code)
	}
	if rec := e.do("POST", "/setup", url.Values{
		"token": {testToken}, "name": {"Mal"}, "email": {"mal@example.com"},
		"password": {testPass}, "confirm": {testPass},
	}, nil); rec.Code != http.StatusNotFound {
		t.Errorf("setup post reused: %d, want 404", rec.Code)
	}
	if n, _ := e.db.CountUsers(context.Background()); n != 1 {
		t.Errorf("users = %d, want 1", n)
	}
}

func TestSetupValidation(t *testing.T) {
	cases := map[string]url.Values{
		"short password": {"name": {"Ann"}, "email": {"ann@example.com"}, "password": {"short"}, "confirm": {"short"}},
		"mismatch":       {"name": {"Ann"}, "email": {"ann@example.com"}, "password": {testPass}, "confirm": {testPass + "x"}},
		"bad email":      {"name": {"Ann"}, "email": {"not-an-email"}, "password": {testPass}, "confirm": {testPass}},
		"no name":        {"name": {" "}, "email": {"ann@example.com"}, "password": {testPass}, "confirm": {testPass}},
	}
	for name, form := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			form.Set("token", testToken)
			rec := e.do("POST", "/setup", form, nil)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d, want 422", rec.Code)
			}
			if n, _ := e.db.CountUsers(context.Background()); n != 0 {
				t.Errorf("rejected setup created %d users", n)
			}
			if rec := e.do("GET", "/setup?token="+testToken, nil, nil); rec.Code != 200 {
				t.Errorf("setup link spent by a rejected attempt: %d", rec.Code)
			}
		})
	}
}

func TestLogin(t *testing.T) {
	e := newEnv(t)
	e.setup()

	rec := e.login("ann@example.com", testPass)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/recipes" {
		t.Fatalf("login: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
	c := sessionOf(rec)
	if c == nil {
		t.Fatal("no session cookie")
	}
	if rec := e.do("GET", "/recipes", nil, c); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Recipes") {
		t.Errorf("signed-in page: %d", rec.Code)
	}
	if rec := e.login("ANN@example.com", testPass); rec.Code != http.StatusSeeOther {
		t.Errorf("email is case-sensitive at sign-in: %d", rec.Code)
	}

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"wrong password": e.login("ann@example.com", "wrong password here"),
		"unknown email":  e.login("nobody@example.com", testPass),
	} {
		if rec.Code != http.StatusUnauthorized || sessionOf(rec) != nil {
			t.Errorf("%s: status %d, cookie %v", name, rec.Code, sessionOf(rec))
		}
		if !strings.Contains(rec.Body.String(), "The email or password is not right.") {
			t.Errorf("%s: message differs from the shared one", name)
		}
	}
}

func TestLoginDisabledUser(t *testing.T) {
	e := newEnv(t)
	e.addUser("off@example.com")
	if _, err := e.db.Exec(`UPDATE users SET disabled = 1 WHERE email = 'off@example.com'`); err != nil {
		t.Fatal(err)
	}
	if rec := e.login("off@example.com", testPass); rec.Code != http.StatusUnauthorized {
		t.Errorf("disabled user: %d, want 401", rec.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	e.addUser("ann@example.com")
	for i := 0; i < 10; i++ {
		if rec := e.login("ann@example.com", "wrong password here"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d, want 401", i, rec.Code)
		}
	}
	// Now even the right password is refused, until the window passes.
	if rec := e.login("ann@example.com", testPass); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after 10 failures: %d, want 429", rec.Code)
	}
	e.now = e.now.Add(16 * time.Minute)
	if rec := e.login("ann@example.com", testPass); rec.Code != http.StatusSeeOther {
		t.Errorf("after the window: %d, want 303", rec.Code)
	}
}

func TestSessionExpiresAndLogoutEndsIt(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	if rec := e.do("GET", "/recipes", nil, c); rec.Code != 200 {
		t.Fatalf("fresh session: %d", rec.Code)
	}

	// Logout kills the session server-side, not just the cookie.
	rec := e.do("POST", "/logout", nil, c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout: %d", rec.Code)
	}
	if rec := e.do("GET", "/recipes", nil, c); rec.Code != http.StatusSeeOther {
		t.Errorf("old cookie after logout: %d, want redirect", rec.Code)
	}

	// Default lifetime is 30 days.
	rec = e.login("ann@example.com", testPass)
	c = sessionOf(rec)
	e.now = e.now.Add(29 * 24 * time.Hour)
	if rec := e.do("GET", "/recipes", nil, c); rec.Code != 200 {
		t.Errorf("day 29: %d, want 200", rec.Code)
	}
	e.now = e.now.Add(2 * 24 * time.Hour)
	if rec := e.do("GET", "/recipes", nil, c); rec.Code != http.StatusSeeOther {
		t.Errorf("day 31: %d, want redirect", rec.Code)
	}
}

func TestSettingsAdminOnly(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	e.addUser("kid@example.com")
	member := sessionOf(e.login("kid@example.com", testPass))

	if rec := e.do("GET", "/settings", nil, admin); rec.Code != 200 {
		t.Errorf("admin GET: %d", rec.Code)
	}
	if rec := e.do("GET", "/settings", nil, member); rec.Code != http.StatusForbidden {
		t.Errorf("member GET: %d, want 403", rec.Code)
	}
	if rec := e.do("POST", "/settings", url.Values{"units": {"metric"}}, member); rec.Code != http.StatusForbidden {
		t.Errorf("member POST: %d, want 403", rec.Code)
	}
	if rec := e.do("GET", "/recipes", nil, member); strings.Contains(rec.Body.String(), `href="/settings"`) {
		t.Error("settings link shown to a non-admin")
	}
}

func goodSettings() url.Values {
	return url.Values{
		"public_base_url": {"https://shopping.example.com"}, "timezone": {"America/Denver"},
		"units": {"metric"}, "week_start": {"1"}, "session_hours": {"12"}, "share_default_days": {"60"},
	}
}

func TestSettingsSaveAndApply(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()

	rec := e.do("POST", "/settings", goodSettings(), admin)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save: %d: %s", rec.Code, rec.Body.String())
	}
	st, err := e.srv.loadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Units != "metric" || st.WeekStart != 1 || st.SessionHours != 12 || st.ShareDefaultDays != 60 ||
		st.Timezone != "America/Denver" || st.PublicBaseURL != "https://shopping.example.com" {
		t.Errorf("stored settings = %+v", st)
	}
	page := e.do("GET", "/settings?saved=1", nil, admin).Body.String()
	if !strings.Contains(page, "Settings saved.") || !strings.Contains(page, `value="America/Denver"`) {
		t.Error("settings page does not show the saved values")
	}

	// The session lifetime setting is used by the next sign-in.
	c := sessionOf(e.login("ann@example.com", testPass))
	if got := c.Expires.Sub(e.now); got != 12*time.Hour {
		t.Errorf("new session lasts %v, want 12h", got)
	}
}

func TestSettingsInvalidSavesNothing(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	bad := goodSettings()
	bad.Set("timezone", "Mars/Olympus")
	bad.Set("share_default_days", "400")

	rec := e.do("POST", "/settings", bad, admin)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Unknown timezone", "from 1 to 365", "nothing was saved"} {
		if !strings.Contains(body, want) {
			t.Errorf("response lacks %q", want)
		}
	}
	m, _ := e.db.AllSettings(context.Background())
	if len(m) != 0 {
		t.Errorf("invalid submit stored %v", m)
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := newEnv(t)
	rec := e.do("GET", "/login", nil, nil)
	h := rec.Header()
	if !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") ||
		h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "no-store" ||
		!strings.Contains(h.Get("X-Robots-Tag"), "noindex") {
		t.Errorf("headers: %v", h)
	}
	if css := e.do("GET", "/static/app.css", nil, nil); css.Code != 200 || css.Header().Get("Cache-Control") == "no-store" {
		t.Errorf("static css: %d, cache %q", css.Code, css.Header().Get("Cache-Control"))
	}
}

func TestCrossSitePostRefused(t *testing.T) {
	e := newEnv(t)
	req := httptest.NewRequest("POST", "/login", strings.NewReader("email=a%40example.com&password=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site POST: %d, want 403", rec.Code)
	}
}

func TestClientIPUsesProxyHeaderOnlyWhenTrusted(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 198.51.100.7")

	s := &Server{}
	if got := s.clientIP(r); got != "10.0.0.1" {
		t.Errorf("untrusted: %q, want the peer address", got)
	}
	s.trustProxy = true
	if got := s.clientIP(r); got != "198.51.100.7" {
		t.Errorf("trusted: %q, want the last forwarded address", got)
	}
}
