package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/voidgrid/voidgrid-shopping/internal/nutrition"
	"github.com/voidgrid/voidgrid-shopping/internal/openfoodfacts"
	"github.com/voidgrid/voidgrid-shopping/internal/storage"
)

func (s *Server) routeNutrition(mux *http.ServeMux) {
	mux.HandleFunc("GET /recipes/{id}/nutrition", s.requireUser(s.handleNutrition))
	mux.HandleFunc("POST /recipes/{id}/nutrition", s.requireUser(s.handleNutritionOverride))
	mux.HandleFunc("GET /nutrition/link", s.requireUser(s.handleLinkPage))
	mux.HandleFunc("POST /nutrition/link", s.requireUser(s.handleLinkSet))
	mux.HandleFunc("POST /nutrition/unlink", s.requireUser(s.handleUnlink))
	mux.HandleFunc("POST /nutrition/foods", s.requireUser(s.handleFoodCreate))
	mux.HandleFunc("POST /nutrition/barcode", s.requireUser(s.handleBarcode))
	mux.HandleFunc("GET /nutrition/product", s.requireUser(s.handleProduct))
}

type nutBox struct {
	Kcal, Protein, Carbs, Fat string
	Note                      string
}

type nutLine struct {
	nutrition.Line
	GramsText string
	Kcal      string
}

type nutView struct {
	Recipe   storage.Recipe
	Lines    []nutLine
	Box      *nutBox
	Override *storage.Macros
	Counted  int
	Needed   int
	TotalTxt string
}

type linkView struct {
	Key       string
	Recipe    int64
	Q         string
	Hits      []storage.USDAHit
	Records   []storage.FoodRecord
	Current   *storage.Food
	CurrentOn storage.IngredientLink
	USDAReady bool
	Product   *storage.OFFProduct
}

func n0(v float64) string { return strconv.FormatFloat(float64(int64(v+0.5)), 'f', 0, 64) }
func n1(v float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(float64(int64(v*10+0.5))/10, 'f', 1, 64), ".0")
}

func boxOf(m storage.Macros, note string) *nutBox {
	return &nutBox{Kcal: n0(m.Kcal), Protein: n1(m.Protein), Carbs: n1(m.Carbs), Fat: n1(m.Fat), Note: note}
}

// computeNutrition calculates a recipe from its linked foods.
func (s *Server) computeNutrition(ctx context.Context, rec storage.Recipe) (nutrition.Result, error) {
	links, err := s.db.IngredientLinks(ctx)
	if err != nil {
		return nutrition.Result{}, err
	}
	cache := map[string]*storage.Food{}
	var ferr error
	res := nutrition.Compute(rec.Ingredients, rec.Servings, func(key string) (storage.Food, bool) {
		if f, ok := cache[key]; ok {
			if f == nil {
				return storage.Food{}, false
			}
			return *f, true
		}
		l, ok := links[key]
		if !ok {
			cache[key] = nil
			return storage.Food{}, false
		}
		f, err := s.db.FoodFor(ctx, l)
		if err != nil {
			if !errors.Is(err, storage.ErrNotFound) {
				ferr = err
			}
			cache[key] = nil
			return storage.Food{}, false
		}
		cache[key] = &f
		return f, true
	})
	return res, ferr
}

// recipeNutBox is the summary shown on the recipe page, or nil when there is nothing to show.
func (s *Server) recipeNutBox(ctx context.Context, rec storage.Recipe) (*nutBox, error) {
	if m, ok, err := s.db.RecipeNutrition(ctx, rec.ID); err != nil {
		return nil, err
	} else if ok {
		return boxOf(m, "Entered by hand"), nil
	}
	res, err := s.computeNutrition(ctx, rec)
	if err != nil || res.Counted == 0 {
		return nil, err
	}
	if rec.Servings <= 0 {
		return nil, nil
	}
	note := fmt.Sprintf("Calculated from %d of %d ingredients", res.Counted, res.Needed)
	if res.Complete() {
		note = "Calculated from all ingredients"
	}
	return boxOf(res.PerServing, note), nil
}

