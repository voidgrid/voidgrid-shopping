package auth

import (
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Fatalf("unexpected hash format: %q", h)
	}
	if !VerifyPassword("correct horse battery", h) {
		t.Error("right password rejected")
	}
	if VerifyPassword("correct horse batterz", h) {
		t.Error("wrong password accepted")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Error("two hashes of one password are identical: salt not random")
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	for _, h := range []string{
		"",
		"plain",
		"$argon2id$v=19$m=65536,t=3,p=2$onlysalt",
		"$bcrypt$v=19$m=65536,t=3,p=2$AAAA$AAAA",
		"$argon2id$v=18$m=65536,t=3,p=2$AAAA$AAAA",
		"$argon2id$v=19$m=999999999,t=3,p=2$AAAA$AAAA", // memory far beyond ours
		"$argon2id$v=19$m=65536,t=0,p=2$AAAA$AAAA",
		"$argon2id$v=19$m=65536,t=3,p=2$!!!$AAAA",
	} {
		if VerifyPassword("anything", h) {
			t.Errorf("malformed hash %q accepted", h)
		}
	}
}

func TestPasswordRules(t *testing.T) {
	if CheckPasswordRules("short") == "" {
		t.Error("short password allowed")
	}
	if CheckPasswordRules(strings.Repeat("a", MinPasswordLength)) != "" {
		t.Error("minimum length refused")
	}
	if CheckPasswordRules(strings.Repeat("a", MaxPasswordLength+1)) == "" {
		t.Error("overlong password allowed")
	}
}

func TestTokens(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewToken()
	if a == b || len(a) < 40 {
		t.Errorf("tokens not unique or too short: %q %q", a, b)
	}
	ha := HashToken(a)
	if ha == a || len(ha) != 64 {
		t.Errorf("token hash %q is not a distinct 64-character digest", ha)
	}
	if ha2 := HashToken(a); ha != ha2 {
		t.Error("token hash is not deterministic")
	}
	if ha == HashToken(b) {
		t.Error("different tokens share a hash")
	}
}

func TestRandomPassword(t *testing.T) {
	p, err := RandomPassword(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 20 {
		t.Fatalf("length %d, want 20", len(p))
	}
	if CheckPasswordRules(p) != "" {
		t.Errorf("generated password breaks the rules: %q", p)
	}
	if strings.ContainsAny(p, "lIO01") {
		t.Errorf("ambiguous character in %q", p)
	}
	q, _ := RandomPassword(20)
	if p == q {
		t.Error("two random passwords identical")
	}
}
