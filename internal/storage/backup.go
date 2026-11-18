package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const backupPrefix = "voidgrid-shopping-"

// Snapshot writes a consistent copy of the database into dir and keeps the
// newest keep copies. The copy is made with VACUUM INTO (safe while the app
// is running), checked, then moved into place, so a reader of dir never sees
// a half-written file. Photos are plain files on the volume and are not copied.
func (db *DB) Snapshot(ctx context.Context, dir string, now time.Time, keep int) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("storage: backup directory: %w", err)
	}
	name := backupPrefix + now.UTC().Format("20060102-150405") + ".db"
	final := filepath.Join(dir, name)
	tmp := filepath.Join(dir, ".partial-"+name)
	_ = os.Remove(tmp)
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?1`, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("storage: snapshot: %w", err)
	}
	if err := checkDB(ctx, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if keep > 0 {
		old, _ := Snapshots(dir)
		for i := keep; i < len(old); i++ {
			_ = os.Remove(filepath.Join(dir, old[i]))
		}
	}
	return final, nil
}

func checkDB(ctx context.Context, path string) error {
	d, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	var res string
	if err := d.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&res); err != nil || res != "ok" {
		return fmt.Errorf("storage: snapshot failed its check (%s): %v", res, err)
	}
	return nil
}

// Snapshots lists the snapshot file names in dir, newest first.
func Snapshots(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasPrefix(e.Name(), backupPrefix) && strings.HasSuffix(e.Name(), ".db") {
			out = append(out, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out, nil
}

// LatestSnapshotTime is when the newest snapshot in dir was made (zero if none).
func LatestSnapshotTime(dir string) time.Time {
	names, _ := Snapshots(dir)
	if len(names) == 0 {
		return time.Time{}
	}
	t, err := time.Parse("20060102-150405", strings.TrimSuffix(strings.TrimPrefix(names[0], backupPrefix), ".db"))
	if err != nil {
		return time.Time{}
	}
	return t
}
