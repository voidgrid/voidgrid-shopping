// Package nutrition works out the calories and macros of a recipe from its
// ingredient lines and the foods a person linked them to.
package nutrition

import (
	"strings"

	"github.com/voidgrid/voidgrid-shopping/internal/ingredient"
	"github.com/voidgrid/voidgrid-shopping/internal/storage"
	"github.com/voidgrid/voidgrid-shopping/internal/units"
)

// Line statuses.
const (
	Counted       = "counted"       // included in the totals
	Unlinked      = "unlinked"      // no food chosen for this ingredient yet
	Unconvertible = "unconvertible" // food chosen, but this amount can't be turned into grams
	Skipped       = "skipped"       // no amount ("salt"), left out on purpose
)

// Line is the outcome for one ingredient line.
type Line struct {
	Text   string
	Key    string
	Status string
	Why    string
	Grams  float64
	Macros storage.Macros
	Food   string
}

// Result is a recipe's nutrition.
type Result struct {
	Lines      []Line
	Total      storage.Macros
	PerServing storage.Macros
	Servings   int
	Counted    int // lines in the totals
	Needed     int // lines that should be: everything except skipped lines
}

// Complete reports whether every ingredient that has an amount was counted.
func (r Result) Complete() bool { return r.Needed > 0 && r.Counted == r.Needed }

// Compute adds up a recipe. foodFor returns the food linked to an ingredient key, if any.
func Compute(ingredients string, servings int, foodFor func(key string) (storage.Food, bool)) Result {
	res := Result{Servings: servings}
	for _, raw := range strings.Split(ingredients, "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		it := ingredient.Parse(raw)
		if it.Section {
			continue
		}
		key := storage.ItemKey(it.Name)
		line := Line{Text: it.Text(ingredient.Display{}), Key: key}
		if !it.HasQty || it.Qty <= 0 {
			line.Status = Skipped
			res.Lines = append(res.Lines, line)
			continue
		}
		res.Needed++
		food, ok := foodFor(key)
		if !ok {
			line.Status, line.Why = Unlinked, "No food chosen yet."
			res.Lines = append(res.Lines, line)
			continue
		}
		line.Food = food.Name
		g, why := Grams(it, food)
		if g <= 0 {
			line.Status, line.Why = Unconvertible, why
			res.Lines = append(res.Lines, line)
			continue
		}
		line.Status, line.Grams = Counted, g
		line.Macros = scale(food.Per100, g/100)
		res.Counted++
		res.Total = add(res.Total, line.Macros)
		res.Lines = append(res.Lines, line)
	}
	if servings > 0 {
		res.PerServing = scale(res.Total, 1/float64(servings))
	}
	return res
}

func scale(m storage.Macros, f float64) storage.Macros {
	return storage.Macros{Kcal: m.Kcal * f, Protein: m.Protein * f, Carbs: m.Carbs * f, Fat: m.Fat * f}
}

func add(a, b storage.Macros) storage.Macros {
	return storage.Macros{Kcal: a.Kcal + b.Kcal, Protein: a.Protein + b.Protein, Carbs: a.Carbs + b.Carbs, Fat: a.Fat + b.Fat}
}

// Grams turns an ingredient line's amount into grams for a food. When it
// can't, it returns 0 and says what is missing.
func Grams(it ingredient.Item, f storage.Food) (float64, string) {
	qty := it.Qty
	if it.QtyHi > 0 {
		qty = (it.Qty + it.QtyHi) / 2
	}
	switch it.Unit.Kind {
	case units.Weight:
		return qty * it.Unit.Base, ""
	case units.Volume:
		if f.GPerMl <= 0 && !hasVolumePortion(f) {
			return 0, "This food has no weight per cup; add one to its record."
		}
		return qty * it.Unit.Base * density(f), ""
	}
	// a count: cans and packages with a size, then cloves, slices, eggs ...
	if it.PackQty > 0 {
		switch it.PackUnit.Kind {
		case units.Weight:
			return qty * it.PackQty * it.PackUnit.Base, ""
		case units.Volume:
			if d := density(f); d > 0 {
				return qty * it.PackQty * it.PackUnit.Base * d, ""
			}
			return 0, "The package is sized by volume, and this food has no weight per cup."
		}
	}
	each := eachGrams(it, f)
	if each <= 0 {
		what := "one " + it.Unit.Name
		if it.Unit.Name == "" {
			what = "one of these"
		}
		return 0, "Don't know what " + what + " weighs: give it a weight when choosing the food."
	}
	return qty * each, ""
}

func hasVolumePortion(f storage.Food) bool {
	for _, p := range f.Portions {
		if p.ML > 0 {
			return true
		}
	}
	return false
}

// density is grams per millilitre: the food's own, else from its plainest volume measure.
func density(f storage.Food) float64 {
	if f.GPerMl > 0 {
		return f.GPerMl
	}
	best, plain := 0.0, false
	for _, p := range f.Portions {
		if p.ML <= 0 || p.Grams <= 0 {
			continue
		}
		isPlain := !strings.ContainsAny(p.Label, ",(") && len(strings.Fields(p.Label)) <= 2
		if best == 0 || (isPlain && !plain) {
			best, plain = p.Grams/p.ML, isPlain
		}
	}
	return best
}

var countPreference = []string{"medium", "large", "each", "item", "whole", "small", "piece", "serving"}

// eachGrams is the weight of one counted thing: the person's figure, a USDA
// measure named like the unit ("clove"), or a typical single item.
func eachGrams(it ingredient.Item, f storage.Food) float64 {
	if f.UnitG > 0 {
		return f.UnitG
	}
	var counts []storage.USDAPortion
	for _, p := range f.Portions {
		if p.ML > 0 || p.Grams <= 0 || p.Amount <= 0 {
			continue
		}
		if u, ok := units.Lookup(firstWord(p.Label)); ok && u.Kind == units.Weight {
			continue
		}
		counts = append(counts, p)
	}
	if it.Unit.Name != "" {
		for _, p := range counts {
			if strings.Contains(strings.ToLower(p.Label), it.Unit.Name) {
				return p.Grams / p.Amount
			}
		}
		return 0
	}
	// a size in the name ("2 large eggs") picks that measure first
	prefs := countPreference
	name := strings.ToLower(it.Name)
	for _, size := range []string{"large", "medium", "small"} {
		if strings.Contains(" "+name+" ", " "+size+" ") {
			prefs = append([]string{size}, countPreference...)
			break
		}
	}
	for _, want := range prefs {
		for _, p := range counts {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(p.Label)), want) {
				return p.Grams / p.Amount
			}
		}
	}
	return 0
}

func firstWord(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return strings.TrimRight(f[0], ",.")
}
