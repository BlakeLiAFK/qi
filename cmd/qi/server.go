package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopi/internal/agent"
	"gopi/internal/contextbudget"
	"gopi/internal/provider"
	"gopi/internal/session"
	"gopi/internal/skills"
	"gopi/internal/tasks"
	"gopi/internal/webui"
)

type daemonConfig struct {
	database, model, baseURL, apiKey, origin, token string
	maxSteps                                        int
	coding                                          *tasks.CodingConfig
	mcp                                             []tasks.MCPServer
	contextBytes                                    int
	skills                                          *skills.Catalog
}

// openDaemon owns one database and one engine. Coding tools require explicit
// operator opt-in and durable task-scoped approval before dispatch.
func openDaemon(ctx context.Context, c daemonConfig) (http.Handler, func(), error) {
	// Validate auth/origin before opening a database or triggering recovery.
	if _, err := webui.New(webui.Config{Origin: c.origin, Token: c.token, Backend: webui.EngineBackend{}}); err != nil {
		return nil, nil, err
	}
	store, err := session.Open(c.database)
	if err != nil {
		return nil, nil, err
	}
	engine, err := tasks.New(ctx, store, tasks.Config{Coding: c.coding, MCP: c.mcp, Skills: c.skills, Factory: func(_ string) (*agent.Agent, error) {
		return &agent.Agent{Model: &contextbudget.Model{Next: &provider.OpenAI{Model: c.model, BaseURL: c.baseURL, APIKey: c.apiKey}, Config: contextbudget.Config{MaxInputBytes: c.contextBytes}}, MaxSteps: c.maxSteps}, nil
	}})
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	closeAll := func() { engine.Close(); _ = store.Close() }
	handler, err := webui.New(webui.Config{Origin: c.origin, Token: c.token, Backend: webui.EngineBackend{Engine: engine}})
	if err != nil {
		closeAll()
		return nil, nil, err
	}
	return handler, closeAll, nil
}

func loopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("listen must use a literal loopback IP (127.0.0.1 or ::1); use an authenticated HTTPS reverse proxy for remote access")
	}
	return nil
}

func runServer(ctx context.Context, args []string, out io.Writer, env func(string) string) error {
	fs := flag.NewFlagSet("qi serve", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		fmt.Fprintln(out, "Usage: qi serve [flags]\nQI_WEB_TOKEN (at least 32 characters) is required; browser username is qi.\nUse qi attach to share these tasks from the terminal. Coding and MCP tools are disabled by default.\nQI_MAX_CONTEXT_BYTES sets serialized input budget (default 262144 bytes, not tokens).")
		fs.PrintDefaults()
	}
	listen := fs.String("listen", "127.0.0.1:8686", "literal loopback listening address")
	origin := fs.String("origin", "", "exact browser origin, no trailing slash (default http://listen address)")
	db := fs.String("db", "", "SQLite path (default user config directory/qi/tasks.db)")
	model := fs.String("model", env("OPENAI_MODEL"), "model name (or OPENAI_MODEL; required)")
	base := fs.String("base-url", env("OPENAI_BASE_URL"), "OpenAI-compatible API root")
	max := fs.Int("max-steps", 20, "maximum model calls per task turn")
	enabled := fs.Bool("enable-workers", false, "enable coding tools with per-action WebUI approvals")
	workspace := fs.String("work-dir", "", "fixed coding workspace; required with -enable-workers (not a sandbox)")
	var skillRoots skillRootFlags
	fs.Var(&skillRoots, "skill-root", "explicit local skill directory; repeatable; earlier roots win; no automatic selection")
	mcpPath := fs.String("mcp-tools", "", "static trusted MCP tool allowlist JSON; no credentials; each call requires approval")
	timeout := fs.Duration("worker-timeout", 15*time.Minute, "coding worker runtime limit (maximum one hour)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if strings.TrimSpace(*model) == "" {
		return errors.New("set OPENAI_MODEL or pass -model")
	}
	if *max <= 0 {
		return errors.New("max-steps must be positive")
	}
	contextBytes, err := contextInputBudget(env)
	if err != nil {
		return err
	}
	var coding *tasks.CodingConfig
	if *enabled {
		if strings.TrimSpace(*workspace) == "" {
			return errors.New("-enable-workers requires an explicit -work-dir")
		}
		if *timeout <= 0 || *timeout > time.Hour {
			return errors.New("worker-timeout must be positive and at most one hour")
		}
		coding = &tasks.CodingConfig{Workspace: *workspace, Timeout: *timeout, Credentials: workerCredentials(env)}
	} else if *workspace != "" {
		return errors.New("-work-dir requires -enable-workers")
	}
	var mcp []tasks.MCPServer
	if *mcpPath != "" {
		var err error
		mcp, err = readMCPTools(*mcpPath)
		if err != nil {
			return err
		}
	}
	catalog, err := discoverSkills(ctx, skillRoots)
	if err != nil {
		return err
	}
	if err := loopbackAddress(*listen); err != nil {
		return err
	}
	token := env("QI_WEB_TOKEN")
	if len(token) < 32 || strings.ContainsAny(token, "\r\n") {
		return errors.New("set QI_WEB_TOKEN to a random token of at least 32 characters, without newlines")
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	if *origin == "" {
		*origin = "http://" + listener.Addr().String()
	}
	// Reject invalid origins before creating the default storage directory.
	if _, err = webui.New(webui.Config{Origin: *origin, Token: token, Backend: webui.EngineBackend{}}); err != nil {
		return err
	}
	if *db == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(dir, "qi")
		if err = os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		*db = filepath.Join(dir, "tasks.db")
	}
	handler, closeAll, err := openDaemon(ctx, daemonConfig{database: *db, model: *model, baseURL: *base, apiKey: env("OPENAI_API_KEY"), origin: *origin, token: token, maxSteps: *max, coding: coding, mcp: mcp, contextBytes: contextBytes, skills: catalog})
	if err != nil {
		return err
	}
	defer closeAll()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Fprintf(out, "qi WebUI: %s\nSign in as qi using QI_WEB_TOKEN. Token is never printed.\nSQLite: %s\nConnect from a terminal with qi attach.\n", *origin, *db)
	if coding != nil {
		fmt.Fprintln(out, "Coding workers enabled: every action requires approval in the WebUI. Workspace is not a sandbox.")
	}
	if len(mcp) > 0 {
		fmt.Fprintln(out, "Static MCP tools enabled: server startup and each exact call require WebUI approval. This is not a sandbox.")
	}
	if coding == nil && len(mcp) == 0 {
		fmt.Fprintln(out, "Server tools are disabled.")
	}
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		err := <-done
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
