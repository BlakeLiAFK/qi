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
	"gopi/internal/artifacts"
	"gopi/internal/contextbudget"
	"gopi/internal/session"
	"gopi/internal/skills"
	"gopi/internal/tasks"
	"gopi/internal/webui"
)

type daemonConfig struct {
	database, model, baseURL, apiKey, origin, token string
	maxSteps                                        int
	maxConcurrent, maxQueued                        int
	coding                                          *tasks.CodingConfig
	mcp                                             []tasks.MCPServer
	contextBytes                                    int
	skills                                          *skills.Catalog
	artifactDir                                     string
	questions                                       bool
	modelStream                                     bool
	providerName                                    string
	maxOutputTokens                                 int
	publicPreview                                   bool
}

// openDaemon owns one database and one engine. Coding tools require explicit
// operator opt-in and durable task-scoped approval before dispatch.
func openDaemon(ctx context.Context, c daemonConfig) (http.Handler, func(), error) {
	if c.publicPreview && (c.coding != nil || len(c.mcp) > 0) {
		return nil, nil, errors.New("public preview forbids coding workers and MCP tools until OS isolation is implemented")
	}
	modelConfig := modelSettings{name: c.providerName, model: c.model, base: c.baseURL, key: c.apiKey, maxTokens: c.maxOutputTokens}
	if _, err := newModelProvider(modelConfig, c.modelStream); err != nil {
		return nil, nil, err
	}
	// Validate auth/origin before opening a database or triggering recovery.
	if _, err := webui.New(webui.Config{PublicPreview: c.publicPreview, Origin: c.origin, Token: c.token, Backend: webui.EngineBackend{}}); err != nil {
		return nil, nil, err
	}
	store, err := session.Open(c.database)
	if err != nil {
		return nil, nil, err
	}
	var deliverables *artifacts.Service
	if c.artifactDir != "" {
		deliverables, err = artifacts.New(c.artifactDir, store)
		if err != nil {
			_ = store.Close()
			return nil, nil, err
		}
	}
	engine, err := tasks.New(ctx, store, tasks.Config{Artifacts: deliverables, MaxConcurrent: c.maxConcurrent, MaxQueued: c.maxQueued, Coding: c.coding, MCP: c.mcp, Skills: c.skills, Questions: c.questions, Factory: func(_ string) (*agent.Agent, error) {
		next, err := newModelProvider(modelConfig, c.modelStream)
		if err != nil {
			return nil, err
		}
		return &agent.Agent{Model: &contextbudget.Model{Next: next, Config: contextbudget.Config{MaxInputBytes: c.contextBytes}}, MaxSteps: c.maxSteps}, nil
	}})
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	closeAll := func() { engine.Close(); _ = store.Close() }
	handler, err := webui.New(webui.Config{PublicPreview: c.publicPreview, Origin: c.origin, Token: c.token, Backend: webui.EngineBackend{Engine: engine}, Artifacts: deliverables})
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
	publicPreview := fs.Bool("public-preview", false, "HTTPS browser sessions with logout; forbids workers/MCP; disables Basic auth and qi attach")
	modelStream := fs.Bool("model-stream", false, "opt in to provisional model text updates; tool arguments remain buffered")
	questions := fs.Bool("question-cards", false, "enable task-local question cards; answers grant no permission and do not resume runs")
	artifactDir := fs.String("artifact-dir", "", "explicit existing deliverable root; task-scoped files only; disabled by default")
	db := fs.String("db", "", "SQLite path (default user config directory/qi/tasks.db)")
	modelFlags := registerModelFlags(fs)
	max := fs.Int("max-steps", 20, "maximum model calls per task turn")
	maxConcurrent := fs.Int("max-concurrent", 8, "maximum concurrently executing task turns")
	maxQueued := fs.Int("max-queued", 64, "maximum accepted task turns waiting for execution")
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
	if *publicPreview && (*enabled || *mcpPath != "") {
		return errors.New("-public-preview cannot be combined with -enable-workers or -mcp-tools: OS isolation is not implemented")
	}
	modelConfig, err := modelFlags.resolve(fs, env)
	if err != nil {
		return err
	}
	if _, err = newModelProvider(modelConfig, *modelStream); err != nil {
		return err
	}
	if *maxConcurrent <= 0 || *maxQueued <= 0 {
		return errors.New("max-concurrent and max-queued must be positive")
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
	if _, err = webui.New(webui.Config{PublicPreview: *publicPreview, Origin: *origin, Token: token, Backend: webui.EngineBackend{}}); err != nil {
		return err
	}
	if *db == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(dir, "qi")
		if err = session.EnsurePrivateDirectory(dir); err != nil {
			return err
		}
		*db = filepath.Join(dir, "tasks.db")
	}
	handler, closeAll, err := openDaemon(ctx, daemonConfig{database: *db, model: modelConfig.model, baseURL: modelConfig.base, apiKey: modelConfig.key, providerName: modelConfig.name, maxOutputTokens: modelConfig.maxTokens, origin: *origin, token: token, maxSteps: *max, maxConcurrent: *maxConcurrent, maxQueued: *maxQueued, coding: coding, mcp: mcp, contextBytes: contextBytes, skills: catalog, artifactDir: *artifactDir, questions: *questions, modelStream: *modelStream, publicPreview: *publicPreview})
	if err != nil {
		return err
	}
	defer closeAll()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Fprintf(out, "qi WebUI: %s\nToken is never printed.\nSQLite: %s\n", *origin, *db)
	if *publicPreview {
		fmt.Fprintln(out, "Public preview session mode: open /login over HTTPS. Basic auth and qi attach are disabled. Restart revokes all sessions.")
	} else {
		fmt.Fprintln(out, "Sign in as qi using QI_WEB_TOKEN. Connect from a terminal with qi attach.")
	}
	if coding != nil {
		fmt.Fprintln(out, "Coding workers enabled: every action requires approval in the WebUI. Workspace is not a sandbox.")
	}
	if len(mcp) > 0 {
		fmt.Fprintln(out, "Static MCP tools enabled: server startup and each exact call require WebUI approval. This is not a sandbox.")
	}
	if *questions {
		fmt.Fprintln(out, "Question cards enabled: answers are task data, not execution approval; send a new message to continue.")
	}
	if coding == nil && len(mcp) == 0 && !*questions {
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
