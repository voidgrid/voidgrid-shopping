package usda

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/voidgrid/voidgrid-shopping/internal/storage"
)

const foodCSV = `"fdc_id","data_type","description","food_category_id","publication_date"
"1","sr_legacy_food","Wheat flour, white, all-purpose","20","2019-04-01"
"2","sr_legacy_food","Egg, whole, raw, fresh","1","2019-04-01"
"3","branded_food","Some Brand Cookies","1","2019-04-01"
`

const portionCSV = `"id","fdc_id","seq_num","amount","measure_unit_id","portion_description","modifier","gram_weight","data_points","footnote","min_year_acquired"
"1","1","1","1","9999","","cup","125","","",""
"2","1","2","1","9999","","tbsp","7.8","","",""
"3","2","1","1","9999","","large","50","","",""
"4","2","2","1","9999","","cup, chopped","243","","",""
"5","3","1","1","9999","","cup","30","","",""
"6","1","3","2","9999","","fl oz","60","","",""
"7","1","4","0","9999","","cup","0","","",""
`

func dir(t *testing.T) string {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "food.csv"), []byte(foodCSV), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "food_portion.csv"), []byte(portionCSV), 0o600); err != nil {
		t.Fatal(err)
	}
	return d
}

func check(t *testing.T, src string) {
	d, err := Read(src)
	if err != nil {
		t.Fatal(err)
	}
	foods, portions := d.Foods, d.Portions
	if len(foods) != 2 {
		t.Errorf("foods: %d, want the 2 SR foods (branded skipped)", len(foods))
	}
	if len(portions) != 5 {
		t.Fatalf("portions: %d, want 5 (branded and zero-weight rows dropped)", len(portions))
	}
	byLabel := map[string]float64{}
	for _, p := range portions {
		byLabel[p.Label] = p.ML
	}
	if ml := byLabel["cup"]; ml < 236 || ml > 237 {
		t.Errorf("cup = %v ml", ml)
	}
	if ml := byLabel["fl oz"]; ml < 59 || ml > 60 {
		t.Errorf("2 fl oz = %v ml", ml)
	}
	if byLabel["cup, chopped"] == 0 {
		t.Error("a chopped cup should still count as a volume")
	}
	if byLabel["large"] != 0 {
		t.Error("an each-measure was taken for a volume")
	}
}

func TestReadFolder(t *testing.T) { check(t, dir(t)) }

const nutrientCSV = `"id","fdc_id","nutrient_id","amount","data_points","derivation_id","min","max","median","footnote","min_year_acquired"
"1","1","1008","364","","","","","","",""
"2","1","1003","10.3","","","","","","",""
"3","1","1004","1","","","","","","",""
"4","1","1005","76.3","","","","","","",""
"5","1","1079","2.7","","","","","","",""
"6","2","1008","143","","","","","","",""
"7","2","1003","12.6","","","","","","",""
"8","3","1008","500","","","","","","",""
"9","99","1008","500","","","","","","",""
`

func TestReadNutrients(t *testing.T) {
	d := dir(t)
	if err := os.WriteFile(filepath.Join(d, "food_nutrient.csv"), []byte(nutrientCSV), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Nutrients) != 2 {
		t.Fatalf("nutrients %+v (branded and unknown foods must be left out)", got.Nutrients)
	}
	byID := map[int64]storage.USDANutrients{}
	for _, n := range got.Nutrients {
		byID[n.FdcID] = n
	}
	if f := byID[1]; f.Kcal != 364 || f.Protein != 10.3 || f.Fat != 1 || f.Carbs != 76.3 {
		t.Errorf("flour %+v (fibre and other nutrients must be ignored)", f)
	}
	if e := byID[2]; e.Kcal != 143 || e.Carbs != 0 {
		t.Errorf("egg %+v", e)
	}
}

func TestReadZip(t *testing.T) {
	zp := filepath.Join(t.TempDir(), "d.zip")
	f, err := os.Create(zp) //nolint:gosec // a test file in a temp dir
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range map[string]string{"FoodData/food.csv": foodCSV, "FoodData/food_portion.csv": portionCSV} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	_ = f.Close()
	check(t, zp)
}

func TestReadRejectsOtherFiles(t *testing.T) {
	d := t.TempDir()
	if _, err := Read(d); err == nil {
		t.Error("empty folder accepted")
	}
	if err := os.WriteFile(filepath.Join(d, "food.csv"), []byte("a,b\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(d); err == nil {
		t.Error("wrong columns accepted")
	}
	bad := filepath.Join(t.TempDir(), "x.zip")
	_ = os.WriteFile(bad, []byte("not a zip"), 0o600)
	if _, err := Read(bad); err == nil {
		t.Error("non-zip accepted")
	}
}
