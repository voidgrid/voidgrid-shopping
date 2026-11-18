package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/voidgrid/voidgrid-shopping/internal/importer"
	"github.com/voidgrid/voidgrid-shopping/internal/ingredient"
	"github.com/voidgrid/voidgrid-shopping/internal/photos"
	"github.com/voidgrid/voidgrid-shopping/internal/storage"
)

const (
	maxRecipeForm = photos.MaxBytes + 1<<20
	maxImportFile = 20 << 20
)

type importReport struct {
	Added, Duplicates int
	Skipped           []string
	PhotosPending     int
}

func (s *Server) routeRecipes(mux *http.ServeMux) {
	mux.HandleFunc("GET /recipes", s.requireUser(s.handleRecipeList))
	mux.HandleFunc("GET /recipes/random", s.requireUser(s.handleRecipeRandom))
	mux.HandleFunc("GET /recipes/new", s.requireUser(s.handleRecipeNew))
	mux.HandleFunc("POST /recipes", s.requireUser(s.handleRecipeCreate))
	mux.HandleFunc("GET /recipes/import", s.requireUser(s.handleImportForm))
	mux.HandleFunc("POST /recipes/import", s.requireUser(s.handleImport))
	mux.HandleFunc("POST /recipes/import/draft", s.requireUser(s.handleImportDraft))
	mux.HandleFunc("GET /recipes/{id}", s.requireUser(s.handleRecipeShow))
	mux.HandleFunc("GET /recipes/{id}/edit", s.requireUser(s.handleRecipeEdit))
	mux.HandleFunc("POST /recipes/{id}", s.requireUser(s.handleRecipeUpdate))
	mux.HandleFunc("POST /recipes/{id}/rating", s.requireUser(s.handleRecipeRating))
	mux.HandleFunc("POST /recipes/{id}/delete", s.requireUser(s.handleRecipeDelete))
	mux.HandleFunc("GET /photos/{name}", s.requireUser(s.handlePhoto))
}

func recipeID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) serverError(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// terms splits "chicken, rice" into words, dropping blanks.
func terms(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" && len(t) <= 80 {
			out = append(out, t)
		}
		if len(out) >= 12 {
			break
		}
	}
	return out
}

func filterFromQuery(q url.Values) (storage.Filter, map[string]string) {
	form := map[string]string{}
	for _, k := range []string{"q", "course", "cuisine", "main", "tag", "rating", "time", "any", "all", "without", "sort"} {
		v := strings.TrimSpace(q.Get(k))
		if len(v) > 200 {
			v = v[:200]
		}
		form[k] = v
	}
	f := storage.Filter{
		Q: form["q"], Course: form["course"], Cuisine: form["cuisine"], Main: form["main"], Tag: form["tag"],
		Any: terms(form["any"]), All: terms(form["all"]), Without: terms(form["without"]), Sort: form["sort"],
	}
	if n, err := strconv.Atoi(form["rating"]); err == nil && n >= 1 && n <= 5 {
		f.MinRating = n
	}
	if n, err := strconv.Atoi(form["time"]); err == nil && n > 0 && n <= 100000 {
		f.MaxTotal = n
	}
	return f, form
}

func (s *Server) handleRecipeList(w http.ResponseWriter, r *http.Request) {
	f, form := filterFromQuery(r.URL.Query())
	cards, err := s.db.ListRecipes(r.Context(), f)
	if err != nil {
		s.serverError(w, "list recipes", err)
		return
	}
	facets, err := s.db.RecipeFacets(r.Context())
	if err != nil {
		s.serverError(w, "recipe facets", err)
		return
	}
	filtered := false
	for k, v := range form {
		if v != "" && k != "sort" {
			filtered = true
		}
	}
	s.render(w, r, http.StatusOK, "recipes", page{
		Title: "Recipes", Nav: "recipes", Cards: cards, Facets: facets, Form: form, Filtered: filtered,
		RandomQuery: r.URL.RawQuery,
	})
}

