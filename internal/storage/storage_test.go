package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := openTest(t)
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n < 1 {
		t.Fatalf("schema_migrations: %d rows, err %v", n, err)
	}
}

func TestFirstAdminOnlyOnce(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	id, err := db.CreateFirstAdmin(ctx, "a@example.com", "Ann", "hash")
	if err != nil {
		t.Fatal(err)
	}
	u, err := db.UserByID(ctx, id)
	if err != nil || !u.IsAdmin || u.Email != "a@example.com" {
		t.Fatalf("first admin: %+v %v", u, err)
	}
	if _, err := db.CreateFirstAdmin(ctx, "b@example.com", "Bob", "hash"); !errors.Is(err, ErrSetupDone) {
		t.Fatalf("second first-admin: err = %v, want ErrSetupDone", err)
	}
	if n, _ := db.CountUsers(ctx); n != 1 {
		t.Errorf("users = %d, want 1", n)
	}
}

func TestEmailIsCaseInsensitiveAndUnique(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if _, err := db.CreateUser(ctx, "Ann@Example.com", "Ann", "h", false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateUser(ctx, "ann@example.com", "Other", "h", false); !errors.Is(err, ErrEmailUsed) {
		t.Fatalf("duplicate email: err = %v, want ErrEmailUsed", err)
	}
	if _, err := db.UserByEmail(ctx, "ANN@EXAMPLE.COM"); err != nil {
		t.Errorf("case-insensitive lookup: %v", err)
	}
	if _, err := db.UserByEmail(ctx, "nobody@example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing user: err = %v, want ErrNotFound", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	id, _ := db.CreateUser(ctx, "a@example.com", "Ann", "h", false)
	now := time.Now()

	if err := db.CreateSession(ctx, "live", id, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, "old", id, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if u, err := db.SessionUser(ctx, "live", now); err != nil || u.ID != id {
		t.Errorf("live session: %+v %v", u, err)
	}
	if _, err := db.SessionUser(ctx, "old", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired session: err = %v, want ErrNotFound", err)
	}
	if _, err := db.SessionUser(ctx, "unknown", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown session: err = %v, want ErrNotFound", err)
	}

	if err := db.PurgeSessions(ctx, now); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&n)
	if n != 1 {
		t.Errorf("after purge %d sessions, want 1", n)
	}

	if err := db.DeleteSession(ctx, "live"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SessionUser(ctx, "live", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted session still valid: %v", err)
	}
}

func TestDisabledUserSessionIsDead(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	id, _ := db.CreateUser(ctx, "a@example.com", "Ann", "h", false)
	_ = db.CreateSession(ctx, "tok", id, time.Now().Add(time.Hour))
	if _, err := db.Exec(`UPDATE users SET disabled = 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SessionUser(ctx, "tok", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("disabled user's session: err = %v, want ErrNotFound", err)
	}
}

func TestSetPasswordEndsSessions(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	id, _ := db.CreateUser(ctx, "a@example.com", "Ann", "old", false)
	_ = db.CreateSession(ctx, "tok", id, time.Now().Add(time.Hour))
	if err := db.SetPassword(ctx, id, "new"); err != nil {
		t.Fatal(err)
	}
	u, _ := db.UserByID(ctx, id)
	if u.PasswordHash != "new" {
		t.Errorf("hash = %q, want new", u.PasswordHash)
	}
	if _, err := db.SessionUser(ctx, "tok", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("session survived a password change: %v", err)
	}
	if err := db.SetPassword(ctx, 9999, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: err = %v, want ErrNotFound", err)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if err := db.SetSettings(ctx, map[string]string{"a": "1", "b": "2"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSettings(ctx, map[string]string{"a": "3"}); err != nil {
		t.Fatal(err)
	}
	m, err := db.AllSettings(ctx)
	if err != nil || m["a"] != "3" || m["b"] != "2" || len(m) != 2 {
		t.Errorf("settings = %v, err %v", m, err)
	}
}
