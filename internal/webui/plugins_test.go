package webui

import (
	"gopi/internal/tasks"
	"strings"
	"testing"
)

type catalogBackend struct {
	fakeBackend
	reads int
}

func (b *catalogBackend) ToolCatalog() tasks.ToolCatalog {
	b.reads++
	return tasks.ToolCatalog{ReadOnly: true, Entries: []tasks.ToolCatalogEntry{{Name: "<script>untrusted</script>", Kind: "mcp", Enabled: true}}}
}
func TestPluginCatalogAuthenticatedReadOnly(t *testing.T) {
	b := &catalogBackend{}
	h, err := New(Config{Origin: "http://127.0.0.1:8080", Token: token, Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/plugins", "/plugins.js", "/api/plugins"} {
		if w := request(h, "GET", path, "", false, ""); w.Code != 401 {
			t.Fatalf("%s: %d", path, w.Code)
		}
		if w := request(h, "GET", path, "", true, ""); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if b.reads != 1 || b.calls != 0 {
		t.Fatal("inspection has side effects")
	}
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		if w := request(h, method, "/api/plugins", `{"enabled":true}`, true, "http://127.0.0.1:8080"); w.Code != 405 {
			t.Fatal(method, w.Code)
		}
	}
	if b.reads != 1 || b.calls != 0 {
		t.Fatal("mutation invoked backend")
	}
	w := request(h, "GET", "/api/plugins", "", true, "")
	if !strings.Contains(w.Body.String(), `"read_only":true`) || strings.Contains(w.Body.String(), "<script>") {
		t.Fatal(w.Body.String())
	}
}
func TestPluginCatalogUnavailableIsExplicit(t *testing.T) {
	h, _ := New(Config{Origin: "http://127.0.0.1:8080", Token: token, Backend: &fakeBackend{}})
	if w := request(h, "GET", "/api/plugins", "", true, ""); w.Code != 503 {
		t.Fatal(w.Code)
	}
}
