//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopi/internal/session"
	"gopi/internal/testutil"
)

func TestSchemaInspectCommandOffline(t *testing.T) {
	path := filepath.Join(testutil.PrivateDir(t), "plain.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE sample(value TEXT); INSERT INTO sample VALUES('SECRET_RECORD')`); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	err = run(context.Background(), []string{"schema-inspect", "-source", path}, strings.NewReader(""), &out, &diag, func(string) string { t.Fatal("inspection read environment/model credentials"); return "" })
	if err != nil {
		t.Fatal(err, diag.String())
	}
	var report session.SchemaInspection
	if err = json.Unmarshal(out.Bytes(), &report); err != nil || report.Objects.Tables != 1 || report.Compatibility != "not_assessed" || strings.Contains(out.String(), "SECRET_RECORD") {
		t.Fatal(out.String(), err)
	}
	if _, err = db.Exec("CREATE TABLE parent(id INTEGER PRIMARY KEY); CREATE TABLE child(parent_id INTEGER REFERENCES parent(id)); INSERT INTO child VALUES(123456789)"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	err = run(context.Background(), []string{"schema-inspect", "-source", path}, strings.NewReader(""), &out, &diag, func(string) string { t.Fatal("read environment"); return "" })
	if !errors.Is(err, session.ErrSchemaUnhealthy) {
		t.Fatal(err)
	}
	if err = json.Unmarshal(out.Bytes(), &report); err != nil || report.ForeignKeys.Status != "failed" || strings.Contains(out.String(), "123456789") {
		t.Fatal(out.String(), err)
	}

}
func TestSchemaInspectCommandArguments(t *testing.T) {
	for _, args := range [][]string{{"schema-inspect"}, {"schema-inspect", "-source", ""}, {"schema-inspect", "-source", "absent", "extra"}, {"schema-inspect", "-unknown"}, {"schema-inspect", "-source", "absent"}} {
		var out, diag bytes.Buffer
		if err := run(context.Background(), args, strings.NewReader(""), &out, &diag, func(string) string { t.Fatal("read environment"); return "" }); err == nil || out.Len() != 0 {
			t.Fatal(args, err, out.String())
		}
	}
	var out, diag bytes.Buffer
	if err := run(context.Background(), []string{"schema-inspect", "-h"}, strings.NewReader(""), &out, &diag, func(string) string { return "" }); err != nil || out.Len() != 0 {
		t.Fatal(err)
	}
}
