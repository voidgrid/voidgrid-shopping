package web

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/voidgrid/voidgrid-shopping/internal/secrets"
)

var b64 = base64.RawURLEncoding

// authenticator is a software passkey: it makes the same messages a browser
// and device would, so the server's checks run for real.
type authenticator struct {
	key    *ecdsa.PrivateKey
	credID []byte
	count  uint32
	origin string
	rpID   string
}

func newAuthenticator(t *testing.T) *authenticator {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &authenticator{key: k, credID: id, origin: "https://shop.example.com", rpID: "shop.example.com"}
}

func (a *authenticator) client(typ, challenge string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": a.origin})
	return b
}

func (a *authenticator) authData(flags byte, attested bool) []byte {
	h := sha256.Sum256([]byte(a.rpID))
	d := append([]byte{}, h[:]...)
	d = append(d, flags)
	var c [4]byte
	binary.BigEndian.PutUint32(c[:], a.count)
	d = append(d, c[:]...)
	if attested {
		d = append(d, make([]byte, 16)...) // aaguid
		d = append(d, 0, byte(16))         // credential id length (ids here are 16 bytes)
		d = append(d, a.credID...)
		raw, _ := a.key.PublicKey.Bytes() // 0x04 || X || Y
		x, y := raw[1:33], raw[33:65]
		pub, _ := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
		d = append(d, pub...)
	}
	return d
}

func (a *authenticator) registration(challenge string) []byte {
	ao, _ := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": a.authData(0x45, true)})
	body, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.credID), "rawId": b64.EncodeToString(a.credID), "type": "public-key",
		"response": map[string]any{"attestationObject": b64.EncodeToString(ao), "clientDataJSON": b64.EncodeToString(a.client("webauthn.create", challenge))},
	})
	return body
}

func (a *authenticator) assertion(challenge string, userID int64, tamper bool) []byte {
	a.count++
	ad := a.authData(0x05, false)
	cd := a.client("webauthn.get", challenge)
	h := sha256.Sum256(cd)
	sum := sha256.Sum256(append(append([]byte{}, ad...), h[:]...))
	sig, _ := ecdsa.SignASN1(rand.Reader, a.key, sum[:])
	if tamper {
		sig[len(sig)-1] ^= 0xff
	}
	body, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.credID), "rawId": b64.EncodeToString(a.credID), "type": "public-key",
		"response": map[string]any{
			"authenticatorData": b64.EncodeToString(ad), "clientDataJSON": b64.EncodeToString(cd),
			"signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString([]byte(strconv.FormatInt(userID, 10))),
		},
	})
	return body
}

