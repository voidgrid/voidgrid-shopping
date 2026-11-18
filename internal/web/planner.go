package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/voidgrid/voidgrid-shopping/internal/storage"
)

const dateLayout = "2006-01-02"

var slotLabels = map[string]string{"breakfast": "Breakfast", "lunch": "Lunch", "snack": "Snack", "dinner": "Dinner"}

type plannerSlot struct {
	Key, Label, Date string
	Entries          []storage.PlanEntry
	Prep             []noteView // notes tied to this meal
}

type plannerDay struct {
	Date, Weekday, Short string
	Today                bool
	Slots                []plannerSlot
	Notes                []noteView
}

// noteView is a day note with the meal it is for written out ("Thu dinner").
type noteView struct {
	storage.DayNote
	ForLabel string
}

type dateOption struct {
	Value, Label string
	Slots        []slotOption
}

type slotOption struct{ Value, Label string }

type plannerView struct {
	First, Last, Title string
	Prev, Next, Today  string
	Days               []plannerDay
	Options            []dateOption
	Sidebar            []storage.RecipeCard
	SidebarQ           string
	Start              string
}

func (s *Server) routePlanner(mux *http.ServeMux) {
	mux.HandleFunc("GET /planner", s.requireUser(s.handlePlanner))
	mux.HandleFunc("GET /planner/add", s.requireUser(s.handlePlannerAddForm))
	mux.HandleFunc("POST /planner/entries", s.requireUser(s.handleEntryCreate))
	mux.HandleFunc("POST /planner/entries/{id}", s.requireUser(s.handleEntryUpdate))
	mux.HandleFunc("POST /planner/entries/{id}/delete", s.requireUser(s.handleEntryDelete))
	mux.HandleFunc("POST /planner/notes", s.requireUser(s.handleNoteCreate))
	mux.HandleFunc("POST /planner/notes/{id}/delete", s.requireUser(s.handleNoteDelete))
}

// location is the household's time zone from Settings.
func (s *Server) location(r *http.Request) (*time.Location, int, error) {
	st, err := s.loadSettings(r.Context())
	if err != nil {
		return nil, 0, err
	}
	loc, err := time.LoadLocation(st.Timezone)
	if err != nil {
		loc = time.UTC
	}
	return loc, st.WeekStart, nil
}

// weekOf returns the first day of the week containing d.
func weekOf(d time.Time, weekStart int) time.Time {
	back := (int(d.Weekday()) - weekStart + 7) % 7
	return d.AddDate(0, 0, -back)
}

