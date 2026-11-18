package importer

import (
	"strings"
	"testing"
)

var sample = "\xef\xbb\xbf" + "Title,Course,Cuisine,Main Ingredient,Description,Source,Url,Url Host,Prep Time,Cook Time,Total Time," +
	"Servings,Yield,Ingredients,Directions,Tags,Rating,Photo Url,Calories,Protein,Cost\r\n" +
	"Mom&#39;s Soup,Dinner,American,Chicken,A soup,Mom,https://example.com/soup,example.com,10.0,50.0,60.0," +
	"8,8 bowls,\"2 cups broth\r\n1 lb chicken\",\"Boil.\nServe.\",\"Soups, Easy, soups\",5,https://images.plantoeat.com/recipes/1/a,500,30,4.5\r\n" +
	"Cold Brew,,,,,,not a url,,,,,,,1 cup coffee,Steep.,,,https://evil.example.net/x.jpg,,,\r\n" +
	",,,,,,,,,,,,,x,y,,,,,,\r\n" +
	"Toast,Breakfast,,,,,,,3.0,2.0,0.0,two,,1 slice bread,Toast it.,,,http://images.plantoeat.com/recipes/2/b,,,\r\n"

func TestPlanToEat(t *testing.T) {
	recs, skipped, err := PlanToEat(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d recipes, want 3", len(recs))
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "row 4") {
		t.Errorf("skipped = %v, want the untitled row 4", skipped)
	}
	soup := recs[0]
	if soup.Title != "Mom's Soup" {
		t.Errorf("title %q: entity not decoded", soup.Title)
	}
	if soup.PrepMin != 10 || soup.CookMin != 50 || soup.TotalMin != 60 || soup.Servings != 8 {
		t.Errorf("numbers: %+v", soup)
	}
	if soup.Ingredients != "2 cups broth\n1 lb chicken" {
		t.Errorf("ingredients %q: line endings not normalised", soup.Ingredients)
	}
	if got := strings.Join(soup.Tags, "|"); got != "Easy|Soups" {
		t.Errorf("tags %q, want duplicates merged and sorted", got)
	}
	if soup.URL != "https://example.com/soup" || soup.PhotoSrc != "https://images.plantoeat.com/recipes/1/a" {
		t.Errorf("addresses: url %q photo %q", soup.URL, soup.PhotoSrc)
	}
	if soup.Rating != 0 {
		t.Error("rating must not be imported")
	}
}

func TestPlanToEatRejectsOtherHostsAndBadAddresses(t *testing.T) {
	recs, _, err := PlanToEat(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	cold, toast := recs[1], recs[2]
	if cold.PhotoSrc != "" {
		t.Errorf("photo from another host kept: %q", cold.PhotoSrc)
	}
	if cold.URL != "" {
		t.Errorf("non-web address kept: %q", cold.URL)
	}
	if toast.PhotoSrc != "" {
		t.Errorf("plain-http photo kept: %q", toast.PhotoSrc)
	}
	if toast.Servings != 0 {
		t.Errorf("servings %d from %q", toast.Servings, "two")
	}
	if toast.TotalMin != 5 {
		t.Errorf("total %d, want prep+cook 5 when the total is 0", toast.TotalMin)
	}
}

func TestPlanToEatNotAnExport(t *testing.T) {
	for _, in := range []string{"", "a,b,c\n1,2,3\n", "Title\nSoup\n"} {
		if _, _, err := PlanToEat(strings.NewReader(in)); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}
