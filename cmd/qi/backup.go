package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"gopi/internal/session"
)

func runBackup(ctx context.Context, command string, args []string, out, diagnostics io.Writer) error {
	fs := flag.NewFlagSet("qi "+command, flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	source := fs.String("source", "", "existing private qi SQLite database (read-only; no migrations)")
	var destination, workDir *string
	if command == "backup" {
		destination = fs.String("destination", "", "new snapshot path in an existing private directory; never overwritten")
	} else {
		workDir = fs.String("work-dir", "", "existing private directory for disposable restore verification")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *source == "" {
		return errors.New("source is required; positional arguments are not accepted")
	}
	if command == "backup" {
		if destination == nil || *destination == "" {
			return errors.New("destination is required")
		}
		if err := session.BackupDatabase(ctx, *source, *destination); err != nil {
			return err
		}
		_, err := fmt.Fprintln(out, "Verified SQLite backup created. Artifact bytes and external credentials/configuration are not included.")
		return err
	}
	if workDir == nil || *workDir == "" {
		return errors.New("work-dir is required")
	}
	report, err := session.VerifyBackup(ctx, *source, *workDir)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(report)
}
