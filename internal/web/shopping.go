package web

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/voidgrid/voidgrid-shopping/internal/importer"
	"github.com/voidgrid/voidgrid-shopping/internal/ingredient"
	"github.com/voidgrid/voidgrid-shopping/internal/storage"
	"github.com/voidgrid/voidgrid-shopping/internal/units"
	webassets "github.com/voidgrid/voidgrid-shopping/web"
)

func (s *Server) routeShopping(mux *http.ServeMux) {
	mux.HandleFunc("GET /list", s.requireUser(s.handleList))
	mux.HandleFunc("GET /list/version", s.requireUser(s.handleListVersion))
	mux.HandleFunc("GET /sw.js", s.handleServiceWorker)
	mux.HandleFunc("POST /list/generate", s.requireUser(s.handleListGenerate))
	mux.HandleFunc("POST /list/items", s.requireUser(s.handleItemAdd))
	mux.HandleFunc("GET /list/items/{id}/edit", s.requireUser(s.handleItemEditForm))
	mux.HandleFunc("POST /list/items/{id}", s.requireUser(s.handleItemUpdate))
	mux.HandleFunc("POST /list/items/{id}/delete", s.requireUser(s.handleItemDelete))
	mux.HandleFunc("POST /list/items/{id}/state", s.requireUser(s.handleItemState))
	mux.HandleFunc("POST /list/items/{id}/purchase", s.requireUser(s.handleItemPurchase))
	mux.HandleFunc("POST /list/purchases/{id}/delete", s.requireUser(s.handlePurchaseDelete))
	mux.HandleFunc("POST /list/clear", s.requireUser(s.handleListClear))

	mux.HandleFunc("GET /stores", s.requireUser(s.handleStores))
	mux.HandleFunc("POST /stores", s.requireUser(s.handleStoreAdd))
	mux.HandleFunc("POST /stores/{id}", s.requireUser(s.handleStoreUpdate))
	mux.HandleFunc("POST /stores/{id}/delete", s.requireUser(s.handleStoreDelete))
	mux.HandleFunc("POST /stores/{id}/aisles", s.requireUser(s.handleAisleAdd))
	mux.HandleFunc("POST /aisles/{id}", s.requireUser(s.handleAisleUpdate))
	mux.HandleFunc("POST /aisles/{id}/delete", s.requireUser(s.handleAisleDelete))

	mux.HandleFunc("GET /staples", s.requireUser(s.handleStaples))
	mux.HandleFunc("POST /staples", s.requireUser(s.handleStapleAdd))
	mux.HandleFunc("GET /staples/{id}/edit", s.requireUser(s.handleStapleEditForm))
	mux.HandleFunc("POST /staples/copy", s.requireUser(s.handleStaplesCopy))
	mux.HandleFunc("POST /staples/import", s.requireUser(s.handleStaplesImport))
	mux.HandleFunc("POST /staples/{id}", s.requireUser(s.handleStapleUpdate))
	mux.HandleFunc("POST /staples/{id}/delete", s.requireUser(s.handleStapleDelete))
}

// ---- formatting

// amountText writes "1½ cups" or "2 cans (15 oz each)".
func amountText(qty float64, unit, pack string) string {
	if qty <= 0 {
		return ""
	}
	u, ok := units.Lookup(unit)
	var b strings.Builder
	if ok {
		text, shown := units.Amount(qty, u)
		b.WriteString(text + " " + u.Label(shown))
	} else {
		text, _ := units.Amount(qty, units.NoUnit)
		b.WriteString(text)
		if unit != "" {
			b.WriteString(" " + unit)
		}
	}
	if pack != "" {
		b.WriteString(" (" + pack + " each)")
	}
	return b.String()
}

func money(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s$%d.%02d", sign, cents/100, cents%100)
}

// parseMoney reads "3.49" or "$3.49" as cents.
func parseMoney(s string) (int64, bool) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "$"))
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || f > 1000000 || math.IsNaN(f) {
		return 0, false
	}
	return int64(math.Round(f * 100)), true
}

func queryEscape(s string) string { return url.QueryEscape(s) }

func dollars(cents int64) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) }

// normUnit turns what was typed into the unit's standard name, or keeps it as a plain label.
func normUnit(s string) string {
	s = strings.TrimSpace(s)
	if u, ok := units.Lookup(s); ok {
		return u.Name
	}
	s = strings.ToLower(s)
	if len(s) > 30 {
		s = s[:30]
	}
	return s
}

