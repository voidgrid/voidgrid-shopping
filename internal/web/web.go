// Package web is the HTTP server: sign-in, first-time setup, the pages and
// the admin Settings tab.
package web

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/voidgrid/voidgrid-shopping/internal/auth"
	"github.com/voidgrid/voidgrid-shopping/internal/importer"
	"github.com/voidgrid/voidgrid-shopping/internal/mailer"
	"github.com/voidgrid/voidgrid-shopping/internal/openfoodfacts"
	"github.com/voidgrid/voidgrid-shopping/internal/photos"
	"github.com/voidgrid/voidgrid-shopping/internal/safehttp"
	"github.com/voidgrid/voidgrid-shopping/internal/secrets"
	"github.com/voidgrid/voidgrid-shopping/internal/settings"
	"github.com/voidgrid/voidgrid-shopping/internal/social"
	"github.com/voidgrid/voidgrid-shopping/internal/storage"
	webassets "github.com/voidgrid/voidgrid-shopping/web"
)

const (
	sessionCookie = "vgs_session"
	maxFormBytes  = 64 << 10
)

// Options configure a Server.
type Options struct {
	Log        *slog.Logger
	TrustProxy bool
	// Now is the clock; tests replace it.
	Now func() time.Time
	// SetupToken, if set, is used instead of a random one (tests).
	SetupToken string
	// PhotoDir is where recipe photos are stored.
	PhotoDir string
	// Off is the Open Food Facts client; nil uses the public service.
	Off *openfoodfacts.Client
	// Fetch is the client for importing web pages; nil refuses private addresses.
	Fetch *http.Client
	// YtDlp is the yt-dlp program for TikTok captions.
	YtDlp string
	// Social overrides the TikTok reader (tests).
	Social *social.Reader
	// Secrets encrypts stored secrets; nil means no key was supplied.
	Secrets *secrets.Box
	// Send overrides email sending (tests).
	Send func(ctx context.Context, c mailer.Config, to, subject, body string) error
}

// Server serves the app.
type Server struct {
	db         *storage.DB
	log        *slog.Logger
	tmpl       map[string]*template.Template
	limiter    *limiter
	trustProxy bool
	now        func() time.Time
	setupToken string
	photos     *photos.Store
	photoKick  chan struct{}
	off        *openfoodfacts.Client
	fetch      *http.Client
	social     *social.Reader
	secrets    *secrets.Box
	send       func(ctx context.Context, c mailer.Config, to, subject, body string) error
	pending    ttlStore[pendingLogin]
	waReg      ttlStore[webauthn.SessionData]
	waLogin    ttlStore[webauthn.SessionData]
	oidcState  ttlStore[oidcPending]
	oidcc      oidcCache
	shareRate  *limiter
	shareBad   *limiter
}

type ctxKey struct{}

// New builds the server. When no account exists yet it creates a one-time
// setup token and logs the setup path.
func New(ctx context.Context, db *storage.DB, o Options) (*Server, error) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Server{
		db:         db,
		log:        o.Log,
		limiter:    newLimiter(10, 15*time.Minute),
		shareRate:  newLimiter(120, time.Minute),
		shareBad:   newLimiter(20, 15*time.Minute),
		trustProxy: o.TrustProxy,
		now:        o.Now,
		tmpl:       map[string]*template.Template{},
		photos:     photos.New(o.PhotoDir, importer.PlanToEatHost),
		photoKick:  make(chan struct{}, 1),
		off:        o.Off,
		fetch:      o.Fetch,
		social:     o.Social,
		secrets:    o.Secrets,
		send:       o.Send,
	}
	if s.send == nil {
		s.send = mailer.Send
	}
	s.photos.Public = true
	if s.fetch == nil {
		s.fetch = safehttp.New(false)
	}
	if s.social == nil {
		s.social = &social.Reader{Bin: o.YtDlp}
	}
	if s.off == nil {
		s.off = openfoodfacts.New("", 10)
	}
	for _, name := range []string{
		"share", "share_gone", "share_manage", "login", "login_2fa", "invite", "reset", "link_gone", "users", "account", "setup", "settings", "recipes", "recipe", "recipe_form", "import", "nutrition", "nutrition_link", "nutrition_product", "planner", "planner_add", "list", "list_edit", "stores", "staples", "staple_edit",
	} {
		t, err := template.New("").Funcs(templateFuncs).ParseFS(webassets.FS, "templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("web: parse template %s: %w", name, err)
		}
		s.tmpl[name] = t
	}

	n, err := db.CountUsers(ctx)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		tok := o.SetupToken
		if tok == "" {
			if tok, err = auth.NewToken(); err != nil {
				return nil, err
			}
		}
		s.setupToken = tok
		s.log.Info("no accounts yet: open the setup page to create the admin", "path", "/setup?token="+tok)
	}
	return s, nil
}

