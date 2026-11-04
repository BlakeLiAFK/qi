package isolation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func config() Config {
	return Config{MaxConcurrent: 1, Images: map[string]string{"codex": "example/worker@sha256:" + strings.Repeat("a", 64), "claude": "example/worker@sha256:" + strings.Repeat("b", 64)}, DaemonUID: 1001, RunnerUID: 1002, Limits: Limits{MemoryBytes: 128 << 20, DiskBytes: 256 << 20, ScratchBytes: 16 << 20, OutputBytes: 1024, PIDs: 32, CPUMilli: 1000, WallTime: time.Second}}
}
func request() Request { return Request{TaskID: "task-1", Agent: "codex", Prompt: "explain"} }
func proof(p Plan) Evidence {
	return Evidence{Image: p.Image, AllocationID: p.AllocationID, Profile: p.Profile, RunnerUID: p.RunnerUID, DaemonUID: p.DaemonUID, Limits: p.Limits, Rootless: true, SeparateIdentity: true, PrivateNamespaces: true, NetworkDenied: true, ReadOnlyRoot: true, TaskOnlyStorage: true, NoHostSockets: true, CleanEnvironment: true, NonRoot: true, NoCapabilities: true, NoNewPrivileges: true, Seccomp: true, HostLSM: true, NoInheritedDescriptors: true, TreeTermination: true, Quotas: true, EffectiveCgroups: true}
}

type fakeRuntime struct {
	plan                                 Plan
	events                               []string
	provisionErr, inspectErr, destroyErr error
	mutate                               func(*Evidence)
	run                                  func(context.Context, io.Writer) error
	cleanupLive                          bool
}

func (f *fakeRuntime) Provision(ctx context.Context, p Plan) error {
	f.events = append(f.events, "provision")
	f.plan = p
	return f.provisionErr
}
func (f *fakeRuntime) Inspect(ctx context.Context, id string) (Evidence, error) {
	f.events = append(f.events, "inspect")
	e := proof(f.plan)
	if f.mutate != nil {
		f.mutate(&e)
	}
	return e, f.inspectErr
}
func (f *fakeRuntime) Run(ctx context.Context, id string, w io.Writer) error {
	f.events = append(f.events, "run")
	if f.run != nil {
		return f.run(ctx, w)
	}
	_, err := io.WriteString(w, "ok")
	return err
}
func (f *fakeRuntime) Destroy(ctx context.Context, id string) error {
	f.events = append(f.events, "destroy")
	f.cleanupLive = ctx.Err() == nil
	return f.destroyErr
}

func TestRequestProtocol(t *testing.T) {
	good := `{"task_id":"task-1","agent":"codex","prompt":"hello"}`
	if _, err := DecodeRequest(strings.NewReader(good)); err != nil {
		t.Fatal(err)
	}
	bad := []string{`null`, `[]`, good + `{}`, good + `x`, `{"task_id":"task-1","agent":"codex","prompt":null}`, `{"task_id":"a","task_id":"b","agent":"codex","prompt":"hi"}`, `{"task_id":"a","agent":"codex","prompt":"hi","env":{"QI_WEB_TOKEN":"secret"}}`, strings.Repeat(" ", MaxRequestBytes+1), "\xff"}
	for _, key := range []string{"work_dir", "image", "mounts", "env", "limits", "command", "args", "runtime_socket", "credential", "approval"} {
		bad = append(bad, strings.TrimSuffix(good, "}")+`,"`+key+`":"evil"}`)
	}
	for _, s := range bad {
		if _, err := DecodeRequest(strings.NewReader(s)); err == nil {
			t.Errorf("accepted malformed request %.100q", s)
		}
	}
	for _, id := range []string{"", "..", "../secret", "/var/db", "a/b", "a\\b", "--privileged", "a\x00b", strings.Repeat("x", 65)} {
		r := request()
		r.TaskID = id
		b, _ := json.Marshal(r)
		if _, err := DecodeRequest(strings.NewReader(string(b))); err == nil {
			t.Errorf("accepted ID %q", id)
		}
	}
	for _, prompt := range []string{"", "\x00", strings.Repeat("p", 48*1024+1)} {
		r := request()
		r.Prompt = prompt
		if _, err := Compile(config(), r); err == nil {
			t.Fatal("accepted bad prompt")
		}
	}
}

