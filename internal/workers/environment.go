package workers

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ValidateCredentials permits only dedicated worker provider credentials. It
// deliberately excludes qi authentication and the parent model's environment.
func ValidateCredentials(credentials map[string]string) error {
	for key, value := range credentials {
		if key != "CODEX_API_KEY" && key != "ANTHROPIC_API_KEY" {
			return errors.New("worker credentials allow only CODEX_API_KEY and ANTHROPIC_API_KEY")
		}
		if strings.ContainsRune(value, 0) {
			return errors.New("worker credential contains NUL")
		}
	}
	return nil
}

// workerEnvironment starts with no inherited environment. Only non-secret
// runtime settings are copied. A private temporary home prevents accidental
// discovery of the operator's CLI configuration, plugins, or saved credentials.
// This is hygiene, not a sandbox: same-user code can still read other files.
func workerEnvironment(credentials map[string]string) ([]string, func(), error) {
	if err := ValidateCredentials(credentials); err != nil {
		return nil, nil, err
	}
	home, err := os.MkdirTemp("", "qi-worker-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(home) }
	vars := map[string]string{}
	for _, key := range []string{"PATH", "SystemRoot", "WINDIR", "LANG", "LC_ALL", "TERM"} {
		if value, ok := os.LookupEnv(key); ok {
			vars[key] = value
		}
	}
	for key, subdir := range map[string]string{"HOME": "", "USERPROFILE": "", "XDG_CONFIG_HOME": "config", "XDG_CACHE_HOME": "cache", "XDG_DATA_HOME": "data", "CODEX_HOME": "codex", "CLAUDE_CONFIG_DIR": "claude", "TMPDIR": "tmp", "TEMP": "tmp", "TMP": "tmp"} {
		path := filepath.Join(home, subdir)
		if err = os.MkdirAll(path, 0700); err != nil {
			cleanup()
			return nil, nil, err
		}
		vars[key] = path
	}
	for key, value := range credentials {
		vars[key] = value
	}
	keys := make([]string, 0, len(vars))
	for key := range vars {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+vars[key])
	}
	return env, cleanup, nil
}
