package ingredient

import (
	"testing"

	"github.com/voidgrid/voidgrid-shopping/internal/units"
)

func TestParse(t *testing.T) {
	cases := []struct {
		line, name, note, unit string
		qty, hi                float64
		hasQty                 bool
	}{
		{"2 cups flour", "flour", "", "cup", 2, 0, true},
		{"1 1/2 cups all-purpose flour, sifted", "all-purpose flour", "sifted", "cup", 1.5, 0, true},
		{"3/4 cup sugar", "sugar", "", "cup", .75, 0, true},
		{"1½ tsp. vanilla", "vanilla", "", "tsp", 1.5, 0, true},
		{"1 ½ tbsp butter", "butter", "", "tbsp", 1.5, 0, true},
		{".5 teaspoon onion powder", "onion powder", "", "tsp", .5, 0, true},
		{"2-3 cabbage leaves, (thinly sliced)", "cabbage leaves", "thinly sliced", "", 2, 3, true},
		{"2 to 3 lbs beef roast", "beef roast", "", "lb", 2, 3, true},
		{"1-1/2 cups milk", "milk", "", "cup", 1.5, 0, true},
		{"4 clove Minced garlic", "Minced garlic", "", "clove", 4, 0, true},
		{"4 lbs beef roast, (cheapest cut)", "beef roast", "cheapest cut", "lb", 4, 0, true},
		{"2 Yellow Onion, chopped", "Yellow Onion", "chopped", "", 2, 0, true},
		{"18 Eggs", "Eggs", "", "", 18, 0, true},
		{"1 cup of milk", "milk", "", "cup", 1, 0, true},
		{"1 tbsp butter, melted, cooled", "butter", "melted, cooled", "tbsp", 1, 0, true},
		{"2 fl oz lemon juice", "lemon juice", "", "fl oz", 2, 0, true},
		{"salt", "salt", "", "", 0, 0, false},
		{"Salt, (to taste)", "Salt", "to taste", "", 0, 0, false},
		{"pinch crushed red pepper flakes", "crushed red pepper flakes", "", "pinch", 1, 0, true},
		{"cumin - ground", "cumin - ground", "", "", 0, 0, false},
		{"` Teaspoon Salt", "Teaspoon Salt", "", "", 0, 0, false},
		{"6 cups half-and-half", "half-and-half", "", "cup", 6, 0, true},
	}
	for _, c := range cases {
		t.Run(c.line, func(t *testing.T) {
			it := Parse(c.line)
			if it.Name != c.name || it.Note != c.note || it.Unit.Name != c.unit ||
				it.HasQty != c.hasQty || !near(it.Qty, c.qty) || !near(it.QtyHi, c.hi) {
				t.Errorf("got name=%q note=%q unit=%q qty=%v hi=%v has=%v", it.Name, it.Note, it.Unit.Name, it.Qty, it.QtyHi, it.HasQty)
			}
		})
	}
}

func near(a, b float64) bool { d := a - b; return d < 1e-9 && d > -1e-9 }

func TestParsePackSizes(t *testing.T) {
	it := Parse("1 (8 oz) package cream cheese, softened")
	if it.Qty != 1 || it.Unit.Name != "package" || it.PackQty != 8 || it.PackUnit != units.Oz ||
		it.Name != "cream cheese" || it.Note != "softened" {
		t.Errorf("%+v", it)
	}
	it = Parse("2 can (12 ounces) cola")
	if it.Qty != 2 || it.Unit.Name != "can" || it.PackQty != 12 || it.PackUnit != units.Oz || it.Name != "cola" {
		t.Errorf("%+v", it)
	}
	it = Parse("1 can whole kernel corn, (15-1/4 ounces, drained)")
	if it.Qty != 1 || it.Unit.Name != "can" || it.Name != "whole kernel corn" || it.Note != "15-1/4 ounces, drained" {
		t.Errorf("%+v", it)
	}
}

func TestParseSectionsAndToTaste(t *testing.T) {
	for _, l := range []string{"For the dough", "Topping:", "For the sauce:"} {
		if it := Parse(l); !it.Section {
			t.Errorf("%q not a section", l)
		}
	}
	if it := Parse("Salt, (to taste)"); !it.ToTaste {
		t.Error("to taste not noticed")
	}
	if Parse("2 cups flour").Section {
		t.Error("ingredient taken for a section")
	}
	if it := Parse("1 cup"); it.Name != "1 cup" || it.HasQty {
		t.Errorf("amount with no name: %+v", it)
	}
}

func TestText(t *testing.T) {
	cases := []struct {
		line string
		d    Display
		want string
	}{
		{"1 1/2 cups flour, sifted", Display{}, "1½ cups flour, sifted"},
		{"1 1/2 cups flour", Display{Factor: 2}, "3 cups flour"},
		{"1 cup sugar", Display{Factor: 1.0 / 3}, "⅓ cup sugar"},
		{"1/4 cup oil", Display{Factor: 2}, "½ cup oil"},
		{"1 tbsp oil", Display{Factor: 8}, "8 tbsp oil"},
		{"250 ml milk", Display{}, "250 ml milk"},
		{"500 g flour", Display{Factor: 2}, "1000 g flour"},
		{"1 tsp salt", Display{Factor: 0.5}, "½ tsp salt"},
		{"1/2 tsp salt", Display{Factor: 3}, "1½ tsp salt"},
		{"3 tsp salt", Display{Factor: 1, System: "us"}, "3 tsp salt"},
		{"2 eggs", Display{Factor: 1.5}, "3 eggs"},
		{"1 egg", Display{Factor: 2}, "2 egg"},
		{"2-3 cloves garlic", Display{Factor: 2}, "4–6 cloves garlic"},
		{"250 ml milk", Display{System: "us"}, "1 cup milk"},
		{"500 g flour", Display{System: "us"}, "1 lb flour"},
		{"100 g butter", Display{System: "us"}, "3½ oz butter"},
		{"1 lb beef", Display{System: "metric"}, "455 g beef"},
		{"2 cups water", Display{System: "metric"}, "475 ml water"},
		{"1 (8 oz) package cream cheese", Display{Factor: 2}, "2 (8 oz) packages cream cheese"},
		{"salt", Display{Factor: 3}, "salt"},
		{"For the dough", Display{Factor: 3}, "For the dough"},
	}
	for _, c := range cases {
		if got := Parse(c.line).Text(c.d); got != c.want {
			t.Errorf("%q %+v = %q, want %q", c.line, c.d, got, c.want)
		}
	}
}
