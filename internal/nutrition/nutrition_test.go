package nutrition

import (
	"math"
	"testing"

	"github.com/voidgrid/voidgrid-shopping/internal/storage"
)

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

var flour = storage.Food{
	Name: "Flour", Per100: storage.Macros{Kcal: 364, Protein: 10, Carbs: 76, Fat: 1},
	Portions: []storage.USDAPortion{{Amount: 1, Label: "cup", Grams: 125, ML: 236.5882365}, {Amount: 1, Label: "tbsp", Grams: 8, ML: 14.78676478125}},
}

var egg = storage.Food{
	Name: "Egg", Per100: storage.Macros{Kcal: 143, Protein: 12.6, Carbs: 0.7, Fat: 9.5},
	Portions: []storage.USDAPortion{{Amount: 1, Label: "large", Grams: 50}, {Amount: 1, Label: "medium", Grams: 44}, {Amount: 1, Label: "cup", Grams: 243, ML: 236.5882365}},
}

var garlic = storage.Food{
	Name: "Garlic", Per100: storage.Macros{Kcal: 149, Protein: 6.4, Carbs: 33, Fat: 0.5},
	Portions: []storage.USDAPortion{{Amount: 1, Label: "clove", Grams: 3}, {Amount: 1, Label: "tsp", Grams: 2.8, ML: 4.9}},
}

func foods(m map[string]storage.Food) func(string) (storage.Food, bool) {
	return func(k string) (storage.Food, bool) { f, ok := m[k]; return f, ok }
}

func TestComputeRecipe(t *testing.T) {
	r := Compute("For the batter\n2 cups flour\n3 eggs\n4 cloves garlic\n8 oz mystery\nsalt, (to taste)\n1 lb flour", 4,
		foods(map[string]storage.Food{"flour": flour, "eggs": egg, "garlic": garlic}))
	// 2 cups of flour is 250 g; 3 eggs are 3 medium (44 g) = 132 g; 4 cloves = 12 g; 1 lb flour = 453.59 g
	wantFlour := 250 + 453.59237
	kcal := wantFlour/100*364 + 132/100.0*143 + 12/100.0*149
	if !near(r.Total.Kcal, kcal) {
		t.Errorf("kcal %.1f, want %.1f", r.Total.Kcal, kcal)
	}
	if !near(r.PerServing.Kcal, kcal/4) {
		t.Errorf("per serving %.1f", r.PerServing.Kcal)
	}
	if r.Counted != 4 || r.Needed != 5 || r.Complete() {
		t.Errorf("counted %d of %d", r.Counted, r.Needed)
	}
	status := map[string]string{}
	for _, l := range r.Lines {
		status[l.Key] = l.Status
	}
	if status["mystery"] != Unlinked || status["salt"] != Skipped || status["flour"] != Counted {
		t.Errorf("statuses %v", status)
	}
}

func TestGramsRules(t *testing.T) {
	cases := []struct {
		line string
		f    storage.Food
		want float64
	}{
		{"1 cup flour", flour, 125},
		{"2 tbsp flour", flour, 15.625}, // one density (from the plain cup) for every volume
		{"100 g flour", flour, 100},
		{"2 large eggs", egg, 100},                      // a size in the name picks that measure
		{"1 small egg", egg, 44},                        // no "small" measure: falls back to the usual one
		{"1 egg", egg, 44},                              // no unit: a typical single item, medium first
		{"1 clove garlic", garlic, 3},                   // matches the measure named like the unit
		{"1 can beans", storage.Food{Name: "Beans"}, 0}, // a can of unknown size
		{"2 (15 oz) can beans", storage.Food{Name: "Beans"}, 2 * 15 * 28.349523125}, // package size gives the weight
		{"1 cup mystery", storage.Food{Name: "x"}, 0},                               // volume with no density
		{"1-2 cups flour", flour, 187.5},                                            // a range takes the middle
	}
	for _, c := range cases {
		r := Compute(c.line, 1, foods(map[string]storage.Food{keyOf(c.line): c.f}))
		got := 0.0
		for _, l := range r.Lines {
			got = l.Grams
		}
		if !near(got, c.want) {
			t.Errorf("%q: %.2f g, want %.2f (%v)", c.line, got, c.want, r.Lines)
		}
	}
}

func keyOf(line string) string {
	r := Compute(line, 1, func(k string) (storage.Food, bool) { return storage.Food{}, false })
	return r.Lines[0].Key
}

func TestPersonsUnitWeightWins(t *testing.T) {
	custom := egg
	custom.UnitG = 60
	r := Compute("2 eggs", 1, foods(map[string]storage.Food{"eggs": custom}))
	if !near(r.Lines[0].Grams, 120) {
		t.Errorf("custom weight ignored: %v", r.Lines[0])
	}
}

func TestManualDensityAndUnconvertible(t *testing.T) {
	honey := storage.Food{Name: "Honey", Per100: storage.Macros{Kcal: 304, Carbs: 82}, GPerMl: 1.42}
	r := Compute("1 tbsp honey", 1, foods(map[string]storage.Food{"honey": honey}))
	if !near(r.Lines[0].Grams, 14.78676478*1.42) {
		t.Errorf("honey %v", r.Lines[0])
	}
	r = Compute("1 cup oil", 1, foods(map[string]storage.Food{"oil": {Name: "Oil", Per100: storage.Macros{Kcal: 884}}}))
	if r.Lines[0].Status != Unconvertible || r.Lines[0].Why == "" || r.Counted != 0 {
		t.Errorf("no density should be unconvertible with a reason: %+v", r.Lines[0])
	}
}

func TestNoServings(t *testing.T) {
	r := Compute("100 g flour", 0, foods(map[string]storage.Food{"flour": flour}))
	if r.Total.Kcal == 0 || r.PerServing.Kcal != 0 {
		t.Errorf("totals %v per serving %v", r.Total, r.PerServing)
	}
}