// Handler returns the full HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, err := fs.Sub(webassets.FS, "static")
	if err != nil {
		panic(err) // the embedded tree is fixed at build time
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServerFS(static))))
	mux.HandleFunc("GET /healthz", s.handleHealth)

	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /setup", s.handleSetupForm)
	mux.HandleFunc("POST /setup", s.handleSetup)

	mux.HandleFunc("GET /{$}", s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/recipes", http.StatusSeeOther)
	}))
	s.routeRecipes(mux)
	s.routeShares(mux)
	s.routePlanner(mux)
	s.routeShopping(mux)
	s.routeNutrition(mux)
	mux.HandleFunc("GET /login/2fa", s.handle2FAForm)
	mux.HandleFunc("POST /login/2fa", s.handle2FA)
	mux.HandleFunc("POST /login/passkey/begin", s.handleLoginPasskeyBegin)
	mux.HandleFunc("POST /login/passkey/finish", s.handleLoginPasskeyFinish)
	mux.HandleFunc("GET /login/oidc", s.handleOIDCStart)
	mux.HandleFunc("GET /login/oidc/callback", s.handleOIDCCallback)
	mux.HandleFunc("GET /invite/{token}", s.handleInviteForm)
	mux.HandleFunc("POST /invite/{token}", s.handleInviteAccept)
	mux.HandleFunc("GET /reset/{token}", s.handleResetForm)
	mux.HandleFunc("POST /reset/{token}", s.handleResetAccept)
	mux.HandleFunc("GET /account", s.requireUser(s.handleAccount))
	mux.HandleFunc("POST /account/password", s.requireUser(s.handlePasswordChange))
	mux.HandleFunc("POST /account/totp/start", s.requireUser(s.handleTOTPStart))
	mux.HandleFunc("POST /account/totp/confirm", s.requireUser(s.handleTOTPConfirm))
	mux.HandleFunc("POST /account/totp/disable", s.requireUser(s.handleTOTPDisable))
	mux.HandleFunc("POST /account/totp/recovery", s.requireUser(s.handleRecoveryRegenerate))
	mux.HandleFunc("POST /account/passkeys/begin", s.requireUser(s.handlePasskeyBegin))
	mux.HandleFunc("POST /account/passkeys/finish", s.requireUser(s.handlePasskeyFinish))
	mux.HandleFunc("POST /account/passkeys/delete", s.requireUser(s.handlePasskeyDelete))
	mux.HandleFunc("GET /users", s.requireAdmin(s.handleUsers))
	mux.HandleFunc("POST /users/invite", s.requireAdmin(s.handleInvite))
	mux.HandleFunc("POST /users/reset", s.requireAdmin(s.handleResetLink))
	mux.HandleFunc("POST /users/links/revoke", s.requireAdmin(s.handleLinkRevoke))
	mux.HandleFunc("POST /users/disable", s.requireAdmin(s.handleUserDisable))
	mux.HandleFunc("POST /settings/email", s.requireAdmin(s.handleEmailSave))
	mux.HandleFunc("POST /settings/email/test", s.requireAdmin(s.handleEmailTest))
	mux.HandleFunc("POST /settings/sso", s.requireAdmin(s.handleSSOSave))
	mux.HandleFunc("GET /settings", s.requireAdmin(s.handleSettingsForm))
	mux.HandleFunc("POST /settings", s.requireAdmin(s.handleSettingsSave))

	// Go's cross-origin protection rejects a browser's cross-site POST
	// (it checks Sec-Fetch-Site and Origin); non-browser clients pass.
	// A cap on every request body, on top of the tighter limits handlers set.
	capped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
		mux.ServeHTTP(w, r)
	})
	return securityHeaders(http.NewCrossOriginProtection().Handler(capped))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

// ---- rendering

type weekday struct {
	Value    int
	Name     string
	Selected bool
}

type step struct {
	Text    string
	Heading bool
}

