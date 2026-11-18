// Package auth holds password hashing and session tokens.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Password length limits. The maximum stops a huge request body from
// turning a sign-in into a CPU attack.
const (
	MinPasswordLength = 12
	MaxPasswordLength = 256
)

// Argon2id parameters (OWASP's second recommended profile, a good fit for a
// small server): 64 MiB, 3 passes, 2 lanes.
const (
	argonMemory  = 64 * 1024
	argonTime    = 3
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// CheckPasswordRules returns a message if the password is not acceptable.
func CheckPasswordRules(pw string) string {
	switch {
	case len(pw) < MinPasswordLength:
		return fmt.Sprintf("Use at least %d characters.", MinPasswordLength)
	case len(pw) > MaxPasswordLength:
		return fmt.Sprintf("Use at most %d characters.", MaxPasswordLength)
	}
	return ""
}

// HashPassword returns an encoded argon2id hash of pw.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: salt: %w", err)
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether pw matches the encoded hash. A malformed
// hash never matches.
func VerifyPassword(pw, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var mem, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil {
		return false
	}
	// Refuse parameters far beyond ours, so a tampered row can't exhaust memory.
	if mem == 0 || mem > 4*argonMemory || t == 0 || t > 10 || p == 0 || p > 16 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, mem, p, uint32(len(want))) // #nosec G115 -- len(want) is a decoded hash, tiny
	return subtle.ConstantTimeCompare(got, want) == 1
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// SpendVerifyTime does one password check against a throwaway hash, so a
// sign-in for an email that doesn't exist takes as long as one that does.
func SpendVerifyTime(pw string) {
	dummyOnce.Do(func() { dummyHash, _ = HashPassword("not-a-real-password") })
	VerifyPassword(pw, dummyHash)
}

// ErrToken is returned when the random source fails.
var ErrToken = errors.New("auth: random token")

// NewToken returns a random URL-safe token (256 bits).
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", ErrToken
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken returns the hex SHA-256 of a token; only hashes are stored.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// RandomPassword returns a random password of n characters from an
// unambiguous alphabet, for the console reset command.
func RandomPassword(n int) (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, n)
	buf := make([]byte, n)
	// Rejection sampling: no modulo bias.
	const limit = 256 - 256%len(alphabet)
	for i := 0; i < n; {
		if _, err := rand.Read(buf); err != nil {
			return "", ErrToken
		}
		for _, b := range buf {
			if int(b) >= limit || i >= n {
				continue
			}
			out[i] = alphabet[int(b)%len(alphabet)]
			i++
		}
	}
	return string(out), nil
}
