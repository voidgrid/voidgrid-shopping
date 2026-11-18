package storage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotIsConsistentAndPruned(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	seed(t, db)
	dir := filepath.Join(t.TempDir(), "backups")
	base := time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)
	var last string
	for i := range 4 {
		p, err := db.Snapshot(ctx, dir, base.Add(time.Duration(i)*time.Hour), 3)
		if err != nil {
			t.Fatal(err)
		}
		last = p
	}
	names, _ := Snapshots(dir)
	if len(names) != 3 || !strings.HasSuffix(last, names[0]) {
		t.Fatalf("kept %v", names)
	}
	if st, _ := os.Stat(last); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".partial") {
			t.Errorf("leftover %s", e.Name())
		}
	}
	// the copy opens on its own and holds the data
	d, err := sql.Open("sqlite", "file:"+last+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM recipes`).Scan(&n); err != nil || n != 3 {
		t.Errorf("recipes in snapshot: %d %v", n, err)
	}
	if got := LatestSnapshotTime(dir); !got.Equal(base.Add(3 * time.Hour)) {
		t.Errorf("latest %v", got)
	}
	if !LatestSnapshotTime(filepath.Join(dir, "none")).IsZero() {
		t.Error("missing dir has a latest time")
	}
}

func TestListRevisionAndOps(t *testing.T) {
	db, ctx := recipeDB(t), context.Background()
	r0, _ := db.ListRevision(ctx)
	if _, err := db.ExecContext(ctx, `INSERT INTO stores (name) VALUES ('A')`); err != nil {
		t.Fatal(err)
	}
	r1, _ := db.ListRevision(ctx)
	if r1 <= r0 {
		t.Errorf("store change did not bump revision: %d -> %d", r0, r1)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO list_items (name, key, created_at, changed_at) VALUES ('milk','milk',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE list_items SET state = 'have'`); err != nil {
		t.Fatal(err)
	}
	if r2, _ := db.ListRevision(ctx); r2 < r1+2 {
		t.Errorf("revision %d after two changes from %d", r2, r1)
	}
	now := time.Unix(100, 0)
	if ok, _ := db.ClaimOp(ctx, "op1", now); !ok {
		t.Error("first claim refused")
	}
	if ok, _ := db.ClaimOp(ctx, "op1", now); ok {
		t.Error("second claim allowed")
	}
	_ = db.ReleaseOp(ctx, "op1")
	if ok, _ := db.ClaimOp(ctx, "op1", now); !ok {
		t.Error("claim after release refused")
	}
	_ = db.PurgeOps(ctx, now.Add(15*24*time.Hour))
	if ok, _ := db.ClaimOp(ctx, "op1", now); !ok {
		t.Error("old op not purged")
	}
}
