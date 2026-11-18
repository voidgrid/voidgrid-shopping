// Package units knows cooking units: how to recognise them, convert between
// US and metric, and write an amount the way a cook would ("1½ cups").
package units

import (
	"math"
	"strconv"
	"strings"
)

// Kind says what a unit measures.
type Kind int

const (
	// Count units (clove, can, pinch) and bare numbers don't convert.
	Count Kind = iota
	Volume
	Weight
)

// Unit is a recognised unit. Base is millilitres for Volume and grams for
// Weight; it is 0 for Count units.
type Unit struct {
	Name   string // canonical singular name: "cup", "tbsp", "oz", "clove"
	Kind   Kind
	Base   float64
	Metric bool
	Plural string // "" means the name is used for any amount
}

// Canonical units.
var (
	Tsp    = Unit{Name: "tsp", Kind: Volume, Base: 4.92892159375}
	Tbsp   = Unit{Name: "tbsp", Kind: Volume, Base: 14.78676478125}
	FlOz   = Unit{Name: "fl oz", Kind: Volume, Base: 29.5735295625}
	Cup    = Unit{Name: "cup", Kind: Volume, Base: 236.5882365, Plural: "cups"}
	Pint   = Unit{Name: "pint", Kind: Volume, Base: 473.176473, Plural: "pints"}
	Quart  = Unit{Name: "quart", Kind: Volume, Base: 946.352946, Plural: "quarts"}
	Gallon = Unit{Name: "gallon", Kind: Volume, Base: 3785.411784, Plural: "gallons"}
	Ml     = Unit{Name: "ml", Kind: Volume, Base: 1, Metric: true}
	Litre  = Unit{Name: "l", Kind: Volume, Base: 1000, Metric: true}
	Oz     = Unit{Name: "oz", Kind: Weight, Base: 28.349523125}
	Lb     = Unit{Name: "lb", Kind: Weight, Base: 453.59237}
	Gram   = Unit{Name: "g", Kind: Weight, Base: 1, Metric: true}
	Kg     = Unit{Name: "kg", Kind: Weight, Base: 1000, Metric: true}
	Pinch  = Unit{Name: "pinch", Kind: Count, Plural: "pinches"}
	NoUnit = Unit{}
)

var aliases = map[string]Unit{}

// containers and other count words: singular and plural spellings.
var countWords = map[string]string{
	"can": "cans", "jar": "jars", "bottle": "bottles", "bag": "bags", "package": "packages", "box": "boxes",
	"stick": "sticks", "clove": "cloves", "head": "heads", "bunch": "bunches", "slice": "slices",
	"stalk": "stalks", "loaf": "loaves", "packet": "packets", "tub": "tubs", "tube": "tubes", "cube": "cubes",
	"sprig": "sprigs", "dash": "dashes", "piece": "pieces", "handful": "handfuls", "sheet": "sheets",
	"fillet": "fillets", "rib": "ribs", "ear": "ears", "leaf": "leaves", "carton": "cartons", "container": "containers",
	"envelope": "envelopes", "pouch": "pouches", "roll": "rolls", "rack": "racks", "chub": "chubs",
}

func init() {
	add := func(u Unit, words ...string) {
		for _, w := range words {
			aliases[w] = u
		}
	}
	add(Tsp, "tsp", "tsps", "teaspoon", "teaspoons", "tspn")
	add(Tbsp, "tbsp", "tbsps", "tbs", "tbl", "tbls", "tablespoon", "tablespoons", "tblsp")
	add(FlOz, "floz")
	add(Cup, "cup", "cups", "c")
	add(Pint, "pint", "pints", "pt")
	add(Quart, "quart", "quarts", "qt")
	add(Gallon, "gallon", "gallons", "gal")
	add(Ml, "ml", "milliliter", "milliliters", "millilitre", "millilitres")
	add(Litre, "l", "liter", "liters", "litre", "litres")
	add(Oz, "oz", "ounce", "ounces")
	add(Lb, "lb", "lbs", "pound", "pounds")
	add(Gram, "g", "gm", "gram", "grams")
	add(Kg, "kg", "kilogram", "kilograms")
	add(Pinch, "pinch", "pinches")
	for one, many := range countWords {
		u := Unit{Name: one, Kind: Count, Plural: many}
		aliases[one], aliases[many] = u, u
	}
	aliases["pkg"], aliases["pkgs"] = aliases["package"], aliases["package"]
	aliases["pkt"] = aliases["packet"]
}

// Lookup finds the unit a word names ("Tbsp.", "cups", "cloves").
func Lookup(word string) (Unit, bool) {
	w := strings.ToLower(strings.TrimRight(strings.TrimSpace(word), ".,;:"))
	u, ok := aliases[w]
	return u, ok
}

