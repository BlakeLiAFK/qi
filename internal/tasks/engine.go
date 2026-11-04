// Package tasks owns independent durable conversations and asynchronous turns.
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gopi/internal/agent"
	"gopi/internal/session"
	"gopi/internal/skills"
)

var ErrBusy = errors.New("task already has an active turn")
var ErrClosed = errors.New("task engine is closed")
var ErrStaleCard = errors.New("card revision or state is stale")

type Run = session.Run
type Event = session.Event
type Action struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
type Card struct {
	Kind      string          `json:"kind,omitempty"`
	TaskID    string          `json:"task_id,omitempty"`
	RunID     string          `json:"run_id,omitempty"`
	InputType string          `json:"input_type,omitempty"`
	Choices   []Action        `json:"choices,omitempty"`
	ExpiresAt time.Time       `json:"expires_at,omitempty"`
	Answer    *QuestionAnswer `json:"answer,omitempty"`
	ID        string          `json:"id"`
	Revision  int64           `json:"revision"`
	Title     string          `json:"title"`
	Summary   string          `json:"summary"`
	State     string          `json:"state"`
	Actions   []Action        `json:"actions"`
}
type ActionRequest struct {
	TaskID   string `json:"task_id,omitempty"`
	ChoiceID string `json:"choice_id,omitempty"`
	Text     string `json:"text,omitempty"`
	CardID   string `json:"card_id"`
	Revision int64  `json:"revision"`
	Action   string `json:"action"`
}

