package importer

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/voidgrid/voidgrid-shopping/internal/ingredient"
	"github.com/voidgrid/voidgrid-shopping/internal/storage"
	"github.com/voidgrid/voidgrid-shopping/internal/units"
)

// PlanToEatStaples reads a Plan to Eat grocery-list export (Store, Category,
// Title, Quantity, Unit, Ingredient Notes, List Notes). Title, quantity, unit
// and the list notes become staples; the store and category are left out on
// purpose, because aisles are learned afresh. Rows that are not read come back
// as "row N: reason".
func PlanToEatStaples(r io.Reader) ([]storage.Staple, []string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, err
	}
	cr := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	head, err := cr.Read()
	if err != nil {
		return nil, nil, errors.New("this does not look like a Plan to Eat grocery list: no header row")
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	if _, ok := col["title"]; !ok {
		return nil, nil, errors.New("this does not look like a Plan to Eat grocery list: no Title column")
	}
	var out []storage.Staple
	var skipped []string
	for row := 2; ; row++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("row %d: %v", row, err))
			continue
		}
		get := func(name string) string {
			i, ok := col[name]
			if !ok || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		name := clip(clean(get("title")), 200)
		if name == "" {
			skipped = append(skipped, fmt.Sprintf("row %d: no title", row))
			continue
		}
		st := storage.Staple{Name: name}
		if q := get("quantity"); q != "" {
			if v, ok := ingredient.ParseQuantity(q); ok && v > 0 && v < 100000 {
				st.Quantity = v
			}
		}
		if u := strings.ToLower(clip(clean(get("unit")), 30)); u != "" {
			if cu, ok := units.Lookup(u); ok {
				u = cu.Name
			}
			if st.Quantity > 0 {
				st.Unit = u
			}
		}
		notes := []string{clean(get("list notes")), clean(get("ingredient notes"))}
		var keep []string
		for _, n := range notes {
			if n != "" {
				keep = append(keep, n)
			}
		}
		st.Note = clip(strings.Join(keep, "; "), 500)
		out = append(out, st)
	}
	return out, skipped, nil
}