// quantityField reads an optional amount from a form.
func quantityField(r *http.Request, name string) (float64, bool) {
	v := strings.TrimSpace(r.PostForm.Get(name))
	if v == "" {
		return 0, true
	}
	q, ok := ingredient.ParseQuantity(v)
	return q, ok && q >= 0 && q <= 100000
}

func idField(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(r.PostForm.Get(name)), 10, 64)
	if n < 0 {
		return 0
	}
	return n
}

func (s *Server) shopError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, storage.ErrInvalid):
		http.Error(w, "That value isn't allowed. Go back and check the form.", http.StatusBadRequest)
	case errors.Is(err, storage.ErrDuplicate):
		http.Error(w, "That name is already used. Go back and pick another.", http.StatusConflict)
	case errors.Is(err, storage.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		s.serverError(w, what, err)
	}
}

// ---- the list

type itemView struct {
	storage.ListItem
	Amount       string
	IsStaple     bool
	RemainingTxt string
	SpentTxt     string
	HintPrice    string
	HintPer      string
	HintBrand    string
	Hints        []storage.PriceHint
}

type aisleGroup struct {
	Name  string
	Pos   int
	Items []itemView
}

type storeGroup struct {
	ID     int64
	Name   string
	Aisles []aisleGroup
}

type listView struct {
	Groups       []storeGroup
	Have, Bought []itemView
	Spent        string
	Stores       []storage.Store
	Filter       string
	NeedCount    int
	From, To     string
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// read the revision first: a change that lands while the page is built
	// makes the next poll see a newer number and refresh once more
	rev, err := s.db.ListRevision(ctx)
	if err != nil {
		s.serverError(w, "list revision", err)
		return
	}
	items, err := s.db.ListItems(ctx)
	if err != nil {
		s.serverError(w, "list items", err)
		return
	}
	stores, err := s.db.Stores(ctx)
	if err != nil {
		s.serverError(w, "list stores", err)
		return
	}
	staples, err := s.db.StapleKeys(ctx)
	if err != nil {
		s.serverError(w, "staple keys", err)
		return
	}
	spent, err := s.db.Spent(ctx)
	if err != nil {
		s.serverError(w, "spent", err)
		return
	}
	loc, weekStart, err := s.location(r)
	if err != nil {
		s.serverError(w, "list settings", err)
		return
	}
	now := s.now().In(loc)
	first := weekOf(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc), weekStart)

	filter := r.URL.Query().Get("store")
	order := map[int64]int{}
	for i, st := range stores {
		order[st.ID] = i
	}
	lv := listView{Stores: stores, Spent: money(spent), Filter: filter, From: first.Format(dateLayout), To: first.AddDate(0, 0, 6).Format(dateLayout)}

	var need []itemView
	for _, it := range items {
		v := itemView{ListItem: it, Amount: amountText(it.Quantity, it.Unit, it.Pack), IsStaple: staples[it.Key]}
		v.SpentTxt = money(it.Spent())
		if filter != "" && strconv.FormatInt(it.StoreID, 10) != filter {
			continue
		}
		switch it.State {
		case storage.StateHave:
			lv.Have = append(lv.Have, v)
		case storage.StateBought:
			lv.Bought = append(lv.Bought, v)
		default:
			rem := it.Remaining()
			v.RemainingTxt, _ = units.Amount(rem, units.NoUnit)
			if u, ok := units.Lookup(it.Unit); ok && u.Kind == units.Weight {
				v.RemainingTxt = "1" // bought by weight: one purchase, the weight goes in its own box
			}
			hints, err := s.db.PriceHints(ctx, it.Key, it.StoreID)
			if err != nil {
				s.serverError(w, "price hints", err)
				return
			}
			v.Hints = hints
			if len(hints) > 0 {
				v.HintPrice, v.HintPer, v.HintBrand = dollars(hints[0].PriceCents), hints[0].WeightUnit, hints[0].Brand
			}
			need = append(need, v)
		}
	}
	lv.NeedCount = len(need)
	rank := func(v itemView) (int, int, string) {
		so := len(stores)
		if i, ok := order[v.StoreID]; ok {
			so = i
		}
		ap := 100000
		if v.AisleID != 0 {
			ap = v.AislePos
		}
		return so, ap, strings.ToLower(v.AisleName)
	}
	sort.SliceStable(need, func(i, j int) bool {
		a1, a2, a3 := rank(need[i])
		b1, b2, b3 := rank(need[j])
		if a1 != b1 {
			return a1 < b1
		}
		if a2 != b2 {
			return a2 < b2
		}
		return a3 < b3
	})
	for _, v := range need {
		if n := len(lv.Groups); n == 0 || lv.Groups[n-1].ID != v.StoreID {
			name := v.StoreName
			if v.StoreID == 0 {
				name = "No store yet"
			}
			lv.Groups = append(lv.Groups, storeGroup{ID: v.StoreID, Name: name})
		}
		g := &lv.Groups[len(lv.Groups)-1]
		aname, apos := v.AisleName, v.AislePos
		if v.AisleID == 0 {
			aname, apos = "No aisle yet", 0
		}
		if n := len(g.Aisles); n == 0 || g.Aisles[n-1].Name != aname {
			g.Aisles = append(g.Aisles, aisleGroup{Name: aname, Pos: apos})
		}
		a := &g.Aisles[len(g.Aisles)-1]
		a.Items = append(a.Items, v)
	}
	s.render(w, r, http.StatusOK, "list", page{Title: "Shopping list", Nav: "list", List: &lv, Version: strconv.FormatInt(rev, 10)})
}

