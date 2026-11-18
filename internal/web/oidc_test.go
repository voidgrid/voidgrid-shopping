package web

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/voidgrid/voidgrid-shopping/internal/secrets"
)

// idp is a tiny OpenID Connect provider for tests.
type idp struct {
	*httptest.Server
	key         *rsa.PrivateKey
	claims      map[string]any // extra claims in the next ID token
	challenge   string
	nonce       string
	badVerifier bool
}

func newIdP(t *testing.T) *idp {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &idp{key: k}
	mux := http.NewServeMux()
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{
			"issuer": p.URL, "authorization_endpoint": p.URL + "/auth", "token_endpoint": p.URL + "/token",
			"jwks_uri": p.URL + "/jwks", "userinfo_endpoint": p.URL + "/userinfo",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &k.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if got := b64.EncodeToString(sum[:]); got != p.challenge && !p.badVerifier {
			http.Error(w, `{"error":"invalid_grant"}`, 400)
			return
		}
		sig, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: k, KeyID: "k1"}}, (&jose.SignerOptions{}).WithType("JWT"))
		std := jwt.Claims{Issuer: p.URL, Subject: "sub-1", Audience: jwt.Audience{"cid"}, IssuedAt: jwt.NewNumericDate(time.Now()), Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour))}
		raw, _ := jwt.Signed(sig).Claims(std).Claims(map[string]any{"nonce": p.nonce}).Claims(p.claims).Serialize()
		writeJSON(w, 200, map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": raw, "expires_in": 3600})
	})
	return p
}

