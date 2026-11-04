package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gopi/internal/agent"
)

func streamFrame(delta string, finish string) string {
	reason := "null"
	if finish != "" {
		reason = fmt.Sprintf("%q", finish)
	}
	return `data: {"choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + reason + "}]}\n\n"
}

const streamDone = "data: [DONE]\n\n"

func responseStream(body string) *http.Response {
	return &http.Response{Header: http.Header{"Content-Type": []string{"text/event-stream; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader(body))}
}

type oneByteReader struct{ io.Reader }

func (r oneByteReader) Read(p []byte) (int, error) { return r.Reader.Read(p[:1]) }

func TestStreamSplitFramesAndMultiline(t *testing.T) {
	body := ": heartbeat\r\n\r\ndata: {\r\ndata: \"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"你好\"},\"finish_reason\":null}]}\r\n\r\n" + streamFrame(`{"content":" world"}`, "stop") + streamDone
	response := responseStream(body)
	response.Body = io.NopCloser(oneByteReader{response.Body})
	var events []agent.Event
	got, err := readStream(context.Background(), response, func(e agent.Event) { events = append(events, e) })
	if err != nil || got.Content != "你好 world" {
		t.Fatalf("%+v %v", got, err)
	}
	if len(events) < 2 || events[0].Type != "model_start" {
		t.Fatalf("events %+v", events)
	}
	text := ""
	for _, e := range events {
		text += e.Delta
		if e.ToolCall != nil || e.Message != nil {
			t.Fatal("unexpected full/partial message")
		}
	}
	if text != got.Content {
		t.Fatalf("delta %q", text)
	}
}

func TestStreamAggregatesToolCallsByIndex(t *testing.T) {
	body := streamFrame(`{"tool_calls":[{"index":1,"id":"second","type":"function","function":{"name":"two","arguments":"{\"b\":"}},{"index":0,"id":"first","type":"function","function":{"name":"one","arguments":"{"}}]}`, "") + streamFrame(`{"tool_calls":[{"index":0,"function":{"arguments":"}"}},{"index":1,"function":{"arguments":"2}"}}]}`, "tool_calls") + streamDone
	got, err := readStream(context.Background(), responseStream(body), nil)
	if err != nil || len(got.ToolCalls) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if got.ToolCalls[0].ID != "first" || string(got.ToolCalls[1].Arguments) != `{"b":2}` {
		t.Fatalf("%+v", got)
	}
}

func TestStreamRejectsIncompleteAndUnsafeResults(t *testing.T) {
	validCall := `{"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]}`
	cases := map[string]string{
		"eof":                 streamFrame(`{"content":"partial"}`, ""),
		"missing_done":        streamFrame(validCall, "tool_calls"),
		"missing_finish":      streamFrame(validCall, "") + streamDone,
		"length":              streamFrame(validCall, "length") + streamDone,
		"filter":              streamFrame(`{}`, "content_filter") + streamDone,
		"error":               `data: {"error":{"message":"private-secret"}}` + "\n\n",
		"malformed":           "data: {private-secret\n\n",
		"partial_args":        streamFrame(strings.Replace(validCall, `"{}"`, `"{"`, 1), "tool_calls") + streamDone,
		"null_args":           streamFrame(strings.Replace(validCall, `"{}"`, `"null"`, 1), "tool_calls") + streamDone,
		"missing_index":       streamFrame(strings.Replace(validCall, `"index":0,`, "", 1), "tool_calls") + streamDone,
		"negative_index":      streamFrame(strings.Replace(validCall, `"index":0`, `"index":-1`, 1), "tool_calls") + streamDone,
		"index_gap":           streamFrame(strings.Replace(validCall, `"index":0`, `"index":1`, 1), "tool_calls") + streamDone,
		"wrong_type":          streamFrame(strings.Replace(validCall, `"type":"function"`, `"type":"custom"`, 1), "tool_calls") + streamDone,
		"stop_with_tools":     streamFrame(validCall, "stop") + streamDone,
		"tools_without_calls": streamFrame(`{}`, "tool_calls") + streamDone,
		"after_finish":        streamFrame(`{}`, "stop") + streamFrame(`{"content":"late"}`, "") + streamDone,
		"unfinished_frame":    strings.TrimSuffix(streamFrame(`{}`, "stop"), "\n") + streamDone,
		"oversized_frame":     "data: " + strings.Repeat("x", maxStreamFrame) + "\n\n",
		"oversized_wire":      strings.Repeat(": "+strings.Repeat("x", 1024)+"\n", maxStreamBytes/1024) + streamFrame(`{}`, "stop") + streamDone,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := readStream(context.Background(), responseStream(body), nil)
			if err == nil || got.Content != "" || len(got.ToolCalls) > 0 || strings.Contains(err.Error(), "private-secret") {
				t.Fatalf("result %+v err %v", got, err)
			}
		})
	}
}

