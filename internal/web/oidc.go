package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/voidgrid/voidgrid-shopping/internal/auth"
	"github.com/voidgrid/voidgrid-shopping/internal/storage"
	"golang.org/x/oauth2"
)

type oidcPending struct{ state, nonce, verifier string }

type oidcCache struct {
	mu       sync.Mutex
	provider *oidc.Provider
	issuer   string
}

func (s *Server) oidcReset() {
	s.oidcc.mu.Lock()
	s.oidcc.provider = nil
	s.oidcc.mu.Unlock()
}

type oidcConf struct {
	issuer, client, secret, claim, required, admin string
}

func (s *Server) loadOIDC(ctx context.Context) (oidcConf, bool) {
	m, err := s.db.AllSettings(ctx)
	if err != nil || m[kOIDCIssuer] == "" || m[kOIDCClient] == "" || m[kOIDCSec] == "" {
		return oidcConf{}, false
	}
	sec, err := s.secrets.Open(m[kOIDCSec])
	if err != nil {
		s.log.Error("oidc secret unreadable", "err", err)
		return oidcConf{}, false
	}
	claim := m[kOIDCGroupClaim]
	if claim == "" {
		claim = "groups"
	}
	return oidcConf{m[kOIDCIssuer], m[kOIDCClient], sec, claim, m[kOIDCReq], m[kOIDCAdm]}, true
}

func (s *Server) oidcProvider(ctx context.Context, c oidcConf) (*oidc.Provider, error) {
	s.oidcc.mu.Lock()
	defer s.oidcc.mu.Unlock()
	if s.oidcc.provider != nil && s.oidcc.issuer == c.issuer {
		return s.oidcc.provider, nil
	}
	ctx = oidc.ClientContext(ctx, &http.Client{Timeout: 15 * time.Second})
	p, err := oidc.NewProvider(ctx, c.issuer)
	if err != nil {
		return nil, err
	}
	s.oidcc.provider, s.oidcc.issuer = p, c.issuer
	return p, nil
}

func (s *Server) oauthConfig(ctx context.Context, c oidcConf, p *oidc.Provider) (*oauth2.Config, error) {
	base := s.linkBase(ctx)
	if base == "" {
		return nil, errors.New("no public address")
	}
	return &oauth2.Config{
		ClientID: c.client, ClientSecret: c.secret, Endpoint: p.Endpoint(),
		RedirectURL: base + "/login/oidc/callback", Scopes: []string{oidc.ScopeOpenID, "profile", "email", "groups"},
	}, nil
}

func (s *Server) ssoFail(w http.ResponseWriter, r *http.Request, msg string) {
	s.render(w, r, http.StatusUnauthorized, "login", page{Title: "Sign in", Error: msg, OIDCOn: true})
}

