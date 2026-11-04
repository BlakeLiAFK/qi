package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gopi/internal/skills"
	"gopi/internal/tasks"
)

type daemonClient struct {
	origin, token string
	http          *http.Client
}

var errApprovalPending = errors.New("task is waiting for approval in the WebUI; work remains pending")

var taskIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func newDaemonClient(origin, token string) (*daemonClient, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("url must be an exact HTTP(S) origin without credentials, path, or trailing slash")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return nil, errors.New("HTTPS is required except for literal loopback addresses")
	}
	if len(token) < 32 || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("set QI_WEB_TOKEN to the daemon token (at least 32 characters)")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &daemonClient{origin: origin, token: token, http: &http.Client{Transport: transport, Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *daemonClient) request(ctx context.Context, method, path string, payload, result any) error {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin+path, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth("qi", c.token)
	if method != "GET" {
		req.Header.Set("Origin", c.origin)
		req.Header.Set("X-Qi-Request", "1")
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("daemon HTTP %d (%s)", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		return err
	}
	if len(b) > 16<<20 {
		return errors.New("daemon response exceeds 16 MiB")
	}
	if result != nil {
		return json.Unmarshal(b, result)
	}
	return nil
}
func (c *daemonClient) get(ctx context.Context, id string) (tasks.Task, error) {
	var task tasks.Task
	err := c.request(ctx, "GET", "/api/tasks/"+id, nil, &task)
	return task, err
}

func runAttach(ctx context.Context, args []string, in io.Reader, out, diagnostics io.Writer, env func(string) string) error {
	fs := flag.NewFlagSet("qi attach", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	fs.Usage = func() {
		fmt.Fprintln(diagnostics, "Usage: qi attach [flags]\nConnect to the same daemon tasks as the WebUI. Auth: QI_WEB_TOKEN environment only.\nCommands: /history, /wait, /cancel, /skills, /use-skills names, /quit. Use the WebUI for approval decisions.\nQuitting or Ctrl-C does not cancel daemon work; use /cancel explicitly.")
		fs.PrintDefaults()
	}
	origin := fs.String("url", "http://127.0.0.1:8686", "exact daemon origin, without trailing slash")
	id := fs.String("task", "", "existing task ID (otherwise creates a task)")
	title := fs.String("title", "Terminal task", "title when creating a task")
	prompt := fs.String("p", "", "one-shot prompt; otherwise interactive")
	skillSelection := fs.String("skills", "", "comma-separated skill names for -p only; untrusted references, no tool permission")
	list := fs.Bool("list", false, "list daemon tasks and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *skillSelection != "" && *prompt == "" {
		return errors.New("-skills requires -p; in interactive mode use /use-skills for the next message")
	}
	selected := selectedSkillNames(*skillSelection)
	if *id != "" && !taskIDPattern.MatchString(*id) {
		return errors.New("invalid task ID")
	}
	if *list && (*id != "" || *prompt != "") {
		return errors.New("-list cannot be combined with -task or -p")
	}
	c, err := newDaemonClient(*origin, env("QI_WEB_TOKEN"))
	if err != nil {
		return err
	}
	defer c.http.CloseIdleConnections()
	if *list {
		var items []tasks.Task
		if err = c.request(ctx, "GET", "/api/tasks", nil, &items); err != nil {
			return err
		}
		for _, t := range items {
			fmt.Fprintf(out, "%s\t%s\t%q\n", t.ID, t.Status, t.Title)
		}
		return nil
	}
	var task tasks.Task
	if *id == "" {
		err = c.request(ctx, "POST", "/api/tasks", map[string]string{"title": *title}, &task)
		if err != nil {
			return err
		}
		*id = task.ID
		if !taskIDPattern.MatchString(*id) {
			return errors.New("daemon returned invalid task ID")
		}
	} else {
		task, err = c.get(ctx, *id)
		if err != nil {
			return err
		}
	}
	fmt.Fprintf(diagnostics, "Task %s (%q) at %s\n", *id, task.Title, *origin)
	lastNoticeRun := ""
	showNotice := func(t tasks.Task) {
		if n := t.ContextNotice; n != nil && n.DroppedMessages > 0 && n.RunID != lastNoticeRun {
			fmt.Fprintf(diagnostics, "Saved context notice (run %s): %d earlier messages, including their user instructions, were omitted from the model input (%d bytes). Full task history is unchanged.\n", n.RunID, n.DroppedMessages, n.InputBytes)
			lastNoticeRun = n.RunID
		}
	}
	showNotice(task)

	show := func(task tasks.Task) {
		for _, m := range task.Messages {
			if m.Role == "assistant" && m.Content != "" {
				fmt.Fprintln(out, m.Content)
			} else if m.Role == "user" {
				fmt.Fprintf(out, "You: %s\n", m.Content)
			}
		}
	}
	wait := func() error {
		for {
			t, err := c.get(ctx, *id)
			if err != nil {
				return err
			}
			showNotice(t)
			if t.Status == "running" {
				var approvals []tasks.Approval
				if err := c.request(ctx, "GET", "/api/tasks/"+*id+"/approvals", nil, &approvals); err != nil {
					return err
				}
				for _, approval := range approvals {
					if approval.Status == "pending" && approval.ExpiresAt.After(time.Now()) {
						fmt.Fprintf(diagnostics, "Pending approval %q for %q. Review the full action in the WebUI, then use /wait.\n", approval.Action.Kind, approval.Action.Target)
						return errApprovalPending
					}
				}
			}
			if t.Status != "running" {
				if len(t.Messages) > 0 {
					last := t.Messages[len(t.Messages)-1]
					if last.Role == "assistant" && last.Content != "" {
						fmt.Fprintln(out, last.Content)
					}
				}
				if t.Status == "failed" || t.Status == "cancelled" || t.Status == "interrupted" {
					return fmt.Errorf("task turn %s; inspect task in WebUI", t.Status)
				}
				return nil
			}
			timer := time.NewTimer(300 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	send := func(text string) error {
		if err := c.request(ctx, "POST", "/api/tasks/"+*id+"/messages", map[string]any{"prompt": text, "skills": selected}, nil); err != nil {
			return err
		}
		selected = nil // Explicit selection applies only to this accepted turn.
		return wait()
	}
	if *prompt != "" {
		return send(*prompt)
	}
	show(task)
	scanner := newInputSession(ctx, in)
	defer scanner.close()
	fmt.Fprintln(diagnostics, "Attached. /history, /wait, /cancel, /skills, /use-skills names, /quit. Daemon work continues when you disconnect.")
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Fprint(diagnostics, "> ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		switch line {
		case "/quit":
			return nil
		case "/history":
			t, err := c.get(ctx, *id)
			if err == nil {
				show(t)
			} else {
				fmt.Fprintln(diagnostics, err)
			}
		case "/wait":
			if err := wait(); err != nil {
				fmt.Fprintln(diagnostics, err)
			}
		case "/skills":
			var available []skills.Metadata
			if err := c.request(ctx, "GET", "/api/skills", nil, &available); err != nil {
				fmt.Fprintln(diagnostics, err)
			} else {
				for _, skill := range available {
					fmt.Fprintf(out, "%s\t%q\n", skill.Name, skill.Description)
				}
			}
		case "/use-skills":
			selected = nil
			fmt.Fprintln(diagnostics, "Skill selection cleared.")
		case "/cancel":
			if err := c.request(ctx, "POST", "/api/tasks/"+*id+"/cancel", struct{}{}, nil); err != nil {
				fmt.Fprintln(diagnostics, err)
			} else {
				fmt.Fprintln(diagnostics, "Cancellation requested.")
			}
		default:
			if strings.HasPrefix(line, "/use-skills ") {
				selected = selectedSkillNames(strings.TrimPrefix(line, "/use-skills "))
				fmt.Fprintf(diagnostics, "Selected for the next message only: %q. Skill text is untrusted and grants no tool permissions.\n", selected)
				continue
			}
			if strings.HasPrefix(line, "/") {
				fmt.Fprintln(diagnostics, "Unknown command. Use /history, /wait, /cancel, /skills, /use-skills names, or /quit.")
				continue
			}
			if err := send(line); err != nil {
				fmt.Fprintln(diagnostics, err)
			}
		}
	}
}
