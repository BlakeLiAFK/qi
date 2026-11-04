// Package artifacts exposes explicitly registered task deliverables, never arbitrary files.
package artifacts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const MaxBytes int64 = 32 << 20
const MaxPerTask = 100
const MaxConcurrentReads = 4

var readSlots = make(chan struct{}, MaxConcurrentReads)
var ErrBusy = errors.New("artifact capacity busy; retry later")
var ErrQuota = errors.New("task artifact limit reached (100)")

// AcquireRead rejects excess concurrent work instead of accumulating waiters.
// HTTP callers hold a separate response slot until the body has been written.
func AcquireRead() (func(), error) {
	select {
	case readSlots <- struct{}{}:
		return func() { <-readSlots }, nil
	default:
		return nil, ErrBusy
	}
}

var ErrUnavailable = errors.New("artifact unavailable or changed")

type Artifact struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	Name      string    `json:"name"`
	MediaType string    `json:"media_type"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
	Path      string    `json:"-"`
}
type Registry interface {
	SaveArtifact(context.Context, Artifact) (Artifact, error)
	ListArtifacts(context.Context, string) ([]Artifact, error)
	GetArtifact(context.Context, string, string) (Artifact, error)
}
type Service struct {
	root     string
	registry Registry
}

func New(root string, registry Registry) (*Service, error) {
	if root == "" || registry == nil {
		return nil, errors.New("artifact directory and registry required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	f, err := openSecure(absolute, "")
	if err != nil {
		return nil, errors.New("artifact directory must exist and contain no symlinks")
	}
	defer f.Close()
	return &Service{root: absolute, registry: registry}, nil
}
func validSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00")
}
func validPath(p string) bool {
	for _, r := range p {
		if r < 32 || r == 127 {
			return false
		}
	}
	if p == "" || len(p) > 1024 || path.IsAbs(p) || path.Clean(p) != p || strings.ContainsAny(p, "\\\x00") {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if !validSegment(s) || strings.HasPrefix(s, ".") {
			return false
		}
	}
	return true
}
func (s *Service) read(task, p string) ([]byte, error) {
	if !validSegment(task) || !validPath(p) {
		return nil, ErrUnavailable
	}
	f, err := openSecure(s.root, task+"/"+p)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxBytes {
		return nil, ErrUnavailable
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil || int64(len(b)) > MaxBytes {
		return nil, ErrUnavailable
	}
	return b, nil
}

// Inspect verifies a staged file without publishing it. The returned metadata has
// no registration ID; it is suitable for binding an exact approval.
func (s *Service) Inspect(ctx context.Context, task, p string) (Artifact, error) {
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	release, err := AcquireRead()
	if err != nil {
		return Artifact{}, err
	}
	defer release()
	b, err := s.read(task, p)
	if err != nil {
		return Artifact{}, err
	}
	hash := sha256.Sum256(b)
	return Artifact{TaskID: task, Name: path.Base(p), Path: p, Size: int64(len(b)), MediaType: http.DetectContentType(b), SHA256: hex.EncodeToString(hash[:])}, nil
}
func (s *Service) Register(ctx context.Context, task, p string) (Artifact, error) {
	return s.register(ctx, task, p, nil)
}

// RegisterVerified only publishes the bytes approved by the user. Downloads
// independently verify the same hash, including changes after registration.
func (s *Service) RegisterVerified(ctx context.Context, task, p string, expected Artifact) (Artifact, error) {
	return s.register(ctx, task, p, &expected)
}
func (s *Service) register(ctx context.Context, task, p string, expected *Artifact) (Artifact, error) {
	a, err := s.Inspect(ctx, task, p)
	if err != nil {
		return Artifact{}, err
	}
	if expected != nil && (expected.TaskID != task || expected.Path != p || expected.Size != a.Size || expected.SHA256 != a.SHA256) {
		return Artifact{}, ErrUnavailable
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return Artifact{}, err
	}
	a.ID, a.CreatedAt = hex.EncodeToString(id[:]), time.Now().UTC()
	return s.registry.SaveArtifact(ctx, a)
}
func (s *Service) List(ctx context.Context, task string) ([]Artifact, error) {
	return s.registry.ListArtifacts(ctx, task)
}
func (s *Service) Download(ctx context.Context, task, id string) (Artifact, []byte, error) {
	release, err := AcquireRead()
	if err != nil {
		return Artifact{}, nil, err
	}
	defer release()
	a, err := s.registry.GetArtifact(ctx, task, id)
	if err != nil {
		return Artifact{}, nil, ErrUnavailable
	}
	if a.TaskID != task {
		return Artifact{}, nil, ErrUnavailable
	}
	b, err := s.read(task, a.Path)
	if err != nil {
		return Artifact{}, nil, ErrUnavailable
	}
	hash := sha256.Sum256(b)
	if int64(len(b)) != a.Size || hex.EncodeToString(hash[:]) != a.SHA256 {
		return Artifact{}, nil, ErrUnavailable
	}
	return a, b, nil
}

// os.File stays private so callers cannot bypass bounded reads and hash checks.