func (s *Server) handleRecipeRandom(w http.ResponseWriter, r *http.Request) {
	f, form := filterFromQuery(r.URL.Query())
	f.Sort, f.Limit = "random", 1
	cards, err := s.db.ListRecipes(r.Context(), f)
	if err != nil {
		s.serverError(w, "random recipe", err)
		return
	}
	if len(cards) == 0 {
		back := url.Values{}
		for k, v := range form {
			if v != "" {
				back.Set(k, v)
			}
		}
		http.Redirect(w, r, "/recipes?"+back.Encode(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/recipes/%d", cards[0].ID), http.StatusSeeOther)
}

func (s *Server) handleRecipeShow(w http.ResponseWriter, r *http.Request) {
	id, ok := recipeID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	rec, err := s.db.Recipe(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, "show recipe", err)
		return
	}
	// Recipes show as written; units convert only when asked for.
	mode := r.URL.Query().Get("units")
	if mode != "us" && mode != "metric" {
		mode = "original"
	}
	target := rec.Servings
	if n, err := strconv.Atoi(r.URL.Query().Get("servings")); err == nil && n >= 1 && n <= 1000 {
		target = n
	}
	factor := 1.0
	if rec.Servings > 0 && target > 0 {
		factor = float64(target) / float64(rec.Servings)
	}
	d := ingredient.Display{Factor: factor, System: mode}
	if mode == "original" {
		d.System = ""
	}
	var lines []ingLine
	for _, l := range strings.Split(rec.Ingredients, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		it := ingredient.Parse(l)
		lines = append(lines, ingLine{Text: it.Text(d), Heading: it.Section})
	}
	box, err := s.recipeNutBox(r.Context(), rec)
	if err != nil {
		s.serverError(w, "recipe nutrition", err)
		return
	}
	s.render(w, r, http.StatusOK, "recipe", page{
		Title: rec.Title, Nav: "recipes", Recipe: rec, Lines: lines, ServingsNow: target, UnitsMode: mode,
		Scaled: target != rec.Servings, NutBox: box,
	})
}

type ingLine struct {
	Text    string
	Heading bool
}

func (s *Server) handleRecipeNew(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "recipe_form", page{
		Title: "New recipe", Nav: "recipes", Form: map[string]string{"servings": "4"}, FormAction: "/recipes",
	})
}

func recipeToForm(rec storage.Recipe) map[string]string {
	num := func(n int) string {
		if n == 0 {
			return ""
		}
		return strconv.Itoa(n)
	}
	return map[string]string{
		"title": rec.Title, "course": rec.Course, "cuisine": rec.Cuisine, "main": rec.MainIngredient,
		"description": rec.Description, "source": rec.Source, "url": rec.URL,
		"prep": num(rec.PrepMin), "cook": num(rec.CookMin), "total": num(rec.TotalMin),
		"servings": num(rec.Servings), "yield": rec.YieldText, "ingredients": rec.Ingredients,
		"directions": rec.Directions, "tags": strings.Join(rec.Tags, ", "),
	}
}

func (s *Server) handleRecipeEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := recipeID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	rec, err := s.db.Recipe(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, "edit recipe", err)
		return
	}
	s.render(w, r, http.StatusOK, "recipe_form", page{
		Title: "Edit " + rec.Title, Nav: "recipes", Recipe: rec, Form: recipeToForm(rec),
		FormAction: fmt.Sprintf("/recipes/%d", id),
	})
}

// readRecipeForm parses and checks the recipe form.
func readRecipeForm(r *http.Request) (storage.Recipe, map[string]string, map[string]string) {
	v := func(k string, max int) string {
		s := strings.TrimSpace(r.PostForm.Get(k))
		s = strings.ReplaceAll(s, "\r\n", "\n")
		if len(s) > max {
			s = strings.ToValidUTF8(s[:max], "")
		}
		return s
	}
	form := map[string]string{
		"title": v("title", 200), "course": v("course", 200), "cuisine": v("cuisine", 200), "main": v("main", 200),
		"description": v("description", 50000), "source": v("source", 200), "url": v("url", 2000),
		"prep": v("prep", 8), "cook": v("cook", 8), "total": v("total", 8), "servings": v("servings", 6),
		"yield": v("yield", 200), "ingredients": v("ingredients", 50000), "directions": v("directions", 50000),
		"tags": v("tags", 1000), "photo_src": v("photo_src", 2000),
	}
	if rows, ok := rowsFromPost(r); ok && r.PostForm.Get("ing_mode") != "bulk" {
		form["ingredients"] = rows
	}
	errs := map[string]string{}
	num := func(k, label string, max int) int {
		if form[k] == "" {
			return 0
		}
		n, err := strconv.Atoi(form[k])
		if err != nil || n < 0 || n > max {
			errs[k] = label + " must be a whole number."
			return 0
		}
		return n
	}
	rec := storage.Recipe{
		Title: form["title"], Course: form["course"], Cuisine: form["cuisine"], MainIngredient: form["main"],
		Description: form["description"], Source: form["source"], YieldText: form["yield"],
		Ingredients: form["ingredients"], Directions: form["directions"],
		Tags:    storage.CleanTags(strings.Split(form["tags"], ",")),
		PrepMin: num("prep", "Prep time", 100000), CookMin: num("cook", "Cook time", 100000),
		TotalMin: num("total", "Total time", 100000), Servings: num("servings", "Servings", 10000),
	}
	if rec.Title == "" {
		errs["title"] = "Give the recipe a title."
	}
	if form["url"] != "" {
		u, err := url.Parse(form["url"])
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs["url"] = "Use a web address starting with http:// or https://."
		} else {
			rec.URL = u.String()
		}
	}
	if rec.TotalMin == 0 {
		rec.TotalMin = rec.PrepMin + rec.CookMin
	}
	if form["photo_src"] != "" {
		if rec.PhotoSrc = webURL(form["photo_src"]); rec.PhotoSrc == "" {
			errs["photo_src"] = "Use a web address starting with http:// or https://."
		}
	}
	return rec, form, errs
}

