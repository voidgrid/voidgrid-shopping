package web

import (
	"context"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/voidgrid/voidgrid-shopping/internal/mailer"
)

// Stored setting keys for email and single sign-on. Secrets are sealed.
const (
	kSMTPHost, kSMTPPort, kSMTPSecurity = "smtp_host", "smtp_port", "smtp_security"
	kSMTPUser, kSMTPPass, kSMTPFrom     = "smtp_username", "smtp_password", "smtp_from"
	kOIDCIssuer, kOIDCClient, kOIDCSec  = "oidc_issuer", "oidc_client_id", "oidc_client_secret"
	kOIDCGroupClaim, kOIDCReq, kOIDCAdm = "oidc_group_claim", "oidc_required_group", "oidc_admin_group"
)

// ttlStore keeps short-lived values (pending sign-ins, challenges) in memory.
// They are lost on restart, which only means starting the sign-in again.
type ttlStore[T any] struct {
	mu sync.Mutex
	m  map[string]ttlItem[T]
}

type ttlItem[T any] struct {
	v   T
	exp time.Time
}

func (s *ttlStore[T]) put(id string, v T, now time.Time, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]ttlItem[T]{}
	}
	for k, it := range s.m {
		if it.exp.Before(now) {
			delete(s.m, k)
		}
	}
	if len(s.m) > 10000 {
		return
	}
	s.m[id] = ttlItem[T]{v, now.Add(ttl)}
}

// get returns the value; take also removes it so it works once.
func (s *ttlStore[T]) get(id string, now time.Time, take bool) (T, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.m[id]
	if !ok || it.exp.Before(now) {
		delete(s.m, id)
		var zero T
		return zero, false
	}
	if take {
		delete(s.m, id)
	}
	return it.v, true
}

func (s *Server) loadMail(ctx context.Context) (mailer.Config, error) {
	m, err := s.db.AllSettings(ctx)
	if err != nil {
		return mailer.Config{}, err
	}
	port, _ := strconv.Atoi(m[kSMTPPort])
	c := mailer.Config{Host: m[kSMTPHost], Port: port, Security: m[kSMTPSecurity], Username: m[kSMTPUser], From: m[kSMTPFrom]}
	if m[kSMTPPass] != "" {
		pw, err := s.secrets.Open(m[kSMTPPass])
		if err != nil {
			s.log.Error("smtp password unreadable", "err", err)
			return mailer.Config{}, nil // behaves as not configured
		}
		c.Password = pw
	}
	return c, nil
}

type smtpView struct {
	HasPassword bool
	NoKey       bool
	Configured  bool
}

type ssoView struct {
	HasSecret   bool
	NoKey       bool
	Callback    string
	Configured  bool
	AnyoneInIdP bool
}

func (s *Server) settingsExtras(ctx context.Context) (smtpView, ssoView, map[string]string) {
	m, _ := s.db.AllSettings(ctx)
	st, _ := s.loadSettings(ctx)
	form := map[string]string{}
	for _, k := range []string{kSMTPHost, kSMTPPort, kSMTPSecurity, kSMTPUser, kSMTPFrom, kOIDCIssuer, kOIDCClient, kOIDCGroupClaim, kOIDCReq, kOIDCAdm} {
		form[k] = m[k]
	}
	if form[kSMTPSecurity] == "" {
		form[kSMTPSecurity] = mailer.StartTLS
	}
	if form[kOIDCGroupClaim] == "" {
		form[kOIDCGroupClaim] = "groups"
	}
	mc, _ := s.loadMail(ctx)
	sv := smtpView{HasPassword: m[kSMTPPass] != "", NoKey: !s.secrets.Available(), Configured: mc.Configured()}
	ov := ssoView{
		HasSecret: m[kOIDCSec] != "", NoKey: !s.secrets.Available(), Configured: m[kOIDCIssuer] != "" && m[kOIDCClient] != "" && m[kOIDCSec] != "",
		AnyoneInIdP: m[kOIDCReq] == "",
	}
	if st.PublicBaseURL != "" {
		ov.Callback = st.PublicBaseURL + "/login/oidc/callback"
	}
	return sv, ov, form
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, status int, notice, errMsg string, errs map[string]string, override map[string]string) {
	st, err := s.loadSettings(r.Context())
	if err != nil {
		s.serverError(w, "load settings", err)
		return
	}
	usda, _ := s.db.USDAStatus(r.Context())
	sv, ov, form := s.settingsExtras(r.Context())
	for k, v := range st.Map() {
		form[k] = v
	}
	for k, v := range override {
		form[k] = v
	}
	s.render(w, r, status, "settings", page{
		Title: "Settings", Nav: "settings", Form: form, Errors: errs, Notice: notice, Error: errMsg,
		Weekdays: weekdayChoices(st.WeekStart), USDA: usda, Mail: sv, SSO: ov,
	})
}