func TestStreamCancellationAfterDelta(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := streamFrame(`{"content":"partial"}`, "") + streamFrame(`{}`, "stop") + streamDone
	got, err := readStream(ctx, responseStream(body), func(e agent.Event) {
		if e.Type == "model_delta" {
			cancel()
		}
	})
	if err != context.Canceled || got.Content != "" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestStreamPreviewBounded(t *testing.T) {
	content := strings.Repeat("你", 100000)
	delta, _ := json.Marshal(map[string]string{"content": content})
	body := streamFrame(string(delta), "stop") + streamDone
	size, count := 0, 0
	got, err := readStream(context.Background(), responseStream(body), func(e agent.Event) {
		if e.Type == "model_delta" {
			size += len(e.Delta)
			count++
			if len(e.Delta) > 8192 {
				t.Fatal("large event")
			}
		}
	})
	if err != nil || got.Content != content || size > 64<<10 || count > 256 || size == 0 {
		t.Fatalf("len=%d bytes=%d count=%d err=%v", len(got.Content), size, count, err)
	}
}

func TestOpenAIStreamingRequestAndIsolation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Stream   bool      `json:"stream"`
			Messages []message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !req.Stream {
			t.Errorf("request %+v %v", req, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		delta, _ := json.Marshal(map[string]string{"content": req.Messages[0].Content})
		io.WriteString(w, streamFrame(string(delta), "stop")+streamDone)
	}))
	defer server.Close()
	p := OpenAI{BaseURL: server.URL, Model: "test", Stream: true}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want := fmt.Sprint(i)
			progress := ""
			got, err := p.CompleteWithEvents(context.Background(), []agent.Message{{Role: "user", Content: want}}, nil, func(e agent.Event) { progress += e.Delta })
			if err != nil || got.Content != want || progress != want {
				t.Errorf("%+v %s %v", got, progress, err)
			}
		}(i)
	}
	wg.Wait()
}

type streamTestTool struct{ executions int }

func (t *streamTestTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "f", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (t *streamTestTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.executions++
	return "done", nil
}
func TestAgentNeverExecutesInterruptedStreamTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, streamFrame(`{"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]}`, "tool_calls"))
	}))
	defer server.Close()
	tool := &streamTestTool{}
	a := agent.Agent{Model: &OpenAI{BaseURL: server.URL, Model: "test", Stream: true}, Tools: []agent.Tool{tool}}
	history, err := a.Run(context.Background(), []agent.Message{{Role: "user", Content: "go"}})
	if err == nil || tool.executions != 0 || len(history) != 1 {
		t.Fatalf("history %+v executions=%d err=%v", history, tool.executions, err)
	}
}

func TestStreamingHTTPCancelWhileWaiting(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, streamFrame(`{"content":"first"}`, ""))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := OpenAI{BaseURL: server.URL, Model: "test", Stream: true}
	result := make(chan error, 1)
	go func() {
		_, err := p.CompleteWithEvents(ctx, nil, nil, func(e agent.Event) {
			if e.Type == "model_delta" {
				cancel()
			}
		})
		result <- err
	}()
	<-started
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancel succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream cancellation blocked")
	}
}

func TestStreamRejectsContentTypeAndDuplicateIDs(t *testing.T) {
	response := responseStream(streamFrame(`{}`, "stop") + streamDone)
	response.Header.Set("Content-Type", "application/json")
	if _, err := readStream(context.Background(), response, nil); err == nil {
		t.Fatal("wrong content type accepted")
	}
	body := streamFrame(`{"tool_calls":[{"index":0,"id":"same","type":"function","function":{"name":"f","arguments":"{}"}},{"index":1,"id":"same","type":"function","function":{"name":"f","arguments":"{}"}}]}`, "tool_calls") + streamDone
	if got, err := readStream(context.Background(), responseStream(body), nil); err == nil || len(got.ToolCalls) > 0 {
		t.Fatalf("%+v %v", got, err)
	}
}
