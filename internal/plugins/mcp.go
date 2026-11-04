// Package plugins runs explicitly configured, trusted MCP stdio servers.
// Plugins execute with the user's privileges: this is not a security sandbox.
package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	cmd.Env = append(os.Environ(), cfg.Env...)
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
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	c.mu.Lock()
	c.next++
	id := c.next
	ch := make(chan response, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	// Writing can block on a non-reading process. Close kills it on deadline.
	written := make(chan error, 1)
	go func() {
		written <- c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	}()
	select {
	case err := <-written:
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		c.Close()
		return nil, ctx.Err()
	case <-c.done:
		return nil, errors.New("plugin exited")
	}
	select {
	case msg := <-ch:
		if msg.Error != nil {
			return nil, fmt.Errorf("MCP %d: %s", msg.Error.Code, msg.Error.Message)
		}
		return msg.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, errors.New("plugin exited")
	}
}
func (c *Client) Tools(ctx context.Context) ([]Tool, error) {
	var all []Tool
	cursor := ""
	seen := map[string]bool{}
	for {
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
	if !json.Valid(args) {
		return nil, errors.New("invalid tool arguments JSON")
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
