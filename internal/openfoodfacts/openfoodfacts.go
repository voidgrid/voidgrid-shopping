// Package openfoodfacts looks products up by barcode in Open Food Facts
// (open data, ODbL licence). It sends the barcode and nothing else, with a
// generic User-Agent, and stays well under the service's read limit.
package openfoodfacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/voidgrid/voidgrid-shopping/internal/storage"
)

// DefaultBase is the public API.
const DefaultBase = "https://world.openfoodfacts.org"

// ErrRateLimited means too many lookups were made in the last minute.
var ErrRateLimited = errors.New("openfoodfacts: too many lookups, wait a minute")

// Client makes lookups. The zero value is not usable; use New.
type Client struct {
	Base string
	HTTP *http.Client

	mu    sync.Mutex
	times []time.Time
	max   int
	now   func() time.Time
}

// New returns a client for base (DefaultBase if empty) allowing at most
// perMinute lookups a minute (the documented limit for product reads is 15).
func New(base string, perMinute int) *Client {
	if base == "" {
		base = DefaultBase
	}
	if perMinute <= 0 {
		perMinute = 10
	}
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 10 * time.Second}, max: perMinute, now: time.Now}
}

func (c *Client) allow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	keep := c.times[:0]
	for _, t := range c.times {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	c.times = keep
	if len(c.times) >= c.max {
		return false
	}
	c.times = append(c.times, now)
	return true
}

// Lookup fetches a product. found is false when Open Food Facts doesn't know the barcode.
func (c *Client) Lookup(ctx context.Context, barcode string) (p storage.OFFProduct, found bool, err error) {
	if !storage.ValidBarcode(barcode) {
		return p, false, storage.ErrInvalid
	}
	if !c.allow() {
		return p, false, ErrRateLimited
	}
	u := c.Base + "/api/v2/product/" + url.PathEscape(barcode) + ".json?fields=" + url.QueryEscape("code,product_name,brands,nutriments,serving_quantity,product_quantity")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return p, false, err
	}
	req.Header.Set("User-Agent", "voidgrid-shopping (self-hosted recipe app)")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return p, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return p, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return p, false, fmt.Errorf("openfoodfacts: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Status  int `json:"status"`
		Product struct {
			Code     string         `json:"code"`
			Name     string         `json:"product_name"`
			Brands   string         `json:"brands"`
			Nutri    map[string]any `json:"nutriments"`
			Serving  any            `json:"serving_quantity"`
			Quantity any            `json:"product_quantity"`
		} `json:"product"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&body); err != nil {
		return p, false, fmt.Errorf("openfoodfacts: unreadable answer: %w", err)
	}
	if body.Status != 1 {
		return p, false, nil
	}
	n := body.Product.Nutri
	kcal, okK := number(n["energy-kcal_100g"])
	protein, okP := number(n["proteins_100g"])
	carbs, okC := number(n["carbohydrates_100g"])
	fat, okF := number(n["fat_100g"])
	serving, _ := number(body.Product.Serving)
	pack, _ := number(body.Product.Quantity)
	p = storage.OFFProduct{
		Barcode: barcode, Name: strings.TrimSpace(body.Product.Name), Brand: strings.TrimSpace(strings.SplitN(body.Product.Brands, ",", 2)[0]),
		Per100:       storage.Macros{Kcal: kcal, Protein: protein, Carbs: carbs, Fat: fat},
		HasNutrition: okK && (okP || okC || okF),
		ServingG:     serving, PackageG: pack,
	}
	return p, true, nil
}

// number reads a JSON number or numeric string; negative or absurd values are rejected.
func number(v any) (float64, bool) {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case string:
		g, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return 0, false
		}
		f = g
	default:
		return 0, false
	}
	if f < 0 || f > 100000 {
		return 0, false
	}
	return f, true
}
