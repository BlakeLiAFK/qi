package tasks

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const MaxLiveTextBytes = 64 << 10

// LiveText is a bounded provisional snapshot, never a persisted chat message or
// executable tool arguments. RequestID and Revision are committed event IDs.
type LiveText struct {
	TaskID    string `json:"task_id"`
	RunID     string `json:"run_id"`
	RequestID int64  `json:"request_id"`
	Revision  int64  `json:"revision"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

func projectLiveText(taskID string, events []Event, runs []Run) *LiveText {
	running := map[string]bool{}
	for _, run := range runs {
		if run.Status == "running" {
			running[run.ID] = true
		}
	}
	var live *LiveText
	var text strings.Builder
	for _, event := range events {
		if !running[event.RunID] {
			continue
		}
		switch event.Type {
		case "model_start":
			live = &LiveText{TaskID: taskID, RunID: event.RunID, RequestID: event.ID, Revision: event.ID}
			text.Reset()
		case "model_delta":
			if live == nil || live.RunID != event.RunID {
				continue
			}
			var payload struct {
				Delta string `json:"delta"`
			}
			if json.Unmarshal(event.Payload, &payload) != nil {
				live = nil
				text.Reset()
				continue
			}
			delta := strings.ToValidUTF8(payload.Delta, "�")
			n := len(delta)
			remaining := MaxLiveTextBytes - text.Len()
			if n > remaining {
				n = remaining
				live.Truncated = true
				for n > 0 && !utf8.ValidString(delta[:n]) {
					n--
				}
			}
			text.WriteString(delta[:n])
			live.Revision = event.ID
		case "message", "error", "run_end":
			if live != nil && live.RunID == event.RunID {
				live = nil
				text.Reset()
			}
		}
	}
	if live == nil || text.Len() == 0 {
		return nil
	}
	live.Text = text.String()
	if text.Len() >= MaxLiveTextBytes {
		live.Truncated = true
	}
	return live
}
