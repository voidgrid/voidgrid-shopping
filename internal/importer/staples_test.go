package importer

import (
	"strings"
	"testing"
)

const pteList = "Store,Category,Title,Quantity,Unit,Ingredient Notes,List Notes\r\n" +
	"HEB,Dairy,Milk,2,gallons,,Buy when less than 1\r\n" +
	"HEB,Produce,Bananas,,,,\r\n" +
	"HEB,Pantry,Reese&#39;s Cups,1 1/2,Bag,,\r\n" +
	"HEB,Misc,,1,,,\r\n" +
	"HEB,Misc,Grab the reusable bags,,,,\r\n"

func TestPlanToEatStaples(t *testing.T) {
	got, skipped, err := PlanToEatStaples(strings.NewReader(pteList))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || len(skipped) != 1 || !strings.Contains(skipped[0], "row 5") {
		t.Fatalf("got %d staples, skipped %v", len(got), skipped)
	}
	milk := got[0]
	if milk.Name != "Milk" || milk.Quantity != 2 || milk.Unit != "" && milk.Unit != "gallon" || milk.Note != "Buy when less than 1" {
		t.Errorf("milk %+v", milk)
	}
	if milk.StoreID != 0 || milk.StoreName != "" {
		t.Errorf("store imported: %+v", milk)
	}
	if got[1].Quantity != 0 || got[1].Unit != "" {
		t.Errorf("bananas %+v", got[1])
	}
	if got[2].Name != "Reese's Cups" || got[2].Quantity != 1.5 || got[2].Unit != "bag" {
		t.Errorf("entities/fractions %+v", got[2])
	}
}

func TestPlanToEatStaplesNotAList(t *testing.T) {
	for _, in := range []string{"", "a,b\n1,2\n"} {
		if _, _, err := PlanToEatStaples(strings.NewReader(in)); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
	// a header-only export is valid and empty
	if got, _, err := PlanToEatStaples(strings.NewReader("Store,Category,Title,Quantity,Unit\n")); err != nil || len(got) != 0 {
		t.Errorf("header only: %v %v", got, err)
	}
}
