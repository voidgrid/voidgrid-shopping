// Package secrets encrypts values (SMTP password, OIDC client secret, TOTP
// secrets) before they go into the database. The key comes from the
// environment or a Docker secret, never from the database.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// ErrNoKey means no encryption key was supplied.
var ErrNoKey = errors.New("secrets: no encryption key is set (VGS_SECRET_KEY or VGS_SECRET_KEY_FILE)")

// MinKeyLength is the shortest accepted key text.
const MinKeyLength = 16

const prefix = "v1:"

// Box seals and opens values. A nil Box has no key.
type Box struct{ aead cipher.AEAD }

// New derives an AES-256-GCM key from the key text.
func New(key string) (*Box, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}
	if len(key) < MinKeyLength {
		return nil, errors.New("secrets: the key must be at least 16 characters")
	}
	sum := sha256.Sum256([]byte("voidgrid-shopping secrets v1\x00" + key))
	b, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	return &Box{aead: g}, nil
}

// Available reports whether a key is set.
func (b *Box) Available() bool { return b != nil }

// Seal encrypts plain. The empty string stays empty (nothing stored).
func (b *Box) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if b == nil {
		return "", ErrNoKey
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return prefix + base64.RawStdEncoding.EncodeToString(b.aead.Seal(nonce, nonce, []byte(plain), nil)), nil
}

// Open decrypts a value made by Seal. A wrong key or damaged value is an error.
func (b *Box) Open(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	if b == nil {
		return "", ErrNoKey
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(enc, prefix))
	if err != nil || !strings.HasPrefix(enc, prefix) || len(raw) < b.aead.NonceSize() {
		return "", errors.New("secrets: stored value is damaged")
	}
	n := b.aead.NonceSize()
	out, err := b.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", errors.New("secrets: cannot decrypt (wrong key?)")
	}
	return string(out), nil
}