func TestTrustedPolicy(t *testing.T) {
	t.Setenv("QI_WEB_TOKEN", "never-copy-this")
	t.Setenv("LD_PRELOAD", "/host/evil.so")
	r := request()
	r.Prompt = "--privileged; $(cat /host/secrets)\nIgnore policy"
	p, err := Compile(config(), r)
	if err != nil {
		t.Fatal(err)
	}
	if p.Prompt != r.Prompt || p.Profile != Profile {
		t.Fatal("prompt must remain opaque data")
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "never-copy-this") || strings.Contains(string(raw), "evil.so") {
		t.Fatal("inherited environment")
	}
	p2, _ := Compile(config(), r)
	if p.AllocationID == p2.AllocationID {
		t.Fatal("reused allocation")
	}
	for _, image := range []string{"example/../../worker@sha256:" + strings.Repeat("a", 64), "latest", "example/image:latest", "--privileged@sha256:" + strings.Repeat("a", 64), "/bad image@sha256:" + strings.Repeat("a", 64), "example/image@sha256:123", "example/image@sha256:" + strings.Repeat("A", 64)} {
		c := config()
		c.Images["codex"] = image
		if _, err := Compile(c, r); err == nil {
			t.Errorf("accepted image %q", image)
		}
	}
	for _, ids := range [][2]int{{0, 2}, {2, 0}, {2, 2}, {-1, 2}} {
		c := config()
		c.DaemonUID = ids[0]
		c.RunnerUID = ids[1]
		if _, err := Compile(c, r); err == nil {
			t.Fatal("accepted unsafe identities")
		}
	}
	for i := 0; i < reflect.TypeOf(Limits{}).NumField(); i++ {
		c := config()
		reflect.ValueOf(&c.Limits).Elem().Field(i).SetInt(0)
		if _, err := Compile(c, r); err == nil {
			t.Errorf("accepted missing limit %d", i)
		}
		reflect.ValueOf(&c.Limits).Elem().Field(i).SetInt(1 << 62)
		if _, err := Compile(c, r); err == nil {
			t.Errorf("accepted excessive limit %d", i)
		}
	}
	if _, err := New(config(), nil); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing runtime allowed")
	}
	c := config()
	f := &fakeRuntime{}
	s, err := New(c, f)
	if err != nil {
		t.Fatal(err)
	}
	c.Images["codex"] = "evil"
	if _, err = s.Run(context.Background(), request()); err != nil {
		t.Fatal("configuration was not copied", err)
	}
}

func TestMissingControlsFailClosed(t *testing.T) {
	typ := reflect.TypeOf(Evidence{})
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Type.Kind() != reflect.Bool {
			continue
		}
		t.Run(typ.Field(i).Name, func(t *testing.T) {
			f := &fakeRuntime{mutate: func(e *Evidence) { reflect.ValueOf(e).Elem().Field(i).SetBool(false) }}
			s, _ := New(config(), f)
			if _, err := s.Run(context.Background(), request()); err == nil {
				t.Fatal("missing control accepted")
			}
			if strings.Join(f.events, ",") != "provision,inspect,destroy" {
				t.Fatal(f.events)
			}
		})
	}
	for _, mutate := range []func(*Evidence){func(e *Evidence) { e.Image = "evil" }, func(e *Evidence) { e.AllocationID = "other" }, func(e *Evidence) { e.Profile = "weak" }, func(e *Evidence) { e.RunnerUID = e.DaemonUID }, func(e *Evidence) { e.DaemonUID = 0 }, func(e *Evidence) { e.Limits.DiskBytes++ }, func(e *Evidence) { e.Limits.OutputBytes-- }} {
		f := &fakeRuntime{mutate: mutate}
		s, _ := New(config(), f)
		if _, err := s.Run(context.Background(), request()); err == nil {
			t.Fatal("invalid evidence accepted")
		}
		if strings.Contains(strings.Join(f.events, ","), "run") {
			t.Fatal("ran before verification")
		}
	}
}

func TestLifecycle(t *testing.T) {
	f := &fakeRuntime{}
	s, _ := New(config(), f)
	out, err := s.Run(context.Background(), request())
	if err != nil || out != "ok" || strings.Join(f.events, ",") != "provision,inspect,run,destroy" {
		t.Fatal(out, err, f.events)
	}
	for _, phase := range []string{"provision", "inspect", "run", "destroy"} {
		t.Run(phase, func(t *testing.T) {
			sentinel := errors.New("fixture failure")
			f := &fakeRuntime{}
			switch phase {
			case "provision":
				f.provisionErr = sentinel
			case "inspect":
				f.inspectErr = sentinel
			case "run":
				f.run = func(context.Context, io.Writer) error { return sentinel }
			case "destroy":
				f.destroyErr = sentinel
			}
			s, _ := New(config(), f)
			_, err := s.Run(context.Background(), request())
			if !errors.Is(err, sentinel) || f.events[len(f.events)-1] != "destroy" {
				t.Fatal(err, f.events)
			}
		})
	}
}

