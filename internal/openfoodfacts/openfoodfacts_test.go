package openfoodfacts

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/voidgrid/voidgrid-shopping/internal/storage"
)

func server(t *testing.T, body string, status int) (*Client, *int, *http.Request) {
	t.Helper()
	var hits int
	var last http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		last = *r.Clone(r.Context())
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, 3), &hits, &last
}

const cheerios = `{"status":1,"product":{"code":"0016000275287","product_name":"Cheerios","brands":"Cheerios, General Mills",
"nutriments":{"energy-kcal_100g":358.97,"proteins_100g":12.8,"carbohydrates_100g":"39","fat_100g":6.1},"serving_quantity":"39","product_quantity":306}}`

func TestLookupFound(t *testing.T) {
	c, hits, last := server(t, cheerios, 200)
	p, found, err := c.Lookup(context.Background(), "0016000275287")
	if err != nil || !found {
		t.Fatalf("lookup: %v %v", found, err)
	}
	if p.Name != "Cheerios" || p.Brand != "Cheerios" || p.Per100.Kcal != 358.97 || p.Per100.Carbs != 39 || !p.HasNutrition || p.ServingG != 39 || p.PackageG != 306 {
		t.Errorf("product %+v", p)
	}
	if *hits != 1 || !strings.Contains(last.URL.Path, "0016000275287") {
		t.Errorf("request %v hits %d", last.URL, *hits)
	}
	if ua := last.Header.Get("User-Agent"); !strings.HasPrefix(ua, "voidgrid-shopping") || strings.ContainsAny(ua, "@") {
		t.Errorf("user agent %q must be generic", ua)
	}
	if q := last.URL.RawQuery; strings.Contains(q, "user") || strings.Contains(q, "email") {
		t.Errorf("query %q carries more than fields", q)
	}
}

func TestLookupMissingNutritionAndNotFound(t *testing.T) {
	c, _, _ := server(t, `{"status":1,"product":{"code":"12345670","product_name":"Water","nutriments":{}}}`, 200)
	p, found, err := c.Lookup(context.Background(), "12345670")
	if err != nil || !found || p.HasNutrition {
		t.Errorf("no nutrition: %+v %v %v", p, found, err)
	}
	c, _, _ = server(t, `{"status":0,"status_verbose":"product not found"}`, 200)
	if _, found, err := c.Lookup(context.Background(), "12345670"); err != nil || found {
		t.Errorf("not found: %v %v", found, err)
	}
	c, _, _ = server(t, `nope`, 200)
	if _, _, err := c.Lookup(context.Background(), "12345670"); err == nil {
		t.Error("garbage accepted")
	}
	c, _, _ = server(t, ``, 500)
	if _, _, err := c.Lookup(context.Background(), "12345670"); err == nil {
		t.Error("server error accepted")
	}
}

func TestLookupRejectsBadBarcodesAndRateLimits(t *testing.T) {
	c, hits, _ := server(t, cheerios, 200)
	for _, b := range []string{"", "abc", "123", "1234567890123456", "../../etc", "12345678;x"} {
		if _, _, err := c.Lookup(context.Background(), b); !errors.Is(err, storage.ErrInvalid) {
			t.Errorf("%q: %v", b, err)
		}
	}
	if *hits != 0 {
		t.Error("an invalid barcode reached the network")
	}
	for i := 0; i < 3; i++ {
		if _, _, err := c.Lookup(context.Background(), "0016000275287"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := c.Lookup(context.Background(), "0016000275287"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("fourth lookup in a minute: %v", err)
	}
	now := time.Now().Add(2 * time.Minute)
	c.now = func() time.Time { return now }
	if _, _, err := c.Lookup(context.Background(), "0016000275287"); err != nil {
		t.Errorf("after the minute: %v", err)
	}
}
