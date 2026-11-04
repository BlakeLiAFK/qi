package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"gopi/internal/session"
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
