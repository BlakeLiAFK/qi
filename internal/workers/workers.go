// Package workers runs explicitly enabled coding CLIs as bounded background tasks.
// These processes can modify their working directory; this package is not a sandbox.
package workers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Enabled        bool
	CodexPath      string
	ClaudePath     string
	MaxOutputBytes int
	Timeout        time.Duration
	MaxTasks       int
}
type Request struct {
	Agent   string `json:"agent"`
	Prompt  string `json:"prompt"`
	WorkDir string `json:"work_dir"`
}
type Task struct {
	ID         string    `json:"id"`
	Agent      string    `json:"agent"`
	Status     string    `json:"status"`
	Output     string    `json:"output"`
	Truncated  bool      `json:"truncated"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}
type entry struct {
	task   Task
	output *boundedBuffer
	cancel context.CancelFunc
}
type Manager struct {
	mu     sync.Mutex
	config Config
	tasks  map[string]*entry
	closed bool
	wg     sync.WaitGroup
}

func New(c Config) *Manager {
	if c.CodexPath == "" {
		c.CodexPath = "codex"
	}
	if c.ClaudePath == "" {
		c.ClaudePath = "claude"
	}
	if c.MaxOutputBytes <= 0 {
		c.MaxOutputBytes = 64 * 1024
	}
	if c.Timeout <= 0 {
		c.Timeout = 15 * time.Minute
	}
	if c.MaxTasks <= 0 {
		c.MaxTasks = 100
	}
	return &Manager{config: c, tasks: make(map[string]*entry)}
}
func (m *Manager) Start(parent context.Context, r Request) (Task, error) {
	if !m.config.Enabled {
		return Task{}, errors.New("coding workers are disabled; enable explicitly")
	}
	if strings.TrimSpace(r.Prompt) == "" || r.WorkDir == "" {
		return Task{}, errors.New("prompt and work_dir are required")
	}
	var executable string
	var args []string
	switch r.Agent {
	case "codex":
		executable = m.config.CodexPath
		args = []string{"exec", "--sandbox", "workspace-write", "--", r.Prompt}
	case "claude":
		executable = m.config.ClaudePath
		args = []string{"--bare", "-p", "--output-format", "text", "--", r.Prompt}
	default:
		return Task{}, fmt.Errorf("unsupported coding agent %q", r.Agent)
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Task{}, err
	}
	ctx, cancel := context.WithTimeout(parent, m.config.Timeout)
	b := &boundedBuffer{limit: m.config.MaxOutputBytes}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = r.WorkDir
	cmd.Stdout = b
	cmd.Stderr = b
	cmd.WaitDelay = 2 * time.Second
	configureProcess(cmd)
	e := &entry{task: Task{ID: hex.EncodeToString(id[:]), Agent: r.Agent, Status: "running", StartedAt: time.Now().UTC()}, output: b, cancel: cancel}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		cancel()
		return Task{}, errors.New("worker manager is closed")
	}
	if len(m.tasks) >= m.config.MaxTasks {
		cancel()
		return Task{}, errors.New("worker task capacity reached")
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return Task{}, fmt.Errorf("start %s: %w", r.Agent, err)
	}
	m.tasks[e.task.ID] = e
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		err := cmd.Wait()
		m.mu.Lock()
		defer m.mu.Unlock()
		e.task.Status = "completed"
		if ctx.Err() != nil {
			e.task.Status = "cancelled"
			e.task.Error = ctx.Err().Error()
		} else if err != nil {
			e.task.Status = "failed"
			e.task.Error = err.Error()
		}
		e.task.FinishedAt = time.Now().UTC()
	}()
	return e.task, nil
}
func (m *Manager) Get(id string) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.tasks[id]
	if !ok {
		return Task{}, errors.New("task not found")
	}
	t := e.task
	t.Output, t.Truncated = e.output.snapshot()
	return t, nil
}
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.tasks[id]
	if !ok {
		return errors.New("task not found")
	}
	if e.task.Status == "running" {
		e.cancel()
	}
	return nil
}
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	for _, e := range m.tasks {
		e.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

// Forget releases retained output only for a terminal task.
func (m *Manager) Forget(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.tasks[id]
	if !ok {
		return errors.New("task not found")
	}
	if e.task.Status == "running" {
		return errors.New("task is running")
	}
	delete(m.tasks, id)
	return nil
}

type boundedBuffer struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := b.limit - len(b.data)
	if n > remaining {
		b.truncated = true
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (b *boundedBuffer) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.ToValidUTF8(string(b.data), "�"), b.truncated
}