// signIn runs the whole redirect dance and returns the callback response.
func (p *idp) signIn(t *testing.T, e *env, claims map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	p.claims = claims
	rec := e.get2("/login/oidc")
	loc, err := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || err != nil || !strings.HasPrefix(loc.String(), p.URL+"/auth") {
		t.Fatalf("start: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	q := loc.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || q.Get("nonce") == "" ||
		q.Get("redirect_uri") != "https://shop.example.com/login/oidc/callback" || !strings.Contains(q.Get("scope"), "groups") {
		t.Fatalf("auth request %v", q)
	}
	p.challenge, p.nonce = q.Get("code_challenge"), q.Get("nonce")
	return e.get2("/login/oidc/callback?code=abc&state="+url.QueryEscape(q.Get("state")), cookieNamed(rec, "vgs_oidc"))
}

func oidcEnv(t *testing.T) (*env, *idp) {
	e := newEnv(t)
	e.setup()
	e.baseURL()
	e.srv.secrets, _ = secrets.New("a long enough test key 123")
	p := newIdP(t)
	sealed, _ := e.srv.secrets.Seal("client-secret")
	_ = e.db.SetSettings(context.Background(), map[string]string{
		kOIDCIssuer: p.URL, kOIDCClient: "cid", kOIDCSec: sealed, kOIDCGroupClaim: "groups", kOIDCReq: "family", kOIDCAdm: "admins",
	})
	return e, p
}

func TestOIDCSignInCreatesAndLinksAccounts(t *testing.T) {
	e, p := oidcEnv(t)
	ctx := context.Background()
	if !strings.Contains(e.get2("/login").Body.String(), "/login/oidc") {
		t.Error("login page has no single sign-on button")
	}
	rec := p.signIn(t, e, map[string]any{"email": "kid@example.com", "email_verified": true, "name": "Kid", "groups": []string{"family"}})
	if rec.Code != http.StatusSeeOther || sessionOf(rec) == nil {
		t.Fatalf("sign-in: %d %s", rec.Code, rec.Body.String())
	}
	u, err := e.db.UserByEmail(ctx, "kid@example.com")
	if err != nil || u.IsAdmin || u.PasswordHash != "" || u.DisplayName != "Kid" {
		t.Fatalf("created %+v %v", u, err)
	}
	// no password can ever match an account without one
	if e.login("kid@example.com", "").Code == http.StatusSeeOther || e.login("kid@example.com", testPass).Code == http.StatusSeeOther {
		t.Error("password sign-in works for a single-sign-on-only account")
	}
	// the same subject again signs in to the same account, and the admin group promotes
	rec = p.signIn(t, e, map[string]any{"email": "other@example.com", "groups": []string{"family", "admins"}})
	if sessionOf(rec) == nil {
		t.Fatalf("second sign-in: %d", rec.Code)
	}
	if u, _ := e.db.UserByEmail(ctx, "kid@example.com"); !u.IsAdmin {
		t.Error("admin group did not make an admin")
	}
	if n, _ := e.db.CountUsers(ctx); n != 2 {
		t.Errorf("%d accounts, want 2", n)
	}
	p.signIn(t, e, map[string]any{"groups": []string{"family"}})
	if u, _ := e.db.UserByEmail(ctx, "kid@example.com"); u.IsAdmin {
		t.Error("leaving the admin group kept admin rights")
	}
}

func TestOIDCRefusals(t *testing.T) {
	e, p := oidcEnv(t)
	ctx := context.Background()
	// not in the required group
	if rec := p.signIn(t, e, map[string]any{"email": "x@example.com", "email_verified": true, "groups": []string{"strangers"}}); sessionOf(rec) != nil || rec.Code != http.StatusUnauthorized {
		t.Errorf("outsider: %d", rec.Code)
	}
	if rec := p.signIn(t, e, map[string]any{"email": "x@example.com", "email_verified": true}); sessionOf(rec) != nil {
		t.Error("no groups claim signed in")
	}
	// an existing account is linked only by a VERIFIED email
	e.addUser("bob@example.com")
	if rec := p.signIn(t, e, map[string]any{"email": "bob@example.com", "email_verified": false, "groups": []string{"family"}}); sessionOf(rec) != nil {
		t.Error("unverified email took over an account")
	}
	if rec := p.signIn(t, e, map[string]any{"email": "bob@example.com", "email_verified": true, "groups": []string{"family"}}); sessionOf(rec) == nil {
		t.Errorf("verified email did not link: %d", rec.Code)
	}
	// blocked users stay blocked
	bob, _ := e.db.UserByEmail(ctx, "bob@example.com")
	_ = e.db.SetUserDisabled(ctx, bob.ID, true)
	if rec := p.signIn(t, e, map[string]any{"groups": []string{"family"}}); sessionOf(rec) != nil {
		t.Error("blocked user signed in")
	}
	// state must match
	rec := e.get2("/login/oidc")
	if r := e.get2("/login/oidc/callback?code=abc&state=wrong", cookieNamed(rec, "vgs_oidc")); sessionOf(r) != nil {
		t.Error("wrong state accepted")
	}
	// no cookie: nothing
	if r := e.get2("/login/oidc/callback?code=abc&state=x"); sessionOf(r) != nil {
		t.Error("callback without a started sign-in worked")
	}
	// without a required group nobody new is created
	_ = e.db.SetSettings(ctx, map[string]string{kOIDCReq: ""})
	if rec := p.signIn(t, e, map[string]any{"email": "new@example.com", "email_verified": true}); sessionOf(rec) != nil {
		t.Error("account created without a gating group")
	}
}

func TestOIDCSettingsAndOff(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	if e.get2("/login/oidc").Code != http.StatusNotFound {
		t.Error("single sign-on reachable while off")
	}
	form := url.Values{"oidc_issuer": {"http://id.example.com"}, "oidc_client_id": {"cid"}}
	if rec := e.post2("/settings/sso", form, admin); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("http issuer accepted: %d", rec.Code)
	}
	form.Set("oidc_issuer", "https://id.example.com")
	form.Set("oidc_client_secret", "shh")
	if rec := e.post2("/settings/sso", form, admin); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("secret without key: %d", rec.Code)
	}
	e.srv.secrets, _ = secrets.New("a long enough test key 123")
	if rec := e.post2("/settings/sso", form, admin); rec.Code != 200 {
		t.Fatalf("save: %d", rec.Code)
	}
	m, _ := e.db.AllSettings(context.Background())
	if m[kOIDCSec] == "" || m[kOIDCSec] == "shh" || m[kOIDCGroupClaim] != "groups" {
		t.Errorf("stored %+v", m)
	}
	if body := e.get2("/settings", admin).Body.String(); strings.Contains(body, "shh") || strings.Contains(body, m[kOIDCSec]) {
		t.Error("secret shown on the settings page")
	}
}
