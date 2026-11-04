package tasks

import (
	"gopi/internal/plugins"
	"testing"
)

func TestToolCatalogReadOnlyAndIndependent(t *testing.T) {
	e := &Engine{cfg: Config{Questions: true, MCP: []MCPServer{{Name: "demo", Command: "/never-execute", Dir: "/workspace", Args: []string{"do-not-display"}, Tools: []plugins.Tool{{Name: "echo", Description: "Static tool"}}}}}}
	c := e.ToolCatalog()
	if !c.ReadOnly || !c.MCPEnabled || len(c.Entries) != 4 {
		t.Fatalf("catalog: %+v", c)
	}
	if !c.Entries[0].Enabled || c.Entries[1].Enabled || c.Entries[2].Enabled {
		t.Fatal("incorrect built-in status")
	}
	m := c.Entries[3]
	if m.Kind != "mcp" || !m.Enabled || m.Tools[0].Name != "mcp_demo__echo" || m.Command != "/never-execute" {
		t.Fatalf("MCP metadata: %+v", m)
	}
	c.Entries[3].Tools[0].Name = "changed"
	if e.ToolCatalog().Entries[3].Tools[0].Name != "mcp_demo__echo" {
		t.Fatal("catalog can mutate registration")
	}
	empty := (&Engine{}).ToolCatalog()
	if empty.MCPEnabled || len(empty.Entries) != 3 {
		t.Fatal("empty config reported enabled")
	}
}
