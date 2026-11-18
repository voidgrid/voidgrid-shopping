package importer

import (
	"encoding/json"
	"errors"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/voidgrid/voidgrid-shopping/internal/storage"
)

// ErrNoRecipe means a page or file held no schema.org Recipe.
var ErrNoRecipe = errors.New("no recipe data found")

var ldScript = regexp.MustCompile(`(?is)<script[^>]*type\s*=\s*["']?application/ld\+json["']?[^>]*>(.*?)</script>`)

// FromHTML reads the schema.org Recipe from a web page's JSON-LD. pageURL is
// recorded as the recipe's website.
func FromHTML(page []byte, pageURL string) (storage.Recipe, error) {
	for _, m := range ldScript.FindAllSubmatch(page, -1) {
		if rec, err := FromJSONLD(m[1], pageURL); err == nil {
			return rec, nil
		}
	}
	return storage.Recipe{}, ErrNoRecipe
}

// FromJSONLD reads a schema.org Recipe from JSON: a single object, an array,
// or an object with an @graph list.
func FromJSONLD(data []byte, pageURL string) (storage.Recipe, error) {
	var v any
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(string(data), "\xef\xbb\xbf"))), &v); err != nil {
		return storage.Recipe{}, errors.New("that is not valid JSON")
	}
	obj := findRecipe(v, 0)
	if obj == nil {
		return storage.Recipe{}, ErrNoRecipe
	}
	return recipeFromLD(obj, pageURL), nil
}

func isRecipe(m map[string]any) bool {
	switch t := m["@type"].(type) {
	case string:
		return t == "Recipe" || strings.HasSuffix(t, "/Recipe")
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok && (s == "Recipe" || strings.HasSuffix(s, "/Recipe")) {
				return true
			}
		}
	}
	return false
}

func findRecipe(v any, depth int) map[string]any {
	if depth > 6 {
		return nil
	}
	switch x := v.(type) {
	case map[string]any:
		if isRecipe(x) {
			return x
		}
		if g, ok := x["@graph"]; ok {
			if r := findRecipe(g, depth+1); r != nil {
				return r
			}
		}
		if m, ok := x["mainEntity"]; ok {
			return findRecipe(m, depth+1)
		}
	case []any:
		for _, e := range x {
			if r := findRecipe(e, depth+1); r != nil {
				return r
			}
		}
	}
	return nil
}

// str returns the text of a JSON-LD value: a string, a number, the first
// element of a list, or an object's name/text/url.
func str(v any) string {
	switch x := v.(type) {
	case string:
		return ldClean(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case []any:
		for _, e := range x {
			if s := str(e); s != "" {
				return s
			}
		}
	case map[string]any:
		for _, k := range []string{"name", "text", "url", "@id"} {
			if s := str(x[k]); s != "" {
				return s
			}
		}
	}
	return ""
}

func strs(v any) []string {
	switch x := v.(type) {
	case []any:
		var out []string
		for _, e := range x {
			if s := str(e); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if s := ldClean(x); s != "" {
			return []string{s}
		}
	}
	return nil
}

var spaceRE = regexp.MustCompile(`[ \t\r\f\v\x{a0}]+`)

func ldClean(s string) string {
	s = html.UnescapeString(s)
	s = spaceRE.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

var durRE = regexp.MustCompile(`(?i)^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:\d+S)?)?$`)

// minutes reads an ISO 8601 duration such as PT1H30M.
func isoMinutes(v any) int {
	m := durRE.FindStringSubmatch(strings.TrimSpace(str(v)))
	if m == nil {
		return 0
	}
	n := func(s string) int { i, _ := strconv.Atoi(s); return i }
	t := n(m[1])*24*60 + n(m[2])*60 + n(m[3])
	if t > 100000 {
		return 0
	}
	return t
}

var firstInt = regexp.MustCompile(`\d+`)

func recipeFromLD(o map[string]any, pageURL string) storage.Recipe {
	rec := storage.Recipe{
		Title:       truncate(str(o["name"]), maxTitle),
		Description: truncate(str(o["description"]), maxText),
		Course:      truncate(str(o["recipeCategory"]), maxShort),
		Cuisine:     truncate(str(o["recipeCuisine"]), maxShort),
		PrepMin:     isoMinutes(o["prepTime"]),
		CookMin:     isoMinutes(o["cookTime"]),
		TotalMin:    isoMinutes(o["totalTime"]),
	}
	if rec.TotalMin == 0 {
		rec.TotalMin = rec.PrepMin + rec.CookMin
	}
	if y := str(o["recipeYield"]); y != "" {
		if n, err := strconv.Atoi(firstInt.FindString(y)); err == nil && n > 0 && n <= 10000 {
			rec.Servings = n
		}
		if _, err := strconv.Atoi(y); err != nil {
			rec.YieldText = truncate(y, maxShort)
		}
	}
	ing := strs(o["recipeIngredient"])
	if len(ing) == 0 {
		ing = strs(o["ingredients"])
	}
	rec.Ingredients = truncate(strings.Join(ing, "\n"), maxText)
	rec.Directions = truncate(strings.Join(steps(o["recipeInstructions"]), "\n"), maxText)

	if kw := o["keywords"]; kw != nil {
		var tags []string
		if s, ok := kw.(string); ok {
			tags = strings.Split(s, ",")
		} else {
			tags = strs(kw)
		}
		rec.Tags = storage.CleanTags(tags)
	}
	if a := str(o["author"]); a != "" {
		rec.Source = truncate(a, maxShort)
	}
	if img := imageURL(o["image"]); img != "" {
		rec.PhotoSrc = img
	}
	if u, err := url.Parse(pageURL); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		rec.URL = u.String()
		if rec.Source == "" {
			rec.Source = strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		}
	} else if u := str(o["url"]); strings.HasPrefix(u, "http") {
		rec.URL = u
	}
	return rec
}

func truncate(s string, n int) string {
	if len(s) > n {
		return strings.ToValidUTF8(s[:n], "")
	}
	return s
}

// steps flattens recipeInstructions: plain text, HowToStep, and HowToSection
// (whose name becomes a heading line ending in a colon).
func steps(v any) []string {
	switch x := v.(type) {
	case string:
		var out []string
		for _, l := range strings.Split(strings.ReplaceAll(x, "\r", ""), "\n") {
			if l = ldClean(l); l != "" {
				out = append(out, l)
			}
		}
		return out
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, steps(e)...)
		}
		return out
	case map[string]any:
		if t, _ := x["@type"].(string); t == "HowToSection" {
			var out []string
			if n := str(x["name"]); n != "" {
				out = append(out, strings.TrimSuffix(n, ":")+":")
			}
			return append(out, steps(x["itemListElement"])...)
		}
		if t := str(x["text"]); t != "" {
			return steps(t)
		}
		if n := str(x["name"]); n != "" {
			return []string{n}
		}
	}
	return nil
}

func imageURL(v any) string {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case []any:
		for _, e := range x {
			if s = imageURL(e); s != "" {
				break
			}
		}
	case map[string]any:
		s = str(x["url"])
		if s == "" {
			s = str(x["contentUrl"])
		}
	}
	s = strings.TrimSpace(s)
	if u, err := url.Parse(s); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return u.String()
	}
	return ""
}
