package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Default operator env file locations (never store secrets in SQLite).
// Process env (systemd EnvironmentFile at start) wins over file overlay.
// Files are re-read so new catalog api_key_env names work without restart.
const (
	// RelMemoryEnv is under --memory (gitignored operator dir).
	RelMemoryEnv = "env"
	// UserConfigEnv is the common systemd EnvironmentFile path.
	UserConfigEnv = ".config/marble/env"
)

var (
	envOverlayMu    sync.Mutex
	envOverlayCache map[string]string
	envOverlayAt    time.Time
	envOverlayTTL   = 2 * time.Second
	// MemoryDirForEnv is set from main after ParseFlags (optional).
	MemoryDirForEnv string
)

// SetMemoryDirForEnv registers $MEMORY so ResolveAPIKeyEnv can read $MEMORY/env.
func SetMemoryDirForEnv(memory string) {
	envOverlayMu.Lock()
	MemoryDirForEnv = strings.TrimSpace(memory)
	envOverlayCache = nil
	envOverlayAt = time.Time{}
	envOverlayMu.Unlock()
}

// EnvFilePaths returns paths checked for KEY=value overlay (for Settings help).
func EnvFilePaths() []string {
	var out []string
	if MemoryDirForEnv != "" {
		out = append(out, filepath.Join(MemoryDirForEnv, RelMemoryEnv))
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		out = append(out, filepath.Join(h, UserConfigEnv))
	}
	return out
}

// ResolveAPIKeyEnv resolves a comma-separated list of env var names to a secret.
// Never logs the secret. First non-empty value wins.
// Order: process environment (os.Getenv), then overlay files ($MEMORY/env, ~/.config/marble/env).
// Empty list → no key.
func ResolveAPIKeyEnv(apiKeyEnv string) (key, used string, configured bool) {
	raw := strings.TrimSpace(apiKeyEnv)
	if raw == "" {
		return "", "", false
	}
	names := splitEnvNames(raw)
	if len(names) == 0 {
		return "", "", false
	}
	// 1) Live process environment
	for _, name := range names {
		val := strings.TrimSpace(os.Getenv(name))
		if val != "" {
			return val, name, true
		}
	}
	// 2) File overlay (re-read so operators can add keys without restart)
	fileMap := loadEnvOverlay()
	for _, name := range names {
		if val := strings.TrimSpace(fileMap[name]); val != "" {
			return val, name, true
		}
	}
	return "", "", false
}

func splitEnvNames(raw string) []string {
	var names []string
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func loadEnvOverlay() map[string]string {
	envOverlayMu.Lock()
	defer envOverlayMu.Unlock()
	if envOverlayCache != nil && time.Since(envOverlayAt) < envOverlayTTL {
		return envOverlayCache
	}
	merged := make(map[string]string)
	// Later files do not override earlier (memory first, then user config as fill-in)
	for _, p := range envFilePathsLocked() {
		m, err := ParseEnvFile(p)
		if err != nil {
			continue
		}
		for k, v := range m {
			if _, exists := merged[k]; !exists && v != "" {
				merged[k] = v
			}
		}
	}
	envOverlayCache = merged
	envOverlayAt = time.Now()
	return merged
}

func envFilePathsLocked() []string {
	var out []string
	if MemoryDirForEnv != "" {
		out = append(out, filepath.Join(MemoryDirForEnv, RelMemoryEnv))
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		out = append(out, filepath.Join(h, UserConfigEnv))
	}
	return out
}

// InvalidateEnvOverlay forces the next ResolveAPIKeyEnv to re-read files.
func InvalidateEnvOverlay() {
	envOverlayMu.Lock()
	envOverlayCache = nil
	envOverlayAt = time.Time{}
	envOverlayMu.Unlock()
}

// ParseEnvFile reads a KEY=VALUE env file (systemd EnvironmentFile-compatible subset).
// Supports # comments, blank lines, optional export prefix, and simple quotes.
// Returns empty map if the file is missing.
func ParseEnvFile(path string) (map[string]string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return map[string]string{}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	defer f.Close()
	out := make(map[string]string)
	sc := bufio.NewScanner(f)
	// allow long lines (API keys can be long)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		// skip systemd-style pass-through without value assignment for our use
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if key == "" || strings.ContainsAny(key, " \t") {
			continue
		}
		val = unquoteEnvValue(val)
		out[key] = val
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	return out, nil
}

func unquoteEnvValue(val string) string {
	if len(val) >= 2 {
		if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
			return val[1 : len(val)-1]
		}
	}
	return val
}

// LookupEnvName reports whether a single env var name is set (process or overlay).
// Never returns the secret.
func LookupEnvName(name string) (configured bool, source string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, ""
	}
	if strings.TrimSpace(os.Getenv(name)) != "" {
		return true, "process"
	}
	if v := strings.TrimSpace(loadEnvOverlay()[name]); v != "" {
		return true, "file"
	}
	return false, ""
}

// AuthHintForAPIKeyEnv returns a safe operator message when keys are missing.
func AuthHintForAPIKeyEnv(apiKeyEnv string) string {
	apiKeyEnv = strings.TrimSpace(apiKeyEnv)
	if apiKeyEnv == "" || strings.EqualFold(apiKeyEnv, "none") {
		return ""
	}
	_, _, ok := ResolveAPIKeyEnv(apiKeyEnv)
	if ok {
		return ""
	}
	names := splitEnvNames(apiKeyEnv)
	paths := EnvFilePaths()
	pathHint := strings.Join(paths, " or ")
	if pathHint == "" {
		pathHint = "~/.config/marble/env"
	}
	return "Env " + strings.Join(names, "/") + " not set in process or env files. Add KEY=… to " +
		pathHint + " (applies within seconds; no restart required for catalog models). " +
		"If you only use systemd EnvironmentFile without re-read, restart marble-harness after editing."
}
