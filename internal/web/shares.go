package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/voidgrid/voidgrid-shopping/internal/auth"
	"github.com/voidgrid/voidgrid-shopping/internal/ingredient"
	"github.com/voidgrid/voidgrid-shopping/internal/storage"
)

type shareRow struct {
	Hash    string
	Created time.Time
	Expires time.Time
	Mine    bool
}

type shareView struct {
	Rows    []shareRow
	NewLink string
	Days    int
}

func (s *Server) routeShares(mux *http.ServeMux) {
	mux.HandleFunc("GET /recipes/{id}/share", s.requireUser(s.handleShareManage))
	mux.HandleFunc("POST /recipes/{id}/share", s.requireUser(s.handleShareCreate))
	mux.HandleFunc("POST /recipes/{id}/share/revoke", s.requireUser(s.handleShareRevoke))
	mux.HandleFunc("GET /s/{token}", s.handleShared)
	mux.HandleFunc("GET /s/{token}/photo", s.handleSharedPhoto)
}

func (s *Server) renderShare(w http.ResponseWriter, r *http.Request, status int, rec storage.Recipe, v shareView, notice, errMsg string) {
	st, err := s.loadSettings(r.Context())
	if err != nil {
		s.serverError(w, "settings", err)
		return
	}
	if v.Days == 0 {
		v.Days = st.ShareDefaultDays
	}
	shares, err := s.db.SharesFor(r.Context(), rec.ID, s.now())
	if err != nil {
		s.serverError(w, "list shares", err)
		return
	}
	me := userFrom(r.Context())
	for _, sh := range shares {
		v.Rows = append(v.Rows, shareRow{Hash: sh.Hash, Created: time.Unix(sh.CreatedAt, 0).UTC(), Expires: time.Unix(sh.ExpiresAt, 0).UTC(), Mine: sh.CreatedBy == me.ID || me.IsAdmin})
	}
	s.render(w, r, status, "share_manage", page{Title: "Share " + rec.Title, Nav: "recipes", Recipe: rec, Share: v, Notice: notice, Error: errMsg})
}

func (s *Server) recipeFromPath(w http.ResponseWriter, r *http.Request) (storage.Recipe, bool) {
	id, ok := recipeID(r)
	if !ok {
		http.NotFound(w, r)
		return storage.Recipe{}, false
	}
	rec, err := s.db.Recipe(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return storage.Recipe{}, false
	}
	if err != nil {
		s.serverError(w, "load recipe", err)
		return storage.Recipe{}, false
	}
	return rec, true
}

func (s *Server) handleShareManage(w http.ResponseWriter, r *http.Request) {
	if rec, ok := s.recipeFromPath(w, r); ok {
		s.renderShare(w, r, http.StatusOK, rec, shareView{}, "", "")
	}
}

func (s *Server) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.recipeFromPath(w, r)
	if !ok || !s.parseForm(w, r) {
		return
	}
	days, err := strconv.Atoi(r.PostForm.Get("days"))
	if err != nil || days < 1 || days > storage.MaxShareDays {
		s.renderShare(w, r, http.StatusUnprocessableEntity, rec, shareView{}, "", "Choose a lifetime from 1 to 365 days. Every link has to expire.")
		return
	}
	base := s.linkBase(r.Context())
	if base == "" {
		s.renderShare(w, r, http.StatusUnprocessableEntity, rec, shareView{Days: days}, "", "Set the public address on the Settings tab first; links are built from it.")
		return
	}
	tok, err := auth.NewToken()
	if err != nil {
		s.serverError(w, "share token", err)
		return
	}
	now := s.now()
	if err := s.db.CreateShare(r.Context(), auth.HashToken(tok), rec.ID, userFrom(r.Context()).ID, now, now.Add(time.Duration(days)*24*time.Hour)); err != nil {
		s.serverError(w, "create share", err)
		return
	}
	s.renderShare(w, r, http.StatusOK, rec, shareView{NewLink: base + "/s/" + tok, Days: days}, "Link created. Copy it now; it is not shown again.", "")
}

func (s *Server) handleShareRevoke(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.recipeFromPath(w, r)
	if !ok || !s.parseForm(w, r) {
		return
	}
	hash := r.PostForm.Get("hash")
	me := userFrom(r.Context())
	// only the person who made a link, or an admin, can withdraw it
	live, err := s.db.SharesFor(r.Context(), rec.ID, s.now())
	if err != nil {
		s.serverError(w, "list shares", err)
		return
	}
	for _, sh := range live {
		if sh.Hash == hash && (sh.CreatedBy == me.ID || me.IsAdmin) {
			if err := s.db.RevokeShare(r.Context(), hash, rec.ID, s.now()); err != nil {
				s.serverError(w, "revoke share", err)
				return
			}
			s.renderShare(w, r, http.StatusOK, rec, shareView{}, "Link withdrawn. It stops working immediately.", "")
			return
		}
	}
	s.renderShare(w, r, http.StatusNotFound, rec, shareView{}, "", "That link was not found, or it is not yours to withdraw.")
}

// ---- the public side: no sign-in, read-only, nothing from the app

// sharedRecipe finds the recipe a link points to. Every failure looks the
// same (unknown, expired, revoked), and repeated failures from one address
// are slowed down.
func (s *Server) sharedRecipe(w http.ResponseWriter, r *http.Request) (storage.Recipe, bool) {
	h := w.Header()
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	now := s.now()
	ip := s.clientIP(r)
	if s.shareRate.blocked(ip, now) || s.shareBad.blocked(ip, now) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return storage.Recipe{}, false
	}
	s.shareRate.fail(ip, now) // counts every request, good or bad
	gone := func() (storage.Recipe, bool) {
		s.shareBad.fail(ip, now)
		s.render(w, r, http.StatusNotFound, "share_gone", page{Title: "Link not available"})
		return storage.Recipe{}, false
	}
	tok := r.PathValue("token")
	if len(tok) < 20 || len(tok) > 100 {
		return gone()
	}
	sh, err := s.db.LiveShare(r.Context(), auth.HashToken(tok), now)
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			s.log.Error("share lookup", "err", err)
		}
		return gone()
	}
	rec, err := s.db.Recipe(r.Context(), sh.RecipeID)
	if err != nil {
		return gone()
	}
	return rec, true
}

func (s *Server) handleShared(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.sharedRecipe(w, r)
	if !ok {
		return
	}
	var lines []ingLine
	for _, l := range splitLines(rec.Ingredients) {
		it := ingredient.Parse(l)
		lines = append(lines, ingLine{Text: it.Text(ingredient.Display{Factor: 1}), Heading: it.Section})
	}
	// only what belongs on a shared recipe: no ids of people, no rating, no notes of the household
	pub := storage.Recipe{
		Title: rec.Title, Course: rec.Course, Cuisine: rec.Cuisine, MainIngredient: rec.MainIngredient,
		Description: rec.Description, Source: rec.Source, URL: rec.URL, PrepMin: rec.PrepMin, CookMin: rec.CookMin,
		TotalMin: rec.TotalMin, Servings: rec.Servings, YieldText: rec.YieldText, Directions: rec.Directions,
		Tags: rec.Tags, Photo: rec.Photo,
	}
	s.render(w, r, http.StatusOK, "share", page{Title: rec.Title, Recipe: pub, Lines: lines, Token: r.PathValue("token")})
}

func (s *Server) handleSharedPhoto(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.sharedRecipe(w, r)
	if !ok {
		return
	}
	p, err := s.photos.Path(rec.Photo)
	if rec.Photo == "" || err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeFile(w, r, p)
}

func splitLines(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
