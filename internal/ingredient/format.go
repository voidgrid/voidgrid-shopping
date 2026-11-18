package ingredient

import (
	"math"
	"strings"

	"github.com/voidgrid/voidgrid-shopping/internal/units"
)

// Display options for Item.Text.
type Display struct {
	Factor float64 // scale; 0 or 1 means as written
	System string  // "us", "metric", or "" to leave units as written
}

// Text writes the line for display: scaled, converted, in cooking fractions.
func (it Item) Text(d Display) string {
	if it.Section {
		return it.Name
	}
	var b strings.Builder
	if it.HasQty {
		f := d.Factor
		if f <= 0 {
			f = 1
		}
		lo, hi, u := it.Qty*f, it.QtyHi*f, it.Unit
		lo, hi, u = convert(lo, hi, u, d.System)
		text, shown := amount(lo, hi, u)
		b.WriteString(text)
		if it.PackQty > 0 {
			pack, _ := units.Amount(it.PackQty, it.PackUnit)
			b.WriteString(" (" + pack + " " + it.PackUnit.Name + ")")
		}
		if u != units.NoUnit {
			b.WriteString(" " + u.Label(shown))
		}
		b.WriteString(" ")
	}
	b.WriteString(it.Name)
	if it.Note != "" {
		b.WriteString(", " + it.Note)
	}
	return strings.TrimSpace(b.String())
}

func maxf(a, b float64) float64 {
	if b > a {
		return b
	}
	return a
}

func amount(lo, hi float64, u units.Unit) (string, float64) {
	s, shown := units.Amount(lo, u)
	if hi > 0 {
		h, hs := units.Amount(hi, u)
		return s + "–" + h, maxf(shown, hs)
	}
	return s, shown
}

// convert changes units to the system a person asked for. Without one, and for
// counts, amounts pass through exactly as written (scaling only multiplies).
func convert(lo, hi float64, u units.Unit, system string) (float64, float64, units.Unit) {
	if u.Kind == units.Count {
		return lo, hi, u
	}
	wantMetric := system == "metric"
	switch {
	case system != "" && u.Metric != wantMetric:
	default:
		return lo, hi, u // nothing converts unless a system was asked for
	}
	top := hi
	if top == 0 {
		top = lo
	}
	base := u.ToBase(top)
	var nu units.Unit
	var q float64
	if wantMetric || (system == "" && u.Metric) {
		q, nu = units.BestMetric(u.Kind, base)
	} else {
		q, nu = units.BestUS(u.Kind, base)
	}
	ratio := q / top
	lo, hi = lo*ratio, hi*ratio
	if u.Metric != nu.Metric { // a converted amount is approximate: snap to a measuring-spoon or cup mark
		lo, hi = snap(lo, nu), snap(hi, nu)
	}
	return lo, hi, nu
}

// snap rounds a converted US volume to the marks on measuring spoons and cups.
func snap(q float64, u units.Unit) float64 {
	step := 0.0
	switch u {
	case units.Cup, units.Tsp:
		step = 0.25
	case units.Tbsp:
		step = 0.5
	default:
		return q
	}
	if q == 0 {
		return 0
	}
	if r := math.Round(q/step) * step; r > 0 {
		return r
	}
	return step
}
