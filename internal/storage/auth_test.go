package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInviteIsSingleUseAndExpires(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	now := time.Unix(1000, 0)
	admin, _ := db.CreateUser(ctx, "a@example.com", "A", "h", true)
	if err := db.CreateInvite(ctx, "h1", "new@example.com", "New", false, admin, now, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateInvite(ctx, "h0", "A@example.com", "", false, admin, now, time.Hour); !errors.Is(err, ErrEmailUsed) {
		t.Errorf("existing email: %v", err)
	}
	// a second invite for the same email replaces the first
	if err := db.CreateInvite(ctx, "h2", "new@example.com", "New", false, admin, now, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Token(ctx, TokenInvite, "h1", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("old invite still valid: %v", err)
	}
	if _, err := db.AcceptInvite(ctx, "h2", "", "pw", now.Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired invite accepted: %v", err)
	}
	id, err := db.AcceptInvite(ctx, "h2", "Newbie", "pw", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := db.UserByID(ctx, id); u.Email != "new@example.com" || u.DisplayName != "Newbie" || u.IsAdmin {
		t.Errorf("user %+v", u)
	}
	if _, err := db.AcceptInvite(ctx, "h2", "", "pw", now.Add(time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Errorf("invite used twice: %v", err)
	}
}

func TestResetChangesPasswordOnceAndEndsSessions(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	now := time.Unix(1000, 0)
	uid, _ := db.CreateUser(ctx, "u@example.com", "U", "old", false)
	if err := db.CreateSession(ctx, "s1", uid, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_ = db.CreateReset(ctx, "r1", uid, uid, now, time.Hour)
	_ = db.CreateReset(ctx, "r2", uid, uid, now, time.Hour) // replaces r1
	if _, err := db.AcceptReset(ctx, "r1", "x", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("replaced link worked: %v", err)
	}
	if _, err := db.AcceptReset(ctx, "r2", "new", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if u, _ := db.UserByID(ctx, uid); u.PasswordHash != "new" {
		t.Error("password not changed")
	}
	if _, err := db.SessionUser(ctx, "s1", now); !errors.Is(err, ErrNotFound) {
		t.Error("session survived reset")
	}
	if _, err := db.AcceptReset(ctx, "r2", "again", now.Add(time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Errorf("reset used twice: %v", err)
	}
	// an invite hash is not a reset
	_ = db.CreateInvite(ctx, "i1", "z@example.com", "Z", false, uid, now, time.Hour)
	if _, err := db.AcceptReset(ctx, "i1", "x", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("invite used as reset: %v", err)
	}
}

func TestTOTPAndRecoveryCodes(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	uid, _ := db.CreateUser(ctx, "u@example.com", "U", "h", false)
	_ = db.SetTOTPSecret(ctx, uid, "sealed")
	if u, _ := db.UserByID(ctx, uid); u.TOTPEnabled {
		t.Error("enabled before confirming")
	}
	_ = db.EnableTOTP(ctx, uid, []string{"a", "b"})
	if u, _ := db.UserByID(ctx, uid); !u.TOTPEnabled {
		t.Error("not enabled")
	}
	if ok, _ := db.AcceptTOTPStep(ctx, uid, 100); !ok {
		t.Error("first use refused")
	}
	if ok, _ := db.AcceptTOTPStep(ctx, uid, 100); ok {
		t.Error("code reused")
	}
	if ok, _ := db.AcceptTOTPStep(ctx, uid, 99); ok {
		t.Error("older step accepted")
	}
	if ok, _ := db.UseRecoveryCode(ctx, uid, "a"); !ok {
		t.Error("recovery code refused")
	}
	if ok, _ := db.UseRecoveryCode(ctx, uid, "a"); ok {
		t.Error("recovery code reused")
	}
	if n, _ := db.RecoveryCodeCount(ctx, uid); n != 1 {
		t.Errorf("%d codes left", n)
	}
	_ = db.DisableTOTP(ctx, uid)
	if s, on, _ := db.TOTPSecret(ctx, uid); s != "" || on {
		t.Error("not disabled")
	}
	if n, _ := db.RecoveryCodeCount(ctx, uid); n != 0 {
		t.Error("codes kept")
	}
}

func TestPasskeysAndIdentities(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	a, _ := db.CreateUser(ctx, "a@example.com", "A", "h", false)
	b, _ := db.CreateUser(ctx, "b@example.com", "B", "h", false)
	now := time.Unix(5, 0)
	if err := db.AddPasskey(ctx, a, []byte{1, 2}, "{}", "phone", now); err != nil {
		t.Fatal(err)
	}
	if err := db.AddPasskey(ctx, b, []byte{1, 2}, "{}", "dup", now); err == nil {
		t.Error("duplicate credential id accepted")
	}
	p, err := db.PasskeyByCredential(ctx, []byte{1, 2})
	if err != nil || p.UserID != a {
		t.Fatalf("%+v %v", p, err)
	}
	if err := db.DeletePasskey(ctx, b, p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted another user's passkey: %v", err)
	}
	if n, _ := db.DeleteAllPasskeys(ctx, a); n != 1 {
		t.Errorf("deleted %d", n)
	}
	if err := db.LinkIdentity(ctx, "https://id", "sub1", b); err != nil {
		t.Fatal(err)
	}
	if u, err := db.IdentityUser(ctx, "https://id", "sub1"); err != nil || u.ID != b {
		t.Errorf("%+v %v", u, err)
	}
	if _, err := db.IdentityUser(ctx, "https://other", "sub1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("other issuer matched: %v", err)
	}
}
