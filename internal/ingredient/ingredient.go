// Package ingredient reads a recipe's free-text ingredient lines
// ("1 1/2 cups all-purpose flour, sifted") into amount, unit, name and note,
// and writes them back scaled or converted.
package ingredient

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/voidgrid/voidgrid-shopping/internal/units"
)

// Item is one ingredient line.
type Item struct {
	Raw     string
	Section bool // a heading such as "For the dough", not an ingredient

	HasQty bool
	Qty    float64
	QtyHi  float64 // upper end of a range such as "2-3"; 0 if none

	Unit units.Unit // units.NoUnit if the line has none

	PackQty  float64 // "(8 oz)" size of each can or package; 0 if none
	PackUnit units.Unit

	Name    string
	Note    string
	ToTaste bool
}

const num = `(?:\d+\s+\d+/\d+|\d+\s*[¼½¾⅓⅔⅛⅜⅝⅞]|\d*\.\d+|\d+/\d+|\d+|[¼½¾⅓⅔⅛⅜⅝⅞])`

var (
	mixedHyphen = regexp.MustCompile(`^(\d+)-(\d+)/(\d+)(?:\s|$)`)
	rangeRE     = regexp.MustCompile(`^(` + num + `)\s*(?:-|–|—|\s+to\s+)\s*(` + num + `)(?:\s|$|[^\d/])`)
	qtyRE       = regexp.MustCompile(`^(` + num + `)`)
	packRE      = regexp.MustCompile(`^\(\s*(` + num + `)\s*([A-Za-z.]+(?:\s+oz\.?)?)\s*\)\s*`)
	spaceRE     = regexp.MustCompile(`\s+`)
)

var vulgar = map[rune]float64{
	'¼': 0.25, '½': 0.5, '¾': 0.75, '⅓': 1.0 / 3, '⅔': 2.0 / 3, '⅛': 0.125, '⅜': 0.375, '⅝': 0.625, '⅞': 0.875,
}

// parseNumber reads "1 1/2", "3/4", ".5", "2", "1½".
func parseNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	total := 0.0
	// vulgar fraction, alone or after a whole number
	if r := []rune(s); vulgar[r[len(r)-1]] != 0 {
		total += vulgar[r[len(r)-1]]
		s = strings.TrimSpace(string(r[:len(r)-1]))
		if s == "" {
			return total, true
		}
	}
	for _, part := range strings.Fields(s) {
		if i := strings.IndexByte(part, '/'); i > 0 {
			n, err1 := strconv.ParseFloat(part[:i], 64)
			d, err2 := strconv.ParseFloat(part[i+1:], 64)
			if err1 != nil || err2 != nil || d == 0 {
				return 0, false
			}
			total += n / d
			continue
		}
		v, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return 0, false
		}
		total += v
	}
	return total, true
}

// ParseQuantity reads an amount a person typed: "2", "1 1/2", "3/4", ".5", "1½".
func ParseQuantity(s string) (float64, bool) { return parseNumber(s) }

