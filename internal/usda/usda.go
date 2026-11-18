// Package usda reads USDA FoodData Central CSV downloads (SR Legacy or
// Foundation Foods; public domain) into foods and household measures.
package usda

import (
	"archive/zip"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/voidgrid/voidgrid-shopping/internal/storage"
	"github.com/voidgrid/voidgrid-shopping/internal/units"
)

// maxFile caps what is read from one file inside an archive.
const maxFile = 600 << 20

// ingredient data types worth importing; branded products are millions of rows we don't want.
var wantedTypes = map[string]bool{"sr_legacy_food": true, "foundation_food": true}

// Data is what Read loads.
type Data struct {
	Foods     []storage.USDAFood
	Portions  []storage.USDAPortion
	Nutrients []storage.USDANutrients
}

// nutrient ids in FoodData Central: energy (kcal), protein, total fat, carbohydrate.
const (
	nutKcal    = 1008
	nutProtein = 1003
	nutFat     = 1004
	nutCarbs   = 1005
)

// Read loads foods, household measures and the four macronutrients from a
// .zip download or an unpacked folder. food_nutrient.csv is optional (an
// older import without it still gives densities).
func Read(src string) (Data, error) {
	open, closeFn, err := opener(src)
	if err != nil {
		return Data{}, err
	}
	defer closeFn()

	fr, err := open("food.csv")
	if err != nil {
		return Data{}, err
	}
	foods, err := readFoods(fr)
	_ = fr.Close()
	if err != nil {
		return Data{}, fmt.Errorf("usda: food.csv: %w", err)
	}
	if len(foods) == 0 {
		return Data{}, errors.New("usda: no SR Legacy or Foundation foods in food.csv (branded-only downloads are not used)")
	}
	pr, err := open("food_portion.csv")
	if err != nil {
		return Data{}, err
	}
	portions, err := readPortions(pr, foods)
	_ = pr.Close()
	if err != nil {
		return Data{}, fmt.Errorf("usda: food_portion.csv: %w", err)
	}
	d := Data{Portions: portions}
	for _, f := range foods {
		d.Foods = append(d.Foods, f)
	}
	if nr, err := open("food_nutrient.csv"); err == nil {
		d.Nutrients, err = readNutrients(nr, foods)
		_ = nr.Close()
		if err != nil {
			return Data{}, fmt.Errorf("usda: food_nutrient.csv: %w", err)
		}
	}
	return d, nil
}

// readNutrients keeps the four macronutrients of each wanted food; a food
// missing energy is dropped, the others default to 0.
func readNutrients(r io.Reader, foods map[int64]storage.USDAFood) ([]storage.USDANutrients, error) {
	cr := csv.NewReader(r)
	col, err := header(cr, "fdc_id", "nutrient_id", "amount")
	if err != nil {
		return nil, err
	}
	got := map[int64]*storage.USDANutrients{}
	seen := map[int64]bool{}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		nid, err1 := strconv.Atoi(rec[col["nutrient_id"]])
		if err1 != nil || (nid != nutKcal && nid != nutProtein && nid != nutFat && nid != nutCarbs) {
			continue
		}
		id, err2 := strconv.ParseInt(rec[col["fdc_id"]], 10, 64)
		amt, err3 := strconv.ParseFloat(rec[col["amount"]], 64)
		if _, ok := foods[id]; !ok || err2 != nil || err3 != nil || amt < 0 {
			continue
		}
		n := got[id]
		if n == nil {
			n = &storage.USDANutrients{FdcID: id}
			got[id] = n
		}
		switch nid {
		case nutKcal:
			n.Kcal, seen[id] = amt, true
		case nutProtein:
			n.Protein = amt
		case nutFat:
			n.Fat = amt
		case nutCarbs:
			n.Carbs = amt
		}
	}
	var out []storage.USDANutrients
	for id, n := range got {
		if seen[id] {
			out = append(out, *n)
		}
	}
	return out, nil
}

