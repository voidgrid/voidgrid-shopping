package safehttp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBlocksNonPublicAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hi")) }))
	defer srv.Close()
	if _, _, err := Get(t.Context(), New(false), srv.URL, 1000, "*/*"); err == nil {
		t.Fatal("loopback server was reachable")
	}
	for _, u := range []string{"http://10.0.0.1/", "http://192.168.1.1/", "http://169.254.169.254/latest", "http://[::1]/", "http://127.0.0.1:1/"} {
		if _, _, err := Get(t.Context(), New(false), u, 1000, "*/*"); err == nil {
			t.Errorf("%s was reachable", u)
		}
	}
	for _, u := range []string{"file:///etc/passwd", "ftp://x.example/", "http://user:pw@x.example/", "javascript:alert(1)"} {
		if _, _, err := Get(t.Context(), New(false), u, 1000, "*/*"); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
}

func TestGetLimitsAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(strings.Repeat("x", 50)))
	}))
	defer srv.Close()
	c := New(true)
	if b, _, err := Get(t.Context(), c, srv.URL, 100, "*/*"); err != nil || len(b) != 50 {
		t.Fatalf("%d %v", len(b), err)
	}
	if _, _, err := Get(t.Context(), c, srv.URL, 10, "*/*"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("over limit: %v", err)
	}
	if _, _, err := Get(t.Context(), c, srv.URL+"/missing", 100, "*/*"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404: %v", err)
	}
}
