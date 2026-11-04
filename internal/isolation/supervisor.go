package isolation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

var ErrBusy = errors.New("isolated worker capacity reached")
var ErrQuarantined = errors.New("runner quarantined after cleanup failure")

var ErrUnavailable = errors.New("isolated runtime unavailable")
var ErrOutputLimit = errors.New("worker output limit exceeded")

// Evidence must be measured by the trusted runtime after provisioning and
// before untrusted execution. Flags alone are not evidence. This structure is
// deliberately NOT an event the worker can submit. Limits must be effective,
// including filesystem quotas and cgroup delegation, not requested values.
type Evidence struct {
	Image                                                             string
	AllocationID                                                      string
	Profile                                                           string
	RunnerUID                                                         int
	DaemonUID                                                         int
	Limits                                                            Limits
	Rootless, SeparateIdentity, PrivateNamespaces, NetworkDenied      bool
	ReadOnlyRoot, TaskOnlyStorage, NoHostSockets, CleanEnvironment    bool
	NonRoot, NoCapabilities, NoNewPrivileges, Seccomp, HostLSM        bool
	NoInheritedDescriptors, TreeTermination, Quotas, EffectiveCgroups bool
}

func (e Evidence) verify(p Plan) error {
	if e.Image != p.Image || e.AllocationID != p.AllocationID || e.Profile != p.Profile || e.RunnerUID != p.RunnerUID || e.DaemonUID != p.DaemonUID || e.Limits != p.Limits || !e.Rootless || !e.SeparateIdentity || !e.PrivateNamespaces || !e.NetworkDenied || !e.ReadOnlyRoot || !e.TaskOnlyStorage || !e.NoHostSockets || !e.CleanEnvironment || !e.NonRoot || !e.NoCapabilities || !e.NoNewPrivileges || !e.Seccomp || !e.HostLSM || !e.NoInheritedDescriptors || !e.TreeTermination || !e.Quotas || !e.EffectiveCgroups {
		return errors.New("isolation prerequisites absent or ineffective")
	}
	return nil
}

// Runtime is a trusted runner-side adapter. Provision creates a stopped sandbox
// without running image hooks or other untrusted code. Inspect measures it.
// Run starts only that allocation and blocks until the complete workload ends;
// it MUST honor context cancellation by terminating every descendant. Destroy
// is idempotent, kills remaining processes, revokes capabilities, and removes
// storage, including after partially failed Provision. No method may fall back
// to host subprocess execution. All methods must honor context deadlines.
// No implementation is supplied: production execution remains disabled.
type Runtime interface {
	Provision(context.Context, Plan) error
	Inspect(context.Context, string) (Evidence, error)
	Run(context.Context, string, io.Writer) error
	Destroy(context.Context, string) error
}

type Supervisor struct {
	config      Config
	runtime     Runtime
	slots       chan struct{}
	quarantined atomic.Bool
}

func New(c Config, runtime Runtime) (*Supervisor, error) {
	if c.MaxConcurrent < 1 || c.MaxConcurrent > 64 {
		return nil, errors.New("explicit worker concurrency limit required (1..64)")
	}
	if runtime == nil {
		return nil, ErrUnavailable
	}
	if err := c.Limits.validate(); err != nil {
		return nil, err
	}
	images := make(map[string]string, len(c.Images))
	for k, v := range c.Images {
		images[k] = v
	}
	c.Images = images
	// Validate config for every configured agent, even if never requested.
	if len(images) == 0 {
		return nil, errors.New("no reviewed worker images configured")
	}
	for agent := range images {
		if _, err := Compile(c, Request{TaskID: "validation", Agent: agent, Prompt: "validate"}); err != nil {
			return nil, err
		}
	}
	return &Supervisor{config: c, runtime: runtime, slots: make(chan struct{}, c.MaxConcurrent)}, nil
}

// Run captures bounded, untrusted text only. Output is never parsed as approval
// or a control message. Callers must still render it safely and authorize the
// TaskID over an authenticated transport before invoking this method.
func (s *Supervisor) Run(parent context.Context, req Request) (output string, err error) {
	if s.quarantined.Load() {
		return "", ErrQuarantined
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return "", ErrBusy
	}
	if s.quarantined.Load() {
		return "", ErrQuarantined
	}
	plan, err := Compile(s.config, req)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(parent, plan.Limits.WallTime)
	defer cancel()
	if err = ctx.Err(); err != nil {
		return "", err
	}
	// Cleanup uses a fresh bounded context even when the job context expired.
	defer func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if cleanupErr := s.runtime.Destroy(cleanupCtx, plan.AllocationID); cleanupErr != nil {
			s.quarantined.Store(true)
			err = errors.Join(err, fmt.Errorf("sandbox cleanup failed: %w", cleanupErr))
		}
	}()
	if err = s.runtime.Provision(ctx, plan); err != nil {
		return "", err
	}
	evidence, err := s.runtime.Inspect(ctx, plan.AllocationID)
	if err != nil {
		return "", err
	}
	if err = evidence.verify(plan); err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	// This check is the launch admission point. Jobs that passed it before a
	// concurrent cleanup failure retain their cancellation/cleanup lifecycle.
	if s.quarantined.Load() {
		return "", ErrQuarantined
	}
	sink := &boundedOutput{limit: plan.Limits.OutputBytes, cancel: cancel}
	err = s.runtime.Run(ctx, plan.AllocationID, sink)
	output, overflow := sink.result()
	if overflow {
		err = errors.Join(err, ErrOutputLimit)
	} else if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	return output, err
}

type boundedOutput struct {
	mu       sync.Mutex
	data     bytes.Buffer
	limit    int64
	overflow bool
	cancel   context.CancelFunc
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - int64(b.data.Len())
	if int64(len(p)) > remaining {
		n, _ := b.data.Write(p[:int(remaining)])
		b.overflow = true
		b.cancel()
		return n, ErrOutputLimit
	}
	return b.data.Write(p)
}
func (b *boundedOutput) result() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String(), b.overflow
}
