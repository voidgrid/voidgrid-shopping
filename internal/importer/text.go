package importer

import (
	"regexp"
	"strings"

	"github.com/voidgrid/voidgrid-shopping/internal/ingredient"
	"github.com/voidgrid/voidgrid-shopping/internal/storage"
)

var (
	ingHead = regexp.MustCompile(`(?i)^(ingredients?|what you.ll need|you.ll need)\s*:?$`)
	dirHead = regexp.MustCompile(`(?i)^(directions?|instructions?|method|steps?|preparation|how to make( it)?)\s*:?$`)
	stepNum = regexp.MustCompile(`(?i)^\s*(step\s*)?\d{1,2}\s*[.):-]\s+`)
	bullet  = regexp.MustCompile(`^\s*[-*•·▪◦]+\s*`)
	hashtag = regexp.MustCompile(`(^|\s)#([\pL\pN_]+)`)
)

// FromText reads a recipe typed or pasted as plain text: a title line, then an
// "Ingredients" part and a "Directions" part. Without those headings, blocks
// separated by blank lines are sorted by whether most lines start with an
// amount. It only makes a draft; the person reviews it before saving.
func FromText(text string) storage.Recipe {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	var rec storage.Recipe
	lines := strings.Split(text, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	if len(lines) == 0 {
		return rec
	}
	first := strings.TrimSpace(lines[0])
	if !ingHead.MatchString(first) && !dirHead.MatchString(first) {
		rec.Title = truncate(first, maxTitle)
		lines = lines[1:]
	}

	mode := ""
	var ing, dir, other []string
	sawHeading := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case ingHead.MatchString(t):
			mode, sawHeading = "ing", true
			continue
		case dirHead.MatchString(t):
			mode, sawHeading = "dir", true
			continue
		}
		switch mode {
		case "ing":
			ing = append(ing, l)
		case "dir":
			dir = append(dir, l)
		default:
			other = append(other, l)
		}
	}
	if !sawHeading {
		for _, block := range blocks(other) {
			if mostlyAmounts(block) {
				ing = append(ing, block...)
			} else {
				dir = append(dir, block...)
			}
		}
	} else if t := strings.TrimSpace(strings.Join(other, "\n")); t != "" {
		rec.Description = truncate(t, maxText)
	}

	var il, dl []string
	for _, l := range ing {
		if l = strings.TrimSpace(bullet.ReplaceAllString(l, "")); l != "" {
			il = append(il, l)
		}
	}
	for _, l := range dir {
		if l = strings.TrimSpace(stepNum.ReplaceAllString(bullet.ReplaceAllString(l, ""), "")); l != "" {
			dl = append(dl, l)
		}
	}
	rec.Ingredients = truncate(strings.Join(il, "\n"), maxText)
	rec.Directions = truncate(strings.Join(dl, "\n"), maxText)
	return rec
}

func blocks(lines []string) [][]string {
	var out [][]string
	var cur []string
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			if len(cur) > 0 {
				out = append(out, cur)
				cur = nil
			}
			continue
		}
		cur = append(cur, l)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func mostlyAmounts(block []string) bool {
	n := 0
	for _, l := range block {
		if it := ingredient.Parse(bullet.ReplaceAllString(l, "")); it.HasQty && !it.Section && len(l) < 120 {
			n++
		}
	}
	return n*2 >= len(block)
}

// FromCaption turns a social media caption into a draft: the text is read like
// pasted text, and #hashtags become tags. fallbackTitle is used when the
// caption gives no title.
func FromCaption(caption, fallbackTitle string) storage.Recipe {
	var tags []string
	for _, m := range hashtag.FindAllStringSubmatch(caption, -1) {
		tags = append(tags, m[2])
	}
	caption = strings.TrimSpace(hashtag.ReplaceAllString(caption, "$1"))
	rec := FromText(caption)
	if rec.Title == "" || rec.Title == caption {
		rec.Title = truncate(strings.TrimSpace(fallbackTitle), maxTitle)
	}
	rec.Tags = storage.CleanTags(tags)
	return rec
}
