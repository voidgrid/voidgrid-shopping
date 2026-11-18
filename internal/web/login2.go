package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/voidgrid/voidgrid-shopping/internal/auth"
	"github.com/voidgrid/voidgrid-shopping/internal/storage"
	"github.com/voidgrid/voidgrid-shopping/internal/totp"
)

const (
	pendingCookie = "vgs_2fa"
	waCookie      = "vgs_wa"
	pendingTTL    = 5 * time.Minute
)

type pendingLogin struct{ uid int64 }

func (s *Server) setShortCookie(w http.ResponseWriter, name, val string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: val, Path: "/", MaxAge: int(pendingTTL.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}

// afterPassword runs once the password is right: it starts the session, or
// asks for the second step first when the account has two-step codes on.
func (s *Server) afterPassword(w http.ResponseWriter, r *http.Request, u storage.User) {
	if u.TOTPEnabled {
		id, err := auth.NewToken()
		if err != nil {
			s.serverError(w, "pending token", err)
			return
		}
		s.pending.put(auth.HashToken(id), pendingLogin{u.ID}, s.now(), pendingTTL)
		s.setShortCookie(w, pendingCookie, id)
		http.Redirect(w, r, "/login/2fa", http.StatusSeeOther)
		return
	}
	if err := s.startSession(w, r, u.ID); err != nil {
		s.serverError(w, "start session", err)
		return
	}
	http.Redirect(w, r, "/recipes", http.StatusSeeOther)
}

func (s *Server) pendingUser(r *http.Request, take bool) (storage.User, bool) {
	c, err := r.Cookie(pendingCookie)
	if err != nil {
		return storage.User{}, false
	}
	p, ok := s.pending.get(auth.HashToken(c.Value), s.now(), take)
	if !ok {
		return storage.User{}, false
	}
	u, err := s.db.UserByID(r.Context(), p.uid)
	if err != nil || u.Disabled {
		return storage.User{}, false
	}
	return u, true
}

func (s *Server) handle2FAForm(w http.ResponseWriter, r *http.Request) {
	u, ok := s.pendingUser(r, false)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render2FA(w, r, http.StatusOK, u, "")
}

func (s *Server) render2FA(w http.ResponseWriter, r *http.Request, status int, u storage.User, msg string) {
	keys, _ := s.db.Passkeys(r.Context(), u.ID)
	_, err := s.webauthn(r.Context())
	s.render(w, r, status, "login_2fa", page{Title: "Two-step sign-in", Error: msg, Form: map[string]string{"passkeys": boolStr(len(keys) > 0 && err == nil)}})
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return ""
}

func (s *Server) handle2FA(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	u, ok := s.pendingUser(r, false)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	now := s.now()
	uKey, ipKey := "2:"+strconv.FormatInt(u.ID, 10), "i:"+s.clientIP(r)
	if s.limiter.blocked(uKey, now) || s.limiter.blocked(ipKey, now) {
		s.render2FA(w, r, http.StatusTooManyRequests, u, "Too many wrong codes. Wait a few minutes and try again.")
		return
	}
	bad := func() {
		s.limiter.fail(uKey, now)
		s.limiter.fail(ipKey, now)
		s.render2FA(w, r, http.StatusUnauthorized, u, "That code is not right.")
	}
	input := strings.TrimSpace(r.PostForm.Get("code"))
	digits := strings.ReplaceAll(input, " ", "")
	valid := false
	if len(digits) == 6 && strings.Trim(digits, "0123456789") == "" {
		sealed, on, err := s.db.TOTPSecret(r.Context(), u.ID)
		if err == nil && on {
			if secret, err := s.secrets.Open(sealed); err != nil {
				s.log.Error("totp secret unreadable", "user", u.ID, "err", err)
			} else if step, ok := totp.Verify(secret, digits, now); ok {
				valid, _ = s.db.AcceptTOTPStep(r.Context(), u.ID, step)
			}
		}
	} else if len(input) <= 20 {
		valid, _ = s.db.UseRecoveryCode(r.Context(), u.ID, auth.HashToken(normalizeRecovery(input)))
	}
	if !valid {
		bad()
		return
	}
	s.pendingUser(r, true)
	clearCookie(w, pendingCookie)
	s.limiter.reset(uKey)
	if err := s.startSession(w, r, u.ID); err != nil {
		s.serverError(w, "start session", err)
		return
	}
	http.Redirect(w, r, "/recipes", http.StatusSeeOther)
}

// ---- passkeys (WebAuthn)

type waUser struct {
	u     storage.User
	creds []webauthn.Credential
}

func (w waUser) WebAuthnID() []byte                         { return []byte(strconv.FormatInt(w.u.ID, 10)) }
func (w waUser) WebAuthnName() string                       { return w.u.Email }
func (w waUser) WebAuthnDisplayName() string                { return w.u.DisplayName }
func (w waUser) WebAuthnCredentials() []webauthn.Credential { return w.creds }

func (s *Server) credsOf(ctx context.Context, id int64) ([]webauthn.Credential, error) {
	keys, err := s.db.Passkeys(ctx, id)
	if err != nil {
		return nil, err
	}
	var out []webauthn.Credential
	for _, k := range keys {
		var c webauthn.Credential
		if err := json.Unmarshal([]byte(k.Data), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// webauthn builds the relying party from the public address. Passkeys are
// bound to that address, so they cannot be used without it.
func (s *Server) webauthn(ctx context.Context) (*webauthn.WebAuthn, error) {
	base := s.linkBase(ctx)
	if base == "" {
		return nil, shownError("Set the public address on the Settings tab to use passkeys.")
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, shownError("The public address is not valid.")
	}
	return webauthn.New(&webauthn.Config{RPID: u.Hostname(), RPDisplayName: "voidgrid shopping", RPOrigins: []string{base}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) handlePasskeyBegin(w http.ResponseWriter, r *http.Request) {
	wa, err := s.webauthn(r.Context())
	if err != nil {
		jsonErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	u := userFrom(r.Context())
	creds, err := s.credsOf(r.Context(), u.ID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not read your passkeys")
		return
	}
	var exclude []protocol.CredentialDescriptor
	for _, c := range creds {
		exclude = append(exclude, c.Descriptor())
	}
	opts, sess, err := wa.BeginRegistration(waUser{u: *u, creds: creds},
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			RequireResidentKey: protocol.ResidentKeyRequired(),
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			UserVerification:   protocol.VerificationRequired,
		}),
		webauthn.WithExclusions(exclude))
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not start")
		return
	}
	s.waReg.put(strconv.FormatInt(u.ID, 10), *sess, s.now(), pendingTTL)
	writeJSON(w, http.StatusOK, opts)
}

func (s *Server) handlePasskeyFinish(w http.ResponseWriter, r *http.Request) {
	wa, err := s.webauthn(r.Context())
	if err != nil {
		jsonErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	u := userFrom(r.Context())
	sess, ok := s.waReg.get(strconv.FormatInt(u.ID, 10), s.now(), true)
	if !ok {
		jsonErr(w, http.StatusBadRequest, "that took too long; try again")
		return
	}
	creds, _ := s.credsOf(r.Context(), u.ID)
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	cred, err := wa.FinishRegistration(waUser{u: *u, creds: creds}, sess, r)
	if err != nil {
		s.log.Warn("passkey registration", "err", err)
		jsonErr(w, http.StatusBadRequest, "the passkey could not be verified")
		return
	}
	data, err := json.Marshal(cred)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not save")
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" || len(name) > 60 {
		name = "Passkey " + s.now().Format("2006-01-02")
	}
	if err := s.db.AddPasskey(r.Context(), u.ID, cred.ID, string(data), name, s.now()); err != nil {
		jsonErr(w, http.StatusConflict, "that passkey is already registered")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "1"})
}

func (s *Server) handleLoginPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	wa, err := s.webauthn(r.Context())
	if err != nil {
		jsonErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if s.limiter.blocked("i:"+s.clientIP(r), s.now()) {
		jsonErr(w, http.StatusTooManyRequests, "too many attempts")
		return
	}
	opts, sess, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not start")
		return
	}
	id, err := auth.NewToken()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not start")
		return
	}
	s.waLogin.put(auth.HashToken(id), *sess, s.now(), pendingTTL)
	s.setShortCookie(w, waCookie, id)
	writeJSON(w, http.StatusOK, opts)
}

func (s *Server) handleLoginPasskeyFinish(w http.ResponseWriter, r *http.Request) {
	wa, err := s.webauthn(r.Context())
	if err != nil {
		jsonErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	c, err := r.Cookie(waCookie)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "that took too long; try again")
		return
	}
	sess, ok := s.waLogin.get(auth.HashToken(c.Value), s.now(), true)
	clearCookie(w, waCookie)
	if !ok {
		jsonErr(w, http.StatusBadRequest, "that took too long; try again")
		return
	}
	ipKey := "i:" + s.clientIP(r)
	fail := func(msg string) {
		s.limiter.fail(ipKey, s.now())
		jsonErr(w, http.StatusUnauthorized, msg)
	}
	var found storage.User
	var stored storage.Passkey
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		pk, err := s.db.PasskeyByCredential(r.Context(), rawID)
		if err != nil {
			return nil, err
		}
		if string(userHandle) != strconv.FormatInt(pk.UserID, 10) {
			return nil, errors.New("user handle does not match")
		}
		u, err := s.db.UserByID(r.Context(), pk.UserID)
		if err != nil || u.Disabled {
			return nil, errors.New("account unavailable")
		}
		creds, err := s.credsOf(r.Context(), u.ID)
		if err != nil {
			return nil, err
		}
		found, stored = u, pk
		return waUser{u: u, creds: creds}, nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	cred, err := wa.FinishDiscoverableLogin(handler, sess, r)
	if err != nil || found.ID == 0 {
		s.log.Warn("passkey sign-in", "err", err)
		fail("that passkey was not accepted")
		return
	}
	if cred.Authenticator.CloneWarning {
		s.log.Warn("passkey sign-in refused: possible cloned authenticator", "user", found.ID)
		fail("that passkey was not accepted")
		return
	}
	if data, err := json.Marshal(cred); err == nil {
		_ = s.db.TouchPasskey(r.Context(), stored.ID, string(data), s.now())
	}
	if err := s.startSession(w, r, found.ID); err != nil {
		jsonErr(w, http.StatusInternalServerError, "could not sign in")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": "/recipes"})
}

// shownError is a message written for the person using the app; it is shown as is.
type shownError string

func (e shownError) Error() string { return string(e) }
