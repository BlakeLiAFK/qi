package webui

import (
	"context"
	"errors"
	"gopi/internal/skills"
	"strings"
	"testing"
)

type skillBackend struct {
	fakeBackend
	selected []string
}

func (b *skillBackend) AvailableSkills() []skills.Metadata {
	return []skills.Metadata{{Name: "review", Description: "Review changes", Source: "explicit", Path: "/trusted/review/SKILL.md"}}
}
func (b *skillBackend) StartWithSkills(_ context.Context, id, prompt string, names []string) error {
	if len(names) != 1 || names[0] != "review" {
		return errors.New("unknown skill")
	}
	b.calls++
	b.selected = append([]string(nil), names...)
	return nil
}
func TestExplicitSkillRoutes(t *testing.T) {
	b := &skillBackend{}
	h, err := New(Config{Origin: "http://127.0.0.1:8080", Token: token, Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	if w := request(h, "GET", "/api/skills", "", false, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request(h, "GET", "/api/skills", "", true, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Review changes") {
		t.Fatal(w.Code, w.Body.String())
	}
	if b.calls != 0 {
		t.Fatal("catalog executed skill")
	}
	path := "/api/tasks/one/messages"
	origin := "http://127.0.0.1:8080"
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"prompt":"test","skills":["review","review"]}`, 400},
		{`{"prompt":"test","skills":[""]}`, 400},
		{`{"prompt":"test","skills":["unknown"]}`, 409},
		{`{"prompt":"test","skills":"review"}`, 400},
	} {
		if w := request(h, "POST", path, tc.body, true, origin); w.Code != tc.want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if b.calls != 0 {
		t.Fatal("invalid selection executed")
	}
	if w := request(h, "POST", path, `{"prompt":"test"}`, true, origin); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if len(b.selected) != 0 || b.calls != 1 {
		t.Fatal("implicit selection")
	}
	if w := request(h, "POST", path, `{"prompt":"test","skills":["review"]}`, true, origin); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if b.calls != 2 || len(b.selected) != 1 || b.selected[0] != "review" {
		t.Fatal("selection missing")
	}
}