type page struct {
	Title      string
	Nav        string
	User       *storage.User
	Error      string
	Notice     string
	Form       map[string]string
	IngRows    []formRow
	Mail       smtpView
	SSO        ssoView
	UV         usersView
	Share      shareView
	Acct       acctView
	Token      string
	Version    string
	OIDCOn     bool
	Errors     map[string]string
	SetupToken string
	NoUsers    bool
	Weekdays   []weekday

	Cards        []storage.RecipeCard
	Facets       storage.Facets
	Filtered     bool
	RandomQuery  string
	Recipe       storage.Recipe
	FormAction   string
	Report       *importReport
	Lines        []ingLine
	ServingsNow  int
	UnitsMode    string
	Scaled       bool
	USDA         storage.USDAStatus
	Planner      *plannerView
	List         *listView
	Item         *itemView
	Staple       *storage.Staple
	StapleGroups []stapleGroup
	StapleCount  int
	Nut          *nutView
	NutBox       *nutBox
	Link         *linkView
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, p page) {
	if p.User == nil {
		p.User = userFrom(r.Context())
	}
	if p.Form == nil {
		p.Form = map[string]string{}
	}
	if p.Errors == nil {
		p.Errors = map[string]string{}
	}
	if c, err := r.Cookie(flashCookie); err == nil && p.User != nil {
		if msg, err := url.QueryUnescape(c.Value); err == nil && p.Notice == "" && len(msg) < 300 {
			p.Notice = msg
		}
		http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	}
	if name == "recipe_form" {
		p.IngRows = rowsFromText(p.Form["ingredients"])
	}
	var buf bytes.Buffer
	if err := s.tmpl[name].ExecuteTemplate(&buf, "layout", p); err != nil {
		s.log.Error("render", "template", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

var templateFuncs = template.FuncMap{
	"seq": func(n int) []int {
		out := make([]int, n)
		for i := range out {
			out[i] = i + 1
		}
		return out
	},
	"lines": func(s string) []string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
		return out
	},
	"steps": func(s string) []step {
		var out []step
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, step{Text: strings.TrimSuffix(l, ":"), Heading: strings.HasSuffix(l, ":")})
			}
		}
		return out
	},
	"blankrow": func(sep bool) formRow { return formRow{Sep: sep} },
	"mins": func(n int) string {
		switch {
		case n <= 0:
			return ""
		case n < 60:
			return fmt.Sprintf("%d min", n)
		case n%60 == 0:
			return fmt.Sprintf("%d h", n/60)
		}
		return fmt.Sprintf("%d h %d min", n/60, n%60)
	},
	"money_c": money,
	"n0":      n0,
	"n1":      n1,
	"qe":      queryEscape,
	"host": func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		return strings.TrimPrefix(u.Hostname(), "www.")
	},
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'none'; img-src 'self' data:; style-src 'self'; script-src 'self'; "+
				"connect-src 'self'; font-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Robots-Tag", "noindex, nofollow")
		h.Set("Strict-Transport-Security", "max-age=31536000")
		h.Set("Permissions-Policy", "camera=(self), microphone=(), geolocation=(), payment=(), usb=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}

// ---- sessions and guards

func userFrom(ctx context.Context) *storage.User {
	u, _ := ctx.Value(ctxKey{}).(*storage.User)
	return u
}

// sessionUser returns the signed-in user for the request, or nil.
func (s *Server) sessionUser(r *http.Request) *storage.User {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	u, err := s.db.SessionUser(r.Context(), auth.HashToken(c.Value), s.now())
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			s.log.Error("session lookup", "err", err)
		}
		return nil
	}
	return &u
}

func (s *Server) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.sessionUser(r)
		if u == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	}
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		if !userFrom(r.Context()).IsAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	st, err := s.loadSettings(r.Context())
	if err != nil {
		return err
	}
	tok, err := auth.NewToken()
	if err != nil {
		return err
	}
	expires := s.now().Add(time.Duration(st.SessionHours) * time.Hour)
	if err := s.db.CreateSession(r.Context(), auth.HashToken(tok), userID, expires); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    tok,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   true, // HTTPS only, LAN users included; no plain-HTTP fallback
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *Server) loadSettings(ctx context.Context) (settings.Settings, error) {
	m, err := s.db.AllSettings(ctx)
	if err != nil {
		return settings.Settings{}, err
	}
	return settings.FromMap(m), nil
}

