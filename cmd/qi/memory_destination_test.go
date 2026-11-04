package main

import (
	"strings"
	"testing"
)

func TestMemoryDestinationDisclosure(t *testing.T) {
	c := modelSettings{name: "openai", model: "fixture", key: "private-credential"}
	d := memoryDestination(c)
	if len(d.ID) != 64 || d.Endpoint != "https://api.openai.com/v1/chat/completions" || d.Provider != "openai" || d.Model != "fixture" {
		t.Fatal(d)
	}
	c.key = "rotated-key"
	if memoryDestination(c).ID != d.ID {
		t.Fatal("credential rotation altered public destination")
	}
	c.model = "different"
	if memoryDestination(c).ID == d.ID {
		t.Fatal("model not bound")
	}
	c.model = "fixture"
	c.name = "anthropic"
	d = memoryDestination(c)
	if d.Endpoint != "https://api.anthropic.com/v1/messages" {
		t.Fatal(d)
	}
	for _, base := range []string{"https://user:secret@example.test/v1", "https://example.test/v1?key=secret", "https://example.test/v1#secret", "https://example.test/v1?", "https://example.test/rotated-key"} {
		c.base = base
		d = memoryDestination(c)
		if d.ID != "" || strings.Contains(d.Endpoint, "secret") {
			t.Fatal("unsafe destination disclosed", d)
		}
	}
}
