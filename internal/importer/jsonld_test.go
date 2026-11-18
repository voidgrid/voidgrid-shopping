package importer

import (
	"errors"
	"strings"
	"testing"
)

const page = `<html><head><title>x</title>
<script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"WebSite","name":"Site"},
{"@type":["Recipe"],"name":"Lemon &amp; Herb Chicken","description":"Juicy.","image":[{"@type":"ImageObject","url":"https://img.example/a.jpg"}],
"author":{"@type":"Person","name":"Pat"},"prepTime":"PT15M","cookTime":"PT1H","totalTime":"PT1H15M","recipeYield":["4","4 servings"],
"recipeCategory":["Dinner"],"recipeCuisine":"Italian","keywords":"chicken, quick, Chicken",
"recipeIngredient":["2 lemons","1 lb chicken thighs"],
"recipeInstructions":[{"@type":"HowToSection","name":"Marinade","itemListElement":[{"@type":"HowToStep","text":"Mix."}]},
{"@type":"HowToStep","text":"Roast.\nRest."}]}]}</script></head><body></body></html>`

func TestFromHTML(t *testing.T) {
	r, err := FromHTML([]byte(page), "https://www.cooks.example/lemon")
	if err != nil {
		t.Fatal(err)
	}
	if r.Title != "Lemon & Herb Chicken" || r.Servings != 4 || r.PrepMin != 15 || r.CookMin != 60 || r.TotalMin != 75 {
		t.Errorf("basics %+v", r)
	}
	if r.Course != "Dinner" || r.Cuisine != "Italian" || r.Source != "Pat" || r.URL != "https://www.cooks.example/lemon" {
		t.Errorf("meta %+v", r)
	}
	if r.PhotoSrc != "https://img.example/a.jpg" {
		t.Errorf("photo %q", r.PhotoSrc)
	}
	if r.Ingredients != "2 lemons\n1 lb chicken thighs" {
		t.Errorf("ingredients %q", r.Ingredients)
	}
	if r.Directions != "Marinade:\nMix.\nRoast.\nRest." {
		t.Errorf("directions %q", r.Directions)
	}
	if len(r.Tags) != 2 {
		t.Errorf("tags %v", r.Tags)
	}
}

func TestFromJSONLDShapes(t *testing.T) {
	r, err := FromJSONLD([]byte(`[{"@type":"Recipe","name":"A","recipeYield":"12 muffins","recipeInstructions":"One.\nTwo.","recipeIngredient":"1 egg"}]`), "")
	if err != nil || r.Servings != 12 || r.YieldText != "12 muffins" || r.Directions != "One.\nTwo." || r.Ingredients != "1 egg" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := FromJSONLD([]byte(`{"@type":"Article"}`), ""); !errors.Is(err, ErrNoRecipe) {
		t.Errorf("article: %v", err)
	}
	if _, err := FromJSONLD([]byte(`{nope`), ""); err == nil {
		t.Error("bad JSON accepted")
	}
	if _, err := FromHTML([]byte("<html>nothing</html>"), ""); !errors.Is(err, ErrNoRecipe) {
		t.Errorf("no script: %v", err)
	}
	// an address that is not http(s) is never recorded
	if r, _ := FromJSONLD([]byte(`{"@type":"Recipe","name":"A","image":"javascript:alert(1)"}`), "javascript:x"); r.PhotoSrc != "" || r.URL != "" {
		t.Errorf("unsafe address kept: %+v", r)
	}
	if isoMinutes("P0DT1H30M") != 90 || isoMinutes("junk") != 0 {
		t.Error("durations")
	}
}

func TestFromTextWithHeadings(t *testing.T) {
	r := FromText("Pancakes\r\n\r\nIngredients:\r\n- 1 cup flour\r\n- 1 egg\r\nFor the syrup\r\n1/2 cup sugar\r\n\r\nDirections\r\n1. Mix.\r\n2) Fry.\r\n")
	if r.Title != "Pancakes" || r.Ingredients != "1 cup flour\n1 egg\nFor the syrup\n1/2 cup sugar" || r.Directions != "Mix.\nFry." {
		t.Errorf("%+v", r)
	}
}

func TestFromTextBlocks(t *testing.T) {
	r := FromText("Quick rice\n\n2 cups rice\n4 cups water\nsalt\n\nBoil the water, add the rice and cover for 15 minutes.\n")
	if r.Ingredients != "2 cups rice\n4 cups water\nsalt" || !strings.HasPrefix(r.Directions, "Boil") {
		t.Errorf("%+v", r)
	}
	if r := FromText("   \n"); r.Title != "" {
		t.Errorf("blank: %+v", r)
	}
}

func TestFromCaption(t *testing.T) {
	r := FromCaption("Garlic noodles\nIngredients\n8 oz noodles\n4 clove garlic\nDirections\nCook. Toss. #noodles #quickdinner", "tt title")
	if r.Title != "Garlic noodles" || len(r.Tags) != 2 || !strings.HasPrefix(r.Directions, "Cook. Toss.") || strings.Contains(r.Directions, "#") {
		t.Errorf("%+v", r)
	}
	r = FromCaption("Just vibes #fyp", "Fallback")
	if r.Title != "Fallback" || len(r.Tags) != 1 {
		t.Errorf("fallback %+v", r)
	}
}