// ---- sign-in

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if s.sessionUser(r) != nil {
		http.Redirect(w, r, "/recipes", http.StatusSeeOther)
		return
	}
	p := page{Title: "Sign in", NoUsers: s.setupToken != ""}
	if _, ok := s.loadOIDC(r.Context()); ok {
		p.OIDCOn = true
	}
	if r.URL.Query().Get("reset") == "1" {
		p.Notice = "Password changed. Sign in with the new one."
	}
	if _, err := s.webauthn(r.Context()); err == nil {
		p.Form = map[string]string{"passkeys": "1"}
	}
	s.render(w, r, http.StatusOK, "login", p)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PostForm.Get("email")))
	password := r.PostForm.Get("password")
	form := map[string]string{"email": email}
	now := s.now()

	emailKey, ipKey := "e:"+email, "i:"+s.clientIP(r)
	if s.limiter.blocked(emailKey, now) || s.limiter.blocked(ipKey, now) {
		s.render(w, r, http.StatusTooManyRequests, "login", page{
			Title: "Sign in", Form: form,
			Error: "Too many failed attempts. Wait a few minutes and try again.",
		})
		return
	}

	fail := func() {
		s.limiter.fail(emailKey, now)
		s.limiter.fail(ipKey, now)
		s.render(w, r, http.StatusUnauthorized, "login", page{
			Title: "Sign in", Form: form, Error: "The email or password is not right.",
		})
	}
	if email == "" || len(password) > auth.MaxPasswordLength {
		fail()
		return
	}
	u, err := s.db.UserByEmail(r.Context(), email)
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			s.log.Error("login lookup", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		auth.SpendVerifyTime(password) // same cost whether or not the account exists
		fail()
		return
	}
	if u.Disabled || !auth.VerifyPassword(password, u.PasswordHash) {
		fail()
		return
	}
	s.limiter.reset(emailKey)
	s.afterPassword(w, r, u)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := s.db.DeleteSession(r.Context(), auth.HashToken(c.Value)); err != nil {
			s.log.Error("logout", "err", err)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// clientIP is the address the rate limiter keys on.
func (s *Server) clientIP(r *http.Request) string {
	if s.trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- first-time setup

// setupOpen reports whether the one-time setup page may be used.
func (s *Server) setupOpen(token string) bool {
	return s.setupToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.setupToken)) == 1
}

func (s *Server) handleSetupForm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if !s.setupOpen(token) {
		http.NotFound(w, r)
		return
	}
	s.render(w, r, http.StatusOK, "setup", page{Title: "Set up", SetupToken: token})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	token := r.PostForm.Get("token")
	if !s.setupOpen(token) {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("name"))
	email := strings.ToLower(strings.TrimSpace(r.PostForm.Get("email")))
	password := r.PostForm.Get("password")
	form := map[string]string{"name": name, "email": email}
	again := func(msg string) {
		s.render(w, r, http.StatusUnprocessableEntity, "setup", page{
			Title: "Set up", SetupToken: token, Form: form, Error: msg,
		})
	}
	switch {
	case name == "" || len(name) > 100:
		again("Enter your name.")
		return
	case !validEmail(email):
		again("Enter a valid email address.")
		return
	case password != r.PostForm.Get("confirm"):
		again("The two passwords don't match.")
		return
	}
	if msg := auth.CheckPasswordRules(password); msg != "" {
		again(msg)
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.log.Error("hash password", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	id, err := s.db.CreateFirstAdmin(r.Context(), email, name, hash)
	if err != nil {
		if errors.Is(err, storage.ErrSetupDone) {
			s.setupToken = ""
			http.NotFound(w, r)
			return
		}
		s.log.Error("create first admin", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.setupToken = "" // the setup link works once
	if err := s.startSession(w, r, id); err != nil {
		s.log.Error("start session", "err", err)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func validEmail(s string) bool {
	if len(s) > 254 {
		return false
	}
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && a.Name == ""
}

// ---- settings

var weekdayNames = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

func weekdayChoices(selected int) []weekday {
	out := make([]weekday, len(weekdayNames))
	for i, n := range weekdayNames {
		out[i] = weekday{Value: i, Name: n, Selected: i == selected}
	}
	return out
}

func (s *Server) handleSettingsForm(w http.ResponseWriter, r *http.Request) {
	notice := ""
	if r.URL.Query().Get("saved") == "1" {
		notice = "Settings saved."
	}
	s.renderSettings(w, r, http.StatusOK, notice, "", nil, nil)
}

func (s *Server) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	st, errs := settings.Parse(r.PostForm.Get)
	if len(errs) > 0 {
		form := map[string]string{}
		for _, k := range []string{
			settings.FieldPublicBaseURL, settings.FieldTimezone, settings.FieldUnits,
			settings.FieldWeekStart, settings.FieldSessionHours, settings.FieldShareDays,
		} {
			form[k] = r.PostForm.Get(k)
		}
		s.renderSettings(w, r, http.StatusUnprocessableEntity, "", "Some settings need fixing; nothing was saved.", errs, form)
		return
	}
	if err := s.db.SetSettings(r.Context(), st.Map()); err != nil {
		s.log.Error("save settings", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}