func opener(src string) (func(string) (io.ReadCloser, error), func(), error) {
	st, err := os.Stat(src)
	if err != nil {
		return nil, nil, err
	}
	if st.IsDir() {
		return func(name string) (io.ReadCloser, error) {
			return os.Open(filepath.Join(src, name)) //nolint:gosec // an admin-supplied folder; only fixed file names are opened
		}, func() {}, nil
	}
	zr, err := zip.OpenReader(src)
	if err != nil {
		return nil, nil, fmt.Errorf("usda: %s is neither a folder nor a zip file: %w", src, err)
	}
	return func(name string) (io.ReadCloser, error) {
		for _, f := range zr.File {
			if path.Base(f.Name) == name && !f.FileInfo().IsDir() {
				if f.UncompressedSize64 > maxFile {
					return nil, fmt.Errorf("usda: %s is unexpectedly large", name)
				}
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				return limited{io.LimitReader(rc, maxFile), rc}, nil
			}
		}
		return nil, fmt.Errorf("usda: %s not found in the download", name)
	}, func() { _ = zr.Close() }, nil
}

type limited struct {
	io.Reader
	c io.Closer
}

func (l limited) Close() error { return l.c.Close() }

func header(cr *csv.Reader, need ...string) (map[string]int, error) {
	h, err := cr.Read()
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, n := range h {
		col[strings.ToLower(strings.TrimSpace(n))] = i
	}
	for _, n := range need {
		if _, ok := col[n]; !ok {
			return nil, fmt.Errorf("missing column %q", n)
		}
	}
	return col, nil
}

func readFoods(r io.Reader) (map[int64]storage.USDAFood, error) {
	cr := csv.NewReader(r)
	col, err := header(cr, "fdc_id", "data_type", "description")
	if err != nil {
		return nil, err
	}
	out := map[int64]storage.USDAFood{}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		dt := rec[col["data_type"]]
		id, err := strconv.ParseInt(rec[col["fdc_id"]], 10, 64)
		if !wantedTypes[dt] || err != nil {
			continue
		}
		out[id] = storage.USDAFood{FdcID: id, Description: strings.TrimSpace(rec[col["description"]]), DataType: dt}
	}
}

func readPortions(r io.Reader, foods map[int64]storage.USDAFood) ([]storage.USDAPortion, error) {
	cr := csv.NewReader(r)
	col, err := header(cr, "fdc_id", "amount", "modifier", "gram_weight")
	if err != nil {
		return nil, err
	}
	pd, hasPD := col["portion_description"]
	var out []storage.USDAPortion
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		id, err1 := strconv.ParseInt(rec[col["fdc_id"]], 10, 64)
		amount, err2 := strconv.ParseFloat(rec[col["amount"]], 64)
		grams, err3 := strconv.ParseFloat(rec[col["gram_weight"]], 64)
		if _, ok := foods[id]; !ok || err1 != nil || err2 != nil || err3 != nil || amount <= 0 || grams <= 0 {
			continue
		}
		label := strings.TrimSpace(rec[col["modifier"]])
		if label == "" && hasPD {
			label = strings.TrimSpace(rec[pd])
		}
		if label == "" {
			continue
		}
		out = append(out, storage.USDAPortion{FdcID: id, Amount: amount, Label: label, Grams: grams, ML: volumeML(label, amount)})
	}
}

// volumeML is the volume of amount of label ("cup", "cup, chopped", "fl oz"), or 0.
func volumeML(label string, amount float64) float64 {
	words := strings.Fields(strings.ToLower(label))
	if len(words) == 0 {
		return 0
	}
	u, ok := units.Lookup(words[0])
	if len(words) > 1 && words[0] == "fl" && strings.TrimRight(words[1], ",.") == "oz" {
		u, ok = units.FlOz, true
	}
	if !ok || u.Kind != units.Volume {
		return 0
	}
	return amount * u.Base
}