func (s *Server) parseRecipePost(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRecipeForm)
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		http.Error(w, "the form or photo is too large", http.StatusRequestEntityTooLarge)
		return false
	}
	return true
}

// storeUpload saves an uploaded photo, returning its name ("" if none sent).
func (s *Server) storeUpload(r *http.Request) (string, string) {
	f, _, err := r.FormFile("photo")
	if err != nil {
		return "", ""
	}
	defer func() { _ = f.Close() }()
	name, err := s.photos.Save(f)
	switch {
	case errors.Is(err, photos.ErrNotImage):
		return "", "That file is not a JPEG, PNG, GIF or WebP image."
	case errors.Is(err, photos.ErrTooLarge):
		return "", "That photo is over 10 MB."
	case err != nil:
		s.log.Error("save photo", "err", err)
		return "", "The photo could not be saved."
	}
	return name, ""
}

func (s *Server) handleRecipeCreate(w http.ResponseWriter, r *http.Request) {
	if !s.parseRecipePost(w, r) {
		return
	}
	rec, form, errs := readRecipeForm(r)
	photo, perr := s.storeUpload(r)
	if perr != "" {
		errs["photo"] = perr
	}
	if len(errs) > 0 {
		s.photos.Remove(photo)
		s.render(w, r, http.StatusUnprocessableEntity, "recipe_form", page{
			Title: "New recipe", Nav: "recipes", Form: form, Errors: errs, FormAction: "/recipes",
			Error: "Some fields need fixing; nothing was saved.",
		})
		return
	}
	rec.Photo = photo
	if photo != "" {
		rec.PhotoSrc = ""
	}
	rec.CreatedBy = userFrom(r.Context()).ID
	id, err := s.db.CreateRecipe(r.Context(), &rec, s.now())
	if err != nil {
		s.photos.Remove(photo)
		s.serverError(w, "create recipe", err)
		return
	}
	if rec.PhotoSrc != "" {
		s.kickPhotos()
	}
	http.Redirect(w, r, fmt.Sprintf("/recipes/%d", id), http.StatusSeeOther)
}

func (s *Server) handleRecipeUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := recipeID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	old, err := s.db.Recipe(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, "load recipe", err)
		return
	}
	if !s.parseRecipePost(w, r) {
		return
	}
	rec, form, errs := readRecipeForm(r)
	photo, perr := s.storeUpload(r)
	if perr != "" {
		errs["photo"] = perr
	}
	if len(errs) > 0 {
		s.photos.Remove(photo)
		s.render(w, r, http.StatusUnprocessableEntity, "recipe_form", page{
			Title: "Edit " + old.Title, Nav: "recipes", Recipe: old, Form: form, Errors: errs,
			FormAction: fmt.Sprintf("/recipes/%d", id), Error: "Some fields need fixing; nothing was saved.",
		})
		return
	}
	rec.ID = id
	if err := s.db.UpdateRecipe(r.Context(), &rec, s.now()); err != nil {
		s.photos.Remove(photo)
		s.serverError(w, "update recipe", err)
		return
	}
	switch {
	case photo != "":
		if err := s.db.SetPhoto(r.Context(), id, photo); err != nil {
			s.serverError(w, "set photo", err)
			return
		}
		s.photos.Remove(old.Photo)
	case rec.PhotoSrc != "":
		if err := s.db.SetPhotoSource(r.Context(), id, rec.PhotoSrc); err != nil {
			s.serverError(w, "set photo source", err)
			return
		}
		s.kickPhotos()
	case r.PostForm.Get("remove_photo") == "1" && old.Photo != "":
		if err := s.db.SetPhoto(r.Context(), id, ""); err != nil {
			s.serverError(w, "remove photo", err)
			return
		}
		s.photos.Remove(old.Photo)
	}
	http.Redirect(w, r, fmt.Sprintf("/recipes/%d", id), http.StatusSeeOther)
}

