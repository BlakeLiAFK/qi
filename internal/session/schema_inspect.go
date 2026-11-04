package session

import (
	"context"
	"database/sql"
	"errors"
	"os"
)

var ErrSchemaUnhealthy = errors.New("database integrity or foreign-key check failed")
var ErrSchemaInspection = errors.New("cannot inspect SQLite structure; no compatibility decision was made")

// SchemaInspection contains only numeric header metadata, fixed labels and
// aggregate counts. It deliberately excludes record values, object names, SQL,
// defaults, row identifiers and raw SQLite diagnostics.
type SchemaInspection struct {
	CheckConstraints string        `json:"check_constraints"`
	ApplicationID    int64         `json:"application_id"`
	UserVersion      int64         `json:"user_version"`
	VersionState     string        `json:"version_state"`
	Compatibility    string        `json:"compatibility"`
	Integrity        SchemaCheck   `json:"integrity"`
	ForeignKeys      SchemaCheck   `json:"foreign_keys"`
	Objects          SchemaObjects `json:"objects"`
	Warning          string        `json:"warning"`
}
type SchemaCheck struct {
	Status   string `json:"status"`
	Problems int64  `json:"problems"`
}
type SchemaObjects struct {
	Tables   int64 `json:"tables"`
	Indexes  int64 `json:"indexes"`
	Views    int64 `json:"views"`
	Triggers int64 `json:"triggers"`
}

// InspectSchema opens an existing private database read-only, without application
// initialization, migrations or recovery. The header, checks and object counts
// share a read snapshot, including committed WAL data. Read-only SQLite can still
// maintain WAL shared-memory coordination files. The standard private-storage
// ownership/permissions guarantees and same-account limitations apply.
func InspectSchema(ctx context.Context, path string) (SchemaInspection, error) {
	if err := ctx.Err(); err != nil {
		return SchemaInspection{}, err
	}
	if path == "" {
		return SchemaInspection{}, errors.New("source path is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return SchemaInspection{}, err
	}
	if err = validatePrivateFile(info); err != nil {
		return SchemaInspection{}, err
	}
	if info.Size() == 0 {
		return SchemaInspection{}, errors.New("source database is empty")
	}
	db, err := openBackupSource(ctx, path)
	if err != nil {
		// Filesystem/privacy errors may explain the path boundary. Driver diagnostics
		// can contain stored schema fragments; never forward them.
		if errors.Is(err, ErrUnsafeStorage) || errors.Is(err, ErrPrivacyUnsupported) || errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			return SchemaInspection{}, err
		}
		return SchemaInspection{}, schemaInspectionError(ctx)
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return SchemaInspection{}, schemaInspectionError(ctx)
	}
	defer conn.Close()
	// Both are connection-local restrictions, not persisted database writes.
	for _, query := range []string{"PRAGMA query_only=ON", "PRAGMA trusted_schema=OFF"} {
		if _, err = conn.ExecContext(ctx, query); err != nil {
			return SchemaInspection{}, schemaInspectionError(ctx)
		}
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SchemaInspection{}, schemaInspectionError(ctx)
	}
	defer tx.Rollback()
	report := SchemaInspection{CheckConstraints: "not_validated_read_only", Compatibility: "not_assessed", VersionState: "unversioned", Warning: "Diagnostic only. Version fields and healthy SQLite checks do not establish qi compatibility or historical provenance. No migration or downgrade protection is provided. Read-only integrity_check does not establish CHECK-constraint validity. Read-only SQLite may update WAL shared-memory sidecars."}
	if err = tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&report.ApplicationID); err != nil {
		return SchemaInspection{}, schemaInspectionError(ctx)
	}
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&report.UserVersion); err != nil {
		return SchemaInspection{}, schemaInspectionError(ctx)
	}
	if report.ApplicationID != 0 || report.UserVersion != 0 {
		report.VersionState = "metadata_present"
	}
	rows, err := tx.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return SchemaInspection{}, schemaInspectionError(ctx)
	}
	var integrityRows int
	for rows.Next() {
		var message string
		if err = rows.Scan(&message); err != nil {
			rows.Close()
			return SchemaInspection{}, schemaInspectionError(ctx)
		}
		integrityRows++
		if message != "ok" {
			report.Integrity.Problems++
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil || integrityRows == 0 {
		return SchemaInspection{}, schemaInspectionError(ctx)
	}
	report.Integrity.Status = "ok"
	if report.Integrity.Problems > 0 {
		report.Integrity.Status = "failed"
	}
	rows, err = tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return SchemaInspection{}, schemaInspectionError(ctx)
	}
	for rows.Next() {
		report.ForeignKeys.Problems++
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return SchemaInspection{}, schemaInspectionError(ctx)
	}
	report.ForeignKeys.Status = "ok"
	if report.ForeignKeys.Problems > 0 {
		report.ForeignKeys.Status = "failed"
	}
	// sqlite_schema is read directly. Views and triggers are not executed, and
	// their SQL (including possibly sensitive literals) never leaves this function.
	err = tx.QueryRowContext(ctx, `SELECT
 COALESCE(sum(type='table'),0),COALESCE(sum(type='index'),0),
 COALESCE(sum(type='view'),0),COALESCE(sum(type='trigger'),0)
 FROM sqlite_schema`).Scan(&report.Objects.Tables, &report.Objects.Indexes, &report.Objects.Views, &report.Objects.Triggers)
	if err != nil {
		return SchemaInspection{}, schemaInspectionError(ctx)
	}
	if err = tx.Commit(); err != nil {
		return SchemaInspection{}, schemaInspectionError(ctx)
	}
	if report.Integrity.Problems > 0 || report.ForeignKeys.Problems > 0 {
		return report, ErrSchemaUnhealthy
	}
	return report, nil
}

func schemaInspectionError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrSchemaInspection
}