func (s *Server) handleEmailSave(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	get := func(k string) string { return strings.TrimSpace(r.PostForm.Get(k)) }
	errs := map[string]string{}
	host, from, user := get(kSMTPHost), get(kSMTPFrom), get(kSMTPUser)
	port, err := strconv.Atoi(get(kSMTPPort))
	sec := get(kSMTPSecurity)
	if host != "" {
		if len(host) > 200 || strings.ContainsAny(host, " /\\") {
			errs[kSMTPHost] = "Enter the server name, such as smtp.example.com."
		}
		if err != nil || port < 1 || port > 65535 {
			errs[kSMTPPort] = "Enter a port from 1 to 65535."
		}
		if sec != mailer.StartTLS && sec != mailer.TLS && sec != mailer.None {
			errs[kSMTPSecurity] = "Choose a connection type."
		}
		if a, e := mail.ParseAddress(from); e != nil || len(from) > 200 || strings.ContainsAny(from, "\r\n") || a == nil {
			errs[kSMTPFrom] = "Enter the address the mail comes from, such as shopping@example.com."
		}
		if len(user) > 200 || strings.ContainsAny(user, "\r\n") {
			errs[kSMTPUser] = "That user name is not valid."
		}
	}
	over := map[string]string{kSMTPHost: host, kSMTPPort: get(kSMTPPort), kSMTPSecurity: sec, kSMTPUser: user, kSMTPFrom: from}
	pass := r.PostForm.Get(kSMTPPass)
	vals := map[string]string{kSMTPHost: host, kSMTPPort: strconv.Itoa(port), kSMTPSecurity: sec, kSMTPUser: user, kSMTPFrom: from}
	if host == "" {
		vals = map[string]string{kSMTPHost: "", kSMTPPort: "", kSMTPSecurity: "", kSMTPUser: "", kSMTPFrom: "", kSMTPPass: ""}
	} else if pass != "" {
		sealed, e := s.secrets.Seal(pass)
		if e != nil {
			errs[kSMTPPass] = "Set VGS_SECRET_KEY (see the README) before saving a password."
		} else {
			vals[kSMTPPass] = sealed
		}
	}
	if len(errs) > 0 {
		s.renderSettings(w, r, http.StatusUnprocessableEntity, "", "Email settings need fixing; nothing was saved.", errs, over)
		return
	}
	if err := s.db.SetSettings(r.Context(), vals); err != nil {
		s.serverError(w, "save email settings", err)
		return
	}
	s.renderSettings(w, r, http.StatusOK, "Email settings saved.", "", nil, nil)
}

func (s *Server) handleEmailTest(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	to := userFrom(r.Context()).Email
	c, err := s.loadMail(r.Context())
	if err != nil {
		s.serverError(w, "load mail", err)
		return
	}
	if !c.Configured() {
		s.renderSettings(w, r, http.StatusUnprocessableEntity, "", "Save the email settings first.", nil, nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.send(ctx, c, to, "Test message from voidgrid shopping", "If you can read this, email works."); err != nil {
		s.renderSettings(w, r, http.StatusBadGateway, "", "The test email failed: "+err.Error(), nil, nil)
		return
	}
	s.renderSettings(w, r, http.StatusOK, "Test email sent to "+to+".", "", nil, nil)
}

func (s *Server) handleSSOSave(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	get := func(k string) string { return strings.TrimSpace(r.PostForm.Get(k)) }
	errs := map[string]string{}
	issuer, client := strings.TrimRight(get(kOIDCIssuer), "/"), get(kOIDCClient)
	claim, req, adm := get(kOIDCGroupClaim), get(kOIDCReq), get(kOIDCAdm)
	if claim == "" {
		claim = "groups"
	}
	over := map[string]string{kOIDCIssuer: issuer, kOIDCClient: client, kOIDCGroupClaim: claim, kOIDCReq: req, kOIDCAdm: adm}
	vals := map[string]string{kOIDCIssuer: issuer, kOIDCClient: client, kOIDCGroupClaim: claim, kOIDCReq: req, kOIDCAdm: adm}
	if issuer != "" {
		if u, e := url.Parse(issuer); e != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			errs[kOIDCIssuer] = "Enter the provider's https address, such as https://id.example.com."
		}
		if client == "" || len(client) > 300 {
			errs[kOIDCClient] = "Enter the client ID."
		}
		if len(claim) > 100 || len(req) > 200 || len(adm) > 200 {
			errs[kOIDCGroupClaim] = "That is too long."
		}
	}
	if issuer == "" {
		vals = map[string]string{kOIDCIssuer: "", kOIDCClient: "", kOIDCSec: "", kOIDCGroupClaim: "", kOIDCReq: "", kOIDCAdm: ""}
	} else if sec := r.PostForm.Get(kOIDCSec); sec != "" {
		sealed, e := s.secrets.Seal(sec)
		if e != nil {
			errs[kOIDCSec] = "Set VGS_SECRET_KEY (see the README) before saving a client secret."
		} else {
			vals[kOIDCSec] = sealed
		}
	}
	if len(errs) > 0 {
		s.renderSettings(w, r, http.StatusUnprocessableEntity, "", "Single sign-on settings need fixing; nothing was saved.", errs, over)
		return
	}
	if err := s.db.SetSettings(r.Context(), vals); err != nil {
		s.serverError(w, "save sso settings", err)
		return
	}
	s.oidcReset()
	s.renderSettings(w, r, http.StatusOK, "Single sign-on settings saved.", "", nil, nil)
}
