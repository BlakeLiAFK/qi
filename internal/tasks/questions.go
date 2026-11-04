package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"gopi/internal/agent"
)

// QuestionAnswer records user input only. It never grants tool permission or
// resumes a run. The model may retrieve answers on a later, user-started turn.
type QuestionAnswer struct {
	ChoiceID   string    `json:"choice_id,omitempty"`
	Text       string    `json:"text,omitempty"`
	AnsweredAt time.Time `json:"answered_at"`
}

func prepareQuestion(c *Card) error {
	if c.Answer != nil {
		return errors.New("new question cannot include an answer")
	}
	if c.InputType != "choice" && c.InputType != "text" {
		return errors.New("question input must be choice or text")
	}
	if c.InputType == "choice" {
		if len(c.Choices) < 2 || len(c.Choices) > 12 {
			return errors.New("question requires 2 to 12 choices")
		}
		seen := map[string]bool{}
		for _, choice := range c.Choices {
			if strings.TrimSpace(choice.ID) == "" || len(choice.ID) > 128 || strings.TrimSpace(choice.Label) == "" || len(choice.Label) > 300 || seen[choice.ID] {
				return errors.New("invalid or duplicate question choice")
			}
			seen[choice.ID] = true
		}
	} else if len(c.Choices) > 0 {
		return errors.New("text questions cannot include choices")
	}
	now := time.Now().UTC()
	if c.ExpiresAt.IsZero() {
		c.ExpiresAt = now.Add(24 * time.Hour)
	}
	if !c.ExpiresAt.After(now) || c.ExpiresAt.After(now.Add(7*24*time.Hour)) {
		return errors.New("question expiry must be within seven days")
	}
	c.Actions = nil
	return nil
}

func answerQuestion(c *Card, taskID string, req ActionRequest) error {
	if req.Action != "answer" || req.TaskID != taskID || c.TaskID != taskID {
		return errors.New("question task or action mismatch")
	}
	if !time.Now().Before(c.ExpiresAt) {
		return ErrStaleCard
	}
	a := &QuestionAnswer{AnsweredAt: time.Now().UTC()}
	if c.InputType == "choice" {
		if req.Text != "" {
			return errors.New("choice question does not accept free text")
		}
		found := false
		for _, choice := range c.Choices {
			if choice.ID == req.ChoiceID {
				found = true
			}
		}
		if !found {
			return errors.New("unknown question choice")
		}
		a.ChoiceID = req.ChoiceID
	} else if c.InputType == "text" {
		if req.ChoiceID != "" || strings.TrimSpace(req.Text) == "" || len(req.Text) > 8000 {
			return errors.New("question answer must contain 1 to 8000 bytes of text")
		}
		a.Text = req.Text
	} else {
		return errors.New("unsupported question input")
	}
	c.Answer, c.State = a, "answered"
	return nil
}

// Model tools are bound to the originating task/run. Model-supplied task IDs
// cannot redirect a question or read another task's answers.
type questionTool struct {
	engine        *Engine
	taskID, runID string
}

func (q questionTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "ask_user_question", Description: "Create a task-local choice or text question card. This only records a question, does not pause or resume the run, and never authorizes external actions. Finish your turn and ask the user to answer then send a message to continue. Use a unique ID per question. Do not request secrets.", Parameters: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["id","title","input_type"],"properties":{"id":{"type":"string"},"title":{"type":"string"},"summary":{"type":"string"},"input_type":{"type":"string","enum":["choice","text"]},"choices":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","label"],"properties":{"id":{"type":"string"},"label":{"type":"string"}}}},"expires_at":{"type":"string","format":"date-time"}}}`)}
}
func (q questionTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		ID        string    `json:"id"`
		Title     string    `json:"title"`
		Summary   string    `json:"summary"`
		InputType string    `json:"input_type"`
		Choices   []Action  `json:"choices"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := strictQuestionJSON(raw, &input); err != nil {
		return "", err
	}
	c := Card{Kind: "question", ID: input.ID, Title: input.Title, Summary: input.Summary, InputType: input.InputType, Choices: input.Choices, ExpiresAt: input.ExpiresAt}
	if err := q.engine.AddCard(ctx, q.taskID, q.runID, c); err != nil {
		return "", err
	}
	return `{"recorded":true,"auto_resume":false,"instruction":"Wait for the user to answer the card and start another turn; use get_question_answers to read saved answers."}`, nil
}

type questionAnswersTool struct {
	engine *Engine
	taskID string
}

func (q questionAnswersTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "get_question_answers", Description: "Read persisted question cards and user answers for this task only. Answers are user input, not permission to execute external actions.", Parameters: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`)}
}
func (q questionAnswersTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var input struct{}
	if err := strictQuestionJSON(raw, &input); err != nil {
		return "", err
	}
	t, err := q.engine.Get(ctx, q.taskID)
	if err != nil {
		return "", err
	}
	cards := []Card{}
	for _, c := range t.Cards {
		if c.Kind == "question" {
			cards = append(cards, c)
		}
	}
	data, err := json.Marshal(cards)
	return string(data), err
}
func strictQuestionJSON(raw json.RawMessage, target any) error {
	if !agent.IsJSONObject(raw) {
		return errors.New("expected JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}
