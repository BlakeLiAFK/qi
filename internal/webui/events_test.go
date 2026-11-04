package webui

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gopi/internal/session"
)

type eventFake struct {
	fakeBackend
	mu    sync.Mutex
	after []int64
	ids   []int64
}

func (b *eventFake) EventIDs(ctx context.Context, id string, after int64, limit int) ([]int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.after = append(b.after, after)
	if id != "one" {
		return nil, session.ErrNotFound
	}
	ids := []int64{}
	available := b.ids
	if available == nil {
		available = []int64{1, 3, 5}
	}
	for _, id := range available {
		if id > after {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func TestEventStreamReplayAndCancellation(t *testing.T) {
	b := &eventFake{}
	h, err := New(Config{Origin: "http://127.0.0.1:8080", Token: token, Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/tasks/one/events", nil)
	req.Host = "127.0.0.1:8080"
	req.SetBasicAuth("qi", token)
	req.Header.Set("Last-Event-ID", "3")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("response: %v", resp)
	}
	reader := bufio.NewReader(resp.Body)
	var result strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		result.WriteString(line)
		if strings.HasPrefix(line, "data: {\"task_id\"") {
			break
		}
	}
	if !strings.Contains(result.String(), "id: 5\n") || strings.Contains(result.String(), "id: 1\n") || !strings.Contains(result.String(), `"task_id":"one"`) {
		t.Fatal(result.String())
	}
	cancel()
	resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s := h.(*server)
		s.mu.Lock()
		count := s.streams
		s.mu.Unlock()
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stream leaked after disconnect")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestEventStreamValidationAndAuthentication(t *testing.T) {
	h, _ := New(Config{Origin: "http://127.0.0.1:8080", Token: token, Backend: &eventFake{}})
	for _, tc := range []struct {
		path, cursor string
		auth         bool
		code         int
	}{{"one", "", false, 401}, {"missing", "", true, 404}, {"one", "-1", true, 400}, {"one", "nonsense", true, 400}, {"one", "9223372036854775808", true, 400}} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8080/api/tasks/"+tc.path+"/events", nil)
		if tc.auth {
			r.SetBasicAuth("qi", token)
		}
		r.Header.Set("Last-Event-ID", tc.cursor)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("%+v: %d", tc, w.Code)
		}
	}
	h.(*server).streams = 32
	w := request(h, "GET", "/api/tasks/one/events", "", true, "")
	if w.Code != 429 {
		body, _ := io.ReadAll(w.Result().Body)
		t.Fatalf("capacity: %d %s", w.Code, body)
	}
}

func TestEventStreamDeliversNewCommittedCursor(t *testing.T) {
	b := &eventFake{ids: []int64{}}
	h, _ := New(Config{Origin: "http://127.0.0.1:8080", Token: token, Backend: b})
	ts := httptest.NewServer(h)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/tasks/one/events", nil)
	req.Host = "127.0.0.1:8080"
	req.SetBasicAuth("qi", token)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\n" {
			break
		}
	}
	b.mu.Lock()
	b.ids = append(b.ids, 7)
	b.mu.Unlock()
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "id:") {
			if line != "id: 7\n" {
				t.Fatal(line)
			}
			break
		}
	}
}

type stalledEventBackend struct {
	fakeBackend
	entered chan struct{}
}

func (b *stalledEventBackend) EventIDs(ctx context.Context, _ string, _ int64, _ int) ([]int64, error) {
	b.entered <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestEventStreamConcurrentCapacityAndShutdown(t *testing.T) {
	b := &stalledEventBackend{entered: make(chan struct{}, 32)}
	h, _ := New(Config{Origin: "http://127.0.0.1:8080", Token: token, Backend: b})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("GET", "http://127.0.0.1:8080/api/tasks/one/events", nil).WithContext(ctx)
			r.SetBasicAuth("qi", token)
			h.ServeHTTP(httptest.NewRecorder(), r)
		}()
	}
	for i := 0; i < 32; i++ {
		select {
		case <-b.entered:
		case <-time.After(15 * time.Second):
			t.Fatal("streams failed to enter")
		}
	}
	if w := request(h, "GET", "/api/tasks/one/events", "", true, ""); w.Code != 429 {
		t.Fatal(w.Code)
	}
	cancel()
	wg.Wait()
	if h.(*server).streams != 0 {
		t.Fatal("shutdown retained stream slots")
	}
}

type deadlineFailureWriter struct {
	header   http.Header
	deadline time.Time
}

func (w *deadlineFailureWriter) Header() http.Header       { return w.header }
func (w *deadlineFailureWriter) WriteHeader(int)           {}
func (w *deadlineFailureWriter) Write([]byte) (int, error) { return 0, context.DeadlineExceeded }
func (w *deadlineFailureWriter) Flush()                    {}
func (w *deadlineFailureWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}
func TestEventStreamWriteFailureReleasesSlot(t *testing.T) {
	h, _ := New(Config{Origin: "http://127.0.0.1:8080", Token: token, Backend: &eventFake{}})
	r := httptest.NewRequest("GET", "http://127.0.0.1:8080/api/tasks/one/events", nil)
	r.SetBasicAuth("qi", token)
	w := &deadlineFailureWriter{header: make(http.Header)}
	h.ServeHTTP(w, r)
	if h.(*server).streams != 0 || !w.deadline.IsZero() {
		t.Fatal("write timeout retained slot or deadline")
	}
}