func (s *Server) handleNutrition(w http.ResponseWriter, r *http.Request) {
	id, ok := recipeID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	rec, err := s.db.Recipe(r.Context(), id)
	if err != nil {
		s.shopError(w, "nutrition recipe", err)
		return
	}
	res, err := s.computeNutrition(r.Context(), rec)
	if err != nil {
		s.serverError(w, "compute nutrition", err)
		return
	}
	v := &nutView{Recipe: rec, Counted: res.Counted, Needed: res.Needed}
	if m, has, err := s.db.RecipeNutrition(r.Context(), id); err != nil {
		s.serverError(w, "recipe nutrition", err)
		return
	} else if has {
		v.Override = &m
		v.Box = boxOf(m, "Entered by hand")
	} else if res.Counted > 0 && rec.Servings > 0 {
		v.Box = boxOf(res.PerServing, fmt.Sprintf("Calculated from %d of %d ingredients", res.Counted, res.Needed))
	}
	if res.Counted > 0 {
		v.TotalTxt = fmt.Sprintf("%s kcal · %s g protein · %s g carbs · %s g fat for the whole recipe", n0(res.Total.Kcal), n1(res.Total.Protein), n1(res.Total.Carbs), n1(res.Total.Fat))
	}
	for _, l := range res.Lines {
		nl := nutLine{Line: l}
		if l.Status == nutrition.Counted {
			nl.GramsText, nl.Kcal = n0(l.Grams)+" g", n0(l.Macros.Kcal)+" kcal"
		}
		v.Lines = append(v.Lines, nl)
	}
	form := map[string]string{}
	if v.Override != nil {
		form = map[string]string{"kcal": n1(v.Override.Kcal), "protein": n1(v.Override.Protein), "carbs": n1(v.Override.Carbs), "fat": n1(v.Override.Fat)}
	}
	s.render(w, r, http.StatusOK, "nutrition", page{Title: "Nutrition: " + rec.Title, Nav: "recipes", Recipe: rec, Nut: v, Form: form})
}

func floatField(r *http.Request, name string) (float64, bool) {
	v := strings.TrimSpace(r.PostForm.Get(name))
	if v == "" {
		return 0, true
	}
	f, err := strconv.ParseFloat(v, 64)
	return f, err == nil && f >= 0 && f <= 100000
}

func macrosFrom(r *http.Request) (storage.Macros, bool) {
	var m storage.Macros
	ok := true
	for name, dst := range map[string]*float64{"kcal": &m.Kcal, "protein": &m.Protein, "carbs": &m.Carbs, "fat": &m.Fat} {
		f, good := floatField(r, name)
		*dst, ok = f, ok && good
	}
	return m, ok
}

func (s *Server) handleNutritionOverride(w http.ResponseWriter, r *http.Request) {
	id, ok := recipeID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !s.parseForm(w, r) {
		return
	}
	var m *storage.Macros
	if r.PostForm.Get("clear") == "" {
		mm, good := macrosFrom(r)
		if !good || (mm == storage.Macros{}) {
			http.Error(w, "Enter the calories, protein, carbs and fat per serving as numbers.", http.StatusBadRequest)
			return
		}
		m = &mm
	}
	if err := s.db.SetRecipeNutrition(r.Context(), id, m); err != nil {
		s.shopError(w, "set recipe nutrition", err)
		return
	}
	s.back(w, r, fmt.Sprintf("/recipes/%d/nutrition", id))
}

// ---- choosing a food for an ingredient

func nutKey(v string) string {
	k := storage.ItemKey(v)
	if len(k) > 200 {
		k = k[:200]
	}
	return k
}

func (s *Server) nutBack(r *http.Request) string {
	if id, err := strconv.ParseInt(r.FormValue("recipe"), 10, 64); err == nil && id > 0 {
		return fmt.Sprintf("/recipes/%d/nutrition", id)
	}
	return "/recipes"
}

func (s *Server) handleLinkPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := nutKey(r.URL.Query().Get("key"))
	if key == "" {
		http.NotFound(w, r)
		return
	}
	rid, _ := strconv.ParseInt(r.URL.Query().Get("recipe"), 10, 64)
	lv := &linkView{Key: key, Recipe: rid, Q: strings.TrimSpace(r.URL.Query().Get("q"))}
	if len(lv.Q) > 100 {
		lv.Q = lv.Q[:100]
	}
	st, err := s.db.USDAStatus(ctx)
	if err != nil {
		s.serverError(w, "usda status", err)
		return
	}
	lv.USDAReady = st.Nutrients > 0
	if lv.Q != "" {
		if lv.Hits, err = s.db.SearchUSDAFoods(ctx, lv.Q, 25); err != nil {
			s.serverError(w, "usda search", err)
			return
		}
	}
	if lv.Records, err = s.db.FoodRecords(ctx); err != nil {
		s.serverError(w, "food records", err)
		return
	}
	links, err := s.db.IngredientLinks(ctx)
	if err != nil {
		s.serverError(w, "links", err)
		return
	}
	if l, ok := links[key]; ok {
		if f, err := s.db.FoodFor(ctx, l); err == nil {
			lv.Current, lv.CurrentOn = &f, l
		}
	}
	form := map[string]string{"name": key, "basis": "per100"}
	s.render(w, r, http.StatusOK, "nutrition_link", page{Title: "Nutrition for " + key, Nav: "recipes", Link: lv, Form: form})
}

