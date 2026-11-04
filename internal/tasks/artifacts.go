package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"time"

	"gopi/internal/agent"
	"gopi/internal/artifacts"
)

type artifactPublishTool struct {
	engine        *Engine
	taskID, runID string
}

const artifactDestination = "Authenticated deliverables in this task only; no external sharing or execution"

type artifactPublishAction struct {
	Path        string             `json:"path"`
	File        artifacts.Artifact `json:"file"`
	Destination string             `json:"destination"`
}

func (t artifactPublishTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "artifact_publish", Description: "Request user approval to register one existing operator-staged file as a downloadable deliverable in this task. Supply only a task-relative path. Does not create files, execute them, import worker output, or share externally. Success means registered, not downloaded or externally delivered.", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)}
}
func (t artifactPublishTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var req struct {
		Path string `json:"path"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return "", errors.New("invalid artifact request")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return "", errors.New("invalid artifact request")
	}
	file, err := t.engine.cfg.Artifacts.Inspect(ctx, t.taskID, req.Path)
	if err != nil {
		return "", errors.New("staged file unavailable, unsafe, busy, or exceeds 32 MiB")
	}
	action := artifactPublishAction{Path: req.Path, File: file, Destination: artifactDestination}
	payload, err := json.Marshal(action)
	if err != nil {
		return "", err
	}
	pending, err := t.engine.RequestApproval(ctx, t.taskID, t.runID, ApprovalAction{Kind: "artifact.publish", Target: file.Name, Payload: payload}, time.Now().Add(5*time.Minute))
	if err != nil {
		return "", err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		decision, err := t.engine.store.GetApproval(ctx, t.taskID, pending.ID)
		if err != nil {
			return "", err
		}
		if !decision.ExpiresAt.After(time.Now()) {
			return "", errors.New("artifact approval expired; not registered")
		}
		switch decision.Status {
		case "pending":
		case "approved":
			claimed, err := t.engine.store.ClaimApproval(ctx, t.taskID, pending.ID, decision.Revision, pending.Action)
			if err != nil {
				return "", fmt.Errorf("%w: artifact approval claim failed", agent.ErrOutcomeUnknown)
			}
			return t.publish(ctx, claimed)
		default:
			return "", fmt.Errorf("artifact approval %s; not registered", decision.Status)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}
func (t artifactPublishTool) publish(ctx context.Context, claimed Approval) (out string, resultErr error) {
	outcome := "unknown"
	defer func() {
		if _, err := t.engine.store.FinishApproval(context.Background(), t.taskID, claimed.ID, claimed.Revision, outcome); err != nil {
			resultErr = fmt.Errorf("%w: cannot persist artifact outcome", agent.ErrOutcomeUnknown)
		}
	}()
	// Only the durable canonical action returned by ClaimApproval controls
	// dispatch. Never retain the pre-approval request as execution authority.
	var action artifactPublishAction
	decoder := json.NewDecoder(bytes.NewReader(claimed.Action.Payload))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&action)
	if decodeErr != nil || decoder.Decode(new(any)) != io.EOF ||
		claimed.Action.Kind != "artifact.publish" || claimed.TaskID != t.taskID ||
		claimed.RunID != t.runID || claimed.Status != "executing" ||
		action.File.TaskID != t.taskID || action.File.Name != path.Base(action.Path) ||
		claimed.Action.Target != action.File.Name || action.Destination != artifactDestination {
		outcome = "failed"
		return "", errors.New("invalid claimed artifact action; not registered")
	}
	// Path is intentionally omitted from public Artifact JSON. The separately
	// approved relative path is the sole path authority for verification.
	action.File.Path = action.Path
	if err := ctx.Err(); err != nil {
		outcome = "failed"
		return "", err
	}
	if !claimed.ExpiresAt.After(time.Now()) {
		outcome = "failed"
		return "", errors.New("artifact approval expired; not registered")
	}
	a, err := t.engine.cfg.Artifacts.RegisterVerified(ctx, t.taskID, action.Path, action.File)
	if err != nil {
		if errors.Is(err, artifacts.ErrUnavailable) || errors.Is(err, artifacts.ErrQuota) || errors.Is(err, artifacts.ErrBusy) {
			outcome = "failed"
			return "", err
		}
		return "", fmt.Errorf("%w: artifact registration could not be confirmed; inspect task deliverables before retrying", agent.ErrOutcomeUnknown)
	}
	outcome = "succeeded"
	result, _ := json.Marshal(struct {
		Status string             `json:"status"`
		File   artifacts.Artifact `json:"artifact"`
		Note   string             `json:"note"`
	}{"registered", a, "Available for authenticated download in this task. Not executed, downloaded, or shared externally."})
	return string(result), nil
}
