// Package social reads the caption of a TikTok post with yt-dlp. Only the
// caption, a title and a thumbnail address are read; no video is downloaded.
package social

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// Post is what could be read from a link.
type Post struct {
	Title       string
	Description string
	Uploader    string
	Thumbnail   string
	URL         string
}

// ErrHost means the address is not on a supported site.
var ErrHost = errors.New("only TikTok links are supported")

// ErrMissing means yt-dlp is not installed.
var ErrMissing = errors.New("yt-dlp is not installed here")

// Runner runs yt-dlp for one address and returns its JSON output.
type Runner func(ctx context.Context, bin, link string) ([]byte, error)

// Reader reads posts through yt-dlp.
type Reader struct {
	Bin string // yt-dlp program; "yt-dlp" if empty
	Run Runner // nil runs the real program
}

// Allowed reports whether raw is an https TikTok address.
func Allowed(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "tiktok.com" || strings.HasSuffix(h, ".tiktok.com")
}

// Read fetches the post's caption.
func (r *Reader) Read(ctx context.Context, link string) (Post, error) {
	link = strings.TrimSpace(link)
	if !Allowed(link) {
		return Post{}, ErrHost
	}
	bin := r.Bin
	if bin == "" {
		bin = "yt-dlp"
	}
	run := r.Run
	if run == nil {
		run = runYtDlp
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := run(ctx, bin, link)
	if err != nil {
		return Post{}, err
	}
	var j struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Uploader    string `json:"uploader"`
		Thumbnail   string `json:"thumbnail"`
		WebpageURL  string `json:"webpage_url"`
	}
	if err := json.Unmarshal(out, &j); err != nil {
		return Post{}, errors.New("yt-dlp gave an answer that could not be read")
	}
	p := Post{Title: j.Title, Description: j.Description, Uploader: j.Uploader, Thumbnail: j.Thumbnail, URL: j.WebpageURL}
	if p.URL == "" || !Allowed(p.URL) {
		p.URL = link
	}
	if strings.TrimSpace(p.Description) == "" && strings.TrimSpace(p.Title) == "" {
		return Post{}, errors.New("that post has no caption")
	}
	return p, nil
}

func runYtDlp(ctx context.Context, bin, link string) ([]byte, error) {
	// bin is the operator's configured program; link passed the TikTok host check and follows "--".
	cmd := exec.CommandContext(ctx, bin, //nolint:gosec // see above "--skip-download", "--no-playlist", "--no-warnings", "--no-progress",
		"--dump-single-json", "--", link)
	var out, errb bytes.Buffer
	cmd.Stdout = &limitedBuffer{b: &out, max: 4 << 20}
	cmd.Stderr = &limitedBuffer{b: &errb, max: 8 << 10}
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return nil, ErrMissing
		}
		if ctx.Err() != nil {
			return nil, errors.New("yt-dlp took too long")
		}
		msg := strings.TrimSpace(errb.String())
		if i := strings.LastIndex(msg, "ERROR:"); i >= 0 {
			msg = strings.TrimSpace(msg[i+len("ERROR:"):])
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("yt-dlp could not read that link: %s", msg)
	}
	return out.Bytes(), nil
}

type limitedBuffer struct {
	b   *bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.b.Len()+len(p) > l.max {
		return 0, errors.New("output too large")
	}
	return l.b.Write(p)
}
