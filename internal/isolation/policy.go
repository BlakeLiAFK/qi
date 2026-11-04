// Package isolation defines the trusted, fail-closed worker policy boundary.
// It has no production runtime adapter and is not connected to public execution.
// A Runtime implementation is trusted security code, not a worker plugin.
package isolation

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxRequestBytes = 64 * 1024
const Profile = "qi-offline-worker-v1"

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
var imageDigest = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]{1,5})?(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*@sha256:[a-f0-9]{64}$`)

// Request is the entire untrusted launch surface. It cannot name a host path,
// environment variable, image, executable, mount, credential, or runtime flag.
type Request struct {
	TaskID string `json:"task_id"`
	Agent  string `json:"agent"`
	Prompt string `json:"prompt"`
}

// DecodeRequest rejects oversized frames, duplicate/unknown keys, null values,
// invalid UTF-8 and trailing data. Transport authentication is a separate duty.
func DecodeRequest(r io.Reader) (Request, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxRequestBytes+1))
	if err != nil {
		return Request{}, err
	}
	if len(b) > MaxRequestBytes || !utf8.Valid(b) {
		return Request{}, errors.New("invalid request frame")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return Request{}, errors.New("request must be an object")
	}
	fields := map[string]string{}
	for dec.More() {
		tok, err = dec.Token()
		if err != nil {
			return Request{}, err
		}
		key, ok := tok.(string)
		if !ok {
			return Request{}, errors.New("invalid request key")
		}
		if key != "task_id" && key != "agent" && key != "prompt" {
			return Request{}, fmt.Errorf("unknown request field %q", key)
		}
		if _, ok = fields[key]; ok {
			return Request{}, errors.New("duplicate request field")
		}
		tok, err = dec.Token()
		if err != nil {
			return Request{}, err
		}
		value, ok := tok.(string)
		if !ok {
			return Request{}, errors.New("request values must be strings")
		}
		fields[key] = value
	}
	if _, err = dec.Token(); err != nil {
		return Request{}, err
	}
	if _, err = dec.Token(); err != io.EOF {
		return Request{}, errors.New("trailing request data")
	}
	req := Request{TaskID: fields["task_id"], Agent: fields["agent"], Prompt: fields["prompt"]}
	return req, validateRequest(req)
}

func validateRequest(r Request) error {
	if !identifier.MatchString(r.TaskID) {
		return errors.New("invalid task ID")
	}
	if r.Agent != "codex" && r.Agent != "claude" {
		return errors.New("unsupported agent")
	}
	if !utf8.ValidString(r.Prompt) || strings.TrimSpace(r.Prompt) == "" || len(r.Prompt) > 48*1024 || strings.ContainsRune(r.Prompt, 0) {
		return errors.New("invalid prompt")
	}
	return nil
}

// Limits are mandatory trusted policy, never supplied by the task. DiskBytes
// bounds the workspace; ScratchBytes bounds all writable home/tmp storage.
type Limits struct {
	MemoryBytes  int64
	DiskBytes    int64
	ScratchBytes int64
	OutputBytes  int64
	PIDs         int
	CPUMilli     int
	WallTime     time.Duration
}

func (l Limits) validate() error {
	if l.MemoryBytes < 16<<20 || l.MemoryBytes > 64<<30 || l.DiskBytes < 1<<20 || l.DiskBytes > 1<<40 || l.ScratchBytes < 1<<20 || l.ScratchBytes > 1<<30 || l.OutputBytes < 1 || l.OutputBytes > 16<<20 || l.PIDs < 1 || l.PIDs > 4096 || l.CPUMilli < 1 || l.CPUMilli > 64000 || l.WallTime < time.Millisecond || l.WallTime > 24*time.Hour {
		return errors.New("missing or out-of-range isolation limits")
	}
	return nil
}

// Config comes only from the trusted launcher, not request JSON. Images must
// already be installed and reviewed. Automatic image pulls are forbidden.
type Config struct {
	MaxConcurrent int
	Images        map[string]string
	DaemonUID     int
	RunnerUID     int
	Limits        Limits
}

// Plan describes the only supported profile. Profile requires read-only root,
// private PID/mount/IPC/network namespaces, network=none, non-root container UID,
// dropped capabilities, no-new-privileges, seccomp and host LSM, no inherited
// descriptors/environment, and fresh quota-bounded task-only volumes. It allows
// no host bind mounts or sockets. Runtime must implement ALL of that contract.
// AllocationID names fresh runtime-owned storage, never a caller-selected path.
// Prompt must go over stdin/protocol data, never a shell command or host argv.
type Plan struct {
	Profile      string
	AllocationID string
	TaskID       string
	Agent        string
	Image        string
	Prompt       string
	DaemonUID    int
	RunnerUID    int
	Limits       Limits
}

func Compile(c Config, r Request) (Plan, error) {
	if err := validateRequest(r); err != nil {
		return Plan{}, err
	}
	if err := c.Limits.validate(); err != nil {
		return Plan{}, err
	}
	if c.RunnerUID <= 0 || c.DaemonUID <= 0 || c.RunnerUID == c.DaemonUID {
		return Plan{}, errors.New("distinct non-root runner and daemon identities required")
	}
	image := c.Images[r.Agent]
	if len(image) > 512 || !imageDigest.MatchString(image) {
		return Plan{}, errors.New("reviewed image digest required")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Plan{}, err
	}
	return Plan{Profile: Profile, AllocationID: "qi-" + hex.EncodeToString(nonce[:]), TaskID: r.TaskID, Agent: r.Agent, Image: image, Prompt: r.Prompt, DaemonUID: c.DaemonUID, RunnerUID: c.RunnerUID, Limits: c.Limits}, nil
}
