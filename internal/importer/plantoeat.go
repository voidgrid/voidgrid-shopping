// Package importer turns other programs' recipe files into storage.Recipe values.
package importer

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/voidgrid/voidgrid-shopping/internal/storage"
)

// PlanToEatHost is the only host Plan to Eat photos are downloaded from.
const PlanToEatHost = "images.plantoeat.com"

const (
	maxTitle = 200
	maxShort = 200
	maxText  = 50000
)

// PlanToEat reads a Plan to Eat recipe export (CSV). Nutrition, rating and
// cost columns are ignored on purpose. The second result lists rows that
// were skipped, as "row N: reason".
func PlanToEat(r io.Reader) ([]storage.Recipe, []string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, err
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	cr := csv.NewReader(bytes.NewReader(data))
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	head, err := cr.Read()
	if err != nil {
		return nil, nil, errors.New("this does not look like a Plan to Eat export: no header row")
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	if _, ok := col["title"]; !ok {
		return nil, nil, errors.New("this does not look like a Plan to Eat export: no Title column")
	}
	if _, ok := col["ingredients"]; !ok {
		return nil, nil, errors.New("this does not look like a Plan to Eat export: no Ingredients column")
	}

	var out []storage.Recipe
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
		title := clip(clean(get("title")), maxTitle)
		if title == "" {
			skipped = append(skipped, fmt.Sprintf("row %d: no title", row))
			continue
		}
		rc := storage.Recipe{
			Title:          title,
			Course:         clip(clean(get("course")), maxShort),
			Cuisine:        clip(clean(get("cuisine")), maxShort),
			MainIngredient: clip(clean(get("main ingredient")), maxShort),
			Description:    clip(clean(get("description")), maxText),
			Source:         clip(clean(get("source")), maxShort),
			URL:            webAddress(get("url")),
			PrepMin:        minutes(get("prep time")),
			CookMin:        minutes(get("cook time")),
			TotalMin:       minutes(get("total time")),
			Servings:       leadingInt(get("servings")),
			YieldText:      clip(clean(get("yield")), maxShort),
			Ingredients:    clip(clean(get("ingredients")), maxText),
			Directions:     clip(clean(get("directions")), maxText),
			Tags:           splitTags(get("tags")),
		}
		if rc.TotalMin == 0 {
			rc.TotalMin = rc.PrepMin + rc.CookMin
		}
		if p := webAddress(get("photo url")); p != "" {
			if u, err := url.Parse(p); err == nil && u.Scheme == "https" && strings.EqualFold(u.Hostname(), PlanToEatHost) {
				rc.PhotoSrc = p
			}
		}
		out = append(out, rc)
	}
	return out, skipped, nil
}

// clean decodes HTML entities and unifies line endings, keeping one entry per line.
func clean(s string) string {
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

func webAddress(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(s) > 2000 {
		return ""
	}
	return u.String()
}

// minutes reads "63.0" as 63; anything unreadable is 0.
func minutes(s string) int {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 0 || f > 100000 || math.IsNaN(f) {
		return 0
	}
	return int(math.Round(f))
}

// leadingInt reads the whole number a text such as "12" or "6 servings" starts with.
func leadingInt(s string) int {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil || n > 10000 {
		return 0
	}
	return n
}

func splitTags(s string) []string {
	return storage.CleanTags(strings.Split(clean(s), ","))
}
