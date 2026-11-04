package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopi/internal/agent"
	"gopi/internal/workers"
)

// CodingConfig explicitly enables approval-gated coding subprocesses. Workspace
// is fixed by the operator, never selected by the model. A working directory is
// not an OS security boundary: use an isolated account/container for untrusted
// repositories. CLI authentication and the CLI's own safety policies still apply.
type CodingConfig struct {
	Credentials   map[string]string
	Workspace     string
	Timeout       time.Duration
	ApprovalTTL   time.Duration
	CodexPath     string
	ClaudePath    string
	workspaceInfo os.FileInfo
	workspaceGate chan struct{}
}

// Gates serialize approved jobs for a canonical workspace across task engines
// in this process. They are coordination, not cross-process security isolation.
var codingWorkspaces = struct {
	sync.Mutex
	gates map[string]chan struct{}
}{gates: make(map[string]chan struct{})}

func codingWorkspaceGate(path string) chan struct{} {
	codingWorkspaces.Lock()
	defer codingWorkspaces.Unlock()
	gate := codingWorkspaces.gates[path]
	if gate == nil {
		gate = make(chan struct{}, 1)
		codingWorkspaces.gates[path] = gate
	}
	return gate
}

func prepareCoding(c *CodingConfig) (*CodingConfig, error) {
	if c == nil {
		return nil, nil
	}
	copy := *c
	if err := workers.ValidateCredentials(c.Credentials); err != nil {
		return nil, err
	}
	copy.Credentials = make(map[string]string, len(c.Credentials))
	for key, value := range c.Credentials {
		copy.Credentials[key] = value
	}
	if strings.TrimSpace(copy.Workspace) == "" {
		return nil, errors.New("coding workspace is required")
	}
	absolute, err := filepath.Abs(copy.Workspace)
	if err != nil {
		return nil, err
	}
	copy.Workspace, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("coding workspace: %w", err)
	}
	copy.workspaceInfo, err = os.Stat(copy.Workspace)
	if err != nil || !copy.workspaceInfo.IsDir() {
		return nil, errors.New("coding workspace must be an existing directory")
	}
	if copy.Timeout == 0 {
		copy.Timeout = 15 * time.Minute
	}
	if copy.ApprovalTTL == 0 {
		copy.ApprovalTTL = 5 * time.Minute
	}
	if copy.Timeout < 0 || copy.Timeout > time.Hour || copy.ApprovalTTL < 0 || copy.ApprovalTTL > time.Hour {
		return nil, errors.New("coding timeout and approval TTL must be positive and at most one hour")
	}
	// Resolve executables before presenting approvals, rather than relying on a
	// mutable PATH between the decision and dispatch. Missing CLIs remain disabled.
	for _, entry := range []struct {
		name string
		path *string
	}{{"codex", &copy.CodexPath}, {"claude", &copy.ClaudePath}} {
		candidate := *entry.path
		if candidate == "" {
			candidate = entry.name
		}
		resolved, lookupErr := exec.LookPath(candidate)
		if lookupErr != nil {
			if *entry.path != "" {
				return nil, lookupErr
			}
			continue
		}
		resolved, err = filepath.Abs(resolved)
		if err != nil {
			return nil, err
		}
		*entry.path = resolved
	}
	copy.workspaceGate = codingWorkspaceGate(copy.Workspace)
	return &copy, nil
}

type codingTool struct {
	engine        *Engine
	taskID, runID string
	config        *CodingConfig
}
type codingAction struct {
	Arguments      []string `json:"arguments"`
	Agent          string   `json:"agent"`
	Prompt         string   `json:"prompt"`
	Workspace      string   `json:"workspace"`
	Executable     string   `json:"executable"`
	TimeoutSeconds float64  `json:"timeout_seconds"`
	Access         string   `json:"access"`
}

