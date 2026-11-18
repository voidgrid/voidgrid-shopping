package web

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"html/template"
	"net/http"
	"strings"

	"github.com/skip2/go-qrcode"
	"github.com/voidgrid/voidgrid-shopping/internal/auth"
	"github.com/voidgrid/voidgrid-shopping/internal/secrets"
	"github.com/voidgrid/voidgrid-shopping/internal/totp"
)

type passkeyRow struct {
	ID      int64
	Name    string
	Created int64
	Used    int64
}

type acctView struct {
	NoKey        bool
	TOTPOn       bool
	Recovery     int
	Passkeys     []passkeyRow
	Setup        bool         // a TOTP secret is waiting to be confirmed
	SetupSecret  string       // shown for typing in by hand
	SetupQR      template.URL // data: image
	NewRecovery  []string     // shown once after enabling
	HasPassword  bool
	CanPasskeys  bool
	PasskeyIssue string
}

func (s *Server) renderAccount(w http.ResponseWriter, r *http.Request, status int, v acctView, notice, errMsg string) {
	u := userFrom(r.Context())
	fresh, err := s.db.UserByID(r.Context(), u.ID)
	if err != nil {
		s.serverError(w, "account", err)
		return
	}
	v.NoKey = !s.secrets.Available()
	v.TOTPOn = fresh.TOTPEnabled
	v.HasPassword = fresh.PasswordHash != ""
	v.Recovery, _ = s.db.RecoveryCodeCount(r.Context(), u.ID)
	keys, _ := s.db.Passkeys(r.Context(), u.ID)
	for _, k := range keys {
		v.Passkeys = append(v.Passkeys, passkeyRow{ID: k.ID, Name: k.Name, Created: k.CreatedAt, Used: k.LastUsedAt})
	}
	if _, err := s.webauthn(r.Context()); err != nil {
		v.PasskeyIssue = err.Error()
	} else {
		v.CanPasskeys = true
	}
	s.render(w, r, status, "account", page{Title: "Account", Nav: "account", Acct: v, Notice: notice, Error: errMsg})
}

func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	s.renderAccount(w, r, http.StatusOK, acctView{}, "", "")
}

func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	u := userFrom(r.Context())
	key := "p:" + u.Email
	if s.limiter.blocked(key, s.now()) {
		s.renderAccount(w, r, http.StatusTooManyRequests, acctView{}, "", "Too many attempts. Wait a few minutes.")
		return
	}
	if !auth.VerifyPassword(r.PostForm.Get("current"), u.PasswordHash) {
		s.limiter.fail(key, s.now())
		s.renderAccount(w, r, http.StatusUnauthorized, acctView{}, "", "The current password is not right.")
		return
	}
	pw := r.PostForm.Get("password")
	msg := ""
	if pw != r.PostForm.Get("confirm") {
		msg = "The two new passwords don't match."
	} else {
		msg = auth.CheckPasswordRules(pw)
	}
	if msg != "" {
		s.renderAccount(w, r, http.StatusUnprocessableEntity, acctView{}, "", msg)
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		s.serverError(w, "hash password", err)
		return
	}
	if err := s.db.SetPassword(r.Context(), u.ID, hash); err != nil { // also ends every session
		s.serverError(w, "set password", err)
		return
	}
	if err := s.startSession(w, r, u.ID); err != nil {
		s.serverError(w, "start session", err)
		return
	}
	s.flashTo(w, r, "/account", "Password changed. Your other sign-ins were ended.")
}

// confirmPassword checks the signed-in user's password for a sensitive change.
func (s *Server) confirmPassword(r *http.Request) bool {
	u := userFrom(r.Context())
	key := "p:" + u.Email
	if s.limiter.blocked(key, s.now()) {
		return false
	}
	if u.PasswordHash == "" || !auth.VerifyPassword(r.PostForm.Get("current"), u.PasswordHash) {
		s.limiter.fail(key, s.now())
		return false
	}
	return true
}

func (s *Server) handleTOTPStart(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	u := userFrom(r.Context())
	secret, err := totp.NewSecret()
	if err != nil {
		s.serverError(w, "totp secret", err)
		return
	}
	sealed, err := s.secrets.Seal(secret)
	if errors.Is(err, secrets.ErrNoKey) {
		s.renderAccount(w, r, http.StatusUnprocessableEntity, acctView{}, "", "Two-step codes need VGS_SECRET_KEY to be set on the server (see the README).")
		return
	}
	if err != nil {
		s.serverError(w, "seal secret", err)
		return
	}
	if err := s.db.SetTOTPSecret(r.Context(), u.ID, sealed); err != nil {
		s.serverError(w, "save totp secret", err)
		return
	}
	s.renderTOTPSetup(w, r, secret, "")
}

