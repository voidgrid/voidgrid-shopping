// Package config reads runtime configuration from environment variables.
// Only what the process needs before it can open its database lives here;
// everything an admin changes at runtime is on the Settings tab.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the runtime configuration.
type Config struct {
	ListenAddr string
	DBPath     string
	// PhotoDir holds recipe photos; it defaults to a photos folder beside the database.
	PhotoDir string
	// TrustProxy makes the sign-in rate limiter use the last X-Forwarded-For
	// address as the client address. Set it only behind a reverse proxy that
	// overwrites that header.
	TrustProxy bool
	// YtDlp is the yt-dlp program used to read TikTok captions.
	YtDlp string
	// SecretKey encrypts stored secrets (SMTP password, OIDC client secret,
	// TOTP secrets). Empty means those features cannot store secrets.
	SecretKey string
	// BackupDir receives database snapshots; BackupEvery is the gap between
	// them (0 turns them off) and BackupKeep how many are kept.
	BackupDir   string
	BackupEvery time.Duration
	BackupKeep  int
}

// Load reads VGS_* variables, applying defaults.
func Load() (Config, error) {
	c := Config{
		ListenAddr: getEnv("VGS_LISTEN_ADDR", ":8800"),
		DBPath:     getEnv("VGS_DB_PATH", "/data/voidgrid-shopping.db"),
	}
	c.SecretKey = os.Getenv("VGS_SECRET_KEY")
	if f := os.Getenv("VGS_SECRET_KEY_FILE"); f != "" && c.SecretKey == "" {
		b, err := os.ReadFile(f) //nolint:gosec // path comes from the operator's environment
		if err != nil {
			return Config{}, fmt.Errorf("config: read VGS_SECRET_KEY_FILE: %w", err)
		}
		c.SecretKey = strings.TrimSpace(string(b))
	}
	c.BackupDir = getEnv("VGS_BACKUP_DIR", filepath.Join(filepath.Dir(c.DBPath), "backups"))
	c.BackupEvery, c.BackupKeep = 24*time.Hour, 7
	if v := os.Getenv("VGS_BACKUP_HOURS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 24*365 {
			return Config{}, fmt.Errorf("config: VGS_BACKUP_HOURS must be a number of hours from 0 (off) to 8760, got %q", v)
		}
		c.BackupEvery = time.Duration(n) * time.Hour
	}
	if v := os.Getenv("VGS_BACKUP_KEEP"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			return Config{}, fmt.Errorf("config: VGS_BACKUP_KEEP must be a number from 1 to 1000, got %q", v)
		}
		c.BackupKeep = n
	}
	c.YtDlp = getEnv("VGS_YTDLP_PATH", "yt-dlp")
	c.PhotoDir = getEnv("VGS_PHOTO_DIR", filepath.Join(filepath.Dir(c.DBPath), "photos"))
	if v := os.Getenv("VGS_TRUST_PROXY"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("config: VGS_TRUST_PROXY must be true or false, got %q", v)
		}
		c.TrustProxy = b
	}
	return c, nil
}

func getEnv(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
