package plugins

import (
	"context"
	"os"
	"testing"
)

func TestScopedRejectsEnvironmentBeforeStart(t *testing.T) {
	if _, err := StartScoped(context.Background(), Config{Command: os.Args[0], Env: []string{"OPENAI_API_KEY=secret"}}); err == nil {
		t.Fatal("scoped server accepted credentials")
	}
}
