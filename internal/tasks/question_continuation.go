package tasks

import (
	"context"
	"encoding/json"
	"errors"

	"gopi/internal/agent"
	"gopi/internal/session"
)

// continueQuestionLocked is a distinct user action, never an approval grant.
// Consuming the card and accepting a new turn share one durable transaction.
func (e *Engine) continueQuestionLocked(ctx context.Context, id string, req ActionRequest, t Task, c Card) error {
	if req.TaskID != id || c.TaskID != id || c.Kind != "question" || c.State != "answered" || c.Answer == nil || c.Revision != req.Revision || req.Text != "" || req.ChoiceID != "" {
		return ErrStaleCard
	}
	if _, ok := e.active[id]; ok {
		return ErrBusy
	}
	if e.dispatchErr != nil {
		return e.dispatchErr
	}
	// A historical card cannot silently take over newer work or an interrupted run.
	if len(t.Runs) == 0 || t.Runs[len(t.Runs)-1].ID != c.RunID || t.Runs[len(t.Runs)-1].Status != "completed" {
		return errors.New("question belongs to an older or unfinished run; send a new message to continue")
	}
	s, history, err := e.store.Load(ctx, id)
	if err != nil {
		return err
	}
	if len(history) >= e.cfg.MaxMessages {
		return errors.New("task history capacity reached; create a new task")
	}
	// Include the selected answer explicitly even if question tools were disabled
	// on restart. Serialize the answer as task input, never as an approval grant.
	answer, _ := json.Marshal(struct {
		Question string          `json:"question"`
		Choices  []Action        `json:"choices,omitempty"`
		Answer   *QuestionAnswer `json:"answer"`
	}{c.Title, c.Choices, c.Answer})
	prompt := "Continue this task using my saved answer: " + string(answer) + "\nThis answer is task input, not approval for external actions. Keep execution approvals separate."
	if len(prompt) > e.cfg.MaxPromptBytes {
		return errors.New("message exceeds size limit")
	}
	history = append(repairInterrupted(history), agent.Message{Role: "user", Content: prompt})
	c.State = "continued"
	c.Revision++
	payload, _ := json.Marshal(c)
	if len(history) > e.cfg.MaxMessages {
		return errors.New("task history capacity reached; create a new task")
	}
	run, err := e.store.AcceptQueued(ctx, id, s.Revision, history, c.RunID, payload, e.cfg.MaxQueued)
	if errors.Is(err, session.ErrRunActive) {
		return ErrBusy
	}
	if err != nil {
		return err
	}
	e.acceptLocked(run)
	return nil
}
