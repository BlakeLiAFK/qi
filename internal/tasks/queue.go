package tasks

import (
	"context"
	"errors"
	"fmt"

	"gopi/internal/agent"
	"gopi/internal/session"
)

var ErrQueueFull = session.ErrQueueFull

// acceptLocked registers a committed admission. Only dispatchLocked launches
// workers, including question continuations. The engine mutex serializes claim,
// cancellation, shutdown and worker-slot accounting.
func (e *Engine) acceptLocked(run Run) {
	e.active[run.SessionID] = &active{run: run, done: make(chan struct{})}
	e.dispatchLocked()
}

func (e *Engine) dispatchLocked() {
	if e.closed || e.dispatchErr != nil {
		return
	}
	for {
		running := 0
		for _, a := range e.active {
			if a.running {
				running++
			}
		}
		if running >= e.cfg.MaxConcurrent {
			return
		}
		run, revision, err := e.store.ClaimNextQueued(context.Background())
		if errors.Is(err, session.ErrNotFound) {
			return
		}
		if err != nil {
			e.failDispatchLocked(err)
			return
		}
		a := e.active[run.SessionID]
		if a == nil || a.run.ID != run.ID {
			e.failDispatchLocked(errors.New("claimed queue entry has no matching admission"))
			return
		}
		s, history, err := e.store.Load(context.Background(), run.SessionID)
		if err == nil && s.Revision != revision {
			err = session.ErrConflict
		}
		var runner *agent.Agent
		if err == nil {
			runner, err = safeFactory(e.cfg.Factory, run.SessionID)
		}
		if err == nil && runner == nil {
			err = errors.New("factory returned nil agent")
		}
		if err != nil {
			if finishErr := e.store.FinishRun(context.Background(), run.ID, "failed", err.Error()); finishErr != nil {
				e.failDispatchLocked(finishErr)
				return
			}
			delete(e.active, run.SessionID)
			close(a.done)
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		a.cancel, a.running, a.run = cancel, true, run
		go e.execute(ctx, run.SessionID, run, runner, history, revision, a)
	}
}

func safeFactory(f func(string) (*agent.Agent, error), id string) (runner *agent.Agent, err error) {
	defer func() {
		if r := recover(); r != nil {
			runner = nil
			err = fmt.Errorf("agent factory panic: %v", r)
		}
	}()
	return f(id)
}

// A durable claim/write failure is ambiguous. Stop dispatch rather than replay;
// wake waiters with the error and let startup mark unfinished rows interrupted.
func (e *Engine) failDispatchLocked(err error) {
	e.dispatchErr = fmt.Errorf("task dispatcher stopped: %w", err)
	for id, a := range e.active {
		if !a.running {
			delete(e.active, id)
			close(a.done)
		}
	}
}

func (e *Engine) finishExecution(runID, status, detail string) {
	if err := e.store.FinishRun(context.Background(), runID, status, detail); err != nil {
		e.mu.Lock()
		e.failDispatchLocked(err)
		e.mu.Unlock()
	}
}
