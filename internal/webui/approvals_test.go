package webui

import (
	"context"
	"encoding/json"
	"errors"
	"gopi/internal/tasks"
	"strings"
	"testing"
)

type approvalBackend struct {
	fakeBackend
	decided  bool
	decision tasks.ApprovalDecision
}

func (b *approvalBackend) Approvals(context.Context, string) ([]tasks.Approval, error) {
	return []tasks.Approval{{ID: "approval-one", Revision: 3, Digest: strings.Repeat("a", 64), Status: "pending", Action: tasks.ApprovalAction{Kind: "coding.run", Target: "/workspace", Payload: json.RawMessage(`{"prompt":"exact prompt"}`)}}}, nil
}
func (b *approvalBackend) DecideApproval(_ context.Context, id string, d tasks.ApprovalDecision) (tasks.Approval, error) {
	if b.decided || id != "one" || d.ID != "approval-one" || d.Revision != 3 || d.Digest != strings.Repeat("a", 64) {
		return tasks.Approval{}, errors.New("stale")
	}
	b.decided = true
	b.decision = d
	return tasks.Approval{ID: d.ID, Status: "approved", Revision: 4}, nil
}
func TestDurableApprovalRoutes(t *testing.T) {
	b := &approvalBackend{}
	h, err := New(Config{Origin: "http://127.0.0.1:8080", Token: token, Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/tasks/one/approvals"
	origin := "http://127.0.0.1:8080"
	if w := request(h, "GET", path, "", false, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := request(h, "GET", path, "", true, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "exact prompt") {
		t.Fatal(w.Code, w.Body.String())
	}
	body := `{"id":"approval-one","revision":3,"digest":"` + strings.Repeat("a", 64) + `","allow":true}`
	for _, trial := range []struct {
		payload, origin string
		want            int
	}{
		{body, "https://evil.example", 403},
		{`{"id":"approval-one","revision":3,"allow":true}`, origin, 400},
		{`{"id":"approval-one","revision":3,"digest":"` + strings.Repeat("a", 64) + `"}`, origin, 400},
		{`{"id":"approval-one","revision":3,"digest":"` + strings.Repeat("x", 64) + `","allow":true}`, origin, 400},
		{strings.Replace(body, `"revision":3`, `"revision":2`, 1), origin, 409},
	} {
		if w := request(h, "POST", path, trial.payload, true, trial.origin); w.Code != trial.want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if b.decided {
		t.Fatal("invalid request decided approval")
	}
	if w := request(h, "POST", path, body, true, origin); w.Code != 200 || !b.decision.Allow {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(h, "POST", path, body, true, origin); w.Code != 409 {
		t.Fatal("replay accepted", w.Code)
	}
}
