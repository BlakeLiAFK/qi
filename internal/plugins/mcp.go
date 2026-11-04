// Package plugins runs explicitly configured, trusted MCP stdio servers.
// Plugins execute with the user's privileges: this is not a security sandbox.
package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gopi/internal/agent"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

type Config struct {
	Command string        `json:"command"`
	Args    []string      `json:"args,omitempty"`
	Env     []string      `json:"env,omitempty"`
	Dir     string        `json:"dir,omitempty"`
	Timeout time.Duration `json:"-"`
}
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}
type response struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
type Client struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	mu      sync.Mutex
	next    uint64
	pending map[uint64]chan response
	done    chan struct{}
	once    sync.Once
	writeMu sync.Mutex
	timeout time.Duration
}

// Start performs MCP initialization; ctx controls startup, not server lifetime.
func Start(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Command == "" {
		return nil, errors.New("plugin command is required")
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Dir
	cmd.Env = pluginEnvironment(cfg.Env)
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		in.Close()
		out.Close()
		return nil, err
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	c := &Client{cmd: cmd, stdin: in, pending: make(map[uint64]chan response), done: make(chan struct{}), timeout: timeout}
	go c.read(out)
	go func() { _ = cmd.Wait(); c.finish() }()
	_, err = c.request(ctx, "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "gopi", "version": "0.1.0"}})
	if err == nil {
		sent := make(chan error, 1)
		go func() { sent <- c.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}) }()
		timer := time.NewTimer(timeout)
		select {
		case err = <-sent:
		case <-ctx.Done():
			err = ctx.Err()
		case <-timer.C:
			err = context.DeadlineExceeded
		case <-c.done:
			err = errors.New("plugin exited")
		}
		timer.Stop()
	}
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("initialize plugin: %w", err)
	}
	return c, nil
}

// Only routine process settings are inherited. API keys and other application
// credentials must be explicitly supplied through Config.Env. This reduces
// accidental leakage, but a trusted plugin still has the user's OS privileges.
func pluginEnvironment(extra []string) []string {
	env := []string{}
	for _, key := range []string{"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "SystemRoot", "WINDIR", "USERPROFILE"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return append(env, extra...)
}

func (c *Client) finish() { c.once.Do(func() { close(c.done) }) }
func (c *Client) read(r io.Reader) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), 16*1024*1024)
	for s.Scan() {
		var msg response
		if json.Unmarshal(s.Bytes(), &msg) != nil {
			continue
		}
		if msg.ID == 0 {
			continue
		}
		c.mu.Lock()
		ch := c.pending[msg.ID]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- msg:
			default:
			}
		}
	}
	c.finish()
}
func (c *Client) write(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return json.NewEncoder(c.stdin).Encode(v)
}
func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-c.done:
		return nil, errors.New("plugin exited")
	default:
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	c.mu.Lock()
	if len(c.pending) >= 128 {
		c.mu.Unlock()
		return nil, errors.New("too many pending plugin requests")
	}
	c.next++
	id := c.next
	ch := make(chan response, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	// Once handed to the transport, failures cannot establish non-execution.
	uncertain := func(err error) error {
		if method == "tools/call" {
			return fmt.Errorf("%w: %w", agent.ErrOutcomeUnknown, err)
		}
		return err
	}
	// Writing can block on a non-reading process. Close kills it on deadline.
	written := make(chan error, 1)
	go func() {
		written <- c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	}()
	select {
	case err := <-written:
		if err != nil {
			return nil, uncertain(err)
		}
	case <-ctx.Done():
		c.Close()
		return nil, uncertain(ctx.Err())
	case <-c.done:
		return nil, uncertain(errors.New("plugin exited"))
	}
	select {
	case msg := <-ch:
		if msg.Error != nil {
			return nil, fmt.Errorf("MCP %d: %s", msg.Error.Code, msg.Error.Message)
		}
		if !agent.IsJSONObject(msg.Result) {
			return nil, uncertain(errors.New("invalid MCP result object"))
		}
		return msg.Result, nil
	case <-ctx.Done():
		return nil, uncertain(ctx.Err())
	case <-c.done:
		return nil, uncertain(errors.New("plugin exited"))
	}
}
func (c *Client) Tools(ctx context.Context) ([]Tool, error) {
	var all []Tool
	cursor := ""
	seen := map[string]bool{}
	for pageCount := 0; ; pageCount++ {
		if pageCount >= 100 {
			return nil, errors.New("MCP discovery page limit exceeded")
		}
		p := map[string]any{}
		if cursor != "" {
			p["cursor"] = cursor
		}
		raw, err := c.request(ctx, "tools/list", p)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools []Tool `json:"tools"`
			Next  string `json:"nextCursor"`
		}
		if err = json.Unmarshal(raw, &page); err != nil {
			return nil, err
		}
		if len(all)+len(page.Tools) > agent.MaxTools {
			return nil, errors.New("MCP tool count limit exceeded")
		}
		for _, tool := range page.Tools {
			if tool.Name == "" || len(tool.Name) > 256 || !agent.IsJSONObject(tool.InputSchema) {
				return nil, errors.New("invalid MCP tool definition")
			}
		}
		all = append(all, page.Tools...)
		if page.Next == "" {
			return all, nil
		}
		if seen[page.Next] {
			return nil, errors.New("repeated MCP pagination cursor")
		}
		seen[page.Next] = true
		cursor = page.Next
	}
}
func (c *Client) Call(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if name == "" || len(name) > 256 || !agent.IsJSONObject(args) {
		return nil, errors.New("invalid tool name or arguments; arguments must be a JSON object within size limit")
	}
	return c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
}
func (c *Client) Close() error {
	c.finish()
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		err := c.cmd.Process.Kill()
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	return nil
}
