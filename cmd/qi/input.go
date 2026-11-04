package main

import (
	"bufio"
	"context"
	"io"
	"sync"
)

// lineInput also permits deterministic in-memory scanners in tool tests.
type lineInput interface {
	Scan() bool
	Text() string
	Err() error
}

// inputSession keeps reads on the caller goroutine. Cancellation closes a
// closable reader (including os.Stdin and pipes), unblocking Scan without leaving
// an abandoned goroutine behind. close joins the lifecycle watcher on every exit.
// A non-closable Reader must itself return promptly: arbitrary blocking Readers
// cannot be interrupted safely without an ownership/Close contract.
type inputSession struct {
	ctx     context.Context
	scanner *bufio.Scanner
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newInputSession(ctx context.Context, in io.Reader) *inputSession {
	s := &inputSession{ctx: ctx, scanner: bufio.NewScanner(in), stop: make(chan struct{}), done: make(chan struct{})}
	s.scanner.Buffer(make([]byte, 4096), 1<<20)
	closer, ok := in.(io.Closer)
	if !ok {
		close(s.done)
		return s
	}
	go func() {
		defer close(s.done)
		select {
		case <-ctx.Done():
			_ = closer.Close()
		case <-s.stop:
		}
	}()
	return s
}
func (s *inputSession) Scan() bool {
	if s.ctx.Err() != nil {
		return false
	}
	ok := s.scanner.Scan()
	return ok && s.ctx.Err() == nil
}
func (s *inputSession) Text() string { return s.scanner.Text() }
func (s *inputSession) Err() error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	return s.scanner.Err()
}
func (s *inputSession) close() { s.once.Do(func() { close(s.stop) }); <-s.done }
