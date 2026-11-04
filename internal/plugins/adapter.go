package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"gopi/internal/agent"
)

type remoteTool struct {
	client *Client
	tool   Tool
	prefix string
}

func (t remoteTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: t.prefix + t.tool.Name, Description: t.tool.Description, Parameters: t.tool.InputSchema}
}
func (t remoteTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	raw, err := t.client.Call(ctx, t.tool.Name, args)
	if err != nil {
		return "", err
	}
	var status struct {
		IsError bool `json:"isError"`
	}
	if err = json.Unmarshal(raw, &status); err != nil {
		return "", err
	}
	if status.IsError {
		return "", fmt.Errorf("MCP tool error: %s", raw)
	}
	return string(raw), nil
}

// AgentTools discovers tools and prefixes their public names to avoid collisions.
func (c *Client) AgentTools(ctx context.Context, prefix string) ([]agent.Tool, error) {
	ts, err := c.Tools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]agent.Tool, 0, len(ts))
	for _, t := range ts {
		out = append(out, remoteTool{client: c, tool: t, prefix: prefix})
	}
	return out, nil
}
