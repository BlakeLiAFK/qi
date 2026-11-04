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
	"gopi/internal/provider"
	"gopi/internal/session"
	"gopi/internal/tasks"
	"gopi/internal/webui"
)

type daemonConfig struct {
	database, model, baseURL, apiKey, origin, token string
	maxSteps                                        int
}

// openDaemon owns one database and one engine. Web runs deliberately have no
// external tools until a durable, task-scoped approval workflow is implemented.
func openDaemon(ctx context.Context, c daemonConfig) (http.Handler, func(), error) {
	// Validate auth/origin before opening a database or triggering recovery.
	if _, err := webui.New(webui.Config{Origin: c.origin, Token: c.token, Backend: webui.EngineBackend{}}); err != nil {
		return nil, nil, err
	}
	store, err := session.Open(c.database)
	if err != nil {
		return nil, nil, err
	}
	engine, err := tasks.New(ctx, store, tasks.Config{Factory: func(_ string) (*agent.Agent, error) {
		return &agent.Agent{Model: &provider.OpenAI{Model: c.model, BaseURL: c.baseURL, APIKey: c.apiKey}, MaxSteps: c.maxSteps}, nil
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
		fmt.Fprintln(out, "Usage: qi serve [flags]\nQI_WEB_TOKEN (at least 32 characters) is required; browser username is qi.\nThis server supports persistent model chat only; standalone REPL sessions are separate.")
		fs.PrintDefaults()
	}
	listen := fs.String("listen", "127.0.0.1:8686", "literal loopback listening address")
	origin := fs.String("origin", "", "exact browser origin, no trailing slash (default http://listen address)")
	db := fs.String("db", "", "SQLite path (default user config directory/qi/tasks.db)")
	model := fs.String("model", env("OPENAI_MODEL"), "model name (or OPENAI_MODEL; required)")
	base := fs.String("base-url", env("OPENAI_BASE_URL"), "OpenAI-compatible API root")
	max := fs.Int("max-steps", 20, "maximum model calls per task turn")
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
	handler, closeAll, err := openDaemon(ctx, daemonConfig{database: *db, model: *model, baseURL: *base, apiKey: env("OPENAI_API_KEY"), origin: *origin, token: token, maxSteps: *max})
	if err != nil {
		return err
	}
	defer closeAll()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Fprintf(out, "qi WebUI: %s\nSign in as qi using QI_WEB_TOKEN. Token is never printed.\nSQLite: %s\nServer tools are disabled. Standalone REPL does not share these tasks.\n", *origin, *db)
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