func (s *Server) handleLinkSet(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	l := storage.IngredientLink{Key: nutKey(r.PostForm.Get("key")), Source: r.PostForm.Get("source"), Barcode: r.PostForm.Get("barcode")}
	l.FdcID, _ = strconv.ParseInt(r.PostForm.Get("fdc_id"), 10, 64)
	l.RecordID, _ = strconv.ParseInt(r.PostForm.Get("record_id"), 10, 64)
	g, ok := floatField(r, "unit_g")
	if !ok {
		http.Error(w, "The weight of one each must be a number of grams.", http.StatusBadRequest)
		return
	}
	l.UnitG = g
	if l.Source != storage.SourceUSDA {
		l.FdcID = 0
	}
	if err := s.db.SetIngredientLink(r.Context(), l, s.now()); err != nil {
		s.shopError(w, "set link", err)
		return
	}
	s.back(w, r, s.nutBack(r))
}

func (s *Server) handleUnlink(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	if err := s.db.DeleteIngredientLink(r.Context(), nutKey(r.PostForm.Get("key"))); err != nil {
		s.serverError(w, "unlink", err)
		return
	}
	s.back(w, r, s.nutBack(r))
}

func (s *Server) handleFoodCreate(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	m, ok := macrosFrom(r)
	serving, ok2 := floatField(r, "serving_g")
	density, ok3 := floatField(r, "g_per_ml")
	if !ok || !ok2 || !ok3 {
		http.Error(w, "The nutrition values must be numbers.", http.StatusBadRequest)
		return
	}
	rec := storage.FoodRecord{Name: r.PostForm.Get("name"), Per100: m, GPerMl: density}
	switch r.PostForm.Get("basis") {
	case "serving": // values are for one serving or each that weighs serving_g
		if serving <= 0 {
			http.Error(w, "Say how many grams one serving or each weighs.", http.StatusBadRequest)
			return
		}
		f := 100 / serving
		rec.Per100 = storage.Macros{Kcal: m.Kcal * f, Protein: m.Protein * f, Carbs: m.Carbs * f, Fat: m.Fat * f}
		rec.UnitG = serving
	default:
		rec.UnitG = serving
	}
	id, err := s.db.AddFoodRecord(r.Context(), rec, s.now())
	if err != nil {
		s.shopError(w, "add food record", err)
		return
	}
	if key := nutKey(r.PostForm.Get("key")); key != "" {
		if err := s.db.SetIngredientLink(r.Context(), storage.IngredientLink{Key: key, Source: storage.SourceManual, RecordID: id}, s.now()); err != nil {
			s.shopError(w, "link food record", err)
			return
		}
	}
	s.back(w, r, s.nutBack(r))
}

func (s *Server) handleBarcode(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	code := strings.TrimSpace(r.PostForm.Get("barcode"))
	key := nutKey(r.PostForm.Get("key"))
	rid, _ := strconv.ParseInt(r.PostForm.Get("recipe"), 10, 64)
	linkURL := "/nutrition/link?key=" + queryEscape(key) + "&recipe=" + strconv.FormatInt(rid, 10)
	if !storage.ValidBarcode(code) {
		s.flashTo(w, r, linkURL, "A barcode is 8 to 14 digits.")
		return
	}
	_, err := s.db.OFFProduct(r.Context(), code)
	if errors.Is(err, storage.ErrNotFound) {
		p, found, lerr := s.off.Lookup(r.Context(), code)
		switch {
		case errors.Is(lerr, openfoodfacts.ErrRateLimited):
			s.flashTo(w, r, linkURL, "Too many barcode lookups just now. Wait a minute and try again.")
			return
		case lerr != nil:
			s.log.Warn("barcode lookup", "err", lerr)
			s.flashTo(w, r, linkURL, "Open Food Facts could not be reached. Try again, or enter the food by hand.")
			return
		case !found:
			s.flashTo(w, r, linkURL, "Open Food Facts doesn't know that barcode. Enter the food by hand below.")
			return
		}
		if err := s.db.SaveOFFProduct(r.Context(), p, s.now()); err != nil {
			s.serverError(w, "cache product", err)
			return
		}
	} else if err != nil {
		s.serverError(w, "product cache", err)
		return
	}
	// code is 8 to 14 digits (checked above), key is escaped, rid is a number.
	http.Redirect(w, r, "/nutrition/product?barcode="+code+"&key="+queryEscape(key)+"&recipe="+strconv.FormatInt(rid, 10), http.StatusSeeOther) //nolint:gosec // see above
}

func (s *Server) handleProduct(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("barcode")
	key := nutKey(r.URL.Query().Get("key"))
	if !storage.ValidBarcode(code) || key == "" {
		http.NotFound(w, r)
		return
	}
	p, err := s.db.OFFProduct(r.Context(), code)
	if err != nil {
		s.shopError(w, "product", err)
		return
	}
	rid, _ := strconv.ParseInt(r.URL.Query().Get("recipe"), 10, 64)
	s.render(w, r, http.StatusOK, "nutrition_product", page{Title: "Scanned product", Nav: "recipes", Link: &linkView{Key: key, Recipe: rid, Product: &p}})
}
