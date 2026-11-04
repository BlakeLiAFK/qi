package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// ScopedSupported reports whether process-group cleanup is available.
func ScopedSupported() bool { return processGroupsSupported }

// StartScoped starts a trusted server with a disposable home and no credentials.
// It is intentionally separate from the CLI's explicit credential-bearing mode.
// A working directory and environment allowlist are hygiene, not an OS sandbox.
// The caller must approve startup itself, which can already have side effects.
func StartScoped(ctx context.Context, cfg Config) (*Client, error) {
	if !processGroupsSupported {
		return nil, errors.New("scoped MCP requires process-group cleanup on this platform")
	}
	if len(cfg.Env) != 0 {
		return nil, errors.New("scoped MCP does not accept credential or environment overrides")
	}
	home, err := os.MkdirTemp("", "qi-mcp-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(home) }
	env := []string{}
	for _, key := range []string{"PATH", "SystemRoot", "WINDIR", "LANG", "LC_ALL"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	for key, subdir := range map[string]string{"HOME": "", "USERPROFILE": "", "XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data", "XDG_CACHE_HOME": "cache", "TMPDIR": "tmp", "TMP": "tmp", "TEMP": "tmp"} {
		path := filepath.Join(home, subdir)
		if err := os.MkdirAll(path, 0700); err != nil {
			cleanup()
			return nil, err
		}
		env = append(env, key+"="+path)
	}
	return start(ctx, cfg, env, cleanup, true)
}
