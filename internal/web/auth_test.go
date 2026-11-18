package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/voidgrid/voidgrid-shopping/internal/mailer"
	"github.com/voidgrid/voidgrid-shopping/internal/secrets"
	"github.com/voidgrid/voidgrid-shopping/internal/totp"
)

// post sends a form with any number of cookies.
func (e *env) post2(path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "192.0.2.10:5555"
	for _, c := range cookies {
		if c != nil {
			req.AddCookie(c)
		}
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *env) get2(path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.RemoteAddr = "192.0.2.10:5555"
	for _, c := range cookies {
		if c != nil {
			req.AddCookie(c)
		}
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *env) baseURL() {
	e.t.Helper()
	if err := e.db.SetSettings(context.Background(), map[string]string{"public_base_url": "https://shop.example.com"}); err != nil {
		e.t.Fatal(err)
	}
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name && c.Value != "" {
			return c
		}
	}
	return nil
}

var linkRE = regexp.MustCompile(`https://shop\.example\.com/(invite|reset)/([A-Za-z0-9_-]+)`)

func TestInviteFlow(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	// no public address: no link
	if rec := e.post2("/users/invite", url.Values{"email": {"new@example.com"}}, admin); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("without public address: %d", rec.Code)
	}
	e.baseURL()
	rec := e.post2("/users/invite", url.Values{"email": {"New@Example.com"}, "name": {"Newbie"}}, admin)
	m := linkRE.FindStringSubmatch(rec.Body.String())
	if rec.Code != 200 || m == nil || m[1] != "invite" {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body.String())
	}
	tok := m[2]
	if rec := e.get2("/invite/" + tok); rec.Code != 200 || !strings.Contains(rec.Body.String(), "new@example.com") {
		t.Fatalf("invite page: %d", rec.Code)
	}
	if rec := e.post2("/invite/"+tok, url.Values{"name": {"Newbie"}, "password": {"short"}, "confirm": {"short"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("weak password: %d", rec.Code)
	}
	rec = e.post2("/invite/"+tok, url.Values{"name": {"Newbie"}, "password": {testPass}, "confirm": {testPass}})
	if rec.Code != http.StatusSeeOther || sessionOf(rec) == nil {
		t.Fatalf("accept: %d", rec.Code)
	}
	u, err := e.db.UserByEmail(context.Background(), "new@example.com")
	if err != nil || u.IsAdmin || u.DisplayName != "Newbie" {
		t.Fatalf("user %+v %v", u, err)
	}
	if rec := e.post2("/invite/"+tok, url.Values{"name": {"x"}, "password": {testPass}, "confirm": {testPass}}); rec.Code != http.StatusNotFound {
		t.Errorf("invite reused: %d", rec.Code)
	}
	if rec := e.get2("/invite/not-a-token"); rec.Code != http.StatusNotFound {
		t.Errorf("bad token: %d", rec.Code)
	}
	// members cannot reach admin pages
	member := sessionOf(e.login("new@example.com", testPass))
	for _, p := range []string{"/users"} {
		if rec := e.get2(p, member); rec.Code != http.StatusForbidden {
			t.Errorf("member GET %s: %d", p, rec.Code)
		}
	}
	if rec := e.post2("/users/invite", url.Values{"email": {"x@example.com"}}, member); rec.Code != http.StatusForbidden {
		t.Errorf("member invite: %d", rec.Code)
	}
}

func TestInviteEmailed(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	e.baseURL()
	_ = e.db.SetSettings(context.Background(), map[string]string{"smtp_host": "mail.example.com", "smtp_port": "587", "smtp_security": "starttls", "smtp_from": "shop@example.com"})
	var to, body string
	e.srv.send = func(_ context.Context, _ mailer.Config, rcpt, _, b string) error { to, body = rcpt, b; return nil }
	rec := e.post2("/users/invite", url.Values{"email": {"a@example.com"}}, admin)
	if !strings.Contains(rec.Body.String(), "Invite emailed to a@example.com") || strings.Contains(rec.Body.String(), "/invite/") && linkRE.MatchString(rec.Body.String()) {
		t.Errorf("page should say emailed and not show the link: %s", rec.Body.String())
	}
	if to != "a@example.com" || !linkRE.MatchString(body) {
		t.Errorf("mail to %q body %q", to, body)
	}
	e.srv.send = func(context.Context, mailer.Config, string, string, string) error { return errors.New("boom") }
	rec = e.post2("/users/invite", url.Values{"email": {"b@example.com"}}, admin)
	if !linkRE.MatchString(rec.Body.String()) {
		t.Error("link not shown when the email fails")
	}
}

func TestResetFlow(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	e.baseURL()
	e.addUser("bob@example.com")
	old := sessionOf(e.login("bob@example.com", testPass))
	rec := e.post2("/users/reset", url.Values{"email": {"bob@example.com"}}, admin)
	m := linkRE.FindStringSubmatch(rec.Body.String())
	if m == nil || m[1] != "reset" {
		t.Fatalf("reset link: %s", rec.Body.String())
	}
	rec = e.post2("/reset/"+m[2], url.Values{"password": {"a brand new password"}, "confirm": {"a brand new password"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?reset=1" {
		t.Fatalf("reset: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if e.get2("/recipes", old).Code != http.StatusSeeOther {
		t.Error("old session survived the reset")
	}
	if e.login("bob@example.com", testPass).Code == http.StatusSeeOther {
		t.Error("old password still works")
	}
	if e.login("bob@example.com", "a brand new password").Code != http.StatusSeeOther {
		t.Error("new password does not work")
	}
	if rec := e.post2("/reset/"+m[2], url.Values{"password": {"another password!!"}, "confirm": {"another password!!"}}); rec.Code != http.StatusNotFound {
		t.Errorf("reset link reused: %d", rec.Code)
	}
}

func TestSMTPSettingsAndSecrets(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	form := url.Values{"smtp_host": {"mail.example.com"}, "smtp_port": {"587"}, "smtp_security": {"starttls"}, "smtp_from": {"Shop <shop@example.com>"}, "smtp_password": {"s3cret"}}
	if rec := e.post2("/settings/email", form, admin); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("password without key: %d", rec.Code)
	}
	e.srv.secrets, _ = secrets.New("a long enough test key 123")
	if rec := e.post2("/settings/email", form, admin); rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	m, _ := e.db.AllSettings(context.Background())
	if m["smtp_password"] == "" || strings.Contains(m["smtp_password"], "s3cret") {
		t.Errorf("password stored as %q", m["smtp_password"])
	}
	c, _ := e.srv.loadMail(context.Background())
	if c.Password != "s3cret" || !c.Configured() {
		t.Errorf("loaded %+v", c)
	}
	if body := e.get2("/settings", admin).Body.String(); strings.Contains(body, "s3cret") || strings.Contains(body, m["smtp_password"]) {
		t.Error("password shown on the settings page")
	}
	form.Set("smtp_password", "") // blank keeps the saved one
	e.post2("/settings/email", form, admin)
	if c, _ := e.srv.loadMail(context.Background()); c.Password != "s3cret" {
		t.Error("blank password cleared the saved one")
	}
	bad := url.Values{"smtp_host": {"mail.example.com"}, "smtp_port": {"99999"}, "smtp_security": {"x"}, "smtp_from": {"nope"}}
	if rec := e.post2("/settings/email", bad, admin); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad email settings: %d", rec.Code)
	}
	var to string
	e.srv.send = func(_ context.Context, _ mailer.Config, rcpt, _, _ string) error { to = rcpt; return nil }
	if rec := e.post2("/settings/email/test", url.Values{}, admin); rec.Code != 200 || to != "ann@example.com" {
		t.Errorf("test mail: %d to %q", rec.Code, to)
	}
}

func TestTOTPSetupAndLogin(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	ctx := context.Background()
	// no key: refused
	if rec := e.post2("/account/totp/start", url.Values{}, admin); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("totp without key: %d", rec.Code)
	}
	e.srv.secrets, _ = secrets.New("a long enough test key 123")
	rec := e.post2("/account/totp/start", url.Values{}, admin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "data:image/png;base64,") {
		t.Fatalf("start: %d", rec.Code)
	}
	u, _ := e.db.UserByEmail(ctx, "ann@example.com")
	sealed, on, _ := e.db.TOTPSecret(ctx, u.ID)
	if on || strings.Contains(sealed, "=") && false {
		t.Fatal("enabled before confirming")
	}
	secret, err := e.srv.secrets.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if rec := e.post2("/account/totp/confirm", url.Values{"code": {"000000"}}, admin); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("wrong code at setup: %d", rec.Code)
	}
	code, _ := totp.Code(secret, e.now)
	rec = e.post2("/account/totp/confirm", url.Values{"code": {code}}, admin)
	codes := regexp.MustCompile(`<code>([a-z2-7]{6}-[a-z2-7]{6})</code>`).FindAllStringSubmatch(rec.Body.String(), -1)
	if rec.Code != 200 || len(codes) != 10 {
		t.Fatalf("confirm: %d, %d recovery codes", rec.Code, len(codes))
	}

	// password alone no longer signs in
	rec = e.login("ann@example.com", testPass)
	pend := cookieNamed(rec, pendingCookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login/2fa" || pend == nil || sessionOf(rec) != nil {
		t.Fatalf("password step: %d %q session=%v", rec.Code, rec.Header().Get("Location"), sessionOf(rec))
	}
	if e.get2("/recipes", pend).Code != http.StatusSeeOther {
		t.Error("pending cookie acts as a session")
	}
	if rec := e.post2("/login/2fa", url.Values{"code": {"123456"}}, pend); rec.Code != http.StatusUnauthorized || sessionOf(rec) != nil {
		t.Errorf("wrong code: %d", rec.Code)
	}
	// the code used at setup cannot be used again
	if rec := e.post2("/login/2fa", url.Values{"code": {code}}, pend); rec.Code != http.StatusUnauthorized {
		t.Errorf("reused code: %d", rec.Code)
	}
	e.now = e.now.Add(30 * time.Second)
	next, _ := totp.Code(secret, e.now)
	rec = e.post2("/login/2fa", url.Values{"code": {next}}, pend)
	if rec.Code != http.StatusSeeOther || sessionOf(rec) == nil {
		t.Fatalf("right code: %d", rec.Code)
	}
	if r2 := e.post2("/login/2fa", url.Values{"code": {next}}, pend); sessionOf(r2) != nil {
		t.Error("pending login worked twice")
	}

	// recovery code works once
	pend = cookieNamed(e.login("ann@example.com", testPass), pendingCookie)
	rc := codes[0][1]
	if rec := e.post2("/login/2fa", url.Values{"code": {strings.ToUpper(rc)}}, pend); rec.Code != http.StatusSeeOther {
		t.Fatalf("recovery code: %d", rec.Code)
	}
	pend = cookieNamed(e.login("ann@example.com", testPass), pendingCookie)
	if rec := e.post2("/login/2fa", url.Values{"code": {rc}}, pend); rec.Code != http.StatusUnauthorized {
		t.Errorf("recovery code reused: %d", rec.Code)
	}

	// turning it off needs the password
	if rec := e.post2("/account/totp/disable", url.Values{"current": {"wrong"}}, admin); rec.Code != http.StatusUnauthorized {
		t.Errorf("disable with wrong password: %d", rec.Code)
	}
	if rec := e.post2("/account/totp/disable", url.Values{"current": {testPass}}, admin); rec.Code != http.StatusSeeOther {
		t.Errorf("disable: %d", rec.Code)
	}
	if e.login("ann@example.com", testPass).Header().Get("Location") != "/recipes" {
		t.Error("still asks for a code after turning it off")
	}
}

func TestTwoFactorAttemptsAreLimited(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.srv.secrets, _ = secrets.New("a long enough test key 123")
	ctx := context.Background()
	u, _ := e.db.UserByEmail(ctx, "ann@example.com")
	sealed, _ := e.srv.secrets.Seal(strings.Repeat("A", 32))
	_ = e.db.SetTOTPSecret(ctx, u.ID, sealed)
	_ = e.db.EnableTOTP(ctx, u.ID, nil)
	pend := cookieNamed(e.login("ann@example.com", testPass), pendingCookie)
	var last int
	for range 12 {
		last = e.post2("/login/2fa", url.Values{"code": {"111111"}}, pend).Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("guessing was not limited: last status %d", last)
	}
}
