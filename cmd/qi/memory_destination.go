package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"gopi/internal/session"
	"net/url"
	"strings"
)

// Never expose a key, URL userinfo, query or fragment. Unsafe endpoint metadata
// disables memory selection, not the existing provider's own validation path.
func memoryDestination(c modelSettings) session.MemoryDestination {
	provider := c.name
	if provider == "" {
		provider = "openai"
	}
	base := strings.TrimRight(c.base, "/")
	suffix := "/chat/completions"
	if provider == "anthropic" {
		suffix = "/messages"
		if base == "" {
			base = "https://api.anthropic.com/v1"
		}
	} else if provider == "openai" {
		if base == "" {
			base = "https://api.openai.com/v1"
		}
	} else {
		return session.MemoryDestination{}
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || len(base) > 2048 || len(c.model) > 256 || strings.TrimSpace(c.model) == "" {
		return session.MemoryDestination{}
	}
	// Refuse control characters and accidentally duplicated configured secrets.
	public := provider + " " + c.model + " " + base
	for _, r := range public {
		if r < 32 || r == 127 {
			return session.MemoryDestination{}
		}
	}
	if c.key != "" && strings.Contains(public, c.key) {
		return session.MemoryDestination{}
	}
	d := session.MemoryDestination{Provider: provider, Model: c.model, Endpoint: base + suffix}
	payload, _ := json.Marshal(d)
	digest := sha256.Sum256(payload)
	d.ID = hex.EncodeToString(digest[:])
	return d
}
