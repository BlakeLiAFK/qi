//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopi/internal/session"
	"gopi/internal/testutil"
)

func TestBackupCommandOfflineRoundTrip(t *testing.T) {
	dir := testutil.PrivateDir(t)
	source := filepath.Join(dir, "live.db")
	store, err := session.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.Create(context.Background(), "offline fixture"); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "snapshot.db")
	for _, args := range [][]string{{"backup", "-source", source, "-destination", destination}, {"backup-verify", "-source", destination, "-work-dir", dir}} {
		var out, diag bytes.Buffer
		err = run(context.Background(), args, strings.NewReader(""), &out, &diag, func(string) string { t.Fatal("backup read model credentials/environment"); return "" })
		if err != nil {
			t.Fatal(err, diag.String())
		}
		if args[0] == "backup-verify" {
			var report session.BackupReport
			if err = json.Unmarshal(out.Bytes(), &report); err != nil || report.Tables["sessions"] != 1 {
				t.Fatal(out.String(), err)
			}
		}
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
}

func TestBackupCommandArguments(t *testing.T) {
	for _, args := range [][]string{{"backup"}, {"backup", "-source", "missing"}, {"backup-verify"}, {"backup-verify", "-source", "missing"}, {"backup", "-source", "missing", "-destination", "unused", "extra"}, {"backup", "-unknown"}} {
		var out, diag bytes.Buffer
		if err := run(context.Background(), args, strings.NewReader(""), &out, &diag, func(string) string { t.Fatal("read environment"); return "" }); err == nil || out.Len() != 0 {
			t.Fatal(args, err, out.String())
		}
	}
	for _, command := range []string{"backup", "backup-verify"} {
		var out, diag bytes.Buffer
		if err := run(context.Background(), []string{command, "-h"}, strings.NewReader(""), &out, &diag, func(string) string { return "" }); err != nil || out.Len() != 0 {
			t.Fatal(err)
		}
	}
}
