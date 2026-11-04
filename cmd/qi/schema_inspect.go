package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"

	"gopi/internal/session"
)

func runSchemaInspect(ctx context.Context, args []string, out, diagnostics io.Writer) error {
	fs := flag.NewFlagSet("qi schema-inspect", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	source := fs.String("source", "", "existing private SQLite file; diagnostic only, no migrations or compatibility approval")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *source == "" || fs.NArg() != 0 {
		return errors.New("source is required; positional arguments are not accepted")
	}
	report, err := session.InspectSchema(ctx, *source)
	if err != nil && !errors.Is(err, session.ErrSchemaUnhealthy) {
		return err
	}
	if encodeErr := json.NewEncoder(out).Encode(report); encodeErr != nil {
		return encodeErr
	}
	return err
}
