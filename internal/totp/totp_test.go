package totp

import (
	"testing"
	"time"
)

// RFC 6238 appendix B test vectors (SHA-1, secret "12345678901234567890").
func TestRFCVectors(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	for ts, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		got, err := code(secret, Step(time.Unix(ts, 0)))
		if err != nil || got != want {
			t.Errorf("t=%d: %s %v, want %s", ts, got, err, want)
		}
	}
}

func TestVerify(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	now := time.Unix(1234567890, 0)
	step, ok := Verify(secret, "005 924", now)
	if !ok || step != Step(now) {
		t.Fatalf("%d %v", step, ok)
	}
	if _, ok := Verify(secret, "005924", now.Add(30*time.Second)); !ok {
		t.Error("one step of drift refused")
	}
	if _, ok := Verify(secret, "005924", now.Add(5*time.Minute)); ok {
		t.Error("stale code accepted")
	}
	for _, bad := range []string{"", "12345", "abcdef", "0059245"} {
		if _, ok := Verify(secret, bad, now); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if s, err := NewSecret(); err != nil || len(s) != 32 {
		t.Errorf("secret %q %v", s, err)
	}
}
