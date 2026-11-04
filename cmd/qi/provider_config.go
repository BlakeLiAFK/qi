package main

import (
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"gopi/internal/agent"
	"gopi/internal/provider"
)

type modelFlags struct {
	name, model, base *string
	maxTokens         *int
}
type modelSettings struct {
	name, model, base, key string
	maxTokens              int
}

func registerModelFlags(fs *flag.FlagSet) modelFlags {
	return modelFlags{
		name:      fs.String("provider", "openai", "model protocol: openai or anthropic (native Messages API)"),
		model:     fs.String("model", "", "model name; defaults to selected provider's MODEL environment variable"),
		base:      fs.String("base-url", "", "API root; defaults to selected provider's BASE_URL environment variable or official endpoint"),
		maxTokens: fs.Int("max-output-tokens", 0, "Anthropic only: output-token cap (default ANTHROPIC_MAX_TOKENS or 4096)"),
	}
}
func (f modelFlags) resolve(fs *flag.FlagSet, env func(string) string) (modelSettings, error) {
	c := modelSettings{name: *f.name, model: *f.model, base: *f.base}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	prefix := ""
	switch c.name {
	case "openai":
		prefix = "OPENAI"
	case "anthropic":
		prefix = "ANTHROPIC"
	default:
		return c, errors.New("-provider must be openai or anthropic")
	}
	if !set["model"] {
		c.model = env(prefix + "_MODEL")
	}
	if !set["base-url"] {
		c.base = env(prefix + "_BASE_URL")
	}
	c.key = env(prefix + "_API_KEY")
	if strings.TrimSpace(c.model) == "" {
		return c, fmt.Errorf("set %s_MODEL or pass -model", prefix)
	}
	if c.name == "openai" {
		if set["max-output-tokens"] {
			return c, errors.New("-max-output-tokens is supported only with -provider anthropic")
		}
		return c, nil
	}
	c.maxTokens = 4096
	if set["max-output-tokens"] {
		c.maxTokens = *f.maxTokens
	} else if raw := env("ANTHROPIC_MAX_TOKENS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return c, errors.New("ANTHROPIC_MAX_TOKENS must be a positive integer")
		}
		c.maxTokens = n
	}
	if c.maxTokens <= 0 {
		return c, errors.New("Anthropic max-output-tokens must be positive")
	}
	return c, nil
}
func newModelProvider(c modelSettings, stream bool) (agent.Model, error) {
	switch c.name {
	case "", "openai":
		return &provider.OpenAI{Model: c.model, BaseURL: c.base, APIKey: c.key, Stream: stream}, nil
	case "anthropic":
		if stream {
			return nil, errors.New("-model-stream is not supported by native Anthropic; use buffered mode")
		}
		if c.maxTokens < 0 {
			return nil, errors.New("Anthropic max-output-tokens must not be negative")
		}
		return &provider.Anthropic{Model: c.model, BaseURL: c.base, APIKey: c.key, MaxTokens: c.maxTokens}, nil
	default:
		return nil, errors.New("unsupported model provider")
	}
}
