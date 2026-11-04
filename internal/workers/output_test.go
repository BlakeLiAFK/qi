package workers

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBoundedBufferKeepsPrefixAndLatestTail(t *testing.T) {
	for _, chunkSize := range []int{1, 7, 1000} {
		input := "START:" + strings.Repeat("middle", 100) + ":FINAL ERROR"
		b := &boundedBuffer{limit: 64}
		for pos := 0; pos < len(input); {
			end := pos + chunkSize
			if end > len(input) {
				end = len(input)
			}
			n, err := b.Write([]byte(input[pos:end]))
			if err != nil || n != end-pos {
				t.Fatalf("write %d: %d %v", chunkSize, n, err)
			}
			pos = end
		}
		output, truncated := b.snapshot()
		if !truncated || len(output) > 64 || !strings.HasPrefix(output, "START:") || !strings.HasSuffix(output, ":FINAL ERROR") || !strings.Contains(output, "[output truncated]") {
			t.Fatalf("chunk %d: %q truncated=%v", chunkSize, output, truncated)
		}
		if len(b.data) > 64 || cap(b.data) > 128 {
			t.Fatalf("buffer storage grew unbounded: %d/%d", len(b.data), cap(b.data))
		}
	}
}

func TestBoundedBufferExactAndTinyLimits(t *testing.T) {
	for _, limit := range []int{0, 1, 2, 5, 64} {
		b := &boundedBuffer{limit: limit}
		exact := strings.Repeat("x", limit)
		_, _ = b.Write([]byte(exact))
		output, truncated := b.snapshot()
		if truncated || output != exact {
			t.Fatalf("exact limit %d: %q %v", limit, output, truncated)
		}
		_, _ = b.Write([]byte("ending"))
		output, truncated = b.snapshot()
		if !truncated || len(output) > limit {
			t.Fatalf("overflow limit %d: %q %v", limit, output, truncated)
		}
	}
}

func TestBoundedBufferUTF8IsValidAndBounded(t *testing.T) {
	for _, limit := range []int{1, 5, 30, 64} {
		b := &boundedBuffer{limit: limit}
		_, _ = b.Write([]byte(strings.Repeat("中文🙂", 100) + "done"))
		output, truncated := b.snapshot()
		if !truncated || !utf8.ValidString(output) || len(output) > limit {
			t.Fatalf("UTF-8 limit %d: %q", limit, output)
		}
	}
}

func TestFailedWorkerPreservesFinalErrorAfterLargeOutput(t *testing.T) {
	executable := fake(t, "printf 'BEGIN\\n'\ni=0; while [ $i -lt 100 ]; do printf 'intermediate output line\\n'; i=$((i+1)); done\nprintf 'FINAL FAILURE: fixture\\n' >&2\nexit 7\n")
	manager := New(Config{Enabled: true, CodexPath: executable, MaxOutputBytes: 128})
	defer manager.Close()
	task, err := manager.Start(context.Background(), Request{"codex", "x", t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	task = await(t, manager, task.ID)
	if task.Status != "failed" || task.Error == "" || !task.Truncated || len(task.Output) > 128 || !strings.HasPrefix(task.Output, "BEGIN\n") || !strings.HasSuffix(task.Output, "FINAL FAILURE: fixture\n") {
		t.Fatalf("terminal failure lost: %+v", task)
	}
}