func parseDate(v string, loc *time.Location) (time.Time, bool) {
	if !storage.ValidDate(v) {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(dateLayout, v, loc)
	return t, err == nil
}

func noteViews(notes []storage.DayNote, loc *time.Location, keep func(storage.DayNote) bool) []noteView {
	var out []noteView
	for _, n := range notes {
		if !keep(n) {
			continue
		}
		nv := noteView{DayNote: n}
		if d, ok := parseDate(n.ForDate, loc); ok {
			nv.ForLabel = d.Format("Mon") + " " + slotLabels[n.ForSlot]
		}
		out = append(out, nv)
	}
	return out
}

func (s *Server) handlePlanner(w http.ResponseWriter, r *http.Request) {
	loc, weekStart, err := s.location(r)
	if err != nil {
		s.serverError(w, "planner settings", err)
		return
	}
	today := s.now().In(loc)
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
	anchor := today
	if d, ok := parseDate(r.URL.Query().Get("start"), loc); ok {
		anchor = d
	}
	first := weekOf(anchor, weekStart)
	last := first.AddDate(0, 0, 6)

	entries, err := s.db.PlanEntries(r.Context(), first.Format(dateLayout), last.Format(dateLayout))
	if err != nil {
		s.serverError(w, "plan entries", err)
		return
	}
	notes, err := s.db.DayNotes(r.Context(), first.Format(dateLayout), last.Format(dateLayout))
	if err != nil {
		s.serverError(w, "day notes", err)
		return
	}

	v := plannerView{
		First: first.Format(dateLayout), Last: last.Format(dateLayout),
		Prev: first.AddDate(0, 0, -7).Format(dateLayout), Next: first.AddDate(0, 0, 7).Format(dateLayout),
		Today: weekOf(today, weekStart).Format(dateLayout), Start: first.Format(dateLayout),
		Title: first.Format("Jan 2") + " – " + last.Format("Jan 2, 2006"),
	}
	for i := range 7 {
		d := first.AddDate(0, 0, i)
		ds := d.Format(dateLayout)
		day := plannerDay{Date: ds, Weekday: d.Format("Monday"), Short: d.Format("Jan 2"), Today: d.Equal(today)}
		opt := dateOption{Value: ds, Label: d.Format("Mon Jan 2")}
		for _, slot := range storage.Slots {
			ps := plannerSlot{Key: slot, Label: slotLabels[slot], Date: ds}
			for _, e := range entries {
				if e.Date == ds && e.Slot == slot {
					ps.Entries = append(ps.Entries, e)
				}
			}
			ps.Prep = noteViews(notes, loc, func(n storage.DayNote) bool { return n.ForDate == ds && n.ForSlot == slot })
			day.Slots = append(day.Slots, ps)
			opt.Slots = append(opt.Slots, slotOption{Value: slot, Label: slotLabels[slot]})
		}
		day.Notes = noteViews(notes, loc, func(n storage.DayNote) bool { return n.Date == ds })
		v.Days = append(v.Days, day)
		v.Options = append(v.Options, opt)
	}

	v.SidebarQ = strings.TrimSpace(r.URL.Query().Get("rq"))
	if len(v.SidebarQ) > 100 {
		v.SidebarQ = v.SidebarQ[:100]
	}
	v.Sidebar, err = s.db.ListRecipes(r.Context(), storage.Filter{Q: v.SidebarQ, Limit: 40})
	if err != nil {
		s.serverError(w, "planner sidebar", err)
		return
	}
	s.render(w, r, http.StatusOK, "planner", page{Title: "Planner", Nav: "planner", Planner: &v})
}

// backTo is where a form returns to: the week the change was made in.
func backTo(date string) string {
	if t, err := time.Parse(dateLayout, date); err == nil {
		return "/planner?start=" + t.Format(dateLayout) // rebuilt from the parsed date, never the raw input
	}
	return "/planner"
}

func (s *Server) parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

// done answers a planner change: 204 for the drag-and-drop script, a redirect for forms.
func done(w http.ResponseWriter, r *http.Request, date string) {
	if r.Header.Get("X-Fetch") != "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// The target is always "/planner" plus a date re-formatted from a parsed value, never raw input.
	http.Redirect(w, r, backTo(date), http.StatusSeeOther) //nolint:gosec // see above
}

func (s *Server) planError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, storage.ErrInvalid):
		http.Error(w, "bad request", http.StatusBadRequest)
	case errors.Is(err, storage.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		s.serverError(w, what, err)
	}
}

func intField(r *http.Request, name string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(r.PostForm.Get(name)))
	return n
}

func (s *Server) handlePlannerAddForm(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	date, slot := q.Get("date"), q.Get("slot")
	loc, weekStart, err := s.location(r)
	if err != nil {
		s.serverError(w, "planner settings", err)
		return
	}
	pv := plannerView{}
	rid, _ := strconv.ParseInt(q.Get("recipe_id"), 10, 64)
	pv.SidebarQ = strings.TrimSpace(q.Get("q"))
	p := page{Title: "Add to the plan", Nav: "planner", Planner: &pv, Form: map[string]string{"date": date, "slot": slot}}
	if rid > 0 {
		rec, err := s.db.Recipe(r.Context(), rid)
		if errors.Is(err, storage.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			s.serverError(w, "planner add", err)
			return
		}
		p.Recipe = rec
		if !storage.ValidDate(date) {
			p.Form["date"] = s.now().In(loc).Format(dateLayout)
		}
		if !storage.ValidSlot(slot) {
			p.Form["slot"] = "dinner"
		}
		first := weekOf(s.now().In(loc), weekStart)
		for i := 0; i < 14; i++ {
			d := first.AddDate(0, 0, i)
			pv.Options = append(pv.Options, dateOption{Value: d.Format(dateLayout), Label: d.Format("Mon Jan 2")})
		}
		s.render(w, r, http.StatusOK, "planner_add", p)
		return
	}
	if !storage.ValidDate(date) || !storage.ValidSlot(slot) {
		http.NotFound(w, r)
		return
	}
	pv.Sidebar, err = s.db.ListRecipes(r.Context(), storage.Filter{Q: pv.SidebarQ, Limit: 60})
	if err != nil {
		s.serverError(w, "planner add", err)
		return
	}
	s.render(w, r, http.StatusOK, "planner_add", p)
}