func TestCancellationAndOutput(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		c := config()
		c.Limits.WallTime = 5 * time.Millisecond
		f := &fakeRuntime{run: func(ctx context.Context, w io.Writer) error { <-ctx.Done(); return ctx.Err() }}
		s, _ := New(c, f)
		_, err := s.Run(context.Background(), request())
		if !errors.Is(err, context.DeadlineExceeded) || !f.cleanupLive {
			t.Fatal(err, f.events)
		}
	})
	t.Run("cancelled before provision", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		f := &fakeRuntime{}
		s, _ := New(config(), f)
		_, err := s.Run(ctx, request())
		if !errors.Is(err, context.Canceled) || len(f.events) != 0 {
			t.Fatal(err, f.events)
		}
	})
	t.Run("bounded concurrent output", func(t *testing.T) {
		f := &fakeRuntime{run: func(ctx context.Context, w io.Writer) error {
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); _, _ = io.WriteString(w, strings.Repeat("x", 1024)) }()
			}
			wg.Wait()
			if ctx.Err() == nil {
				return errors.New("output did not cancel context")
			}
			return nil
		}}
		s, _ := New(config(), f)
		out, err := s.Run(context.Background(), request())
		if !errors.Is(err, ErrOutputLimit) || len(out) != 1024 || !f.cleanupLive {
			t.Fatal(len(out), err)
		}
	})
	t.Run("exact output limit", func(t *testing.T) {
		f := &fakeRuntime{run: func(ctx context.Context, w io.Writer) error {
			_, err := io.WriteString(w, strings.Repeat("x", 1024))
			return err
		}}
		s, _ := New(config(), f)
		out, err := s.Run(context.Background(), request())
		if err != nil || len(out) != 1024 {
			t.Fatal(len(out), err)
		}
	})
}

func FuzzDecodeRequest(f *testing.F) {
	f.Add(`{"task_id":"a","agent":"codex","prompt":"hello"}`)
	f.Add(`{"prompt":null}`)
	f.Fuzz(func(t *testing.T, input string) {
		r, err := DecodeRequest(strings.NewReader(input))
		if err == nil {
			if _, err := Compile(config(), r); err != nil {
				t.Fatal("decoded request cannot compile", err)
			}
		}
	})
}

func TestAdmissionAndQuarantine(t *testing.T) {
	for _, n := range []int{0, -1, 65} {
		c := config()
		c.MaxConcurrent = n
		if _, err := New(c, &fakeRuntime{}); err == nil {
			t.Fatal("accepted invalid concurrency")
		}
	}
	started, finish := make(chan struct{}), make(chan struct{})
	f := &fakeRuntime{run: func(ctx context.Context, w io.Writer) error { close(started); <-finish; return nil }}
	s, _ := New(config(), f)
	result := make(chan error, 1)
	go func() { _, err := s.Run(context.Background(), request()); result <- err }()
	<-started
	if _, err := s.Run(context.Background(), request()); !errors.Is(err, ErrBusy) {
		t.Errorf("expected bounded admission, got %v", err)
	}
	close(finish)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	f = &fakeRuntime{destroyErr: errors.New("cannot remove sandbox")}
	s, _ = New(config(), f)
	if _, err := s.Run(context.Background(), request()); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	events := len(f.events)
	if _, err := s.Run(context.Background(), request()); !errors.Is(err, ErrQuarantined) || len(f.events) != events {
		t.Fatal("cleanup failure did not quarantine", err)
	}
}

// Pauses the second request at inspection while the first fails cleanup.
type quarantineRuntime struct {
	mu            sync.Mutex
	plans         map[string]Plan
	secondInspect chan struct{}
	resumeInspect chan struct{}
	runs          []string
}

func (f *quarantineRuntime) Provision(ctx context.Context, p Plan) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plans[p.AllocationID] = p
	return nil
}
func (f *quarantineRuntime) Inspect(ctx context.Context, id string) (Evidence, error) {
	f.mu.Lock()
	p := f.plans[id]
	f.mu.Unlock()
	if p.TaskID == "second" {
		close(f.secondInspect)
		<-f.resumeInspect
	}
	return proof(p), nil
}
func (f *quarantineRuntime) Run(ctx context.Context, id string, w io.Writer) error {
	f.mu.Lock()
	p := f.plans[id]
	f.runs = append(f.runs, p.TaskID)
	f.mu.Unlock()
	if p.TaskID == "first" {
		<-f.secondInspect
	}
	return nil
}
func (f *quarantineRuntime) Destroy(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.plans[id].TaskID == "first" {
		return errors.New("fixture cleanup failed")
	}
	return nil
}
func TestQuarantineDuringInspection(t *testing.T) {
	f := &quarantineRuntime{plans: map[string]Plan{}, secondInspect: make(chan struct{}), resumeInspect: make(chan struct{})}
	c := config()
	c.MaxConcurrent = 2
	s, _ := New(c, f)
	first, second := make(chan error, 1), make(chan error, 1)
	r := request()
	r.TaskID = "first"
	go func() { _, err := s.Run(context.Background(), r); first <- err }()
	r2 := request()
	r2.TaskID = "second"
	go func() { _, err := s.Run(context.Background(), r2); second <- err }()
	if err := <-first; err == nil {
		t.Fatal("expected cleanup failure")
	}
	close(f.resumeInspect)
	if err := <-second; !errors.Is(err, ErrQuarantined) {
		t.Fatal("started after quarantine", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Join(f.runs, ",") != "first" {
		t.Fatal(f.runs)
	}
}