func (s *Server) renderTOTPSetup(w http.ResponseWriter, r *http.Request, secret, errMsg string) {
	u := userFrom(r.Context())
	png, err := qrcode.Encode(totp.URI("voidgrid shopping", u.Email, secret), qrcode.Medium, 220)
	if err != nil {
		s.serverError(w, "qr", err)
		return
	}
	status := http.StatusOK
	if errMsg != "" {
		status = http.StatusUnprocessableEntity
	}
	s.renderAccount(w, r, status, acctView{
		Setup: true, SetupSecret: groups4(secret),
		SetupQR: template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png)), //nolint:gosec // our own PNG
	}, "", errMsg)
}

func groups4(s string) string {
	var b strings.Builder
	for i, c := range s {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	u := userFrom(r.Context())
	sealed, enabled, err := s.db.TOTPSecret(r.Context(), u.ID)
	if err != nil || sealed == "" || enabled {
		s.renderAccount(w, r, http.StatusUnprocessableEntity, acctView{}, "", "Start again: choose Set up two-step codes.")
		return
	}
	secret, err := s.secrets.Open(sealed)
	if err != nil {
		s.renderAccount(w, r, http.StatusUnprocessableEntity, acctView{}, "", "The stored secret could not be read; start again.")
		return
	}
	step, ok := totp.Verify(secret, r.PostForm.Get("code"), s.now())
	if !ok {
		s.renderTOTPSetup(w, r, secret, "That code is not right. Check the time on your phone and try the next code.")
		return
	}
	codes, hashes, err := newRecoveryCodes(10)
	if err != nil {
		s.serverError(w, "recovery codes", err)
		return
	}
	if err := s.db.EnableTOTP(r.Context(), u.ID, hashes); err != nil {
		s.serverError(w, "enable totp", err)
		return
	}
	_, _ = s.db.AcceptTOTPStep(r.Context(), u.ID, step)
	s.renderAccount(w, r, http.StatusOK, acctView{NewRecovery: codes}, "Two-step codes are on. Save these recovery codes now; they are shown only once.", "")
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	if !s.confirmPassword(r) {
		s.renderAccount(w, r, http.StatusUnauthorized, acctView{}, "", "The password is not right.")
		return
	}
	if err := s.db.DisableTOTP(r.Context(), userFrom(r.Context()).ID); err != nil {
		s.serverError(w, "disable totp", err)
		return
	}
	s.flashTo(w, r, "/account", "Two-step codes are off.")
}

// newRecoveryCodes makes n single-use codes; only their hashes are stored.
func newRecoveryCodes(n int) (codes, hashes []string, err error) {
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	for range n {
		b := make([]byte, 7)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, err
		}
		s := strings.ToLower(enc.EncodeToString(b)) // 12 characters
		code := s[:6] + "-" + s[6:]
		codes = append(codes, code)
		hashes = append(hashes, auth.HashToken(normalizeRecovery(code)))
	}
	return codes, hashes, nil
}

func normalizeRecovery(s string) string {
	return strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(s)))
}

func (s *Server) handleRecoveryRegenerate(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	u := userFrom(r.Context())
	if !s.confirmPassword(r) {
		s.renderAccount(w, r, http.StatusUnauthorized, acctView{}, "", "The password is not right.")
		return
	}
	if !u.TOTPEnabled {
		http.NotFound(w, r)
		return
	}
	codes, hashes, err := newRecoveryCodes(10)
	if err != nil {
		s.serverError(w, "recovery codes", err)
		return
	}
	if err := s.db.EnableTOTP(r.Context(), u.ID, hashes); err != nil {
		s.serverError(w, "recovery codes", err)
		return
	}
	s.renderAccount(w, r, http.StatusOK, acctView{NewRecovery: codes}, "New recovery codes. The old ones no longer work.", "")
}

func (s *Server) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	var id int64
	for _, c := range r.PostForm.Get("id") {
		if c < '0' || c > '9' || id > 1<<40 {
			http.NotFound(w, r)
			return
		}
		id = id*10 + int64(c-'0')
	}
	if err := s.db.DeletePasskey(r.Context(), userFrom(r.Context()).ID, id); err != nil {
		http.NotFound(w, r)
		return
	}
	s.flashTo(w, r, "/account", "Passkey removed.")
}
