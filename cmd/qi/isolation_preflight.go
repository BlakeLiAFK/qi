package main

import (
	"encoding/json"
	"errors"
	"flag"
	"gopi/internal/isolation"
	"io"
)

func runIsolationPreflight(args []string, out, diagnostics io.Writer) error {
	fs := flag.NewFlagSet("qi isolation-preflight", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("isolation-preflight accepts no positional arguments")
	}
	report := isolation.Preflight()
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return err
	}
	return report.RequireReady()
}
