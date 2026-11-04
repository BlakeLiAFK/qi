package skills

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func document(name, description, body string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n" + body
}
func discover(t *testing.T, roots ...Root) *Catalog {
	t.Helper()
	c, err := Discover(context.Background(), Config{Roots: roots})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestExplicitSelectionPrecedenceAndCopies(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	write(t, filepath.Join(a, "alpha", "SKILL.md"), document("alpha", "First", "first body"))
	write(t, filepath.Join(b, "SKILL.md"), document("alpha", "Second", "second body"))
	write(t, filepath.Join(a, "zeta", "SKILL.md"), document("zeta", "Zed", "other"))
	c := discover(t, Root{"project", a}, Root{"user", b})
	if got := c.List(); len(got) != 2 || got[0].Name != "alpha" {
		t.Fatal(got)
	}
	if len(c.Collisions()) != 1 || c.Collisions()[0].Ignored.Source != "user" {
		t.Fatal(c.Collisions())
	}
	listed := c.List()
	listed[0].Name = "tamper"
	collisions := c.Collisions()
	collisions[0].Name = "tamper"
	if c.List()[0].Name != "alpha" || c.Collisions()[0].Name != "alpha" {
		t.Fatal("mutable catalog")
	}
	result, err := c.Select(context.Background(), nil)
	if err != nil || len(result) != 0 {
		t.Fatal(result, err)
	}
	result, err = c.Select(context.Background(), []string{"zeta", "alpha"})
	if err != nil || result[1].Body != "first body" {
		t.Fatal(result, err)
	}
	for _, names := range [][]string{{"alpha", "alpha"}, {"alpha", "missing"}} {
		if result, err := c.Select(context.Background(), names); err == nil || result != nil {
			t.Fatal("expected atomic failure", result, err)
		}
	}
}
func TestMetadataGrammar(t *testing.T) {
	good := []string{document("test", "plain words", "body"), document("test", `"quoted: # text"`, "body"), document("test", `'it''s fine'`, "body"), strings.ReplaceAll(document("test", "hello", "body"), "\n", "\r\n")}
	for _, s := range good {
		n, d, off, err := header(strings.NewReader(s), 1024)
		if err != nil || n != "test" || d == "" || s[off:] != "body" {
			t.Fatalf("%q: %s %s %d %v", s, n, d, off, err)
		}
	}
	bad := []string{"no frontmatter", "---\nname: test\n", document("../test", "hello", ""), document("Upper", "hello", ""), document("a--b", "hello", ""), document("test", "&ref hello", ""), document("test", "*ref", ""), document("test", "!!str hello", ""), document("test", "|", ""), document("test", "[hello]", ""), document("test", `"bad\ncontrol"`, ""), document("test", `"bad\xff"`, ""), document("test", `'bad'quote'`, ""), "---\nname: test\nname: other\ndescription: hello\n---\n", "---\nname: test\ndescription: hi\nunknown: yep\n---\n", document("test", string([]byte{255}), "")}
	for _, s := range bad {
		if _, _, _, err := header(strings.NewReader(s), 1024); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}
func TestOnDemandAndMetadataChanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "SKILL.md")
	write(t, path, document("test", "hello", "before"))
	c := discover(t, Root{"project", root})
	write(t, path, document("test", "hello", "after"))
	got, err := c.Select(context.Background(), []string{"test"})
	if err != nil || got[0].Body != "after" {
		t.Fatal(got, err)
	}
	write(t, path, document("test", "changed", "after"))
	if _, err := c.Select(context.Background(), []string{"test"}); err == nil {
		t.Fatal("metadata drift accepted")
	}
	write(t, path, document("test", "hello", string([]byte{255})))
	if _, err := c.Select(context.Background(), []string{"test"}); err == nil {
		t.Fatal("invalid body accepted")
	}
	// Discovery validates metadata, not instruction bytes.
	discover(t, Root{"project", root})
}
func TestSymlinkContainmentAndRetarget(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	write(t, filepath.Join(outside, "SKILL.md"), document("evil", "hello", "outside"))
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip(err)
	}
	if _, err := Discover(context.Background(), Config{Roots: []Root{{"project", root}}}); err == nil {
		t.Fatal("escaping directory accepted")
	}
	os.Remove(link)
	if err := os.Symlink(filepath.Join(outside, "SKILL.md"), filepath.Join(root, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(context.Background(), Config{Roots: []Root{{"project", root}}}); err == nil {
		t.Fatal("escaping file accepted")
	}
	os.Remove(filepath.Join(root, "SKILL.md"))
	write(t, filepath.Join(root, "assets", "one"), document("test", "hello", "one"))
	write(t, filepath.Join(root, "assets", "two"), document("test", "hello", "two"))
	os.Symlink(filepath.Join(root, "assets", "one"), filepath.Join(root, "SKILL.md"))
	c := discover(t, Root{"project", root})
	os.Remove(filepath.Join(root, "SKILL.md"))
	os.Symlink(filepath.Join(root, "assets", "two"), filepath.Join(root, "SKILL.md"))
	if _, err := c.Select(context.Background(), []string{"test"}); err == nil {
		t.Fatal("retarget accepted")
	}
}
func TestLimitsAndMissingRoots(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "one", "SKILL.md"), document("one", "hello", strings.Repeat("x", 100)))
	write(t, filepath.Join(root, "two", "SKILL.md"), document("two", "hello", ""))
	for _, limits := range []Limits{{MaxRoots: 1}, {MaxEntries: 1}, {MaxSkills: 1}, {MaxFileBytes: 32}, {MaxHeaderBytes: 10}, {MaxSkills: -1}} {
		roots := []Root{{"project", root}}
		if limits.MaxRoots == 1 {
			roots = append(roots, Root{"user", root})
		}
		if _, err := Discover(context.Background(), Config{Roots: roots, Limits: limits}); err == nil {
			t.Fatalf("accepted limits %+v", limits)
		}
	}
	c := discover(t, Root{"project", root})
	write(t, filepath.Join(root, "one", "SKILL.md"), document("one", "hello", strings.Repeat("x", 1024*1024)))
	if _, err := c.Select(context.Background(), []string{"one"}); err == nil {
		t.Fatal("oversize on load accepted")
	}
	if _, err := Discover(context.Background(), Config{Roots: []Root{{"missing", filepath.Join(root, "missing")}}}); err == nil {
		t.Fatal("missing root accepted")
	}
	empty := discover(t)
	if !reflect.DeepEqual(empty.List(), []Metadata(nil)) {
		t.Fatal(empty.List())
	}
}
func TestCancelled(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "SKILL.md"), document("test", "hello", "body"))
	c := discover(t, Root{"project", root})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Discover(ctx, Config{Roots: []Root{{"project", root}}}); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := c.Select(ctx, []string{"test"}); err != context.Canceled {
		t.Fatal(err)
	}
}
func TestNoRecursiveDiscovery(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a", "b", "SKILL.md"), document("hidden", "hello", "body"))
	if len(discover(t, Root{"project", root}).List()) != 0 {
		t.Fatal("recursive discovery")
	}
}

