//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package session

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"gopi/internal/testutil"
)

func inspectionFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(testutil.PrivateDir(t), "input.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err = db.Exec("CREATE TABLE original(id INTEGER PRIMARY KEY, value TEXT)"); err != nil {
		t.Fatal(err)
	}
	return db, path
}
func TestSchemaInspectWALMetadataAndNoPayloadOutput(t *testing.T) {
	db, path := inspectionFixture(t)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;
 PRAGMA application_id=123456; PRAGMA user_version=987;
 CREATE TABLE "SECRET_OBJECT_NAME"(id INTEGER PRIMARY KEY,value TEXT DEFAULT 'SECRET_DEFAULT');
 CREATE INDEX secret_index ON "SECRET_OBJECT_NAME"(value);
 CREATE VIEW secret_view AS SELECT 'SECRET_VIEW_LITERAL';
 CREATE TRIGGER secret_trigger AFTER INSERT ON original BEGIN INSERT INTO "SECRET_OBJECT_NAME"(value) VALUES('SECRET_TRIGGER_LITERAL'); END;
 INSERT INTO original(value) VALUES('SECRET_RECORD_VALUE')`); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint32(before[60:64]) != 0 {
		t.Fatal("user_version was checkpointed; fixture must exercise WAL metadata")
	}
	walBefore, err := os.ReadFile(path + "-wal")
	if err != nil || len(walBefore) == 0 {
		t.Fatal("missing live WAL", err)
	}
	report, err := InspectSchema(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if report.ApplicationID != 123456 || report.UserVersion != 987 || report.VersionState != "metadata_present" || report.Compatibility != "not_assessed" || report.Integrity.Status != "ok" || report.ForeignKeys.Status != "ok" {
		t.Fatal(report)
	}
	if report.Objects.Tables != 2 || report.Objects.Indexes != 1 || report.Objects.Views != 1 || report.Objects.Triggers != 1 {
		t.Fatal(report.Objects)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "SECRET") || strings.Contains(string(encoded), "secret_") {
		t.Fatal("schema or record literals leaked", string(encoded))
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("main database changed", err)
	}
	walAfter, err := os.ReadFile(path + "-wal")
	if err != nil || !bytes.Equal(walBefore, walAfter) {
		t.Fatal("WAL records changed", err)
	}
	var initialized int
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='sessions'`).Scan(&initialized); err != nil || initialized != 0 {
		t.Fatal("ran application initialization", err)
	}
}
func TestSchemaInspectUnversionedReadOnlyFile(t *testing.T) {
	db, path := inspectionFixture(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := InspectSchema(context.Background(), path)
	if err != nil || report.VersionState != "unversioned" || report.Compatibility != "not_assessed" {
		t.Fatal(report, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("source changed", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0400 {
		t.Fatal("source mode changed", err)
	}
}
func TestSchemaInspectUnhealthyChecksAreRedacted(t *testing.T) {
	db, path := inspectionFixture(t)
	if _, err := db.Exec(`CREATE TABLE "SECRET_CHECK_NAME"(value INTEGER CHECK(value>0));
 PRAGMA ignore_check_constraints=ON; INSERT INTO "SECRET_CHECK_NAME" VALUES(-1); PRAGMA ignore_check_constraints=OFF;
 CREATE TABLE parent(id INTEGER PRIMARY KEY); CREATE TABLE child(id INTEGER PRIMARY KEY,parent_id INTEGER REFERENCES parent(id)); INSERT INTO child VALUES(987654321,123456789)`); err != nil {
		t.Fatal(err)
	}

	report, err := InspectSchema(context.Background(), path)
	if !errors.Is(err, ErrSchemaUnhealthy) || report.Integrity.Status != "ok" || report.CheckConstraints != "not_validated_read_only" || report.ForeignKeys.Status != "failed" || report.ForeignKeys.Problems != 1 {
		t.Fatal(report, err)
	}
	encoded, _ := json.Marshal(report)
	for _, secret := range []string{"SECRET_CHECK_NAME", "987654321", "123456789"} {
		if strings.Contains(string(encoded)+err.Error(), secret) {
			t.Fatal("diagnostics leaked record/schema data")
		}
	}
}
func TestSchemaInspectRejectsUnsafeMissingAndCorrupt(t *testing.T) {
	for _, kind := range []string{"empty-path", "missing", "zero-bytes", "garbage", "truncated", "directory", "symlink", "hardlink", "fifo", "public-file", "public-directory", "public-sidecar"} {
		t.Run(kind, func(t *testing.T) {
			db, path := inspectionFixture(t)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			source := path
			switch kind {
			case "empty-path":
				source = ""
			case "missing":
				source = filepath.Join(filepath.Dir(path), "absent.db")
			case "zero-bytes":
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "garbage":
				if err := os.WriteFile(path, []byte("SECRET_CORRUPT_CONTENT not sqlite"), 0600); err != nil {
					t.Fatal(err)
				}
			case "truncated":
				if err := os.Truncate(path, 100); err != nil {
					t.Fatal(err)
				}
			case "directory":
				source = filepath.Dir(path)
			case "symlink":
				source = path + ".link"
				if err := os.Symlink(path, source); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				source = path + ".link"
				if err := os.Link(path, source); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				source = path + ".fifo"
				if err := unix.Mkfifo(source, 0600); err != nil {
					t.Fatal(err)
				}
			case "public-file":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "public-directory":
				if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
			case "public-sidecar":
				if err := os.WriteFile(path+"-wal", []byte("private"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path)
			_, err := InspectSchema(context.Background(), source)
			if err == nil {
				t.Fatal("accepted invalid source")
			}
			if strings.Contains(err.Error(), "SECRET_CORRUPT_CONTENT") {
				t.Fatal("raw corrupt content leaked")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("changed database")
			}
			if kind == "missing" {
				if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("created source", err)
				}
			}
		})
	}
}
func TestSchemaInspectCancellation(t *testing.T) {
	_, path := inspectionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := InspectSchema(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSchemaInspectIntegrityFailureRedactsDiagnostics(t *testing.T) {
	db, path := inspectionFixture(t)
	// Deliberately damage only this test-owned database's schema contract. Reopen
	// forces SQLite to load the new NOT NULL rule over the pre-existing NULL row.
	if _, err := db.Exec(`CREATE TABLE SECRET_NULL_NAME(value TEXT); INSERT INTO SECRET_NULL_NAME VALUES(NULL);
 PRAGMA writable_schema=ON;
 UPDATE sqlite_schema SET sql='CREATE TABLE SECRET_NULL_NAME(value TEXT NOT NULL)' WHERE name='SECRET_NULL_NAME';
 PRAGMA writable_schema=OFF`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := InspectSchema(context.Background(), path)
	if !errors.Is(err, ErrSchemaUnhealthy) || report.Integrity.Status != "failed" || report.Integrity.Problems < 1 {
		t.Fatal(report, err)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded)+err.Error(), "SECRET_NULL_NAME") {
		t.Fatal("raw integrity diagnostic leaked")
	}
}