func (s *Server) handleRecipeRating(w http.ResponseWriter, r *http.Request) {
	id, ok := recipeID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	n, err := strconv.Atoi(r.PostForm.Get("rating"))
	if err != nil || n < 0 || n > 5 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch err := s.db.SetRating(r.Context(), id, n); {
	case errors.Is(err, storage.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		s.serverError(w, "set rating", err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/recipes/%d", id), http.StatusSeeOther)
}

func (s *Server) handleRecipeDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := recipeID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	photo, err := s.db.DeleteRecipe(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, "delete recipe", err)
		return
	}
	s.photos.Remove(photo)
	http.Redirect(w, r, "/recipes", http.StatusSeeOther)
}

func (s *Server) handlePhoto(w http.ResponseWriter, r *http.Request) {
	p, err := s.photos.Path(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, p)
}

// ---- Plan to Eat import

func (s *Server) handleImportForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "import", page{Title: "Import recipes", Nav: "recipes"})
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportFile+1<<20)
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		http.Error(w, "the file is too large", http.StatusRequestEntityTooLarge)
		return
	}
	fail := func(msg string) {
		s.render(w, r, http.StatusUnprocessableEntity, "import", page{Title: "Import recipes", Nav: "recipes", Error: msg})
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		fail("Choose the Plan to Eat export file (.csv).")
		return
	}
	defer func() { _ = f.Close() }()
	recs, skipped, err := importer.PlanToEat(f)
	if err != nil {
		fail(err.Error())
		return
	}
	rep := importReport{Skipped: skipped}
	uid := userFrom(r.Context()).ID
	for i := range recs {
		rec := recs[i]
		rec.CreatedBy = uid
		dup, err := s.db.HasRecipe(r.Context(), rec.Title, rec.URL, rec.Ingredients)
		if err != nil {
			s.serverError(w, "import: check duplicate", err)
			return
		}
		if dup {
			rep.Duplicates++
			continue
		}
		if _, err := s.db.CreateRecipe(r.Context(), &rec, s.now()); err != nil {
			s.serverError(w, "import: create", err)
			return
		}
		rep.Added++
		if rec.PhotoSrc != "" {
			rep.PhotosPending++
		}
	}
	if rep.PhotosPending > 0 {
		s.kickPhotos()
	}
	s.render(w, r, http.StatusOK, "import", page{Title: "Import recipes", Nav: "recipes", Report: &rep})
}

// ---- photo downloads

func (s *Server) kickPhotos() {
	select {
	case s.photoKick <- struct{}{}:
	default:
	}
}

// RunPhotoWorker downloads the photos of imported recipes until ctx ends.
// It starts with a pass over anything left from an earlier run.
func (s *Server) RunPhotoWorker(ctx context.Context) {
	s.kickPhotos()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.photoKick:
		}
		s.downloadPending(ctx)
	}
}

func (s *Server) downloadPending(ctx context.Context) {
	pending, err := s.db.PendingPhotos(ctx)
	if err != nil {
		s.log.Error("photos: list pending", "err", err)
		return
	}
	if len(pending) == 0 {
		return
	}
	s.log.Info("photos: downloading", "count", len(pending))
	var wg sync.WaitGroup
	work := make(chan storage.PendingPhoto)
	var mu sync.Mutex
	done, failed := 0, 0
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range work {
				ok := s.downloadOne(ctx, p)
				mu.Lock()
				if ok {
					done++
				} else {
					failed++
				}
				mu.Unlock()
			}
		}()
	}
loop:
	for _, p := range pending {
		select {
		case work <- p:
		case <-ctx.Done():
			break loop
		}
	}
	close(work)
	wg.Wait()
	s.log.Info("photos: finished", "stored", done, "not stored", failed)
}

func (s *Server) downloadOne(ctx context.Context, p storage.PendingPhoto) bool {
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		name, err := s.photos.Fetch(ctx, p.Src)
		if err == nil {
			if err := s.db.SetPhoto(ctx, p.ID, name); err != nil {
				s.photos.Remove(name)
				s.log.Error("photos: record", "recipe", p.ID, "err", err)
				return false
			}
			return true
		}
		if errors.Is(err, photos.ErrPermanent) {
			s.log.Warn("photos: unavailable", "recipe", p.ID, "err", err)
			if err := s.db.GiveUpPhoto(ctx, p.ID); err != nil {
				s.log.Error("photos: give up", "recipe", p.ID, "err", err)
			}
			return false
		}
		s.log.Warn("photos: download failed", "recipe", p.ID, "attempt", attempt+1, "err", err)
	}
	return false
}