func TestLoadRechecksContainment(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "SKILL.md")
	write(t, path, document("test", "hello", "inside"))
	write(t, filepath.Join(outside, "SKILL.md"), document("test", "hello", "outside"))
	c := discover(t, Root{"project", root})
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "SKILL.md"), path); err != nil {
		t.Skip(err)
	}
	if _, err := c.Select(context.Background(), []string{"test"}); err == nil {
		t.Fatal("load followed escaping symlink")
	}
}

func TestLimitCeilings(t *testing.T) {
	for _, l := range []Limits{{MaxRoots: 1025}, {MaxEntries: 1000001}, {MaxSkills: 4097}, {MaxFileBytes: 17 * 1024 * 1024}, {MaxHeaderBytes: 2 * 1024 * 1024}} {
		if _, err := Discover(context.Background(), Config{Limits: l}); err == nil {
			t.Fatalf("accepted %+v", l)
		}
	}
}

func FuzzHeader(f *testing.F) {
	f.Add(document("test", "hello", "body"))
	f.Add("---\nname: test\ndescription: \"quoted\"\n---\n")
	f.Add("---\nname: bad\n")
	f.Fuzz(func(t *testing.T, input string) {
		n, d, offset, err := header(strings.NewReader(input), 16384)
		if err == nil && (!validName(n) || d == "" || offset < 0 || offset > len(input) || offset > 16384) {
			t.Fatal("invalid successful parse")
		}
	})
}

func TestSelectionAggregateByteBudget(t *testing.T) {
	root := t.TempDir()
	content := document("one", "hello", "body")
	write(t, filepath.Join(root, "one", "SKILL.md"), content)
	write(t, filepath.Join(root, "two", "SKILL.md"), document("two", "hello", "body"))
	c := discover(t, Root{"project", root})
	if got, err := c.SelectBounded(context.Background(), []string{"one", "two"}, int64(len(content)*2-1)); err == nil || got != nil {
		t.Fatal("aggregate budget bypassed", got, err)
	}
	if got, err := c.SelectBounded(context.Background(), []string{"one", "two"}, int64(len(content)*2)); err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
}