// back redirects to a page of this app. Callers pass paths they built themselves
// (fixed paths plus numbers and escaped query values), never an address from a request.
func (s *Server) back(w http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(w, r, to, http.StatusSeeOther) //nolint:gosec // see above
}

const flashCookie = "vgs_flash"

// flashTo redirects to one of our own pages and shows msg once on the next page.
// The text travels in a short-lived cookie, never in the address, so a link can't carry a message.
func (s *Server) flashTo(w http.ResponseWriter, r *http.Request, to, msg string) {
	http.SetCookie(w, &http.Cookie{
		Name: flashCookie, Value: url.QueryEscape(msg), Path: "/", MaxAge: 60,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	s.back(w, r, to)
}

func (s *Server) handleListGenerate(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	from, to := r.PostForm.Get("from"), r.PostForm.Get("to")
	if !storage.ValidDate(from) || !storage.ValidDate(to) || from > to {
		http.Error(w, "Choose a start date that is not after the end date.", http.StatusBadRequest)
		return
	}
	if a, _ := time.Parse(dateLayout, from); a.AddDate(0, 0, 62).Before(mustDate(to)) {
		http.Error(w, "Choose at most two months at a time.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	entries, err := s.db.PlanEntries(ctx, from, to)
	if err != nil {
		s.serverError(w, "generate: entries", err)
		return
	}
	done, err := s.db.AddedEntries(ctx)
	if err != nil {
		s.serverError(w, "generate: added", err)
		return
	}
	var needs []storage.Need
	var ids []int64
	meals := 0
	for _, e := range entries {
		if e.Kind != storage.KindRecipe || done[e.ID] {
			continue
		}
		rec, err := s.db.Recipe(ctx, e.RecipeID)
		if err != nil {
			continue
		}
		meals++
		ids = append(ids, e.ID)
		factor := 1.0
		if e.Servings > 0 && rec.Servings > 0 {
			factor = float64(e.Servings) / float64(rec.Servings)
		}
		for _, line := range strings.Split(rec.Ingredients, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			it := ingredient.Parse(line)
			if it.Section {
				continue
			}
			n := storage.Need{Name: it.Name, Unit: it.Unit.Name}
			if it.HasQty {
				q := it.Qty
				if it.QtyHi > 0 {
					q = it.QtyHi // a range: buy enough for the top of it
				}
				n.Quantity = q * factor
			}
			if it.PackQty > 0 {
				pk, _ := units.Amount(it.PackQty, it.PackUnit)
				n.Pack = pk + " " + it.PackUnit.Name
			}
			if it.Note != "" && !it.ToTaste {
				n.Note = it.Note
			}
			needs = append(needs, n)
		}
	}
	added, merged, err := s.db.AddNeeds(ctx, needs, ids, s.now())
	if err != nil {
		s.serverError(w, "generate: add", err)
		return
	}
	msg := fmt.Sprintf("Added %d meal%s: %d new item%s, %d merged into items already there.", meals, plural(meals), added, plural(added), merged)
	if meals == 0 {
		msg = "Nothing new to add: those days have no planned recipes, or they were added already."
	}
	s.flashTo(w, r, "/list", msg)
}

func mustDate(v string) time.Time { t, _ := time.Parse(dateLayout, v); return t }

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (s *Server) handleItemAdd(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	q, ok := quantityField(r, "quantity")
	if !ok {
		http.Error(w, "The amount must be a number such as 2 or 1 1/2.", http.StatusBadRequest)
		return
	}
	n := storage.Need{Name: r.PostForm.Get("name"), Quantity: q, Unit: normUnit(r.PostForm.Get("unit")), Note: r.PostForm.Get("note"), StoreID: idField(r, "store_id")}
	if _, _, err := s.db.AddListItem(r.Context(), n, s.now()); err != nil {
		s.shopError(w, "add list item", err)
		return
	}
	s.back(w, r, "/list")
}

func itemID(r *http.Request) (int64, bool) { return recipeID(r) }

func (s *Server) handleItemEditForm(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	it, err := s.db.ListItem(r.Context(), id)
	if err != nil {
		s.shopError(w, "edit item", err)
		return
	}
	stores, err := s.db.Stores(r.Context())
	if err != nil {
		s.serverError(w, "edit item stores", err)
		return
	}
	form := map[string]string{
		"name": it.Name, "quantity": qtyInput(it.Quantity), "unit": it.Unit, "note": it.Note,
		"store_id": strconv.FormatInt(it.StoreID, 10), "aisle_id": strconv.FormatInt(it.AisleID, 10),
	}
	s.render(w, r, http.StatusOK, "list_edit", page{Title: "Edit " + it.Name, Nav: "list", List: &listView{Stores: stores}, Form: form, Item: &itemView{ListItem: it}})
}

func qtyInput(q float64) string {
	if q == 0 {
		return ""
	}
	return strconv.FormatFloat(math.Round(q*1000)/1000, 'f', -1, 64)
}

func (s *Server) handleItemUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !s.parseForm(w, r) {
		return
	}
	q, ok := quantityField(r, "quantity")
	if !ok {
		http.Error(w, "The amount must be a number such as 2 or 1 1/2.", http.StatusBadRequest)
		return
	}
	e := storage.ItemEdit{
		Name: r.PostForm.Get("name"), Quantity: q, Unit: normUnit(r.PostForm.Get("unit")), Note: r.PostForm.Get("note"),
		StoreID: idField(r, "store_id"), AisleID: idField(r, "aisle_id"), NewAisle: r.PostForm.Get("new_aisle"),
	}
	if err := s.db.UpdateListItem(r.Context(), id, e, s.now()); err != nil {
		s.shopError(w, "update item", err)
		return
	}
	s.back(w, r, "/list")
}

func (s *Server) handleItemDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.db.DeleteListItem(r.Context(), id); err != nil {
		s.shopError(w, "delete item", err)
		return
	}
	s.back(w, r, "/list")
}

func (s *Server) handleItemState(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !s.parseForm(w, r) {
		return
	}
	if err := s.db.SetItemState(r.Context(), id, r.PostForm.Get("state"), s.now()); err != nil {
		s.shopError(w, "item state", err)
		return
	}
	s.back(w, r, "/list")
}

func (s *Server) handleItemPurchase(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !s.parseForm(w, r) {
		return
	}
	qty, ok1 := quantityField(r, "quantity")
	price, ok2 := parseMoney(r.PostForm.Get("price"))
	if !ok1 || !ok2 || qty == 0 {
		http.Error(w, "Enter how many you bought and the price, such as 3.49.", http.StatusBadRequest)
		return
	}
	p := storage.Purchase{Quantity: qty, PriceCents: price, Brand: r.PostForm.Get("brand"), WeightUnit: r.PostForm.Get("per")}
	if p.WeightUnit != "" {
		w2, ok := quantityField(r, "weight")
		if !ok || w2 == 0 {
			http.Error(w, "A price per weight needs the weight you bought.", http.StatusBadRequest)
			return
		}
		p.Weight = w2
	}
	// A change made offline carries an id; applying it twice (a replay after a
	// dropped connection) must not record the purchase twice.
	op := r.PostForm.Get("op")
	if op != "" {
		if len(op) > 64 || strings.Trim(op, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-") != "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		fresh, err := s.db.ClaimOp(r.Context(), op, s.now())
		if err != nil {
			s.serverError(w, "claim op", err)
			return
		}
		if !fresh {
			s.back(w, r, "/list")
			return
		}
	}
	if err := s.db.AddPurchase(r.Context(), id, p, s.now()); err != nil {
		if op != "" {
			_ = s.db.ReleaseOp(r.Context(), op)
		}
		s.shopError(w, "add purchase", err)
		return
	}
	s.back(w, r, "/list")
}

func (s *Server) handlePurchaseDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.db.DeletePurchase(r.Context(), id, s.now()); err != nil {
		s.shopError(w, "delete purchase", err)
		return
	}
	s.back(w, r, "/list")
}

func (s *Server) handleListClear(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	n, err := s.db.ClearList(r.Context(), r.PostForm.Get("which"))
	if err != nil {
		s.shopError(w, "clear list", err)
		return
	}
	s.flashTo(w, r, "/list", fmt.Sprintf("Removed %d item%s.", n, plural(int(n))))
}

// ---- stores and aisles

func (s *Server) handleStores(w http.ResponseWriter, r *http.Request) {
	stores, err := s.db.Stores(r.Context())
	if err != nil {
		s.serverError(w, "stores", err)
		return
	}
	s.render(w, r, http.StatusOK, "stores", page{Title: "Stores and aisles", Nav: "list", List: &listView{Stores: stores}})
}

func intForm(r *http.Request, name string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(r.PostForm.Get(name)))
	return n
}

func (s *Server) storeAction(w http.ResponseWriter, r *http.Request, what string, do func(id int64) error) {
	id, ok := itemID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !s.parseForm(w, r) {
		return
	}
	if err := do(id); err != nil {
		s.shopError(w, what, err)
		return
	}
	s.back(w, r, "/stores")
}

func (s *Server) handleStoreAdd(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	if _, err := s.db.AddStore(r.Context(), r.PostForm.Get("name")); err != nil {
		s.shopError(w, "add store", err)
		return
	}
	s.back(w, r, "/stores")
}

func (s *Server) handleStoreUpdate(w http.ResponseWriter, r *http.Request) {
	s.storeAction(w, r, "update store", func(id int64) error {
		return s.db.UpdateStore(r.Context(), id, r.PostForm.Get("name"), intForm(r, "position"))
	})
}

func (s *Server) handleStoreDelete(w http.ResponseWriter, r *http.Request) {
	s.storeAction(w, r, "delete store", func(id int64) error { return s.db.DeleteStore(r.Context(), id) })
}

func (s *Server) handleAisleAdd(w http.ResponseWriter, r *http.Request) {
	s.storeAction(w, r, "add aisle", func(id int64) error {
		_, err := s.db.AddAisle(r.Context(), id, r.PostForm.Get("name"), intForm(r, "position"))
		return err
	})
}

func (s *Server) handleAisleUpdate(w http.ResponseWriter, r *http.Request) {
	s.storeAction(w, r, "update aisle", func(id int64) error {
		return s.db.UpdateAisle(r.Context(), id, r.PostForm.Get("name"), intForm(r, "position"))
	})
}

func (s *Server) handleAisleDelete(w http.ResponseWriter, r *http.Request) {
	s.storeAction(w, r, "delete aisle", func(id int64) error { return s.db.DeleteAisle(r.Context(), id) })
}

// ---- staples

type stapleView struct {
	storage.Staple
	Amount string
}

type stapleGroup struct {
	Store   string
	Staples []stapleView
}

func (s *Server) handleStaples(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	staples, err := s.db.Staples(ctx)
	if err != nil {
		s.serverError(w, "staples", err)
		return
	}
	stores, err := s.db.Stores(ctx)
	if err != nil {
		s.serverError(w, "staples stores", err)
		return
	}
	var groups []stapleGroup
	for _, st := range staples {
		name := st.StoreName
		if st.StoreID == 0 {
			name = "No store yet"
		}
		if n := len(groups); n == 0 || groups[n-1].Store != name {
			groups = append(groups, stapleGroup{Store: name})
		}
		g := &groups[len(groups)-1]
		g.Staples = append(g.Staples, stapleView{Staple: st, Amount: amountText(st.Quantity, st.Unit, "")})
	}
	p := page{Title: "Staples", Nav: "staples", List: &listView{Stores: stores}, StapleGroups: groups, StapleCount: len(staples)}
	s.render(w, r, http.StatusOK, "staples", p)
}

func stapleFromForm(r *http.Request) (storage.Staple, bool) {
	q, ok := quantityField(r, "quantity")
	return storage.Staple{
		Name: r.PostForm.Get("name"), Quantity: q, Unit: normUnit(r.PostForm.Get("unit")),
		Note: r.PostForm.Get("note"), StoreID: idField(r, "store_id"),
	}, ok
}

func (s *Server) handleStapleAdd(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	st, ok := stapleFromForm(r)
	if !ok {
		http.Error(w, "The amount must be a number such as 2 or 1 1/2.", http.StatusBadRequest)
		return
	}
	if _, err := s.db.AddStaple(r.Context(), st, s.now()); err != nil {
		s.shopError(w, "add staple", err)
		return
	}
	s.back(w, r, "/staples")
}

func (s *Server) handleStapleEditForm(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	all, err := s.db.Staples(r.Context())
	if err != nil {
		s.serverError(w, "staple edit", err)
		return
	}
	stores, err := s.db.Stores(r.Context())
	if err != nil {
		s.serverError(w, "staple edit stores", err)
		return
	}
	for _, st := range all {
		if st.ID == id {
			form := map[string]string{"name": st.Name, "quantity": qtyInput(st.Quantity), "unit": st.Unit, "note": st.Note, "store_id": strconv.FormatInt(st.StoreID, 10)}
			s.render(w, r, http.StatusOK, "staple_edit", page{Title: "Edit " + st.Name, Nav: "staples", List: &listView{Stores: stores}, Form: form, Staple: &st})
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Server) handleStapleUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !s.parseForm(w, r) {
		return
	}
	st, okq := stapleFromForm(r)
	if !okq {
		http.Error(w, "The amount must be a number such as 2 or 1 1/2.", http.StatusBadRequest)
		return
	}
	st.ID = id
	if err := s.db.UpdateStaple(r.Context(), st); err != nil {
		s.shopError(w, "update staple", err)
		return
	}
	s.back(w, r, "/staples")
}

func (s *Server) handleStapleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.db.DeleteStaple(r.Context(), id); err != nil {
		s.shopError(w, "delete staple", err)
		return
	}
	s.back(w, r, "/staples")
}

func (s *Server) handleStaplesCopy(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	var ids []int64
	if r.PostForm.Get("all") == "" {
		for _, v := range r.PostForm["ids"] {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			s.flashTo(w, r, "/staples", "Tick the staples to copy first, or use “Copy all”.")
			return
		}
	}
	added, already, err := s.db.CopyStaples(r.Context(), ids, s.now())
	if err != nil {
		s.shopError(w, "copy staples", err)
		return
	}
	msg := fmt.Sprintf("Copied %d staple%s to the list", added, plural(added))
	if already > 0 {
		msg += fmt.Sprintf("; %d already on the list", already)
	}
	s.flashTo(w, r, "/list", msg+".")
}

func (s *Server) handleStaplesImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportFile+1<<20)
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		http.Error(w, "the file is too large", http.StatusRequestEntityTooLarge)
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		s.flashTo(w, r, "/staples", "Choose the Plan to Eat grocery list file (.csv).")
		return
	}
	defer func() { _ = f.Close() }()
	staples, skipped, err := importer.PlanToEatStaples(f)
	if err != nil {
		s.flashTo(w, r, "/staples", err.Error())
		return
	}
	added, dup, err := s.db.ImportStaples(r.Context(), staples, s.now())
	if err != nil {
		s.serverError(w, "import staples", err)
		return
	}
	msg := fmt.Sprintf("Imported %d staple%s", added, plural(added))
	if dup > 0 {
		msg += fmt.Sprintf("; %d were already here", dup)
	}
	if len(skipped) > 0 {
		msg += fmt.Sprintf("; %d rows could not be read", len(skipped))
	}
	s.flashTo(w, r, "/staples", msg+".")
}

// handleListVersion answers with the list's revision number; other devices
// poll it to learn that something changed.
func (s *Server) handleListVersion(w http.ResponseWriter, r *http.Request) {
	n, err := s.db.ListRevision(r.Context())
	if err != nil {
		s.serverError(w, "list revision", err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(strconv.FormatInt(n, 10)))
}

// handleServiceWorker serves the offline script from the root so its scope
// covers the whole app. It holds no secrets and is the same for everyone.
func (s *Server) handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(webassets.FS, "static/sw.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}
