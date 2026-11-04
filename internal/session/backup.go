package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"modernc.org/sqlite"
)

// BackupDatabase makes a consistent, verified SQLite snapshot, including committed
// WAL data. It never opens the source through Store.Open (which migrates schema).
// Destination must not exist and its directory must already be private. The
// same-user/administrator and filesystem limitations of ValidatePrivateStorage apply.
func BackupDatabase(ctx context.Context, source, destination string) error {
	return backupDatabase(ctx, source, destination, nil)
}

// afterSnapshot is an internal test seam, invoked after the read snapshot is pinned.
func backupDatabase(ctx context.Context, source, destination string, afterSnapshot func()) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if source == "" || destination == "" {
		return errors.New("source and destination paths are required")
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if err = ValidatePrivateStorage(destination); err != nil {
		return err
	}
	if _, err = os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		return errors.New("backup destination already exists")
	}
	// Reject orphan sidecars rather than attach stale journal state to a new file.
	for _, suffix := range []string{"-wal", "-shm", "-journal", ".engine.lock"} {
		if _, err = os.Lstat(destination + suffix); !errors.Is(err, os.ErrNotExist) {
			return errors.New("backup destination has existing sidecars")
		}
	}
	db, err := openBackupSource(ctx, source)
	if err != nil {
		return err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// A transaction and actual read pin one snapshot across all backup steps.
	if _, err = conn.ExecContext(ctx, "BEGIN"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	var schemaVersion int
	if err = conn.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&schemaVersion); err != nil {
		return err
	}
	if afterSnapshot != nil {
		afterSnapshot()
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".qi-backup-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(stage); err != nil {
			result = errors.Join(result, fmt.Errorf("backup staging cleanup failed: %w", err))
		}
	}()
	temp := filepath.Join(stage, "snapshot.db")
	f, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	err = conn.Raw(func(driver any) (result error) {
		b, ok := driver.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("SQLite online backup unsupported")
		}
		backup, err := b.NewBackup((&url.URL{Scheme: "file", Path: temp}).String())
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, backup.Finish()) }()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			more, err := backup.Step(128)
			if err != nil {
				return err
			}
			if !more {
				return ctx.Err()
			}
		}
	})
	if err != nil {
		return fmt.Errorf("SQLite backup: %w", err)
	}
	if _, err = inspectBackupCopy(ctx, temp); err != nil {
		return err
	}
	f, err = os.OpenFile(temp, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// link is atomic and unlike Rename never replaces an existing target. Remove
	// the staging link before returning so the final file has exactly one link.
	if err = os.Link(temp, destination); err != nil {
		return err
	}
	if err = os.Remove(temp); err != nil {
		return fmt.Errorf("backup published but staging cleanup failed: %w", err)
	}
	if err = syncBackupDirectory(stage); err != nil {
		return fmt.Errorf("backup published but staging directory sync failed: %w", err)
	}
	dir, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return fmt.Errorf("backup published but directory sync failed: %w", err)
	}
	err = errors.Join(dir.Sync(), dir.Close())
	if err != nil {
		return fmt.Errorf("backup published but directory sync failed: %w", err)
	}
	return nil
}

func openBackupSource(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("source path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = ValidatePrivateStorage(absolute); err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if err = validatePrivateFile(info); err != nil {
		return nil, err
	}
	// mode=ro prevents missing database creation. Do not use immutable=1: it
	// ignores a live WAL. No schema migration, checkpoint, or recovery is requested.
	u := &url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// BackupReport reports record counts, never stored payloads or secrets.
type BackupReport struct {
	Tables map[string]int64 `json:"tables"`
}

// VerifyBackup restores a snapshot to a disposable database under an existing
// private work directory, validates it without migrations, then removes it.
// It does not overwrite or switch a running daemon's database.
func VerifyBackup(ctx context.Context, source, workDir string) (report BackupReport, result error) {
	if err := ctx.Err(); err != nil {
		return BackupReport{}, err
	}
	if source == "" || workDir == "" {
		return BackupReport{}, errors.New("source and private work directory are required")
	}
	if err := validateStorageDirectory(workDir, true); err != nil {
		return BackupReport{}, err
	}
	temp, err := os.MkdirTemp(workDir, ".qi-restore-check-")
	if err != nil {
		return BackupReport{}, err
	}
	defer func() {
		if err := os.RemoveAll(temp); err != nil {
			result = errors.Join(result, fmt.Errorf("disposable verification cleanup failed: %w", err))
		}
	}()
	restored := filepath.Join(temp, "restored.db")
	if err = BackupDatabase(ctx, source, restored); err != nil {
		return BackupReport{}, err
	}
	return inspectBackupCopy(ctx, restored)
}

// inspectBackupCopy is only for a freshly constructed private staging or
// disposable restore copy, never a caller's source or original backup. SQLite
// omits parsed CHECK expressions on mode=ro connections. Open this owned copy
// with mode=rw (no creation), but prohibit logical writes with query_only.
func inspectBackupCopy(ctx context.Context, path string) (BackupReport, error) {
	db, err := openBackupCopyForCheck(ctx, path)
	if err != nil {
		return BackupReport{}, err
	}
	defer db.Close()
	var integrity string
	if err = db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return BackupReport{}, err
	}
	if integrity != "ok" {
		return BackupReport{}, errors.New("backup integrity check failed")
	}
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return BackupReport{}, err
	}
	bad := rows.Next()
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return BackupReport{}, err
	}
	if bad {
		return BackupReport{}, errors.New("backup foreign key check failed")
	}
	report := BackupReport{Tables: map[string]int64{}}
	// Static names only. Optional tables vary with schema age and enabled features.
	// Require the core conversation tables, but never migrate older snapshots.
	for _, table := range []string{"sessions", "messages", "runs", "events", "approvals", "curated_memory", "curated_memory_versions", "turn_memory", "admission_keys", "task_creation_keys", "task_results", "task_result_reads", "artifacts"} {
		var exists int
		if err = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&exists); err != nil {
			return BackupReport{}, err
		}
		if exists == 0 && table != "sessions" && table != "messages" {
			continue
		}
		var count int64
		if err = db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			return BackupReport{}, fmt.Errorf("backup table %s: %w", table, err)
		}
		report.Tables[table] = count
	}
	return report, nil
}

func syncBackupDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func openBackupCopyForCheck(ctx context.Context, path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = ValidatePrivateStorage(absolute); err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if err = validatePrivateFile(info); err != nil {
		return nil, err
	}
	u := &url.URL{Scheme: "file", Path: absolute}
	query := url.Values{"mode": []string{"rw"}, "_pragma": []string{"query_only(1)", "trusted_schema(0)"}}
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