func (t codingTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "coding_run", Description: "Request explicit user approval to run a coding CLI in the operator-configured workspace. Waits for approval and completion. No execution occurs before approval. Cancellation or failure may leave partial file changes; never claim rollback.", Parameters: json.RawMessage(`{"type":"object","properties":{"agent":{"type":"string","enum":["codex","claude"]},"prompt":{"type":"string"}},"required":["agent","prompt"],"additionalProperties":false}`)}
}
func (t codingTool) workspaceValid() bool {
	current, err := os.Stat(t.config.Workspace)
	if err != nil || !os.SameFile(current, t.config.workspaceInfo) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(t.config.Workspace)
	return err == nil && resolved == t.config.Workspace
}
func (t codingTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var request struct {
		Agent  string `json:"agent"`
		Prompt string `json:"prompt"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return "", err
	}
	if strings.TrimSpace(request.Prompt) == "" || len(request.Prompt) > 64*1024 {
		return "", errors.New("coding prompt must contain 1 to 65536 bytes")
	}
	executable := ""
	switch request.Agent {
	case "codex":
		executable = t.config.CodexPath
	case "claude":
		executable = t.config.ClaudePath
	default:
		return "", errors.New("unsupported coding agent")
	}
	if executable == "" {
		return "", errors.New("requested coding CLI is not installed or configured")
	}
	if !t.workspaceValid() {
		return "", errors.New("coding workspace changed; restart with a verified workspace")
	}
	args, err := workers.Arguments(request.Agent, request.Prompt)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(codingAction{args, request.Agent, request.Prompt, t.config.Workspace, executable, t.config.Timeout.Seconds(), "May modify files and access network. Uses a temporary home, minimal runtime environment, and only explicitly configured worker credentials. Working directory and environment filtering are not a sandbox; same-user code can still read other host files. Codex disables interactive escalation prompts while using workspace-write; Claude uses default permissions. Review the exact arguments."})
	if err != nil {
		return "", err
	}
	pending, err := t.engine.RequestApproval(ctx, t.taskID, t.runID, ApprovalAction{Kind: "coding.run", Target: executable, Payload: payload}, time.Now().Add(t.config.ApprovalTTL))
	if err != nil {
		return "", err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		decision, getErr := t.engine.store.GetApproval(ctx, t.taskID, pending.ID)
		if getErr != nil {
			return "", getErr
		}
		if !decision.ExpiresAt.After(time.Now()) {
			return "", errors.New("coding approval expired; nothing was dispatched")
		}
		switch decision.Status {
		case "pending":
		case "approved":
			// Do not hold a workspace slot while waiting for the user. An
			// approved request waits here, still subject to expiry/cancellation.
			expiry := time.NewTimer(time.Until(decision.ExpiresAt))
			select {
			case t.config.workspaceGate <- struct{}{}:
				expiry.Stop()
				defer func() { <-t.config.workspaceGate }()
			case <-ctx.Done():
				expiry.Stop()
				return "", ctx.Err()
			case <-expiry.C:
				return "", errors.New("coding approval expired while waiting for workspace")
			}
			if !t.workspaceValid() {
				return "", errors.New("coding workspace changed before dispatch")
			}
			claimed, claimErr := t.engine.store.ClaimApproval(ctx, t.taskID, pending.ID, decision.Revision, pending.Action)
			if claimErr != nil {
				return "", fmt.Errorf("%w: approval claim failed: %v", agent.ErrOutcomeUnknown, claimErr)
			}
			return t.executeClaim(ctx, claimed)
		default:
			return "", fmt.Errorf("coding approval %s; nothing was dispatched", decision.Status)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}
func (t codingTool) executeClaim(ctx context.Context, claimed Approval) (output string, resultErr error) {
	outcome := "unknown"
	defer func() {
		if _, err := t.engine.store.FinishApproval(context.Background(), t.taskID, claimed.ID, claimed.Revision, outcome); err != nil {
			resultErr = fmt.Errorf("%w: cannot persist coding outcome: %v", agent.ErrOutcomeUnknown, err)
		}
	}()
	var action codingAction
	if err := json.Unmarshal(claimed.Action.Payload, &action); err != nil {
		outcome = "failed"
		return "", err
	}
	if err := ctx.Err(); err != nil {
		outcome = "failed"
		return "", err
	}
	if !t.workspaceValid() {
		outcome = "failed"
		return "", errors.New("coding workspace changed before dispatch")
	}
	if !claimed.ExpiresAt.After(time.Now()) {
		outcome = "failed"
		return "", errors.New("coding approval expired before dispatch")
	}
	manager := workers.New(workers.Config{Enabled: true, Credentials: t.config.Credentials, CodexPath: action.Executable, ClaudePath: action.Executable, Timeout: time.Duration(action.TimeoutSeconds * float64(time.Second)), MaxTasks: 1, MaxOutputBytes: 64 * 1024})
	defer manager.Close()
	worker, err := manager.Start(ctx, workers.Request{Agent: action.Agent, Prompt: action.Prompt, WorkDir: action.Workspace})
	if err != nil {
		outcome = "failed"
		return "", err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := manager.Get(worker.ID)
		if err != nil {
			return "", fmt.Errorf("%w: %v", agent.ErrOutcomeUnknown, err)
		}
		if current.Status != "running" {
			encoded, _ := json.Marshal(current)
			if current.Status == "completed" {
				outcome = "succeeded"
				return string(encoded), nil
			}
			// The CLI may have modified files before exit or cancellation. A failed
			// process is not a rolled-back operation and must never be blindly replayed.
			return string(encoded), fmt.Errorf("%w: coding process %s; inspect workspace before retrying", agent.ErrOutcomeUnknown, current.Status)
		}
		<-ticker.C // Manager observes ctx and terminates its process group.
	}
}
