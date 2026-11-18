// Package totp implements time-based one-time passwords (RFC 6238, SHA-1,
// 6 digits, 30 seconds), as authenticator apps expect.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 default; authenticator apps expect SHA-1
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const period = 30

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a random 160-bit secret in base32.
func NewSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b32.EncodeToString(b), nil
}

func code(secret string, step int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return "", err
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step)) //nolint:gosec // step is a positive time counter
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1000000), nil
}

// Step is the counter for time t.
func Step(t time.Time) int64 { return t.Unix() / period }

// Verify checks a 6-digit code for the steps around now (one step of clock
// drift each way). It returns the matching step so the caller can refuse a
// step it has already accepted.
func Verify(secret, input string, now time.Time) (int64, bool) {
	input = strings.ReplaceAll(strings.TrimSpace(input), " ", "")
	if len(input) != 6 {
		return 0, false
	}
	cur := Step(now)
	var hit int64
	ok := false
	for _, s := range []int64{cur - 1, cur, cur + 1} {
		want, err := code(secret, s)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(input)) == 1 {
			hit, ok = s, true
		}
	}
	return hit, ok
}

// URI is the otpauth:// address an authenticator app imports.
func URI(issuer, account, secret string) string {
	v := url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + v.Encode()
}

// Code returns the current code for secret; it is what an authenticator app
// shows, and is here for tests and checks.
func Code(secret string, t time.Time) (string, error) { return code(secret, Step(t)) }
