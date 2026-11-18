package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/voidgrid/voidgrid-shopping/internal/importer"
	"github.com/voidgrid/voidgrid-shopping/internal/safehttp"
	"github.com/voidgrid/voidgrid-shopping/internal/social"
	"github.com/voidgrid/voidgrid-shopping/internal/storage"
)

const maxPageBytes = 5 << 20

// handleImportDraft reads a recipe from a web page, a TikTok link, pasted
// text or a file and shows it in the recipe form. Nothing is saved until the
// person reviews it and presses Save.
func (s *Server) handleImportDraft(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPageBytes+1<<20)
	if err := r.ParseMultipartForm(2 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		http.Error(w, "the form or file is too large", http.StatusRequestEntityTooLarge)
		return
	}
	fail := func(msg string) {
		s.render(w, r, http.StatusUnprocessableEntity, "import", page{Title: "Import recipes", Nav: "recipes", Error: msg, Form: map[string]string{
			"address": r.PostForm.Get("address"), "tiktok": r.PostForm.Get("tiktok"), "text": r.PostForm.Get("text"),
		}})
	}
	var rec storage.Recipe
	var err error
	switch r.PostForm.Get("kind") {
	case "url":
		addr := strings.TrimSpace(r.PostForm.Get("address"))
		var b []byte
		if b, _, err = safehttp.Get(r.Context(), s.fetch, addr, maxPageBytes, "text/html,application/xhtml+xml"); err != nil {
			fail("That page could not be read: " + friendly(err) + " You can paste the recipe text instead.")
			return
		}
		if rec, err = importer.FromHTML(b, addr); err != nil {
			fail("That page has no recipe data a program can read. You can paste its text instead.")
			return
		}
	case "tiktok":
		var post social.Post
		if post, err = s.social.Read(r.Context(), r.PostForm.Get("tiktok")); err != nil {
			fail(err.Error() + ". You can paste the caption as text instead.")
			return
		}
		text := post.Description
		if strings.TrimSpace(text) == "" {
			text = post.Title
		}
		rec = importer.FromCaption(text, post.Title)
		rec.URL = post.URL
		if post.Uploader != "" {
			rec.Source = "TikTok @" + strings.TrimPrefix(post.Uploader, "@")
		}
		rec.PhotoSrc = webURL(post.Thumbnail)
	case "text":
		rec = importer.FromText(r.PostForm.Get("text"))
		if rec.Title == "" && rec.Ingredients == "" && rec.Directions == "" {
			fail("Paste the recipe text first.")
			return
		}
	case "file":
		f, _, ferr := r.FormFile("file")
		if ferr != nil {
			fail("Choose a recipe file (.json or .html).")
			return
		}
		defer func() { _ = f.Close() }()
		buf := make([]byte, maxPageBytes+1)
		n, _ := f.Read(buf)
		for n < len(buf) {
			m, e := f.Read(buf[n:])
			n += m
			if e != nil {
				break
			}
		}
		if n > maxPageBytes {
			fail("That file is over 5 MB.")
			return
		}
		b := buf[:n]
		if t := strings.TrimSpace(string(b[:min(n, 64)])); strings.HasPrefix(t, "<") {
			rec, err = importer.FromHTML(b, "")
		} else {
			rec, err = importer.FromJSONLD(b, "")
		}
		if err != nil {
			if errors.Is(err, importer.ErrNoRecipe) {
				fail("That file has no schema.org recipe in it.")
			} else {
				fail(err.Error() + ".")
			}
			return
		}
	default:
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	form := recipeToForm(rec)
	form["photo_src"] = rec.PhotoSrc
	if dup, _ := s.db.HasRecipe(r.Context(), rec.Title, rec.URL, rec.Ingredients); dup {
		form["title"] = rec.Title
	}
	s.render(w, r, http.StatusOK, "recipe_form", page{
		Title: "New recipe", Nav: "recipes", Form: form, FormAction: "/recipes",
		Notice: "Read from the source. Check it over, fix anything, then Save. Nothing is saved yet.",
	})
}

func friendly(err error) string {
	switch {
	case errors.Is(err, safehttp.ErrBlocked):
		return "that address is not a public website."
	case errors.Is(err, safehttp.ErrTooLarge):
		return "the page is too large."
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Timeout() {
		return "the site took too long."
	}
	return strings.TrimRight(err.Error(), ".") + "."
}

// webURL returns raw if it is a plain http(s) address, else "".
func webURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || len(raw) > 2000 {
		return ""
	}
	return u.String()
}
