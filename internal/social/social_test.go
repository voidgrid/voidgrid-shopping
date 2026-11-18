package social

import (
	"context"
	"errors"
	"testing"
)

func TestAllowed(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://www.tiktok.com/@a/video/1": true, "https://vm.tiktok.com/abc": true, "https://tiktok.com/x": true,
		"http://www.tiktok.com/x": false, "https://eviltiktok.com/x": false, "https://tiktok.com.evil.net/x": false,
		"https://user@www.tiktok.com/x": false, "https://example.com/": false, "": false,
	} {
		if got := Allowed(raw); got != want {
			t.Errorf("%q: %v want %v", raw, got, want)
		}
	}
}

func TestRead(t *testing.T) {
	var asked string
	r := &Reader{Run: func(_ context.Context, _, link string) ([]byte, error) {
		asked = link
		return []byte(`{"title":"Easy pasta","description":"Easy pasta\n1 lb pasta\n#dinner","uploader":"cook","thumbnail":"https://x.example/t.jpg","webpage_url":"https://www.tiktok.com/@cook/video/9"}`), nil
	}}
	p, err := r.Read(t.Context(), " https://vm.tiktok.com/abc ")
	if err != nil || asked != "https://vm.tiktok.com/abc" || p.Uploader != "cook" || p.URL != "https://www.tiktok.com/@cook/video/9" {
		t.Fatalf("%+v %v (asked %q)", p, err, asked)
	}
	if _, err := r.Read(t.Context(), "https://example.com/x"); !errors.Is(err, ErrHost) {
		t.Errorf("foreign host: %v", err)
	}
	empty := &Reader{Run: func(context.Context, string, string) ([]byte, error) { return []byte(`{"title":" "}`), nil }}
	if _, err := empty.Read(t.Context(), "https://vm.tiktok.com/a"); err == nil {
		t.Error("empty caption accepted")
	}
	missing := &Reader{Bin: "/nonexistent/yt-dlp"}
	if _, err := missing.Read(t.Context(), "https://vm.tiktok.com/a"); !errors.Is(err, ErrMissing) {
		t.Errorf("missing binary: %v", err)
	}
}
