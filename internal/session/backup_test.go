//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package session

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopi/internal/testutil"
)

func TestBackupWALSnapshotAndRestore(t *testing.T) {
	ctx := context.Background()
	s := queueStore(t)
	if _, err := s.db.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	creation := AdmissionIdentity{Principal: "owner", Key: "creation", Digest: strings.Repeat("b", 64)}
	// Force multiple backup batches so the pinned snapshot spans page steps.
	if _, err := s.db.Exec("CREATE TABLE backup_padding(payload BLOB); INSERT INTO backup_padding VALUES(zeroblob(2097152))"); err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateIdempotent(ctx, "backup task", creation, 10)
	if err != nil {
		t.Fatal(err)
	}
	fact := MemoryFact{Content: "explicit fact", SourceRef: "user:1", ApprovalRef: "user:2", Kind: "verified", Confidence: 1, Confidentiality: "private"}
	memory, err := s.CreateMemory(ctx, MemoryScope{TaskID: task.ID}, fact)
	if err != nil {
		t.Fatal(err)
	}
	identity := AdmissionIdentity{Principal: "owner", Key: "backup-test", Digest: strings.Repeat("a", 64)}
	run, err := s.AcceptQueued(ctx, task.ID, task.Revision, queuePrompt("retained prompt"), "", nil, 4, identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ClaimNextQueued(ctx); err != nil {
		t.Fatal(err)
	}
	approval, err := s.RequestApproval(ctx, task.ID, run.ID, ApprovalAction{Kind: "test.noop", Target: "local", Payload: []byte(`{}`)}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.LoadTaskSnapshot(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	wal, err := os.Stat(s.path + "-wal")
	if err != nil || wal.Size() == 0 {
		t.Fatal("fixture has no uncheckpointed WAL", err)
	}
	destination := filepath.Join(testutil.PrivateDir(t), "snapshot.db")
	err = backupDatabase(ctx, s.path, destination, func() {
		if _, e := s.Create(ctx, "committed after snapshot"); e != nil {
			t.Fatal(e)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("backup permissions", info, err)
	}
	restored, err := Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	after, err := restored.LoadTaskSnapshot(ctx, task.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("snapshot changed", before, after, err)
	}
	count, err := restored.Count(ctx)
	if err != nil || count != 1 {
		t.Fatal("snapshot included later commit", count, err)
	}
	gotMemory, err := restored.GetMemory(ctx, MemoryScope{TaskID: task.ID}, memory.ID)
	if err != nil || !reflect.DeepEqual(memory, gotMemory) {
		t.Fatal("memory restore", gotMemory, err)
	}
	gotApproval, err := restored.GetApproval(ctx, task.ID, approval.ID)
	if err != nil || !reflect.DeepEqual(approval, gotApproval) {
		t.Fatal("approval restore", gotApproval, err)
	}
	replay, err := restored.CreateIdempotent(ctx, "backup task", creation, 10)
	if err != nil || !reflect.DeepEqual(task, replay) {
		t.Fatal("creation replay restore", replay, err)
	}
	gotRun, found, err := restored.LookupAdmission(ctx, task.ID, identity)
	if err != nil || !found || !reflect.DeepEqual(run, gotRun) {
		t.Fatal("idempotency restore", gotRun, found, err)
	}
	work := testutil.PrivateDir(t)
	report, err := VerifyBackup(ctx, destination, work)
	if err != nil || report.Tables["sessions"] != 1 || report.Tables["curated_memory"] != 1 || report.Tables["approvals"] != 1 || report.Tables["admission_keys"] != 1 {
		t.Fatal(report, err)
	}
	entries, err := os.ReadDir(work)
	if err != nil || len(entries) != 0 {
		t.Fatal("restore leaked files", entries, err)
	}
}

func TestBackupRejectsUnsafeOrMissingPaths(t *testing.T) {
	s, source := openTest(t)
	if _, err := s.Create(context.Background(), "preserved"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"missing-source", "source-symlink", "unsafe-source", "destination-exists", "destination-symlink", "destination-sidecar", "public-directory", "same-path"} {
		t.Run(kind, func(t *testing.T) {
			dir := testutil.PrivateDir(t)
			src, dst := source, filepath.Join(dir, "backup.db")
			switch kind {
			case "missing-source":
				src = filepath.Join(dir, "missing.db")
			case "source-symlink":
				src = filepath.Join(dir, "alias.db")
				if err := os.Symlink(source, src); err != nil {
					t.Fatal(err)
				}
			case "unsafe-source":
				src = filepath.Join(dir, "public.db")
				if err := os.WriteFile(src, []byte("unchanged"), 0644); err != nil {
					t.Fatal(err)
				}
			case "destination-exists":
				if err := os.WriteFile(dst, []byte("unchanged"), 0600); err != nil {
					t.Fatal(err)
				}
			case "destination-symlink":
				if err := os.Symlink(source, dst); err != nil {
					t.Fatal(err)
				}
			case "destination-sidecar":
				if err := os.WriteFile(dst+"-wal", []byte("unchanged"), 0600); err != nil {
					t.Fatal(err)
				}
			case "public-directory":
				if err := os.Chmod(dir, 0755); err != nil {
					t.Fatal(err)
				}
			case "same-path":
				dst = source
			}
			before, _ := os.ReadFile(src)
			if err := BackupDatabase(context.Background(), src, dst); err == nil {
				t.Fatal("accepted unsafe path")
			}
			after, _ := os.ReadFile(src)
			if !bytes.Equal(before, after) {
				t.Fatal("source changed")
			}
			if kind == "missing-source" {
				if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("created source", err)
				}
			}
			if kind == "destination-exists" {
				got, _ := os.ReadFile(dst)
				if string(got) != "unchanged" {
					t.Fatal("overwrote destination")
				}
			}
		})
	}
}

func TestBackupCancellationCleansStage(t *testing.T) {
	_, source := openTest(t)
	for _, afterSnapshot := range []bool{false, true} {
		dir := testutil.PrivateDir(t)
		ctx, cancel := context.WithCancel(context.Background())
		var hook func()
		if afterSnapshot {
			hook = cancel
		} else {
			cancel()
		}
		err := backupDatabase(ctx, source, filepath.Join(dir, "backup.db"), hook)
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatal("cancellation leaked output", entries, err)
		}
	}
}

func TestBackupNoClobberAtPublication(t *testing.T) {
	_, source := openTest(t)
	destination := filepath.Join(testutil.PrivateDir(t), "backup.db")
	err := backupDatabase(context.Background(), source, destination, func() {
		if err := os.WriteFile(destination, []byte("race winner"), 0600); err != nil {
			t.Fatal(err)
		}
	})
	if err == nil {
		t.Fatal("overwrote concurrent destination")
	}
	got, _ := os.ReadFile(destination)
	if string(got) != "race winner" {
		t.Fatal("concurrent output overwritten")
	}
}

func TestBackupLegacySourceNoMigration(t *testing.T) {
	dir := testutil.PrivateDir(t)
	source := filepath.Join(dir, "legacy.db")
	if err := os.WriteFile(source, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: source}).String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE sessions(id TEXT PRIMARY KEY); CREATE TABLE messages(payload TEXT); INSERT INTO sessions VALUES('legacy')"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(source, 0400); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "copy.db")
	if err = BackupDatabase(context.Background(), source, destination); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("source migrated or changed", err)
	}
	info, err := os.Stat(source)
	if err != nil || info.Mode().Perm() != 0400 {
		t.Fatal("source permissions changed", err)
	}
	report, err := VerifyBackup(context.Background(), destination, dir)
	if err != nil || len(report.Tables) != 2 || report.Tables["sessions"] != 1 {
		t.Fatal(report, err)
	}
}

func TestBackupInvalidDatabaseCleansOutput(t *testing.T) {
	for _, kind := range []string{"non-sqlite", "foreign-key"} {
		t.Run(kind, func(t *testing.T) {
			s, source := openTest(t)
			if kind == "non-sqlite" {
				s.Close()
				if err := os.WriteFile(source, []byte("not a database"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := s.db.Exec("PRAGMA foreign_keys=OFF; INSERT INTO messages(session_id,position,payload) VALUES('missing',0,'[]')"); err != nil {
					t.Fatal(err)
				}
			}
			dir := testutil.PrivateDir(t)
			if err := BackupDatabase(context.Background(), source, filepath.Join(dir, "output.db")); err == nil {
				t.Fatal("accepted invalid source")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed backup leaked files", entries, err)
			}
		})
	}
}
