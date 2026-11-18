package web

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/voidgrid/voidgrid-shopping/internal/ingredient"
	"github.com/voidgrid/voidgrid-shopping/internal/units"
)

// formRow is one ingredient row in the recipe form: amount, unit, name and
// note, or a heading (separator) that uses only Name.
type formRow struct {
	Amount, Unit, Name, Note string
	Sep                      bool
}

var amountRE = regexp.MustCompile(`^[\d\s/.,\-–½¼¾⅓⅔⅛⅜⅝⅞]+`)

// splitRow breaks a stored ingredient line into form fields. Nothing is
// reinterpreted: the amount is kept as written.
func splitRow(line string) formRow {
	line = strings.TrimSpace(line)
	if line == "" {
		return formRow{}
	}
	if ingredient.Parse(line).Section {
		return formRow{Sep: true, Name: strings.TrimSpace(strings.TrimSuffix(line, ":"))}
	}
	var r formRow
	rest := line
	if m := amountRE.FindString(rest); m != "" {
		r.Amount = strings.TrimSpace(m)
		rest = strings.TrimSpace(rest[len(m):])
	}
	if r.Amount != "" {
		word, after, _ := strings.Cut(rest, " ")
		if _, ok := units.Lookup(strings.TrimRight(word, ".")); ok && after != "" {
			r.Unit = word
			rest = strings.TrimSpace(after)
		}
	}
	r.Name, r.Note, _ = strings.Cut(rest, ", ")
	r.Name, r.Note = strings.TrimSpace(r.Name), strings.TrimSpace(r.Note)
	return r
}

// line is the stored text of a row.
func (r formRow) line() string {
	if r.Sep {
		n := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(r.Name), ":"))
		if n == "" {
			return ""
		}
		return n + ":"
	}
	var parts []string
	for _, p := range []string{r.Amount, r.Unit, r.Name} {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	out := strings.Join(parts, " ")
	if n := strings.TrimSpace(r.Note); n != "" {
		if out == "" {
			return n
		}
		out += ", " + n
	}
	return out
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// rowsFromPost reads the ingredient rows of a posted form, in order. ok is
// false when the form carried no rows (older forms and tests send text).
func rowsFromPost(r *http.Request) (string, bool) {
	kinds := r.PostForm["ing_k"]
	if len(kinds) == 0 {
		return "", false
	}
	get := func(k string, i int) string {
		v := r.PostForm[k]
		if i < len(v) {
			return oneLine(v[i])
		}
		return ""
	}
	var lines []string
	for i, k := range kinds {
		row := formRow{Sep: k == "s", Amount: get("ing_a", i), Unit: get("ing_u", i), Name: get("ing_n", i), Note: get("ing_x", i)}
		if l := row.line(); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n"), true
}

// rowsFromText turns stored ingredient text into form rows, always ending
// with one empty row to type into.
func rowsFromText(text string) []formRow {
	var rows []formRow
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) != "" {
			rows = append(rows, splitRow(l))
		}
	}
	return append(rows, formRow{})
}