// Parse reads one ingredient line.
func Parse(line string) Item {
	it := Item{Raw: line}
	s := spaceRE.ReplaceAllString(strings.TrimSpace(line), " ")
	// Stray punctuation in front (a typo like "` Teaspoon salt") is dropped.
	s = strings.TrimLeftFunc(s, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r == '.' || r == '(' || vulgar[r] != 0 || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r > 127)
	})
	if s == "" {
		it.Section = true
		return it
	}

	rest := s
	if m := mixedHyphen.FindStringSubmatch(rest); m != nil { // "1-1/2 cups" is 1½
		whole, _ := strconv.ParseFloat(m[1], 64)
		n, _ := strconv.ParseFloat(m[2], 64)
		d, _ := strconv.ParseFloat(m[3], 64)
		if d != 0 {
			it.HasQty, it.Qty = true, whole+n/d
			rest = strings.TrimSpace(rest[len(m[1])+1+len(m[2])+1+len(m[3]):])
		}
	} else if m := rangeRE.FindStringSubmatch(rest); m != nil {
		lo, ok1 := parseNumber(m[1])
		hi, ok2 := parseNumber(m[2])
		if ok1 && ok2 && hi > lo {
			it.HasQty, it.Qty, it.QtyHi = true, lo, hi
			rest = strings.TrimSpace(rest[strings.Index(rest, m[2])+len(m[2]):])
		}
	}
	if !it.HasQty {
		if m := qtyRE.FindString(rest); m != "" {
			if v, ok := parseNumber(m); ok {
				it.HasQty, it.Qty = true, v
				rest = strings.TrimSpace(rest[len(m):])
			}
		}
	}

	if !it.HasQty {
		low := strings.ToLower(s)
		if strings.HasSuffix(low, ":") || strings.HasPrefix(low, "for the ") || strings.HasPrefix(low, "for ") && !strings.Contains(s, ",") && len(strings.Fields(s)) <= 5 {
			it.Section = true
			it.Name = strings.TrimSpace(strings.TrimSuffix(s, ":"))
			return it
		}
	}

	// "(8 oz)" can size, before or after the unit
	takePack := func() {
		if m := packRE.FindStringSubmatch(rest); m != nil && it.PackQty == 0 {
			q, ok := parseNumber(m[1])
			u, isUnit := units.Lookup(m[2])
			if !isUnit && strings.EqualFold(strings.TrimRight(m[2], "."), "fl") {
				u, isUnit = units.FlOz, true
			}
			if ok && isUnit && u.Kind != units.Count {
				it.PackQty, it.PackUnit = q, u
				rest = strings.TrimSpace(rest[len(m[0]):])
			}
		}
	}
	if it.HasQty {
		takePack()
	}

	// unit
	words := strings.Fields(rest)
	if len(words) > 0 {
		switch {
		case len(words) > 1 && strings.EqualFold(strings.TrimRight(words[0], "."), "fl") && strings.EqualFold(strings.TrimRight(words[1], "."), "oz"):
			it.Unit = units.FlOz
			rest = strings.TrimSpace(strings.Join(words[2:], " "))
		default:
			if u, ok := units.Lookup(words[0]); ok && (it.HasQty || u.Name == units.Pinch.Name || u.Name == "dash") {
				it.Unit = u
				rest = strings.TrimSpace(strings.Join(words[1:], " "))
				if !it.HasQty {
					it.HasQty, it.Qty = true, 1
				}
			}
		}
		if it.Unit != units.NoUnit {
			takePack()
			if low := strings.ToLower(rest); strings.HasPrefix(low, "of ") {
				rest = strings.TrimSpace(rest[3:])
			}
		}
	}

	it.Name, it.Note = splitNameNote(rest)
	if it.Name == "" {
		it.Name = strings.TrimSpace(s)
		it.HasQty, it.Qty, it.QtyHi, it.Unit = false, 0, 0, units.NoUnit
		it.PackQty, it.PackUnit = 0, units.NoUnit
		it.Note = ""
	}
	low := strings.ToLower(it.Note + " " + it.Name)
	it.ToTaste = strings.Contains(low, "to taste")
	return it
}

// splitNameNote splits "cheddar cheese, shredded (sharp)" into the name and the rest.
func splitNameNote(s string) (name, note string) {
	s = strings.TrimSpace(s)
	cut := len(s)
	if i := strings.IndexAny(s, ",("); i >= 0 {
		cut = i
	}
	name = strings.TrimSpace(s[:cut])
	note = strings.TrimSpace(strings.TrimLeft(s[cut:], ", "))
	if strings.HasPrefix(note, "(") && strings.HasSuffix(note, ")") && strings.Count(note, "(") == 1 {
		note = strings.TrimSpace(note[1 : len(note)-1])
	}
	return strings.TrimRight(name, " -–,"), note
}
