// Command voidgrid-shopping is the self-hosted recipe, meal plan and
// shopping list server.
//
//	voidgrid-shopping [serve]                    run the server
//	voidgrid-shopping admin reset-login <email>  set a new random password for an account
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/voidgrid/voidgrid-shopping/internal/auth"
	"github.com/voidgrid/voidgrid-shopping/internal/config"
	"github.com/voidgrid/voidgrid-shopping/internal/secrets"
	"github.com/voidgrid/voidgrid-shopping/internal/storage"
	"github.com/voidgrid/voidgrid-shopping/internal/usda"
	"github.com/voidgrid/voidgrid-shopping/internal/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log, os.Args[1:]); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	switch {
	case len(args) == 0 || args[0] == "serve":
		return serve(log, cfg)
	case args[0] == "admin":
		return admin(cfg, args[1:])
	default:
		return fmt.Errorf("unknown command %q (use: serve, admin reset-login <email>)", args[0])
	}
}

func openDB(ctx context.Context, cfg config.Config) (*storage.DB, error) {
	db, err := storage.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func serve(log *slog.Logger, cfg config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	box, err := secrets.New(cfg.SecretKey)
	if err != nil {
		return err
	}
	if box == nil {
		log.Warn("no VGS_SECRET_KEY set: SMTP password, single sign-on secret and two-step codes cannot be stored")
	}
	srv, err := web.New(ctx, db, web.Options{Log: log, TrustProxy: cfg.TrustProxy, PhotoDir: cfg.PhotoDir, YtDlp: cfg.YtDlp, Secrets: box})
	if err != nil {
		return err
	}
	go srv.RunPhotoWorker(ctx)
	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go purgeSessions(ctx, log, db)
	go runBackups(ctx, log, db, cfg)

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.ListenAddr)
		errc <- httpSrv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdown)
	}
	return nil
}

func purgeSessions(ctx context.Context, log *slog.Logger, db *storage.DB) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := db.PurgeSessions(ctx, time.Now()); err != nil {
				log.Error("purge sessions", "err", err)
			}
			if err := db.PurgeOps(ctx, time.Now()); err != nil {
				log.Error("purge ops", "err", err)
			}
			if err := db.PurgeShares(ctx, time.Now()); err != nil {
				log.Error("purge shares", "err", err)
			}
			if err := db.PurgeTokens(ctx, time.Now()); err != nil {
				log.Error("purge links", "err", err)
			}
		}
	}
}

// runBackups writes a snapshot at start (if the newest is older than the
// interval) and then every interval.
func runBackups(ctx context.Context, log *slog.Logger, db *storage.DB, cfg config.Config) {
	if cfg.BackupEvery <= 0 {
		log.Info("database snapshots are off (VGS_BACKUP_HOURS=0)")
		return
	}
	snap := func() {
		p, err := db.Snapshot(ctx, cfg.BackupDir, time.Now(), cfg.BackupKeep)
		if err != nil {
			log.Error("database snapshot failed", "err", err)
			return
		}
		log.Info("database snapshot written", "file", p)
	}
	if time.Since(storage.LatestSnapshotTime(cfg.BackupDir)) >= cfg.BackupEvery {
		snap()
	}
	t := time.NewTicker(cfg.BackupEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			snap()
		}
	}
}

// admin runs the console commands, used from inside the container:
//
//	docker exec <container> voidgrid-shopping admin reset-login <email>
//	docker exec <container> voidgrid-shopping admin backup
//	docker exec <container> voidgrid-shopping admin disable-passkeys <email>
//	docker exec <container> voidgrid-shopping admin disable-totp <email>
//	docker exec <container> voidgrid-shopping admin import-usda <zip or folder>
//	docker exec <container> voidgrid-shopping admin parse-report
//	docker exec <container> voidgrid-shopping admin usda-search "<words>"
func admin(cfg config.Config, args []string) error {
	const usage = "usage: voidgrid-shopping admin backup | reset-login <email> | disable-passkeys <email> | disable-totp <email> | import-usda <zip or folder> | parse-report | usda-search <words>"
	if len(args) == 0 {
		return errors.New(usage)
	}
	ctx := context.Background()
	db, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	switch {
	case args[0] == "reset-login" && len(args) == 2:
		return resetLogin(ctx, db, args[1])
	case args[0] == "disable-passkeys" && len(args) == 2:
		return disableSecondFactor(ctx, db, args[1], true, false)
	case args[0] == "disable-totp" && len(args) == 2:
		return disableSecondFactor(ctx, db, args[1], false, true)
	case args[0] == "backup" && len(args) == 1:
		p, err := db.Snapshot(ctx, cfg.BackupDir, time.Now(), cfg.BackupKeep)
		if err != nil {
			return err
		}
		fmt.Println("Snapshot written:", p)
		return nil
	case args[0] == "import-usda" && len(args) == 2:
		return importUSDA(ctx, db, args[1])
	case args[0] == "usda-search" && len(args) == 2:
		return searchUSDA(ctx, db, args[1])
	case args[0] == "parse-report" && len(args) == 1:
		return parseReport(ctx, db)
	}
	return errors.New(usage)
}

func resetLogin(ctx context.Context, db *storage.DB, email string) error {
	u, err := db.UserByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return fmt.Errorf("no account with email %q", email)
		}
		return err
	}
	pw, err := auth.RandomPassword(20)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	if err := db.SetPassword(ctx, u.ID, hash); err != nil {
		return err
	}
	fmt.Printf("New password for %s (shown once, all their sessions ended):\n%s\n", u.Email, pw)
	return nil
}

// disableSecondFactor removes a user's passkeys and/or two-step codes so they
// can sign in with their password alone again.
func disableSecondFactor(ctx context.Context, db *storage.DB, email string, passkeys, totp bool) error {
	u, err := db.UserByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return fmt.Errorf("no account with email %q", email)
		}
		return err
	}
	if passkeys {
		n, err := db.DeleteAllPasskeys(ctx, u.ID)
		if err != nil {
			return err
		}
		fmt.Printf("Removed %d passkey(s) for %s.\n", n, u.Email)
	}
	if totp {
		if err := db.DisableTOTP(ctx, u.ID); err != nil {
			return err
		}
		fmt.Printf("Two-step codes are off for %s.\n", u.Email)
	}
	return nil
}

func importUSDA(ctx context.Context, db *storage.DB, src string) error {
	d, err := usda.Read(src)
	if err != nil {
		return err
	}
	if err := db.ReplaceUSDA(ctx, d.Foods, d.Portions, d.Nutrients); err != nil {
		return err
	}
	st, err := db.USDAStatus(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("USDA FoodData Central loaded: %d foods, %d with nutrition, %d household measures, %d with a weight per volume.\n",
		st.Foods, st.Nutrients, st.Portions, st.Densities)
	return nil
}
