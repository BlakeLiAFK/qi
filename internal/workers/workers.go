// Package workers runs explicitly enabled coding CLIs as bounded background tasks.
// These processes can modify their working directory; this package is not a sandbox.
package workers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Credentials    map[string]string
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
	credentials := make(map[string]string, len(c.Credentials))
	for key, value := range c.Credentials {
		credentials[key] = value
	}
	c.Credentials = credentials
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

// Arguments is the single allowlisted argument plan used for review and launch.
// Never add permission-bypass or unrestricted-execution flags here.
func Arguments(agent, prompt string) ([]string, error) {
	switch agent {
	case "codex":
		return []string{"--ask-for-approval", "never", "exec", "--sandbox", "workspace-write", "--", prompt}, nil
	case "claude":
		return []string{"--bare", "--permission-mode", "default", "-p", "--output-format", "text", "--", prompt}, nil
	default:
		return nil, fmt.Errorf("unsupported coding agent %q", agent)
	}
}

func (m *Manager) Start(parent context.Context, r Request) (Task, error) {
	if !m.config.Enabled {
		return Task{}, errors.New("coding workers are disabled; enable explicitly")
	}
	if strings.TrimSpace(r.Prompt) == "" || r.WorkDir == "" {
		return Task{}, errors.New("prompt and work_dir are required")
	}
	args, err := Arguments(r.Agent, r.Prompt)
	if err != nil {
		return Task{}, err
	}
	executable := m.config.CodexPath
	if r.Agent == "claude" {
		executable = m.config.ClaudePath
	}

	env, cleanEnvironment, err := workerEnvironment(m.config.Credentials)
	if err != nil {
		return Task{}, err
	}
	started := false
	defer func() {
		if !started {
			cleanEnvironment()
		}
	}()
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Task{}, err
	}
	ctx, cancel := context.WithTimeout(parent, m.config.Timeout)
	b := &boundedBuffer{limit: m.config.MaxOutputBytes}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = env
	cmd.Dir = r.WorkDir
	// Own the output pipe so cmd.Wait observes the CLI parent exit rather than
	// waiting for descendants to close inherited stdout before group cleanup.
	outputReader, outputWriter, err := os.Pipe()
	if err != nil {
		cancel()
		return Task{}, err
	}
	defer func() {
		if !started {
			outputReader.Close()
			outputWriter.Close()
		}
	}()
	cmd.Stdout = outputWriter
	cmd.Stderr = outputWriter
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
	started = true
	_ = outputWriter.Close()
	outputDone := make(chan struct{})
	go func() { defer close(outputDone); defer outputReader.Close(); _, _ = io.Copy(b, outputReader) }()
	m.tasks[e.task.ID] = e
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		defer cleanEnvironment()
		err := cmd.Wait()
		cleanupProcess(cmd)
		// A descendant escaping its group must not hold this run open forever.
		select {
		case <-outputDone:
		case <-time.After(100 * time.Millisecond):
			_ = outputReader.Close()
			<-outputDone
		}
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

// Write keeps the original prefix and a rolling suffix within the byte budget.
// Once output exceeds the budget, middle bytes are discarded, never the ending.
func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if n == 0 {
		return 0, nil
	}
	if b.limit <= 0 {
		b.truncated = true
		return n, nil
	}
	oldLen := len(b.data)
	if !b.truncated && n <= b.limit-oldLen {
		b.data = append(b.data, p...)
		return n, nil
	}
	b.truncated = true
	prefixLen := b.limit / 2
	tailLen := b.limit - prefixLen
	if oldLen < b.limit {
		b.data = append(b.data, make([]byte, b.limit-oldLen)...)
	}
	if oldLen < prefixLen {
		copy(b.data[oldLen:prefixLen], p[:prefixLen-oldLen])
	}
	if n >= tailLen {
		copy(b.data[prefixLen:], p[n-tailLen:])
	} else {
		keep := tailLen - n
		copy(b.data[prefixLen:prefixLen+keep], b.data[oldLen-keep:oldLen])
		copy(b.data[prefixLen+keep:], p)
	}
	return n, nil
}
func (b *boundedBuffer) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// ASCII replacement keeps the returned byte count within the configured cap,
	// including when truncation splits a multi-byte UTF-8 character.
	valid := func(p []byte) string { return strings.ToValidUTF8(string(p), "?") }
	if !b.truncated {
		return valid(b.data), false
	}
	if b.limit <= 0 {
		return "", true
	}
	marker := "\n...[output truncated]...\n"
	if len(marker) >= b.limit {
		marker = "~"
	}
	remaining := b.limit - len(marker)
	prefixLen := remaining / 2
	tailLen := remaining - prefixLen
	return valid(b.data[:prefixLen]) + marker + valid(b.data[len(b.data)-tailLen:]), true
}
