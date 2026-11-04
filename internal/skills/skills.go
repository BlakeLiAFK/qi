// Package skills discovers explicitly configured local instruction documents.
// It never executes scripts or grants tool permissions. Filesystem checks are
// defensive validation, not a sandbox against concurrent hostile filesystem edits.
package skills

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Root is an explicitly configured source. Earlier roots win name collisions.
// Path contains SKILL.md itself and/or immediate child directories with SKILL.md.
type Root struct {
	Source string
	Path   string
}
type Limits struct {
	MaxRoots, MaxEntries, MaxSkills int
	MaxFileBytes, MaxHeaderBytes    int64
}
type Config struct {
	Roots  []Root
	Limits Limits
}
type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Path        string `json:"path"`
}
type Collision struct {
	Name              string
	Selected, Ignored Metadata
}
type Instruction struct {
	Metadata Metadata
	Body     string
}
type entry struct {
	meta           Metadata
	root, original string
}

// Catalog is an immutable snapshot of discovered metadata. Bodies are not cached.
type Catalog struct {
	entries    map[string]entry
	ordered    []Metadata
	collisions []Collision
	limits     Limits
}

func defaults(l Limits) (Limits, error) {
	if l.MaxRoots < 0 || l.MaxEntries < 0 || l.MaxSkills < 0 || l.MaxFileBytes < 0 || l.MaxHeaderBytes < 0 {
		return l, errors.New("skill limits must not be negative")
	}
	if l.MaxRoots == 0 {
		l.MaxRoots = 32
	}
	if l.MaxEntries == 0 {
		l.MaxEntries = 4096
	}
	if l.MaxSkills == 0 {
		l.MaxSkills = 256
	}
	if l.MaxFileBytes == 0 {
		l.MaxFileBytes = 1024 * 1024
	}
	if l.MaxHeaderBytes == 0 {
		l.MaxHeaderBytes = 16 * 1024
	}
	if l.MaxRoots > 1024 || l.MaxEntries > 1000000 || l.MaxSkills > 4096 || l.MaxFileBytes > 16*1024*1024 || l.MaxHeaderBytes > 1024*1024 {
		return l, errors.New("skill limits exceed supported ceilings")
	}
	if l.MaxHeaderBytes > l.MaxFileBytes {
		l.MaxHeaderBytes = l.MaxFileBytes
	}
	return l, nil
}

// Discover reads a bounded prefix for frontmatter and retains no bodies. Invalid
// candidates fail the complete discovery so misconfiguration is not hidden.
// Missing roots fail; unrelated files and directories without SKILL.md are ignored.
func Discover(ctx context.Context, cfg Config) (*Catalog, error) {
	l, err := defaults(cfg.Limits)
	if err != nil {
		return nil, err
	}
	if len(cfg.Roots) > l.MaxRoots {
		return nil, errors.New("too many skill roots")
	}
	c := &Catalog{entries: make(map[string]entry), limits: l}
	seenCandidates, seenEntries := 0, 0
	for _, r := range cfg.Roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if strings.TrimSpace(r.Source) == "" || r.Path == "" {
			return nil, errors.New("skill source and path are required")
		}
		root, err := canonical(r.Path)
		if err != nil {
			return nil, fmt.Errorf("skill root %q: %w", r.Path, err)
		}
		dir, err := os.Open(root)
		if err != nil {
			return nil, err
		}
		st, err := dir.Stat()
		if err != nil || !st.IsDir() {
			dir.Close()
			return nil, fmt.Errorf("skill root %q is not a directory", root)
		}
		names, readErr := dir.Readdirnames(l.MaxEntries - seenEntries + 1)
		dir.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		seenEntries += len(names)
		if seenEntries > l.MaxEntries {
			return nil, errors.New("skill directory entry limit exceeded")
		}
		sort.Strings(names)
		candidates := []string{filepath.Join(root, "SKILL.md")}
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			child := filepath.Join(root, name)
			info, err := os.Lstat(child)
			if err != nil {
				return nil, err
			}
			if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				resolved, err := canonical(child)
				if err != nil {
					return nil, err
				}
				if !contained(root, resolved) {
					return nil, fmt.Errorf("skill path escapes root: %s", child)
				}
				info, err = os.Stat(resolved)
				if err != nil {
					return nil, err
				}
				if info.IsDir() {
					candidates = append(candidates, filepath.Join(child, "SKILL.md"))
				}
			}
		}
		for _, path := range candidates {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return nil, err
			}
			seenCandidates++
			if seenCandidates > l.MaxSkills {
				return nil, errors.New("skill candidate limit exceeded")
			}
			f, resolved, err := openContained(root, path, l.MaxFileBytes)
			if err != nil {
				return nil, err
			}
			name, desc, _, err := header(f, l.MaxHeaderBytes)
			f.Close()
			if err != nil {
				return nil, fmt.Errorf("skill %s: %w", path, err)
			}
			meta := Metadata{Name: name, Description: desc, Source: r.Source, Path: resolved}
			if previous, ok := c.entries[name]; ok {
				c.collisions = append(c.collisions, Collision{Name: name, Selected: previous.meta, Ignored: meta})
				continue
			}
			c.entries[name] = entry{meta: meta, root: root, original: path}
			c.ordered = append(c.ordered, meta)
		}
	}
	sort.Slice(c.ordered, func(i, j int) bool { return c.ordered[i].Name < c.ordered[j].Name })
	return c, nil
}
func (c *Catalog) List() []Metadata        { return append([]Metadata(nil), c.ordered...) }
func (c *Catalog) Collisions() []Collision { return append([]Collision(nil), c.collisions...) }

