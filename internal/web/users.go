package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/voidgrid/voidgrid-shopping/internal/auth"
	"github.com/voidgrid/voidgrid-shopping/internal/storage"
)

const (
	inviteTTL = 7 * 24 * time.Hour
	resetTTL  = 24 * time.Hour
)

type userRow struct {
	storage.User
	Passkeys int
	Self     bool
}

type linkRow struct {
	Hash    string
	Kind    string
	Email   string
	Expires time.Time
}

type usersView struct {
	Users []userRow
	Links []linkRow
	// NewLink is shown once, right after it was made, when it was not emailed.
	NewLink  string
	LinkFor  string
	LinkKind string
}

func (s *Server) renderUsers(w http.ResponseWriter, r *http.Request, status int, v usersView, notice, errMsg string, form map[string]string) {
	users, err := s.db.ListUsers(r.Context())
	if err != nil {
		s.serverError(w, "list users", err)
		return
	}
	me := userFrom(r.Context())
	for _, u := range users {
		keys, _ := s.db.Passkeys(r.Context(), u.ID)
		v.Users = append(v.Users, userRow{User: u, Passkeys: len(keys), Self: u.ID == me.ID})
	}
	toks, err := s.db.OpenTokens(r.Context(), s.now())
	if err != nil {
		s.serverError(w, "list links", err)
		return
	}
	for _, t := range toks {
		v.Links = append(v.Links, linkRow{Hash: t.Hash, Kind: t.Kind, Email: t.Email, Expires: time.Unix(t.ExpiresAt, 0).UTC()})
	}
	s.render(w, r, status, "users", page{Title: "Users", Nav: "users", UV: v, Notice: notice, Error: errMsg, Form: form})
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	s.renderUsers(w, r, http.StatusOK, usersView{}, "", "", nil)
}

// linkBase is the public address links are built from; without it no link is made.
func (s *Server) linkBase(ctx context.Context) string {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return ""
	}
	return st.PublicBaseURL
}

// deliver emails a link, or says it could not and hands it back to copy.
func (s *Server) deliver(ctx context.Context, to, subject, text, link string) (emailed bool, why string) {
	c, err := s.loadMail(ctx)
	if err != nil || !c.Configured() {
		return false, "Email is not set up, so copy the link and send it yourself."
	}
	sctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := s.send(sctx, c, to, subject, text+"\n\n"+link+"\n"); err != nil {
		s.log.Warn("send link email", "err", err)
		return false, "The email could not be sent (" + err.Error() + "), so copy the link and send it yourself."
	}
	return true, ""
}

func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PostForm.Get("email")))
	name := strings.TrimSpace(r.PostForm.Get("name"))
	form := map[string]string{"email": email, "name": name}
	if !validEmail(email) || len(name) > 100 {
		s.renderUsers(w, r, http.StatusUnprocessableEntity, usersView{}, "", "Enter a valid email address.", form)
		return
	}
	base := s.linkBase(r.Context())
	if base == "" {
		s.renderUsers(w, r, http.StatusUnprocessableEntity, usersView{}, "", "Set the public address on the Settings tab first; links are built from it.", form)
		return
	}
	tok, err := auth.NewToken()
	if err != nil {
		s.serverError(w, "invite token", err)
		return
	}
	me := userFrom(r.Context())
	switch err := s.db.CreateInvite(r.Context(), auth.HashToken(tok), email, name, r.PostForm.Get("admin") == "1", me.ID, s.now(), inviteTTL); {
	case errors.Is(err, storage.ErrEmailUsed):
		s.renderUsers(w, r, http.StatusUnprocessableEntity, usersView{}, "", "That email already has an account.", form)
		return
	case err != nil:
		s.serverError(w, "create invite", err)
		return
	}
	link := base + "/invite/" + tok
	emailed, why := s.deliver(r.Context(), email, "You are invited to voidgrid shopping",
		"You have been invited to the household's recipe and shopping app. Open this link to choose a password (it works once and expires in 7 days):", link)
	if emailed {
		s.renderUsers(w, r, http.StatusOK, usersView{}, "Invite emailed to "+email+".", "", nil)
		return
	}
	s.renderUsers(w, r, http.StatusOK, usersView{NewLink: link, LinkFor: email, LinkKind: "invite"}, why, "", nil)
}

