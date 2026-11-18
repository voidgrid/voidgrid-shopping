package photos

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var png = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)

func TestSaveStoresImages(t *testing.T) {
	s := New(t.TempDir())
	name, err := s.Save(bytes.NewReader(png))
	if err != nil {
		t.Fatal(err)
	}
	if !ValidName(name) || !strings.HasSuffix(name, ".png") {
		t.Fatalf("name %q", name)
	}
	p, err := s.Path(name)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("stored file: %v %v", st, err)
	}
	s.Remove(name)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("Remove left the file")
	}
	if left, _ := filepath.Glob(filepath.Join(s.Dir, ".upload-*")); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
}

func TestSaveRefusesNonImagesAndHugeFiles(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Save(strings.NewReader("<html><script>alert(1)</script></html>")); !errors.Is(err, ErrNotImage) {
		t.Errorf("html accepted: %v", err)
	}
	big := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxBytes)...)
	if _, err := s.Save(bytes.NewReader(big)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversize accepted: %v", err)
	}
}

func TestPathRefusesTraversal(t *testing.T) {
	s := New(t.TempDir())
	for _, n := range []string{"../etc/passwd", "a.png", "", strings.Repeat("a", 32) + ".svg", strings.Repeat("A", 32) + ".png"} {
		if _, err := s.Path(n); err == nil {
			t.Errorf("Path(%q) accepted", n)
		}
	}
}

func TestAllowedHosts(t *testing.T) {
	s := New(t.TempDir(), "images.plantoeat.com")
	for raw, want := range map[string]bool{
		"https://images.plantoeat.com/a.jpg":          true,
		"https://IMAGES.plantoeat.com/a.jpg":          true,
		"http://images.plantoeat.com/a.jpg":           false,
		"https://images.plantoeat.com.evil.net/a.jpg": false,
		"https://evil.net/images.plantoeat.com":       false,
		"https://user@images.plantoeat.com/a.jpg":     false,
		"https://127.0.0.1/a.jpg":                     false,
		"file:///etc/passwd":                          false,
	} {
		if got := s.Allowed(raw); got != want {
			t.Errorf("Allowed(%q) = %v, want %v", raw, got, want)
		}
	}
	if _, err := s.Fetch(t.Context(), "https://evil.net/a.jpg"); !errors.Is(err, ErrPermanent) {
		t.Errorf("fetch from other host: %v", err)
	}
}

func TestPublicModeStillRefusesPrivateAddresses(t *testing.T) {
	s := New(t.TempDir())
	s.Public = true
	if !s.Allowed("http://cdn.example/a.jpg") || s.Allowed("ftp://cdn.example/a.jpg") || s.Allowed("https://u@cdn.example/a.jpg") {
		t.Error("public mode address check")
	}
	if _, err := s.Fetch(t.Context(), "http://127.0.0.1:9/a.jpg"); !errors.Is(err, ErrPermanent) {
		t.Errorf("loopback fetch: %v", err)
	}
}