// ContextNotice describes the latest recorded context eviction, not a summary.
// It contains no prompt content; full conversation history remains stored.
type ContextNotice struct {
	RunID           string `json:"run_id"`
	DroppedMessages int    `json:"dropped_messages"`
	InputBytes      int    `json:"input_bytes"`
}
type Task struct {
	ContextNotice *ContextNotice  `json:"context_notice,omitempty"`
	ID            string          `json:"id"`
	Title         string          `json:"title"`
	Status        string          `json:"status"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	Messages      []agent.Message `json:"messages"`
	Runs          []Run           `json:"runs"`
	Cards         []Card          `json:"cards"`
}
type Config struct {
	Questions      bool
	Skills         *skills.Catalog
	MCP            []MCPServer
	Coding         *CodingConfig
	Factory        func(taskID string) (*agent.Agent, error)
	MaxTasks       int
	MaxConcurrent  int
	MaxMessages    int
	MaxPromptBytes int
}
type active struct {
	cancel context.CancelFunc
	done   chan struct{}
}
type Engine struct {
	mu        sync.Mutex
	store     *session.Store
	cfg       Config
	active    map[string]*active
	closed    bool
	release   func()
	closeOnce sync.Once
}

// New recovers abandoned runs as interrupted. It never replays external work.
// Only one engine may own a database at a time.
func New(ctx context.Context, s *session.Store, cfg Config) (*Engine, error) {
	if s == nil || cfg.Factory == nil {
		return nil, errors.New("store and agent factory are required")
	}
	if cfg.MaxTasks <= 0 {
		cfg.MaxTasks = 1000
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 8
	}
	if cfg.MaxMessages <= 0 {
		cfg.MaxMessages = 2000
	}
	if cfg.MaxPromptBytes <= 0 {
		cfg.MaxPromptBytes = 64 * 1024
	}
	coding, err := prepareCoding(cfg.Coding)
	if err != nil {
		return nil, err
	}
	cfg.Coding = coding
	cfg.MCP, err = prepareMCP(cfg.MCP)
	if err != nil {
		return nil, err
	}
	release, err := s.AcquireEngineLock()
	if err != nil {
		return nil, err
	}
	if err := s.InterruptRuns(ctx); err != nil {
		release()
		return nil, err
	}
	if err := s.InterruptApprovals(ctx); err != nil {
		release()
		return nil, err
	}
	return &Engine{store: s, cfg: cfg, active: map[string]*active{}, release: release}, nil
}
func (e *Engine) Create(ctx context.Context, title string) (Task, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return Task{}, ErrClosed
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "New task"
	}
	if len(title) > 160 {
		return Task{}, errors.New("title exceeds 160 bytes")
	}
	existing, err := e.store.List(ctx)
	if err != nil {
		return Task{}, err
	}
	if len(existing) >= e.cfg.MaxTasks {
		return Task{}, errors.New("task capacity reached")
	}
	s, err := e.store.Create(ctx, title)
	if err != nil {
		return Task{}, err
	}
	return Task{ID: s.ID, Title: s.Title, Status: "idle", CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Messages: []agent.Message{}, Runs: []Run{}, Cards: []Card{}}, nil
}

// List returns metadata only. Fetch a single task with Get for its conversation.
func (e *Engine) List(ctx context.Context) ([]Task, error) {
	metadata, err := e.store.ListTaskMetadata(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Task, 0, len(metadata))
	for _, item := range metadata {
		result = append(result, Task{ID: item.ID, Title: item.Title, Status: item.Status, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt})
	}
	return result, nil
}
func (e *Engine) Get(ctx context.Context, id string) (Task, error) {
	snapshot, err := e.store.LoadTaskSnapshot(ctx, id)
	if err != nil {
		return Task{}, err
	}
	s, history, runs, events := snapshot.Session, snapshot.Messages, snapshot.Runs, snapshot.Events
	t := Task{ID: id, Title: s.Title, Status: "idle", CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Messages: history, Runs: runs, Cards: []Card{}}
	for _, r := range runs {
		if r.StartedAt.After(t.UpdatedAt) {
			t.UpdatedAt = r.StartedAt
		}
		if r.FinishedAt.After(t.UpdatedAt) {
			t.UpdatedAt = r.FinishedAt
		}
	}
	if len(runs) > 0 {
		latest := runs[0]
		for _, r := range runs[1:] {
			if r.StartedAt.After(latest.StartedAt) {
				latest = r
			}
		}
		t.Status = latest.Status
	}
	indices := map[string]int{}
	for _, ev := range events {
		if ev.Type == "context_trim" {
			var notice ContextNotice
			if err := json.Unmarshal(ev.Payload, &notice); err != nil {
				return Task{}, fmt.Errorf("decode context notice: %w", err)
			}
			if notice.DroppedMessages > 0 && notice.InputBytes > 0 {
				notice.RunID = ev.RunID
				t.ContextNotice = &notice
			}
			continue
		}
		if ev.Type != "card" {
			continue
		}
		var c Card
		if err := json.Unmarshal(ev.Payload, &c); err != nil {
			return Task{}, fmt.Errorf("decode card: %w", err)
		}
		if index, ok := indices[c.ID]; ok {
			t.Cards[index] = c
		} else {
			indices[c.ID] = len(t.Cards)
			t.Cards = append(t.Cards, c)
		}
	}
	return t, nil
}
func (e *Engine) Events(ctx context.Context, id string, after int64) ([]Event, error) {
	return e.store.Events(ctx, id, after)
}

// Start starts one turn independent of the requesting HTTP connection lifetime.
// Cancellation is explicit through Cancel or Close. Other tasks run concurrently.
func (e *Engine) Start(ctx context.Context, id, prompt string) (Run, error) {
	if len(prompt) > e.cfg.MaxPromptBytes {
		return Run{}, errors.New("message exceeds size limit")
	}
	if strings.TrimSpace(prompt) == "" {
		return Run{}, errors.New("message is required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return Run{}, ErrClosed
	}
	if _, ok := e.active[id]; ok {
		return Run{}, ErrBusy
	}
	if len(e.active) >= e.cfg.MaxConcurrent {
		return Run{}, errors.New("concurrent task capacity reached")
	}
	s, history, err := e.store.Load(ctx, id)
	if err != nil {
		return Run{}, err
	}
	if len(history) >= e.cfg.MaxMessages {
		return Run{}, errors.New("task history capacity reached; create a new task")
	}
	runner, err := e.cfg.Factory(id)
	if err != nil {
		return Run{}, err
	}
	if runner == nil {
		return Run{}, errors.New("factory returned nil agent")
	}
	run, err := e.store.CreateRun(ctx, id)
	if err != nil {
		if errors.Is(err, session.ErrRunActive) {
			return Run{}, ErrBusy
		}
		return Run{}, err
	}
	// Tool calls left unanswered by interruption receive an explicit unknown-result
	// marker. They are never executed again by recovery.
	history = repairInterrupted(history)
	history = append(history, agent.Message{Role: "user", Content: prompt})
	if err = e.store.SaveRevision(ctx, id, s.Revision, history); err != nil {
		_ = e.store.FinishRun(context.Background(), run.ID, "failed", err.Error())
		return Run{}, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	a := &active{cancel: cancel, done: make(chan struct{})}
	e.active[id] = a
	go e.execute(runCtx, id, run, runner, history, s.Revision+1, a)
	return run, nil
}
func repairInterrupted(history []agent.Message) []agent.Message {
	result := make([]agent.Message, 0, len(history))
	pending := []agent.ToolCall{}
	flush := func() {
		for _, call := range pending {
			result = append(result, agent.Message{Role: "tool", ToolCallID: call.ID, Content: `{"error":"Previous turn interrupted. Tool outcome unknown; do not assume success or replay without checking."}`})
		}
		pending = nil
	}
	for _, m := range history {
		if m.Role == "tool" {
			for i, c := range pending {
				if c.ID == m.ToolCallID {
					pending = append(pending[:i], pending[i+1:]...)
					break
				}
			}
		} else {
			flush()
		}
		result = append(result, m)
		if m.Role == "assistant" {
			pending = append(pending, m.ToolCalls...)
		}
	}
	flush()
	return result
}
func (e *Engine) execute(ctx context.Context, id string, run Run, runner *agent.Agent, history []agent.Message, revision int64, a *active) {
	defer func() { a.cancel(); e.mu.Lock(); delete(e.active, id); close(a.done); e.mu.Unlock() }()
	// Revoke before publishing done, so callers cannot observe orphan approvals.
	defer func() { _ = e.store.RevokeRunApprovals(context.Background(), id, run.ID) }()
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = e.store.FinishRun(context.Background(), run.ID, "failed", fmt.Sprintf("agent panic: %v", recovered))
		}
	}()
	// Copy the runner so factories may safely return a template.
	copyRunner := *runner
	if e.cfg.Questions {
		copyRunner.Tools = append(append([]agent.Tool(nil), runner.Tools...), questionTool{engine: e, taskID: id, runID: run.ID}, questionAnswersTool{engine: e, taskID: id})
	}
	if e.cfg.Coding != nil {
		copyRunner.Tools = append(append([]agent.Tool(nil), copyRunner.Tools...), codingTool{engine: e, taskID: id, runID: run.ID, config: e.cfg.Coding})
	}
	for _, server := range e.cfg.MCP {
		for _, tool := range server.Tools {
			copyRunner.Tools = append(append([]agent.Tool(nil), copyRunner.Tools...), mcpTool{engine: e, taskID: id, runID: run.ID, server: server, tool: tool})
		}
	}
	var persistErr error
	original := copyRunner.OnEvent
	copyRunner.OnEvent = func(ev agent.Event) {
		if persistErr != nil {
			return
		}
		payload, err := json.Marshal(ev)
		if err == nil && len(payload) > 1024*1024 {
			err = errors.New("event exceeds 1 MiB limit")
		}
		if err == nil && ev.Message != nil && len(history) >= e.cfg.MaxMessages {
			err = errors.New("task history capacity reached")
		}
		if err == nil {
			_, err = e.store.AppendEvent(context.Background(), id, run.ID, ev.Type, payload)
		}
		if err == nil && ev.Message != nil {
			history = append(history, *ev.Message)
			err = e.store.SaveRevision(context.Background(), id, revision, history)
			if err == nil {
				revision++
			}
		}
		if err != nil {
			persistErr = err
			a.cancel()
			return
		}
		if original != nil {
			original(ev)
		}
	}
	_, err := copyRunner.Run(ctx, history)
	status := "completed"
	errorText := ""
	if persistErr != nil {
		status = "failed"
		errorText = "persist run: " + persistErr.Error()
	} else if ctx.Err() != nil {
		status = "cancelled"
		errorText = ctx.Err().Error()
	} else if err != nil {
		status = "failed"
		errorText = err.Error()
	}
	// If this write fails, the durable run remains running and startup recovery
	// marks it interrupted rather than claiming success.
	_ = e.store.FinishRun(context.Background(), run.ID, status, errorText)
}
func (e *Engine) Cancel(ctx context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.store.Get(ctx, id); err != nil {
		return err
	}
	if a := e.active[id]; a != nil {
		a.cancel()
	}
	return nil
}
func (e *Engine) Wait(ctx context.Context, id string) error {
	e.mu.Lock()
	a := e.active[id]
	e.mu.Unlock()
	if a == nil {
		return nil
	}
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (e *Engine) Close() {
	e.mu.Lock()
	e.closed = true
	waiting := []chan struct{}{}
	for _, a := range e.active {
		a.cancel()
		waiting = append(waiting, a.done)
	}
	e.mu.Unlock()
	for _, done := range waiting {
		<-done
	}
	e.closeOnce.Do(e.release)
}

// AddCard stores an informational approval card. Cards cannot execute commands:
// approved means a recorded decision, not permission to replay a prior tool.
func (e *Engine) AddCard(ctx context.Context, id, runID string, c Card) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrClosed
	}
	if c.ID == "" || c.Title == "" || len(c.ID) > 128 || len(c.Title) > 200 || len(c.Summary) > 8000 {
		return errors.New("card ID and title required")
	}
	if c.Revision != 0 {
		return errors.New("new card revision must be zero")
	}
	t, err := e.Get(ctx, id)
	if err != nil {
		return err
	}
	for _, old := range t.Cards {
		if old.ID == c.ID {
			return ErrStaleCard
		}
	}
	c.Revision = 1
	c.State = "pending"
	c.TaskID, c.RunID = id, runID
	if c.Kind == "question" {
		if err := prepareQuestion(&c); err != nil {
			return err
		}
	} else if c.Kind == "" || c.Kind == "approval" {
		if c.InputType != "" || len(c.Choices) != 0 || c.Answer != nil {
			return errors.New("question fields require question kind")
		}
		c.Actions = []Action{{ID: "approve", Label: "Approve"}, {ID: "reject", Label: "Reject"}}
	} else {
		return errors.New("unsupported card kind")
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = e.store.AppendEvent(ctx, id, runID, "card", payload)
	return err
}
func (e *Engine) Act(ctx context.Context, id string, req ActionRequest) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrClosed
	}
	if req.Action != "approve" && req.Action != "reject" && req.Action != "answer" {
		return errors.New("unsupported card action")
	}
	t, err := e.Get(ctx, id)
	if err != nil {
		return err
	}
	for _, c := range t.Cards {
		if c.ID != req.CardID {
			continue
		}
		if c.Revision != req.Revision || c.State != "pending" {
			return ErrStaleCard
		}
		if c.Kind == "question" {
			if err := answerQuestion(&c, id, req); err != nil {
				return err
			}
		} else if req.Action == "answer" || req.Text != "" || req.ChoiceID != "" {
			return errors.New("invalid approval action")
		} else if req.Action == "approve" {
			c.State = "approved"
		} else {
			c.State = "rejected"
		}
		c.Revision++
		payload, _ := json.Marshal(c)
		events, err := e.store.Events(ctx, id, 0)
		if err != nil {
			return err
		}
		runID := ""
		for _, event := range events {
			if event.Type == "card" {
				var old Card
				if json.Unmarshal(event.Payload, &old) == nil && old.ID == c.ID {
					runID = event.RunID
				}
			}
		}
		_, err = e.store.AppendEvent(ctx, id, runID, "card", payload)
		return err
	}
	return session.ErrNotFound
}