func (s *Server) handleResetLink(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	u, err := s.db.UserByEmail(r.Context(), r.PostForm.Get("email"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	base := s.linkBase(r.Context())
	if base == "" {
		s.renderUsers(w, r, http.StatusUnprocessableEntity, usersView{}, "", "Set the public address on the Settings tab first; links are built from it.", nil)
		return
	}
	tok, err := auth.NewToken()
	if err != nil {
		s.serverError(w, "reset token", err)
		return
	}
	if err := s.db.CreateReset(r.Context(), auth.HashToken(tok), u.ID, userFrom(r.Context()).ID, s.now(), resetTTL); err != nil {
		s.serverError(w, "create reset", err)
		return
	}
	link := base + "/reset/" + tok
	emailed, why := s.deliver(r.Context(), u.Email, "Reset your voidgrid shopping password",
		"An administrator started a password reset for your account. Open this link to choose a new password (it works once and expires in 24 hours):", link)
	if emailed {
		s.renderUsers(w, r, http.StatusOK, usersView{}, "Reset link emailed to "+u.Email+".", "", nil)
		return
	}
	s.renderUsers(w, r, http.StatusOK, usersView{NewLink: link, LinkFor: u.Email, LinkKind: "reset"}, why, "", nil)
}

func (s *Server) handleLinkRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	if err := s.db.DeleteToken(r.Context(), r.PostForm.Get("hash")); err != nil {
		s.serverError(w, "revoke link", err)
		return
	}
	s.renderUsers(w, r, http.StatusOK, usersView{}, "Link withdrawn.", "", nil)
}

func (s *Server) handleUserDisable(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	u, err := s.db.UserByEmail(r.Context(), r.PostForm.Get("email"))
	me := userFrom(r.Context())
	if err != nil || u.ID == me.ID {
		s.renderUsers(w, r, http.StatusUnprocessableEntity, usersView{}, "", "You can't change your own account here.", nil)
		return
	}
	off := r.PostForm.Get("disable") == "1"
	if err := s.db.SetUserDisabled(r.Context(), u.ID, off); err != nil {
		s.serverError(w, "disable user", err)
		return
	}
	msg := u.Email + " can sign in again."
	if off {
		msg = u.Email + " is blocked and signed out."
	}
	s.renderUsers(w, r, http.StatusOK, usersView{}, msg, "", nil)
}

// ---- pages opened from a link (no sign-in)

func (s *Server) tokenGuard(w http.ResponseWriter, r *http.Request, kind string) (storage.UserToken, string, bool) {
	ipKey := "t:" + s.clientIP(r)
	if s.limiter.blocked(ipKey, s.now()) {
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return storage.UserToken{}, "", false
	}
	raw := r.PathValue("token")
	t, err := s.db.Token(r.Context(), kind, auth.HashToken(raw), s.now())
	if err != nil {
		s.limiter.fail(ipKey, s.now())
		s.render(w, r, http.StatusNotFound, "link_gone", page{Title: "Link not valid"})
		return storage.UserToken{}, "", false
	}
	return t, raw, true
}

func (s *Server) handleInviteForm(w http.ResponseWriter, r *http.Request) {
	t, raw, ok := s.tokenGuard(w, r, storage.TokenInvite)
	if !ok {
		return
	}
	s.render(w, r, http.StatusOK, "invite", page{Title: "Create your account", Token: raw, Form: map[string]string{"name": t.DisplayName, "email": t.Email}})
}

func (s *Server) handleInviteAccept(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	t, raw, ok := s.tokenGuard(w, r, storage.TokenInvite)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("name"))
	form := map[string]string{"name": name, "email": t.Email}
	again := func(msg string) {
		s.render(w, r, http.StatusUnprocessableEntity, "invite", page{Title: "Create your account", Token: raw, Form: form, Error: msg})
	}
	pw := r.PostForm.Get("password")
	switch {
	case name == "" || len(name) > 100:
		again("Enter your name.")
		return
	case pw != r.PostForm.Get("confirm"):
		again("The two passwords don't match.")
		return
	}
	if msg := auth.CheckPasswordRules(pw); msg != "" {
		again(msg)
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		s.serverError(w, "hash password", err)
		return
	}
	id, err := s.db.AcceptInvite(r.Context(), auth.HashToken(raw), name, hash, s.now())
	if err != nil {
		s.render(w, r, http.StatusNotFound, "link_gone", page{Title: "Link not valid"})
		return
	}
	if err := s.startSession(w, r, id); err != nil {
		s.serverError(w, "start session", err)
		return
	}
	http.Redirect(w, r, "/recipes", http.StatusSeeOther)
}

func (s *Server) handleResetForm(w http.ResponseWriter, r *http.Request) {
	if _, raw, ok := s.tokenGuard(w, r, storage.TokenReset); ok {
		s.render(w, r, http.StatusOK, "reset", page{Title: "Choose a new password", Token: raw})
	}
}

func (s *Server) handleResetAccept(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	_, raw, ok := s.tokenGuard(w, r, storage.TokenReset)
	if !ok {
		return
	}
	pw := r.PostForm.Get("password")
	msg := ""
	if pw != r.PostForm.Get("confirm") {
		msg = "The two passwords don't match."
	} else {
		msg = auth.CheckPasswordRules(pw)
	}
	if msg != "" {
		s.render(w, r, http.StatusUnprocessableEntity, "reset", page{Title: "Choose a new password", Token: raw, Error: msg})
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		s.serverError(w, "hash password", err)
		return
	}
	if _, err := s.db.AcceptReset(r.Context(), auth.HashToken(raw), hash, s.now()); err != nil {
		s.render(w, r, http.StatusNotFound, "link_gone", page{Title: "Link not valid"})
		return
	}
	http.Redirect(w, r, "/login?reset=1", http.StatusSeeOther)
}
