package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"gopi/internal/tasks"
)

// readMCPTools reads only the explicit operator allowlist. Unlike standalone
// plugins, this format has no environment or credential fields and no discovery.
func readMCPTools(path string) ([]tasks.MCPServer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	const limit = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, errors.New("MCP tools configuration exceeds 1 MiB")
	}
	// Validate structure with strict decoding before resolving executable paths.
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var servers []tasks.MCPServer
	if err = d.Decode(&servers); err != nil {
		return nil, fmt.Errorf("MCP tools config: %w", err)
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return nil, errors.New("MCP tools config must contain one JSON array")
	}
	if len(servers) == 0 || len(servers) > 16 {
		return nil, errors.New("MCP tools config requires 1 to 16 servers; omit -mcp-tools to disable")
	}
	return servers, nil
}
