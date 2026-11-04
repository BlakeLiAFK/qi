//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package workers

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNormalExitTerminatesBackgroundDescendants(t *testing.T) {
	for _, redirect := range []string{"", " </dev/null >/dev/null 2>&1"} {
		t.Run(redirect, func(t *testing.T) {
			wd := t.TempDir()
			executable := fake(t, "(sleep 0.3; touch escaped)"+redirect+" &\nexit 0\n")
			manager := New(Config{Enabled: true, CodexPath: executable})
			task, err := manager.Start(context.Background(), Request{"codex", "x", wd})
			if err != nil {
				t.Fatal(err)
			}
			task = await(t, manager, task.ID)
			manager.Close()
			if task.Status != "completed" {
				t.Fatalf("status %s", task.Status)
			}
			time.Sleep(400 * time.Millisecond)
			if _, err := os.Stat(filepath.Join(wd, "escaped")); !os.IsNotExist(err) {
				t.Fatal("background descendant survived normal parent exit")
			}

		})
	}
}
