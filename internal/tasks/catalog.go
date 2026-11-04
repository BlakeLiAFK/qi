package tasks

// ToolCatalog is a read-only snapshot of capabilities registered by the daemon.
// It never discovers plugins, calls a runner factory, or starts an executable.
// Standalone CLI plugins and tools supplied by custom factories are not included.
type ToolCatalog struct {
	ReadOnly   bool               `json:"read_only"`
	MCPEnabled bool               `json:"mcp_enabled"`
	Entries    []ToolCatalogEntry `json:"entries"`
}
type ToolCatalogEntry struct {
	Name        string                  `json:"name"`
	Kind        string                  `json:"kind"`
	Source      string                  `json:"source"`
	Enabled     bool                    `json:"enabled"`
	Description string                  `json:"description"`
	Warning     string                  `json:"warning"`
	Command     string                  `json:"command,omitempty"`
	Directory   string                  `json:"directory,omitempty"`
	Tools       []ToolCatalogCapability `json:"tools"`
}
type ToolCatalogCapability struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (e *Engine) ToolCatalog() ToolCatalog {
	catalog := ToolCatalog{ReadOnly: true, MCPEnabled: len(e.cfg.MCP) > 0, Entries: []ToolCatalogEntry{
		{Name: "Question cards", Kind: "builtin", Source: "qi daemon: -question-cards", Enabled: e.cfg.Questions, Description: "Task-local questions and saved answers.", Warning: "Answers are not permission to perform external actions.", Tools: []ToolCatalogCapability{{Name: "ask_user_question", Description: "Create a question card."}, {Name: "get_question_answers", Description: "Read this task's persisted answers."}}},
		{Name: "Artifacts", Kind: "builtin", Source: "qi daemon: -artifact-dir", Enabled: e.cfg.Artifacts != nil, Description: "Register an operator-staged file for download in its task.", Warning: "Each publication requires approval. Does not create, execute, or externally share files.", Tools: []ToolCatalogCapability{{Name: "artifact_publish", Description: "Request approval to register an existing staged file."}}},
		{Name: "Coding workers", Kind: "builtin", Source: "qi daemon: -enable-workers", Enabled: e.cfg.Coding != nil, Description: "Run an operator-configured coding CLI after explicit approval.", Warning: "NOT an OS sandbox. Same-user filesystem and network access remain possible. Cancellation does not roll back changes. Forbidden in public preview.", Tools: []ToolCatalogCapability{{Name: "coding_run", Description: "Request approval for a coding run in the configured workspace."}}},
	}}
	for _, server := range e.cfg.MCP {
		entry := ToolCatalogEntry{Name: server.Name, Kind: "mcp", Source: "Operator-managed startup allowlist (-mcp-tools)", Enabled: true, Description: "Static MCP stdio registration; available does not mean running or healthy.", Warning: "NOT an OS sandbox. Server startup and each exact call require approval. Startup may have side effects; same-user filesystem and network access remain possible. No credential support. Forbidden in public preview.", Command: server.Command, Directory: server.Dir, Tools: []ToolCatalogCapability{}}
		for _, tool := range server.Tools {
			entry.Tools = append(entry.Tools, ToolCatalogCapability{Name: "mcp_" + server.Name + "__" + tool.Name, Description: tool.Description})
		}
		catalog.Entries = append(catalog.Entries, entry)
	}
	return catalog
}
