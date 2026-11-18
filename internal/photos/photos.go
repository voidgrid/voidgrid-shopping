// Package photos stores recipe photos as files and downloads them from a
// short list of allowed hosts.
package photos

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/voidgrid/voidgrid-shopping/internal/safehttp"
)

// MaxBytes is the largest photo accepted.
const MaxBytes = 10 << 20

var (
	// ErrNotImage means the bytes are not a JPEG, PNG, GIF or WebP.
	ErrNotImage = errors.New("photos: not a JPEG, PNG, GIF or WebP image")
	// ErrTooLarge means the photo is over MaxBytes.
	ErrTooLarge = errors.New("photos: image is too large")
	// ErrPermanent marks a download that will never work (the address is gone or refused).
	ErrPermanent = errors.New("photos: the remote image is not available")

	nameRE = regexp.MustCompile(`^[0-9a-f]{32}\.(jpg|png|gif|webp)$`)
)

// Store is a directory of photo files.
type Store struct {
	Dir string
	// AllowedHosts are the only hosts Fetch will contact (exact, lower case).
	AllowedHosts []string
	// Public lets Fetch take any http or https address; connections to
	// private or loopback addresses are still refused.
	Public bool
	client *http.Client
}

// New returns a Store that keeps files in dir and may download from hosts.
func New(dir string, hosts ...string) *Store {
	s := &Store{Dir: dir, AllowedHosts: hosts}
	s.client = safehttp.New(false)
	s.client.Timeout = 30 * time.Second
	s.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		if !s.allowed(req.URL) {
			return fmt.Errorf("redirect to %s refused", req.URL.Host)
		}
		return nil
	}
	return s
}

func (s *Store) allowed(u *url.URL) bool {
	if s.Public {
		return (u.Scheme == "https" || u.Scheme == "http") && u.User == nil && u.Host != ""
	}
	if u.Scheme != "https" || u.User != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, a := range s.AllowedHosts {
		if h == a {
			return true
		}
	}
	return false
}

// Allowed reports whether raw is an https address on an allowed host.
func (s *Store) Allowed(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && s.allowed(u)
}

// ValidName reports whether name is a file name this package made.
func ValidName(name string) bool { return nameRE.MatchString(name) }

// Path is the file path for a valid name.
func (s *Store) Path(name string) (string, error) {
	if !ValidName(name) {
		return "", errors.New("photos: bad file name")
	}
	return filepath.Join(s.Dir, name), nil
}

// Save checks that r holds an image of at most MaxBytes and stores it.
func (s *Store) Save(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > MaxBytes {
		return "", ErrTooLarge
	}
	ext := ""
	switch ct := http.DetectContentType(data); ct {
	case "image/jpeg":
		ext = ".jpg"
	case "image/png":
		ext = ".png"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	default:
		return "", ErrNotImage
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	name := hex.EncodeToString(b[:]) + ext
	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(s.Dir, ".upload-*")
	if err != nil {
		return "", err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return "", errors.Join(werr, cerr)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.Dir, name)); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return name, nil
}

// Remove deletes a stored photo; a missing file is not an error.
func (s *Store) Remove(name string) {
	if p, err := s.Path(name); err == nil {
		_ = os.Remove(p)
	}
}

// Fetch downloads src (an allowed https address) and stores it.
func (s *Store) Fetch(ctx context.Context, src string) (string, error) {
	u, err := url.Parse(src)
	if err != nil || !s.allowed(u) {
		return "", fmt.Errorf("%w: address not allowed", ErrPermanent)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "voidgrid-shopping")
	resp, err := s.client.Do(req)
	if errors.Is(err, safehttp.ErrBlocked) {
		return "", fmt.Errorf("%w: not a public address", ErrPermanent)
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests:
		return "", fmt.Errorf("%w: HTTP %d", ErrPermanent, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("photos: HTTP %d", resp.StatusCode)
	}
	name, err := s.Save(resp.Body)
	if errors.Is(err, ErrNotImage) || errors.Is(err, ErrTooLarge) {
		return "", fmt.Errorf("%w: %v", ErrPermanent, err)
	}
	return name, err
}
