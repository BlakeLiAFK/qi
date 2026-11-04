package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"gopi/internal/agent"
)

const maxStreamBytes = 8 << 20
const maxStreamFrame = 1 << 20

// readStream never returns a partial completion. Even a syntactically complete
// tool call remains untrusted until a successful finish reason AND [DONE].
func readStream(ctx context.Context, response *http.Response, emit func(agent.Event)) (agent.Message, error) {
	zero := agent.Message{}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "text/event-stream" {
		return zero, errors.New("model stream requires text/event-stream")
	}
	reader := &io.LimitedReader{R: response.Body, N: maxStreamBytes + 1}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxStreamFrame)
	var text strings.Builder
	calls := map[int]*call{}
	var pending strings.Builder
	lastEmit := time.Time{}
	previewBytes, previewEvents := 0, 0
	flush := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if pending.Len() > 0 {
			if emit != nil && previewEvents < 256 {
				emit(agent.Event{Type: "model_delta", Delta: pending.String()})
				previewEvents++
			}
			pending.Reset()
			lastEmit = time.Now()
		}
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if emit != nil {
		emit(agent.Event{Type: "model_start"})
	}
	finished := false
	finishReason := ""
	process := func(data string) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if data == "[DONE]" {
			if !finished {
				return false, errors.New("model stream ended without finish reason")
			}
			return true, nil
		}
		if !utf8.ValidString(data) {
			return false, errors.New("invalid model stream encoding")
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Role      string `json:"role"`
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    *int     `json:"index"`
						ID       string   `json:"id"`
						Type     string   `json:"type"`
						Function function `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		// Do not echo parser errors or provider error fields; both can contain secrets.
		if json.Unmarshal([]byte(data), &chunk) != nil {
			return false, errors.New("decode model stream frame")
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return false, errors.New("model stream reported an error")
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				return false, errors.New("model stream returned unexpected choice")
			}
			if finished {
				return false, errors.New("model stream continued after finish")
			}
			d := choice.Delta
			if d.Role != "" && d.Role != "assistant" {
				return false, errors.New("model stream returned unexpected role")
			}
			if text.Len()+len(d.Content) > maxStreamBytes {
				return false, errors.New("model stream text exceeds limit")
			}
			text.WriteString(d.Content)
			// Bound each persisted event, including worst-case JSON escaping. Flush on
			// frame arrival, without timers/goroutines that can reorder or outlive a run.
			for len(d.Content) > 0 && previewBytes < 64<<10 && previewEvents < 256 {
				n := len(d.Content)
				if n > 4096 {
					n = 4096
				}
				if n > (64<<10)-previewBytes {
					n = (64 << 10) - previewBytes
				}
				if n < len(d.Content) {
					for n > 0 && !utf8.RuneStart(d.Content[n]) {
						n--
					}
				}
				if n == 0 {
					break
				}
				previewBytes += n
				pending.WriteString(d.Content[:n])
				d.Content = d.Content[n:]
				if pending.Len() >= 4096 || time.Since(lastEmit) >= 50*time.Millisecond {
					if err := flush(); err != nil {
						return false, err
					}
				}
			}
			for _, delta := range d.ToolCalls {
				if delta.Index == nil || *delta.Index < 0 || *delta.Index >= agent.MaxToolCalls {
					return false, errors.New("invalid streamed tool index")
				}
				c := calls[*delta.Index]
				if c == nil {
					c = &call{}
					calls[*delta.Index] = c
				}
				c.ID += delta.ID
				c.Type += delta.Type
				c.Function.Name += delta.Function.Name
				c.Function.Arguments += delta.Function.Arguments
				if len(c.ID) > 4096 || len(c.Type) > 32 || len(c.Function.Name) > 4096 || len(c.Function.Arguments) > agent.MaxToolBytes {
					return false, errors.New("streamed tool call exceeds limit")
				}
			}
			if choice.FinishReason != nil {
				finishReason = *choice.FinishReason
				switch finishReason {
				case "stop", "tool_calls":
					finished = true
				case "length":
					return false, errors.New("model response was truncated by token limit")
				case "content_filter":
					return false, errors.New("model response was blocked by content filter")
				default:
					return false, errors.New("unsupported model stream finish reason")
				}
			}
		}
		return false, nil
	}
	var frame strings.Builder
	done := false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if reader.N <= 0 {
			return zero, errors.New("model stream exceeds 8 MiB")
		}
		line := scanner.Text()
		if line == "" {
			if frame.Len() == 0 {
				continue
			}
			data := strings.TrimSuffix(frame.String(), "\n")
			frame.Reset()
			done, err = process(data)
			if err != nil {
				return zero, err
			}
			if done {
				break
			}
		} else if strings.HasPrefix(line, "data:") {
			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimPrefix(data, " ")
			if frame.Len()+len(data)+1 > maxStreamFrame {
				return zero, errors.New("model stream frame exceeds limit")
			}
			frame.WriteString(data)
			frame.WriteByte('\n')
		}
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if !done {
		if reader.N <= 0 {
			return zero, errors.New("model stream exceeds 8 MiB")
		}
		if scanner.Err() != nil {
			return zero, errors.New("read model stream failed")
		}
		return zero, errors.New("model stream ended before [DONE]")
	}
	if (len(calls) > 0) != (finishReason == "tool_calls") {
		return zero, errors.New("model stream finish reason does not match tool calls")
	}
	out := agent.Message{Role: "assistant", Content: text.String()}
	ids := map[string]bool{}
	for i := 0; i < len(calls); i++ {
		c := calls[i]
		if c == nil || c.ID == "" || ids[c.ID] || c.Type != "function" || c.Function.Name == "" || !agent.IsJSONObject(json.RawMessage(c.Function.Arguments)) {
			return zero, errors.New("invalid completed streamed tool call")
		}
		ids[c.ID] = true
		out.ToolCalls = append(out.ToolCalls, agent.ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: json.RawMessage(c.Function.Arguments)})
	}
	if err := flush(); err != nil {
		return zero, err
	}
	return out, nil
}