// Label is the unit's name for an amount: "cup" for one or less, "cups" for more.
func (u Unit) Label(qty float64) string {
	if u.Plural != "" && qty > 1.0001 {
		return u.Plural
	}
	return u.Name
}

// ToBase converts qty of u to millilitres or grams.
func (u Unit) ToBase(qty float64) float64 { return qty * u.Base }

// Convert changes qty from one unit to another of the same kind.
func Convert(qty float64, from, to Unit) (float64, bool) {
	if from.Kind != to.Kind || from.Kind == Count || to.Base == 0 {
		return 0, false
	}
	return qty * from.Base / to.Base, true
}

// VolumeToWeight turns millilitres into grams for a food with the given
// density (grams per millilitre), and back.
func VolumeToWeight(ml, gPerMl float64) float64 { return ml * gPerMl }

// WeightToVolume turns grams into millilitres.
func WeightToVolume(g, gPerMl float64) float64 {
	if gPerMl <= 0 {
		return 0
	}
	return g / gPerMl
}

// BestUS picks the US unit a cook would use for an amount of millilitres or grams.
func BestUS(kind Kind, base float64) (float64, Unit) {
	switch kind {
	case Volume:
		switch {
		case base < 3*Tsp.Base:
			return base / Tsp.Base, Tsp
		case base < 0.25*Cup.Base-0.01:
			return base / Tbsp.Base, Tbsp
		case base < 16*Cup.Base:
			return base / Cup.Base, Cup
		}
		return base / Gallon.Base, Gallon
	case Weight:
		if base < Lb.Base {
			return base / Oz.Base, Oz
		}
		return base / Lb.Base, Lb
	}
	return base, NoUnit
}

// BestMetric picks a metric unit.
func BestMetric(kind Kind, base float64) (float64, Unit) {
	switch kind {
	case Volume:
		if base >= 1000 {
			return base / 1000, Litre
		}
		return base, Ml
	case Weight:
		if base >= 1000 {
			return base / 1000, Kg
		}
		return base, Gram
	}
	return base, NoUnit
}

// fractions a cook uses, in order.
var fractions = []struct {
	v float64
	s string
}{
	{0, ""},
	{1.0 / 8, "⅛"},
	{1.0 / 4, "¼"},
	{1.0 / 3, "⅓"},
	{3.0 / 8, "⅜"},
	{1.0 / 2, "½"},
	{5.0 / 8, "⅝"},
	{2.0 / 3, "⅔"},
	{3.0 / 4, "¾"},
	{7.0 / 8, "⅞"},
	{1, ""},
}

// Fraction writes q rounded to the nearest cooking fraction: 1.33 → "1⅓".
// An amount that would round to nothing is written as "⅛".
func Fraction(q float64) string {
	if q <= 0 {
		return "0"
	}
	whole := math.Floor(q)
	frac := q - whole
	best, bestD := 0, 2.0
	for i, f := range fractions {
		if d := math.Abs(frac - f.v); d < bestD {
			best, bestD = i, d
		}
	}
	if best == len(fractions)-1 {
		whole++
		best = 0
	}
	switch {
	case whole == 0 && best == 0:
		return "⅛"
	case best == 0:
		return trimFloat(whole, 0)
	case whole == 0:
		return fractions[best].s
	}
	return trimFloat(whole, 0) + fractions[best].s
}

func trimFloat(v float64, places int) string {
	s := strconv.FormatFloat(v, 'f', places, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}

// Amount writes qty of u the way a recipe would and also returns the value
// as written, so the caller can choose "cup" or "cups" to match what is shown.
func Amount(qty float64, u Unit) (string, float64) {
	switch {
	case u.Metric:
		switch {
		case qty >= 100:
			qty = math.Round(qty/5) * 5
		case qty >= 10:
			qty = math.Round(qty)
		default:
			qty = math.Round(qty*10) / 10
		}
		return trimFloat(qty, 1), qty
	case u.Name == Oz.Name && qty >= 10:
		qty = math.Round(qty)
		return trimFloat(qty, 0), qty
	case u.Name == Oz.Name && qty >= 1:
		qty = math.Round(qty*2) / 2
		return Fraction(qty), qty
	case u.Name == Lb.Name:
		qty = math.Round(qty*4) / 4
		return Fraction(qty), qty
	}
	s := Fraction(qty)
	return s, shown(s)
}

// shown reads back the number Fraction wrote.
func shown(s string) float64 {
	total := 0.0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			total = total*10 + float64(r-'0')
		default:
			for _, f := range fractions {
				if f.s == string(r) {
					total += f.v
				}
			}
		}
	}
	return total
}