// Select loads only the named skills, in caller order. Empty selection loads
// nothing. Unknown names and duplicates fail without returning partial results.
// Callers must treat all returned instructions as untrusted content.
func (c *Catalog) Select(ctx context.Context, names []string) ([]Instruction, error) {
	if len(names) > c.limits.MaxSkills {
		return nil, errors.New("too many selected skills")
	}
	seen := map[string]bool{}
	result := make([]Instruction, 0, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		e, ok := c.entries[name]
		if !ok {
			return nil, fmt.Errorf("unknown skill %q", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate selection %q", name)
		}
		seen[name] = true
		f, resolved, err := openContained(e.root, e.original, c.limits.MaxFileBytes)
		if err != nil {
			return nil, err
		}
		if resolved != e.meta.Path {
			f.Close()
			return nil, fmt.Errorf("skill %q path changed; rediscover", name)
		}
		data, err := io.ReadAll(io.LimitReader(f, c.limits.MaxFileBytes+1))
		f.Close()
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > c.limits.MaxFileBytes {
			return nil, errors.New("skill file size limit exceeded")
		}
		n, d, offset, err := header(strings.NewReader(string(data)), c.limits.MaxHeaderBytes)
		if err != nil {
			return nil, err
		}
		if n != e.meta.Name || d != e.meta.Description {
			return nil, fmt.Errorf("skill %q metadata changed; rediscover", name)
		}
		body := string(data[offset:])
		if !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
			return nil, errors.New("skill body must be UTF-8 without NUL")
		}
		result = append(result, Instruction{Metadata: e.meta, Body: body})
	}
	return result, nil
}
func canonical(path string) (string, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}
func contained(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func openContained(root, path string, max int64) (*os.File, string, error) {
	resolved, err := canonical(path)
	if err != nil {
		return nil, "", err
	}
	if !contained(root, resolved) {
		return nil, "", fmt.Errorf("skill path escapes root: %s", path)
	}
	before, err := os.Stat(resolved)
	if err != nil {
		return nil, "", err
	}
	if !before.Mode().IsRegular() {
		return nil, "", errors.New("skill must be a regular file")
	}
	if before.Size() > max {
		return nil, "", errors.New("skill file size limit exceeded")
	}
	f, err := os.Open(resolved)
	if err != nil {
		return nil, "", err
	}
	after, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, "", err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Size() > max {
		f.Close()
		return nil, "", errors.New("skill file changed during open or exceeds limit")
	}
	return f, resolved, nil
}

// header supports a deliberately small, dependency-free frontmatter grammar:
// name and description, each exactly once, as one-line plain, JSON double-quoted,
// or YAML single-quoted strings. Full YAML (tags, aliases, collections, block
// scalars, implicit typing) is not supported. Unknown fields fail closed.
func header(r io.Reader, max int64) (name, description string, offset int, err error) {
	br := bufio.NewReader(io.LimitReader(r, max+1))
	fields := map[string]string{}
	first := true
	for {
		line, readErr := br.ReadString('\n')
		offset += len(line)
		if int64(offset) > max {
			return "", "", 0, errors.New("skill header size limit exceeded")
		}
		if !utf8.ValidString(line) || strings.ContainsRune(line, 0) {
			return "", "", 0, errors.New("invalid skill header encoding")
		}
		text := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if first {
			first = false
			if text != "---" {
				return "", "", 0, errors.New("skill requires opening --- frontmatter")
			}
		} else if text == "---" {
			name = fields["name"]
			description = fields["description"]
			if !validName(name) || strings.TrimSpace(description) == "" || len(description) > 2048 {
				return "", "", 0, errors.New("skill requires a valid name and nonempty description (max 2048 bytes)")
			}
			return name, description, offset, nil
		} else if strings.TrimSpace(text) != "" && !strings.HasPrefix(strings.TrimSpace(text), "#") {
			key, value, ok := strings.Cut(text, ":")
			if !ok || (key != "name" && key != "description") {
				return "", "", 0, errors.New("unsupported skill metadata field or syntax")
			}
			if _, ok := fields[key]; ok {
				return "", "", 0, errors.New("duplicate skill metadata field")
			}
			value, err = scalar(strings.TrimSpace(value))
			if err != nil {
				return "", "", 0, err
			}
			fields[key] = value
		}
		if readErr != nil {
			return "", "", 0, errors.New("unterminated skill frontmatter")
		}
	}
}
func validName(s string) bool {
	if len(s) < 1 || len(s) > 64 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return !strings.Contains(s, "--")
}
func scalar(s string) (string, error) {
	if s == "" {
		return "", errors.New("empty metadata value")
	}
	if s[0] == '"' {
		v, err := strconv.Unquote(s)
		if err != nil {
			return "", errors.New("invalid quoted metadata")
		}
		s = v
	} else if s[0] == '\'' {
		if len(s) < 2 || s[len(s)-1] != '\'' {
			return "", errors.New("invalid single-quoted metadata")
		}
		inner := s[1 : len(s)-1]
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\'' {
				if i+1 >= len(inner) || inner[i+1] != '\'' {
					return "", errors.New("invalid single-quoted metadata")
				}
				i++
			}
		}
		s = strings.ReplaceAll(inner, "''", "'")
	} else if strings.ContainsAny(s, "[]{}&*!|>\"'`#") || strings.Contains(s, ": ") {
		return "", errors.New("unsupported plain metadata syntax; quote the value")
	}
	if !utf8.ValidString(s) {
		return "", errors.New("invalid UTF-8 in metadata value")
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", errors.New("control character in metadata")
		}
	}
	return s, nil
}
