package secrets

import (
	"errors"
	"strings"
	"testing"
)

func TestSealOpen(t *testing.T) {
	b, err := New("a long enough key for testing")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := b.Seal("hunter2")
	if err != nil || strings.Contains(enc, "hunter2") || !strings.HasPrefix(enc, "v1:") {
		t.Fatalf("%q %v", enc, err)
	}
	if again, _ := b.Seal("hunter2"); again == enc {
		t.Error("same ciphertext twice (nonce reused)")
	}
	if got, err := b.Open(enc); err != nil || got != "hunter2" {
		t.Errorf("open: %q %v", got, err)
	}
	other, _ := New("a different long key here")
	if _, err := other.Open(enc); err == nil {
		t.Error("wrong key opened it")
	}
	if _, err := b.Open("v1:!!!"); err == nil {
		t.Error("damaged value opened")
	}
	if s, err := b.Seal(""); s != "" || err != nil {
		t.Errorf("empty: %q %v", s, err)
	}
}

func TestNoKey(t *testing.T) {
	b, err := New("  ")
	if b != nil || err != nil || b.Available() {
		t.Fatalf("%v %v", b, err)
	}
	if _, err := b.Seal("x"); !errors.Is(err, ErrNoKey) {
		t.Errorf("seal: %v", err)
	}
	if _, err := b.Open("v1:abc"); !errors.Is(err, ErrNoKey) {
		t.Errorf("open: %v", err)
	}
	if _, err := New("short"); err == nil {
		t.Error("short key accepted")
	}
}
