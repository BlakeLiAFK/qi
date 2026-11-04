package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"gopi/internal/agent"
	"gopi/internal/session"
	"strings"
)

// Submit binds the client key to the original semantic request, not mutable
// skill file contents or prior conversation. Retry uses the accepted snapshot.
// principal must come from the authenticated server, never a request body.
func (e *Engine) Submit(ctx context.Context, id, principal, key, prompt string, names []string) (Run, error) {
	if names == nil {
		names = []string{}
	}
	payload, err := json.Marshal(struct {
		Version int
		Prompt  string
		Skills  []string
	}{1, prompt, names})
	if err != nil {
		return Run{}, err
	}
	digest := sha256.Sum256(payload)
	identity := session.AdmissionIdentity{Principal: principal, Key: key, Digest: hex.EncodeToString(digest[:])}
	if err = identity.Validate(); err != nil {
		return Run{}, err
	}
	// Resolve retries before rereading a skill that may have changed or disappeared.
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return Run{}, ErrClosed
	}
	prior, found, err := e.store.LookupAdmission(ctx, id, identity)
	e.mu.Unlock()
	if err != nil || found {
		return prior, err
	}
	return e.startWithSkills(ctx, id, prompt, names, &identity)
}

// CreateIdempotent binds the client key to the normalized title. Callers supply
// an authenticated, stable principal, not a token or user-supplied account ID.
func (e *Engine) CreateIdempotent(ctx context.Context, principal, key, title string) (Task, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "New task"
	}
	if len(title) > 160 {
		return Task{}, errors.New("title exceeds 160 bytes")
	}
	payload, _ := json.Marshal(struct {
		Version int
		Title   string
	}{1, title})
	digest := sha256.Sum256(payload)
	identity := session.AdmissionIdentity{Principal: principal, Key: key, Digest: hex.EncodeToString(digest[:])}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return Task{}, ErrClosed
	}
	s, err := e.store.CreateIdempotent(ctx, title, identity, e.cfg.MaxTasks)
	if err != nil {
		return Task{}, err
	}
	return Task{ID: s.ID, Title: s.Title, Status: "idle", CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Messages: []agent.Message{}, Runs: []Run{}, Cards: []Card{}}, nil
}
