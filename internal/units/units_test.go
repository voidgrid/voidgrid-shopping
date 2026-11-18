package units

import (
	"math"
	"testing"
)

func TestLookup(t *testing.T) {
	for w, want := range map[string]string{
		"cup": "cup", "Cups": "cup", "c": "cup", "tbsp.": "tbsp", "Tablespoons": "tbsp", "TSP": "tsp",
		"lbs": "lb", "pounds": "lb", "oz": "oz", "ounces": "oz", "g": "g", "kg": "kg", "ml": "ml", "liters": "l",
		"cloves": "clove", "pkg": "package", "boxes": "box", "loaves": "loaf",
	} {
		u, ok := Lookup(w)
		if !ok || u.Name != want {
			t.Errorf("Lookup(%q) = %q %v, want %q", w, u.Name, ok, want)
		}
	}
	for _, w := range []string{"large", "yellow", "flour", "", "t", "eggs"} {
		if _, ok := Lookup(w); ok {
			t.Errorf("Lookup(%q) found a unit", w)
		}
	}
}

func TestConvert(t *testing.T) {
	q, ok := Convert(1, Cup, Tbsp)
	if !ok || math.Abs(q-16) > 0.01 {
		t.Errorf("1 cup = %v tbsp", q)
	}
	if q, _ := Convert(1, Lb, Oz); math.Abs(q-16) > 0.001 {
		t.Errorf("1 lb = %v oz", q)
	}
	if q, _ := Convert(1, Kg, Gram); q != 1000 {
		t.Errorf("1 kg = %v g", q)
	}
	if _, ok := Convert(1, Cup, Oz); ok {
		t.Error("volume converted to weight without a density")
	}
	if _, ok := Convert(1, Cup, Pinch); ok {
		t.Error("converted to a count unit")
	}
}

func TestDensity(t *testing.T) {
	// all-purpose flour: 125 g per cup
	g := VolumeToWeight(Cup.Base, 125/Cup.Base)
	if math.Abs(g-125) > 1e-9 {
		t.Errorf("a cup of flour weighs %v g", g)
	}
	if ml := WeightToVolume(250, 125/Cup.Base); math.Abs(ml-2*Cup.Base) > 1e-6 {
		t.Errorf("250 g of flour is %v ml", ml)
	}
	if WeightToVolume(5, 0) != 0 {
		t.Error("zero density should give 0")
	}
}

func TestFraction(t *testing.T) {
	for q, want := range map[float64]string{
		0.5: "½", 0.33: "⅓", 0.34: "⅓", 0.66: "⅔", 0.25: "¼", 0.75: "¾", 0.125: "⅛", 1: "1", 1.5: "1½",
		1.33: "1⅓", 2.74: "2¾", 2.97: "3", 0.01: "⅛", 0.99: "1", 12: "12",
	} {
		if got := Fraction(q); got != want {
			t.Errorf("Fraction(%v) = %q, want %q", q, got, want)
		}
	}
}

func TestBest(t *testing.T) {
	cases := []struct {
		kind Kind
		base float64
		unit string
	}{
		{Volume, 2, "tsp"},
		{Volume, 20, "tbsp"},
		{Volume, 100, "cup"},
		{Volume, 960, "cup"},
		{Volume, 3000, "cup"},
		{Volume, 4000 * 4, "gallon"},
		{Weight, 100, "oz"},
		{Weight, 500, "lb"},
	}
	for _, c := range cases {
		if _, u := BestUS(c.kind, c.base); u.Name != c.unit {
			t.Errorf("BestUS(%v, %v) = %q, want %q", c.kind, c.base, u.Name, c.unit)
		}
	}
	if _, u := BestMetric(Volume, 1500); u != Litre {
		t.Errorf("1500 ml = %q", u.Name)
	}
}