func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadOIDC(r.Context())
	if !ok {
		http.NotFound(w, r)
		return
	}
	p, err := s.oidcProvider(r.Context(), c)
	if err != nil {
		s.log.Error("oidc discovery", "err", err)
		s.ssoFail(w, r, "Single sign-on is not reachable right now.")
		return
	}
	cfg, err := s.oauthConfig(r.Context(), c, p)
	if err != nil {
		s.ssoFail(w, r, "Single sign-on needs the public address to be set.")
		return
	}
	state, _ := auth.NewToken()
	nonce, _ := auth.NewToken()
	id, err := auth.NewToken()
	if err != nil || state == "" || nonce == "" {
		s.serverError(w, "oidc state", err)
		return
	}
	verifier := oauth2.GenerateVerifier()
	s.oidcState.put(auth.HashToken(id), oidcPending{state, nonce, verifier}, s.now(), 10*time.Minute)
	http.SetCookie(w, &http.Cookie{Name: "vgs_oidc", Value: id, Path: "/login/oidc", MaxAge: 600, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, cfg.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadOIDC(r.Context())
	ck, cerr := r.Cookie("vgs_oidc")
	if !ok || cerr != nil {
		http.NotFound(w, r)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "vgs_oidc", Value: "", Path: "/login/oidc", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	pend, ok := s.oidcState.get(auth.HashToken(ck.Value), s.now(), true)
	q := r.URL.Query()
	if !ok || q.Get("state") == "" || q.Get("state") != pend.state {
		s.ssoFail(w, r, "That sign-in attempt expired. Try again.")
		return
	}
	if e := q.Get("error"); e != "" {
		s.ssoFail(w, r, "The provider refused the sign-in.")
		return
	}
	ctx := oidc.ClientContext(r.Context(), &http.Client{Timeout: 15 * time.Second})
	p, err := s.oidcProvider(ctx, c)
	if err != nil {
		s.ssoFail(w, r, "Single sign-on is not reachable right now.")
		return
	}
	cfg, err := s.oauthConfig(r.Context(), c, p)
	if err != nil {
		s.ssoFail(w, r, "Single sign-on needs the public address to be set.")
		return
	}
	tok, err := cfg.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(pend.verifier))
	if err != nil {
		s.log.Warn("oidc exchange", "err", err)
		s.ssoFail(w, r, "The sign-in could not be completed.")
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := p.Verifier(&oidc.Config{ClientID: c.client}).Verify(ctx, raw)
	if err != nil || idt.Nonce != pend.nonce {
		s.log.Warn("oidc verify", "err", err)
		s.ssoFail(w, r, "The sign-in could not be verified.")
		return
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		s.ssoFail(w, r, "The sign-in could not be read.")
		return
	}
	if _, has := claims[c.claim]; !has {
		if ui, err := p.UserInfo(ctx, oauth2.StaticTokenSource(tok)); err == nil {
			var uc map[string]any
			if ui.Claims(&uc) == nil && uc["sub"] == idt.Subject {
				for k, v := range uc {
					if _, set := claims[k]; !set {
						claims[k] = v
					}
				}
			}
		}
	}
	u, err := s.oidcUser(r.Context(), c, idt.Issuer, idt.Subject, claims)
	if err != nil {
		s.log.Info("oidc sign-in refused", "reason", err)
		s.ssoFail(w, r, err.Error())
		return
	}
	if err := s.startSession(w, r, u.ID); err != nil {
		s.serverError(w, "start session", err)
		return
	}
	http.Redirect(w, r, "/recipes", http.StatusSeeOther)
}

func claimStrings(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// oidcUser maps a verified provider identity to an account, applying the
// group rules. Errors are shown to the person, so they say little.
func (s *Server) oidcUser(ctx context.Context, c oidcConf, issuer, sub string, claims map[string]any) (storage.User, error) {
	groups := claimStrings(claims[c.claim])
	if c.required != "" && !contains(groups, c.required) {
		return storage.User{}, shownError("This account is not allowed to use this app.")
	}
	admin := c.admin != "" && contains(groups, c.admin)
	email := strings.ToLower(strings.TrimSpace(firstString(claims["email"])))
	verified, _ := claims["email_verified"].(bool)

	u, err := s.db.IdentityUser(ctx, issuer, sub)
	switch {
	case err == nil:
	case !errors.Is(err, storage.ErrNotFound):
		return storage.User{}, shownError("Sign-in failed. Try again.")
	default:
		// first sign-in: link by a verified email, else (only when a group gates access) create the account
		var found bool
		if email != "" && verified {
			if byMail, e := s.db.UserByEmail(ctx, email); e == nil {
				u, found = byMail, true
			}
		}
		if !found {
			if c.required == "" || !validEmail(email) {
				return storage.User{}, shownError("No account here matches that sign-in. Ask for an invite.")
			}
			name := firstString(claims["name"])
			if name == "" {
				name = firstString(claims["preferred_username"])
			}
			if name == "" {
				name = email
			}
			id, e := s.db.CreateUser(ctx, email, truncateName(name), "", admin)
			if e != nil {
				return storage.User{}, shownError("That email already has an account here; sign in with your password once to link it, or ask the admin.")
			}
			if u, e = s.db.UserByID(ctx, id); e != nil {
				return storage.User{}, shownError("Sign-in failed. Try again.")
			}
		}
		if err := s.db.LinkIdentity(ctx, issuer, sub, u.ID); err != nil {
			return storage.User{}, shownError("Sign-in failed. Try again.")
		}
	}
	if u.Disabled {
		return storage.User{}, shownError("This account is blocked.")
	}
	if c.admin != "" && u.IsAdmin != admin {
		if err := s.db.SetUserAdmin(ctx, u.ID, admin); err == nil {
			u.IsAdmin = admin
		}
	}
	return u, nil
}

func firstString(v any) string {
	s, _ := v.(string)
	return s
}

func truncateName(s string) string {
	if len(s) > 100 {
		return strings.ToValidUTF8(s[:100], "")
	}
	return s
}
