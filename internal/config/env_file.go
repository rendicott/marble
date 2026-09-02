package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ValidEnvKeyRE matches shell-safe env var names (no $ or spaces).
var ValidEnvKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Default operator env file location (never store secrets in SQLite).
// Process env (systemd EnvironmentFile / shell export) wins over the file overlay.
// $MEMORY/env is re-read so new catalog api_key_env names work without restart.
const (
	// RelMemoryEnv is under --memory (gitignored operator dir). Settings → Secrets writes here.
	RelMemoryEnv = "env"
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

// EnvFilePaths returns overlay file paths checked after process env (for Settings help).
func EnvFilePaths() []string {
	if MemoryDirForEnv == "" {
		return nil
	}
	return []string{filepath.Join(MemoryDirForEnv, RelMemoryEnv)}
}

// ResolveAPIKeyEnv resolves a comma-separated list of env var names to a secret.
// Never logs the secret. First non-empty value wins.
// Order: process environment (os.Getenv), then $MEMORY/env overlay.
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
	for _, p := range envFilePathsLocked() {
		m, err := ParseEnvFile(p)
		if err != nil {
			continue
		}
		for k, v := range m {
			if v != "" {
				merged[k] = v
			}
		}
	}
	envOverlayCache = merged
	envOverlayAt = time.Now()
	return merged
}

func envFilePathsLocked() []string {
	if MemoryDirForEnv == "" {
		return nil
	}
	return []string{filepath.Join(MemoryDirForEnv, RelMemoryEnv)}
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

// ManagedEnvPath is the file Settings → Secrets writes ($MEMORY/env).
func ManagedEnvPath() string {
	envOverlayMu.Lock()
	defer envOverlayMu.Unlock()
	if MemoryDirForEnv != "" {
		return filepath.Join(MemoryDirForEnv, RelMemoryEnv)
	}
	return ""
}

// EnvEntry is one KEY from the managed env file (and optional process note).
type EnvEntry struct {
	Name          string `json:"name"`
	Value         string `json:"value"`
	InManagedFile bool   `json:"in_managed_file"`
	InProcess     bool   `json:"in_process"`
}

// ValidateEnvKey returns an error if name is not a valid env identifier.
func ValidateEnvKey(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name required")
	}
	if !ValidEnvKeyRE.MatchString(name) {
		return fmt.Errorf("invalid env name %q (use A-Z, 0-9, _; must start with letter or _)", name)
	}
	return nil
}

// ListManagedEnv returns entries from the managed file plus process flags.
func ListManagedEnv() (path string, readPaths []string, entries []EnvEntry, err error) {
	path = ManagedEnvPath()
	readPaths = EnvFilePaths()
	fileMap, err := ParseEnvFile(path)
	if err != nil {
		return path, readPaths, nil, err
	}
	keys := make([]string, 0, len(fileMap))
	for k := range fileMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e := EnvEntry{
			Name:          k,
			Value:         fileMap[k],
			InManagedFile: true,
		}
		if pv := strings.TrimSpace(os.Getenv(k)); pv != "" {
			e.InProcess = true
			// Prefer displaying process value when set (matches resolve order)
			e.Value = pv
		}
		entries = append(entries, e)
	}
	return path, readPaths, entries, nil
}

// UpsertManagedEnv sets KEY=value in the managed env file (mode 0600). Creates file/dir if needed.
func UpsertManagedEnv(name, value string) (path string, err error) {
	if err := ValidateEnvKey(name); err != nil {
		return "", err
	}
	path = ManagedEnvPath()
	if path == "" {
		return "", fmt.Errorf("no managed env path (set --memory)")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return path, err
	}
	lines, err := readEnvLines(path)
	if err != nil {
		return path, err
	}
	quoted := quoteEnvValue(value)
	replaced := false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		export := false
		if strings.HasPrefix(trim, "export ") {
			export = true
			trim = strings.TrimSpace(strings.TrimPrefix(trim, "export "))
		}
		eq := strings.IndexByte(trim, '=')
		if eq <= 0 {
			continue
		}
		if strings.TrimSpace(trim[:eq]) != name {
			continue
		}
		if export {
			lines[i] = "export " + name + "=" + quoted
		} else {
			lines[i] = name + "=" + quoted
		}
		replaced = true
		break
	}
	if !replaced {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, name+"="+quoted)
	}
	if err := writeEnvLines(path, lines); err != nil {
		return path, err
	}
	InvalidateEnvOverlay()
	return path, nil
}

// DeleteManagedEnv removes KEY from the managed env file.
func DeleteManagedEnv(name string) (path string, err error) {
	if err := ValidateEnvKey(name); err != nil {
		return "", err
	}
	path = ManagedEnvPath()
	if path == "" {
		return "", fmt.Errorf("no managed env path (set --memory)")
	}
	lines, err := readEnvLines(path)
	if err != nil {
		return path, err
	}
	out := make([]string, 0, len(lines))
	removed := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		check := trim
		if strings.HasPrefix(check, "export ") {
			check = strings.TrimSpace(strings.TrimPrefix(check, "export "))
		}
		if eq := strings.IndexByte(check, '='); eq > 0 {
			if strings.TrimSpace(check[:eq]) == name {
				removed = true
				continue
			}
		}
		out = append(out, line)
	}
	if !removed {
		return path, fmt.Errorf("key %q not found in managed env file", name)
	}
	if err := writeEnvLines(path, out); err != nil {
		return path, err
	}
	InvalidateEnvOverlay()
	return path, nil
}

func readEnvLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{
				"# Marble operator secrets (Settings → Secrets)",
				"# Mode 0600. Not injected into model context. Not stored in SQLite.",
				"",
			}, nil
		}
		return nil, err
	}
	raw := strings.ReplaceAll(string(b), "\r\n", "\n")
	return strings.Split(raw, "\n"), nil
}

func writeEnvLines(path string, lines []string) error {
	// Ensure trailing newline
	body := strings.Join(lines, "\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func quoteEnvValue(val string) string {
	// Always double-quote so spaces / # / = are safe
	esc := strings.ReplaceAll(val, `\`, `\\`)
	esc = strings.ReplaceAll(esc, `"`, `\"`)
	return `"` + esc + `"`
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
		pathHint = "$MEMORY/env"
	}
	return "Env " + strings.Join(names, "/") + " not set in process or $MEMORY/env. Add KEY=… via Settings → Secrets (or edit " +
		pathHint + "; applies within seconds for catalog models). " +
		"If the var exists only in process env from an old EnvironmentFile, restart marble-harness after clearing/updating it."
}
