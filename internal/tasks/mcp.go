package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"gopi/internal/agent"
	"gopi/internal/plugins"
)

// MCPServer grants a static, operator-selected capability set. No discovery or
// process starts before approval. Command/Args must contain no credentials:
// they are shown in full in durable approvals. Credentials are unsupported.
// These are trusted local executables, NOT sandboxed or safe for hostile code.
type MCPServer struct {
	Name          string         `json:"name"`
	Command       string         `json:"command"`
	Args          []string       `json:"args,omitempty"`
	Dir           string         `json:"dir"`
	Tools         []plugins.Tool `json:"tools"`
	Timeout       time.Duration  `json:"-"`
	ApprovalTTL   time.Duration  `json:"-"`
	workspaceInfo os.FileInfo
	workspaceGate chan struct{}
}

var mcpName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,23}$`)

func prepareMCP(servers []MCPServer) ([]MCPServer, error) {
	if len(servers) != 0 && !plugins.ScopedSupported() {
		return nil, errors.New("scoped MCP requires process-group cleanup on this platform")
	}
	if len(servers) > 16 {
		return nil, errors.New("at most 16 MCP servers may be configured")
	}
	out := make([]MCPServer, 0, len(servers))
	names := map[string]bool{}
	exposedNames := map[string]bool{}
	count := 0
	for _, s := range servers {
		if !mcpName.MatchString(s.Name) || names[s.Name] {
			return nil, errors.New("invalid or duplicate MCP server name")
		}
		names[s.Name] = true
		if s.Command == "" || s.Dir == "" || len(s.Tools) == 0 {
			return nil, errors.New("MCP requires a command, explicit directory, and static tools")
		}
		resolved, err := exec.LookPath(s.Command)
		if err != nil {
			return nil, fmt.Errorf("MCP executable: %w", err)
		}
		s.Command, err = filepath.Abs(resolved)
		if err != nil {
			return nil, err
		}
		absolute, err := filepath.Abs(s.Dir)
		if err != nil {
			return nil, err
		}
		s.Dir, err = filepath.EvalSymlinks(absolute)
		if err != nil {
			return nil, err
		}
		s.workspaceInfo, err = os.Stat(s.Dir)
		if err != nil || !s.workspaceInfo.IsDir() {
			return nil, errors.New("MCP directory must exist")
		}
		if s.Timeout == 0 {
			s.Timeout = 30 * time.Second
		}
		if s.ApprovalTTL == 0 {
			s.ApprovalTTL = 5 * time.Minute
		}
		if s.Timeout < 0 || s.Timeout > time.Hour || s.ApprovalTTL < 0 || s.ApprovalTTL > time.Hour {
			return nil, errors.New("MCP timeout and approval TTL must be positive and at most one hour")
		}
		s.workspaceGate = codingWorkspaceGate(s.Dir)
		s.Args = append([]string(nil), s.Args...)
		s.Tools = append([]plugins.Tool(nil), s.Tools...)
		toolNames := map[string]bool{}
		for i, tool := range s.Tools {
			if !mcpName.MatchString(tool.Name) || toolNames[tool.Name] || !agent.IsJSONObject(tool.InputSchema) {
				return nil, errors.New("invalid or duplicate static MCP tool")
			}
			toolNames[tool.Name] = true
			exposed := "mcp_" + s.Name + "__" + tool.Name
			if exposedNames[exposed] {
				return nil, errors.New("colliding exposed MCP tool names")
			}
			exposedNames[exposed] = true
			s.Tools[i].InputSchema = append(json.RawMessage(nil), tool.InputSchema...)
			count++
		}
		if count > agent.MaxTools {
			return nil, errors.New("MCP tool count limit exceeded")
		}
		out = append(out, s)
	}
	return out, nil
}

type mcpTool struct {
	engine        *Engine
	taskID, runID string
	server        MCPServer
	tool          plugins.Tool
}
type mcpAction struct {
	Server         string          `json:"server"`
	Command        string          `json:"command"`
	Args           []string        `json:"args"`
	Directory      string          `json:"directory"`
	Tool           string          `json:"tool"`
	Arguments      json.RawMessage `json:"arguments"`
	TimeoutSeconds float64         `json:"timeout_seconds"`
	Access         string          `json:"access"`
}

func (t mcpTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "mcp_" + t.server.Name + "__" + t.tool.Name, Description: "Requires explicit approval for server startup and this exact tool call. " + t.tool.Description, Parameters: t.tool.InputSchema}
}
func (t mcpTool) directoryValid() bool {
	info, err := os.Stat(t.server.Dir)
	if err != nil || !os.SameFile(info, t.server.workspaceInfo) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(t.server.Dir)
	return err == nil && resolved == t.server.Dir
}
func (t mcpTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if !agent.IsJSONObject(args) {
		return "", errors.New("MCP arguments must be a bounded JSON object")
	}
	if !t.directoryValid() {
		return "", errors.New("MCP directory changed")
	}
	payload, err := json.Marshal(mcpAction{t.server.Name, t.server.Command, t.server.Args, t.server.Dir, t.tool.Name, args, t.server.Timeout.Seconds(), "Starts a trusted local server, then makes exactly this tool call. Startup itself may have side effects. No ambient credentials; disposable home; fresh process for each call. NOT an OS sandbox: same-user filesystem and network access remain possible. Output goes to this task and its model. Cancellation does not roll back changes."})
	if err != nil {
		return "", err
	}
	pending, err := t.engine.RequestApproval(ctx, t.taskID, t.runID, ApprovalAction{Kind: "mcp.call", Target: t.server.Name + "/" + t.tool.Name, Payload: payload}, time.Now().Add(t.server.ApprovalTTL))
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
			return "", errors.New("MCP approval expired; nothing dispatched")
		}
		switch decision.Status {
		case "pending":
		case "approved":
			// Share the same canonical workspace gate as coding jobs. Approval waits
			// never monopolize the workspace, and queued claims still expire.
			expiry := time.NewTimer(time.Until(decision.ExpiresAt))
			select {
			case t.server.workspaceGate <- struct{}{}:
				expiry.Stop()
				defer func() { <-t.server.workspaceGate }()
			case <-ctx.Done():
				expiry.Stop()
				return "", ctx.Err()
			case <-expiry.C:
				return "", errors.New("MCP approval expired while waiting for workspace")
			}
			if !t.directoryValid() {
				return "", errors.New("MCP directory changed before dispatch")
			}
			claimed, err := t.engine.store.ClaimApproval(ctx, t.taskID, pending.ID, decision.Revision, pending.Action)
			if err != nil {
				return "", fmt.Errorf("%w: MCP claim failed", agent.ErrOutcomeUnknown)
			}
			return t.executeClaim(ctx, claimed)
		default:
			return "", fmt.Errorf("MCP approval %s; nothing dispatched", decision.Status)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}
func (t mcpTool) executeClaim(ctx context.Context, claimed Approval) (output string, resultErr error) {
	outcome := "failed"
	defer func() {
		if _, err := t.engine.store.FinishApproval(context.Background(), t.taskID, claimed.ID, claimed.Revision, outcome); err != nil {
			resultErr = fmt.Errorf("%w: cannot persist MCP outcome", agent.ErrOutcomeUnknown)
		}
	}()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !claimed.ExpiresAt.After(time.Now()) || !t.directoryValid() {
		return "", errors.New("MCP approval expired or directory changed before dispatch")
	}
	var action mcpAction
	if err := json.Unmarshal(claimed.Action.Payload, &action); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, t.server.Timeout)
	defer cancel()
	outcome = "unknown" // even server startup can have external effects
	client, err := plugins.StartScoped(ctx, plugins.Config{Command: action.Command, Args: action.Args, Dir: action.Directory, Timeout: t.server.Timeout})
	if err != nil {
		return "", fmt.Errorf("%w: MCP startup failed; inspect before retrying", agent.ErrOutcomeUnknown)
	}
	defer client.Close()
	raw, err := client.Call(ctx, action.Tool, action.Arguments)
	if err != nil {
		return "", fmt.Errorf("%w: MCP call failed; inspect before retrying", agent.ErrOutcomeUnknown)
	}
	var status struct {
		IsError bool `json:"isError"`
	}
	if err = json.Unmarshal(raw, &status); err != nil || status.IsError {
		return "", fmt.Errorf("%w: MCP reported an error; inspect before retrying", agent.ErrOutcomeUnknown)
	}
	outcome = "succeeded"
	return string(raw), nil
}
