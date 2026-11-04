package main

import (
	"bytes"
	"context"
	"encoding/json"
	"gopi/internal/isolation"
	"strings"
	"testing"
)

func TestIsolationPreflightDispatch(t *testing.T) {
	var out, diag bytes.Buffer
	err := run(context.Background(), []string{"isolation-preflight"}, strings.NewReader(""), &out, &diag, func(string) string { t.Fatal("preflight read application environment"); return "" })
	if err == nil {
		t.Fatal("preflight must fail closed without runtime implementation")
	}
	var report isolation.PreflightReport
	if e := json.Unmarshal(out.Bytes(), &report); e != nil {
		t.Fatal(e, out.String())
	}
	if report.Ready || report.IsolationVerified || len(report.Checks) == 0 {
		t.Fatal(report)
	}
}
func TestIsolationPreflightArguments(t *testing.T) {
	for _, args := range [][]string{{"isolation-preflight", "--enable"}, {"isolation-preflight", "/host/path"}} {
		var out, diag bytes.Buffer
		if err := run(context.Background(), args, strings.NewReader(""), &out, &diag, func(string) string { return "" }); err == nil || out.Len() != 0 {
			t.Fatal("accepted unsafe arguments")
		}
	}
	var out, diag bytes.Buffer
	if err := run(context.Background(), []string{"isolation-preflight", "-h"}, strings.NewReader(""), &out, &diag, func(string) string { return "" }); err != nil || out.Len() != 0 {
		t.Fatal(err)
	}
}