func (e *env) json(path string, body []byte, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:5555"
	for _, c := range cookies {
		if c != nil {
			req.AddCookie(c)
		}
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func challengeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var o struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil || o.PublicKey.Challenge == "" {
		t.Fatalf("no challenge in %d %s", rec.Code, rec.Body.String())
	}
	return o.PublicKey.Challenge
}

func TestPasskeyRegisterAndSignIn(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	ctx := context.Background()
	e.srv.secrets, _ = secrets.New("a long enough test key 123")
	// no public address: refused with a reason
	if rec := e.json("/account/passkeys/begin", []byte("{}"), admin); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("without public address: %d", rec.Code)
	}
	e.baseURL()
	u, _ := e.db.UserByEmail(ctx, "ann@example.com")
	dev := newAuthenticator(t)

	rec := e.json("/account/passkeys/begin", []byte("{}"), admin)
	if rec.Code != 200 {
		t.Fatalf("begin: %d %s", rec.Code, rec.Body.String())
	}
	var opts struct {
		PublicKey struct {
			AuthenticatorSelection struct {
				ResidentKey      string `json:"residentKey"`
				UserVerification string `json:"userVerification"`
			} `json:"authenticatorSelection"`
		} `json:"publicKey"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &opts)
	if opts.PublicKey.AuthenticatorSelection.ResidentKey != "required" || opts.PublicKey.AuthenticatorSelection.UserVerification != "required" {
		t.Errorf("selection %+v", opts.PublicKey.AuthenticatorSelection)
	}
	// a registration from the wrong site is refused
	evil := *dev
	evil.origin = "https://evil.example.com"
	if rec := e.json("/account/passkeys/finish?name=x", evil.registration(challengeOf(t, rec)), admin); rec.Code != http.StatusBadRequest {
		t.Errorf("wrong origin accepted: %d", rec.Code)
	}
	rec = e.json("/account/passkeys/begin", []byte("{}"), admin)
	if rec := e.json("/account/passkeys/finish?name=Laptop", dev.registration(challengeOf(t, rec)), admin); rec.Code != 200 {
		t.Fatalf("finish: %d %s", rec.Code, rec.Body.String())
	}
	keys, _ := e.db.Passkeys(ctx, u.ID)
	if len(keys) != 1 || keys[0].Name != "Laptop" {
		t.Fatalf("keys %+v", keys)
	}

	signIn := func(tamper bool) *httptest.ResponseRecorder {
		b := e.json("/login/passkey/begin", []byte("{}"))
		if b.Code != 200 {
			t.Fatalf("login begin: %d", b.Code)
		}
		return e.json("/login/passkey/finish", dev.assertion(challengeOf(t, b), u.ID, tamper), cookieNamed(b, waCookie))
	}
	if rec := signIn(true); rec.Code != http.StatusUnauthorized || sessionOf(rec) != nil {
		t.Errorf("bad signature: %d", rec.Code)
	}
	rec = signIn(false)
	if rec.Code != 200 || sessionOf(rec) == nil {
		t.Fatalf("sign-in: %d %s", rec.Code, rec.Body.String())
	}
	if e.get2("/recipes", sessionOf(rec)).Code != 200 {
		t.Error("session from passkey does not work")
	}
	// the challenge works once
	b := e.json("/login/passkey/begin", []byte("{}"))
	ch, wc := challengeOf(t, b), cookieNamed(b, waCookie)
	if rec := e.json("/login/passkey/finish", dev.assertion(ch, u.ID, false), wc); rec.Code != 200 {
		t.Fatalf("second sign-in: %d", rec.Code)
	}
	if rec := e.json("/login/passkey/finish", dev.assertion(ch, u.ID, false), wc); rec.Code == 200 {
		t.Error("challenge reused")
	}
	// a counter that goes backwards means a cloned key
	dev.count = 0
	if rec := signIn(false); rec.Code == 200 {
		t.Error("cloned authenticator accepted")
	}
	// a blocked account cannot sign in with its passkey
	dev.count = 100
	_ = e.db.SetUserDisabled(ctx, u.ID, true)
	if rec := signIn(false); rec.Code == 200 {
		t.Error("blocked user signed in")
	}
	_ = e.db.SetUserDisabled(ctx, u.ID, false)
	// removing it by console
	if n, _ := e.db.DeleteAllPasskeys(ctx, u.ID); n != 1 {
		t.Errorf("removed %d", n)
	}
	dev.count = 200
	if rec := signIn(false); rec.Code == 200 {
		t.Error("removed passkey still signs in")
	}
}

func TestPasskeysOfOtherUsersAreSafe(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	e.baseURL()
	e.addUser("bob@example.com")
	bob := sessionOf(e.login("bob@example.com", testPass))
	dev := newAuthenticator(t)
	rec := e.json("/account/passkeys/begin", []byte("{}"), bob)
	if rec := e.json("/account/passkeys/finish", dev.registration(challengeOf(t, rec)), bob); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	keys, _ := e.db.Passkeys(context.Background(), 2)
	if len(keys) != 1 {
		t.Fatal("bob's key missing")
	}
	// the admin cannot delete bob's passkey through the account page
	if rec := e.post2("/account/passkeys/delete", map[string][]string{"id": {strconv.FormatInt(keys[0].ID, 10)}}, admin); rec.Code != http.StatusNotFound {
		t.Errorf("deleted someone else's passkey: %d", rec.Code)
	}
}