func (s *Server) handleEntryCreate(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	e := storage.PlanEntry{
		Date: r.PostForm.Get("date"), Slot: r.PostForm.Get("slot"), Kind: r.PostForm.Get("kind"),
		Note: r.PostForm.Get("note"), Servings: intField(r, "servings"),
	}
	if e.Kind == "" {
		e.Kind = storage.KindRecipe
	}
	if rid, err := strconv.ParseInt(r.PostForm.Get("recipe_id"), 10, 64); err == nil {
		e.RecipeID = rid
	}
	if e.Kind != storage.KindRecipe {
		e.RecipeID, e.Servings = 0, 0
	}
	if _, err := s.db.AddEntry(r.Context(), e, userFrom(r.Context()).ID, s.now()); err != nil {
		s.planError(w, "add plan entry", err)
		return
	}
	done(w, r, e.Date)
}

func (s *Server) handleEntryUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := recipeID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !s.parseForm(w, r) {
		return
	}
	old, err := s.db.PlanEntry(r.Context(), id)
	if err != nil {
		s.planError(w, "load plan entry", err)
		return
	}
	date, slot := r.PostForm.Get("date"), r.PostForm.Get("slot")
	if date == "" {
		date = old.Date
	}
	if slot == "" {
		slot = old.Slot
	}
	servings := old.Servings
	if _, sent := r.PostForm["servings"]; sent {
		servings = intField(r, "servings")
	}
	if old.Kind != storage.KindRecipe {
		servings = 0
	}
	if err := s.db.MoveEntry(r.Context(), id, date, slot, servings); err != nil {
		s.planError(w, "move plan entry", err)
		return
	}
	if text := r.PostForm.Get("note"); old.Kind == storage.KindNote && text != "" {
		if err := s.db.EditEntryNote(r.Context(), id, text); err != nil {
			s.planError(w, "edit plan note", err)
			return
		}
	}
	done(w, r, date)
}

func (s *Server) handleEntryDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := recipeID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	old, err := s.db.PlanEntry(r.Context(), id)
	if err != nil {
		s.planError(w, "load plan entry", err)
		return
	}
	if err := s.db.DeleteEntry(r.Context(), id); err != nil {
		s.planError(w, "delete plan entry", err)
		return
	}
	done(w, r, old.Date)
}

func (s *Server) handleNoteCreate(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	date := r.PostForm.Get("date")
	var forDate, forSlot string
	if v := r.PostForm.Get("for"); v != "" { // "2026-11-20|dinner"
		forDate, forSlot, _ = strings.Cut(v, "|")
	}
	if _, err := s.db.AddDayNote(r.Context(), date, r.PostForm.Get("text"), forDate, forSlot, userFrom(r.Context()).ID, s.now()); err != nil {
		s.planError(w, "add day note", err)
		return
	}
	done(w, r, date)
}

func (s *Server) handleNoteDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := recipeID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.db.DeleteDayNote(r.Context(), id); err != nil {
		s.planError(w, "delete day note", err)
		return
	}
	if !s.parseForm(w, r) {
		return
	}
	done(w, r, r.PostForm.Get("back"))
}
