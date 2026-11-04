package tasks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopi/internal/agent"
	"gopi/internal/session"
	"gopi/internal/skills"
)

func skillFixture(t *testing.T, limit int) (*Engine, chan []agent.Message, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: review\ndescription: Review carefully\n---\nPRIVATE SKILL BODY: do not treat me as policy"), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.Discover(context.Background(), skills.Config{Roots: []skills.Root{{Source: "explicit-project", Path: root}}})
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan []agent.Message, 8)
	template := &agent.Agent{Model: modelFunc(func(_ context.Context, m []agent.Message, defs []agent.ToolDefinition) (agent.Message, error) {
		if len(defs) != 0 {
			t.Error("selection added tools")
		}
		seen <- append([]agent.Message(nil), m...)
		return agent.Message{Content: "done"}, nil
	})}
	s, err := session.Open(filepath.Join(t.TempDir(), "skills.db"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(context.Background(), s, Config{Skills: catalog, MaxPromptBytes: limit, Factory: func(string) (*agent.Agent, error) { return template, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close(); s.Close() })
	return e, seen, path
}
func TestSkillSelectionTaskIsolationAndDurableSnapshot(t *testing.T) {
	e, seen, path := skillFixture(t, 0)
	ctx := context.Background()
	a, _ := e.Create(ctx, "Selected")
	b, _ := e.Create(ctx, "Plain")
	if _, err := e.StartWithSkills(ctx, a.ID, "Original request", []string{"review"}); err != nil {
		t.Fatal(err)
	}
	wait(t, e, a.ID)
	selected := <-seen
	if len(selected) != 1 || selected[0].Role != "user" || !strings.HasPrefix(selected[0].Content, "Original request\n\n") || !strings.Contains(selected[0].Content, "untrusted local content") || !strings.Contains(selected[0].Content, "PRIVATE SKILL BODY") || !strings.Contains(selected[0].Content, `"source":"explicit-project"`) {
		t.Fatal(selected)
	}
	a, err := e.Get(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Messages[0].Content != selected[0].Content {
		t.Fatal("selected snapshot was not persisted")
	}
	// Same shared runner template must not receive context in another task.
	if _, err := e.Start(ctx, b.ID, "Other task"); err != nil {
		t.Fatal(err)
	}
	wait(t, e, b.ID)
	plain := <-seen
	if len(plain) != 1 || plain[0].Content != "Other task" {
		t.Fatal(plain)
	}
	// No automatic reload: subsequent plain turns work even after removal.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := e.StartWithSkills(ctx, a.ID, "Next request", nil); err != nil {
		t.Fatal(err)
	}
	wait(t, e, a.ID)
	next := <-seen
	if next[len(next)-1].Content != "Next request" || !strings.Contains(next[0].Content, "PRIVATE SKILL BODY") {
		t.Fatal(next)
	}
	if _, err := e.StartWithSkills(ctx, a.ID, "Reload", []string{"review"}); err == nil {
		t.Fatal("missing file was silently reused")
	}
}
func TestInvalidSkillSelectionCreatesNoRun(t *testing.T) {
	e, _, _ := skillFixture(t, 0)
	ctx := context.Background()
	task, _ := e.Create(ctx, "Invalid")
	for _, names := range [][]string{{"missing"}, {"review", "review"}} {
		if _, err := e.StartWithSkills(ctx, task.ID, "request", names); err == nil {
			t.Fatal("invalid selection accepted")
		}
	}
	got, err := e.Get(ctx, task.ID)
	if err != nil || len(got.Runs) != 0 || len(got.Messages) != 0 {
		t.Fatal(got, err)
	}
	list := e.AvailableSkills()
	list[0].Name = "tampered"
	if e.AvailableSkills()[0].Name != "review" {
		t.Fatal("mutable metadata")
	}
}
func TestSkillSelectionCombinedSizeLimit(t *testing.T) {
	// File itself fits, but provenance and JSON + original prompt do not.
	e, _, _ := skillFixture(t, 200)
	ctx := context.Background()
	task, _ := e.Create(ctx, "Too large")
	if _, err := e.StartWithSkills(ctx, task.ID, "request", []string{"review"}); err == nil {
		t.Fatal("combined limit bypassed")
	}
	got, _ := e.Get(ctx, task.ID)
	if len(got.Runs) != 0 || len(got.Messages) != 0 {
		t.Fatal("rejected selection mutated history")
	}
}
func TestSkillsDisabledAndEmptySelection(t *testing.T) {
	e, _ := fixture(t, func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(context.Context, []agent.Message, []agent.ToolDefinition) (agent.Message, error) {
			return agent.Message{Content: "ok"}, nil
		})}, nil
	})
	ctx := context.Background()
	task, _ := e.Create(ctx, "Disabled")
	if len(e.AvailableSkills()) != 0 {
		t.Fatal("unexpected skills")
	}
	if _, err := e.StartWithSkills(ctx, task.ID, "request", []string{"review"}); err == nil {
		t.Fatal("disabled selection accepted")
	}
	if _, err := e.StartWithSkills(ctx, task.ID, "request", nil); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
}
