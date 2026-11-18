package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/voidgrid/voidgrid-shopping/internal/ingredient"
	"github.com/voidgrid/voidgrid-shopping/internal/storage"
	"github.com/voidgrid/voidgrid-shopping/internal/units"
)

// parseReport runs the ingredient parser over every stored recipe and prints
// how it did and what it could not read.
func parseReport(ctx context.Context, db *storage.DB) error {
	cards, err := db.ListRecipes(ctx, storage.Filter{})
	if err != nil {
		return err
	}
	var total, sections, noQty, withUnit, volume int
	unknown := map[string]int{}
	for _, c := range cards {
		r, err := db.Recipe(ctx, c.ID)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(r.Ingredients, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			total++
			it := ingredient.Parse(line)
			switch {
			case it.Section:
				sections++
				continue
			case !it.HasQty:
				noQty++
				continue
			}
			if it.Unit == units.NoUnit {
				if w := strings.Fields(strings.ToLower(it.Name)); len(w) > 0 {
					unknown[w[0]]++
				}
				continue
			}
			withUnit++
			if it.Unit.Kind != units.Volume {
				continue
			}
			volume++
		}
	}
	fmt.Printf("recipes %d, ingredient lines %d\n", len(cards), total)
	fmt.Printf("  headings %d, no amount %d (salt, to taste ...), amount with a unit %d\n", sections, noQty, withUnit)
	fmt.Printf("  of those, measured by volume %d\n", volume)

	type kv struct {
		k string
		v int
	}
	var words []kv
	for k, v := range unknown {
		words = append(words, kv{k, v})
	}
	sort.Slice(words, func(i, j int) bool {
		return words[i].v > words[j].v || words[i].v == words[j].v && words[i].k < words[j].k
	})
	fmt.Println("\nFirst word after an amount that is not a unit (a missing unit shows up here):")
	for i, w := range words {
		if i == 40 {
			break
		}
		fmt.Printf("  %4d  %s\n", w.v, w.k)
	}
	return nil
}

// searchUSDA lists USDA foods that have a weight per volume and match every word.
func searchUSDA(ctx context.Context, db *storage.DB, words string) error {
	list, err := db.SearchDensities(ctx, words, 15)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("no USDA food with a volume measure has all those words")
		return nil
	}
	for _, d := range list {
		fmt.Printf("%4.0f g/cup  %s (fdc %d)\n", d.GPerMl*units.Cup.Base, d.Description, d.FdcID)
	}
	return nil
}
