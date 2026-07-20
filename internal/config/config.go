package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config holds runtime settings for marble-harness.
type Config struct {
	BaseURL        string
	Model          string
	ContextLimit   int
	MaxOutput      int
	ContextReserve int
	Workspace      string
	Memory         string
	// MemoryCreated is true when --memory did not exist and was created at launch.
	MemoryCreated bool
	PersistEvery  time.Duration
	Addr          string
	// MaxToolIters is the hard stop for tool rounds per user turn (ADR-0005 Q1/Q30).
	MaxToolIters int
	// ToolRoundSoft is the soft advisory threshold (ADR-0005 Q1).
	ToolRoundSoft int
	// SoftWall is continuous tool-round wall clock before advisory (ADR-0005 Q2).
	SoftWall time.Duration
	// ContextWarnRatio soft-warns when usage_ratio >= this (default 0.60).
	ContextWarnRatio float64
	// ContextAutoCompactRatio auto-compact trigger (default 0.85).
	ContextAutoCompactRatio float64
	// ContextAutoCompactRounds consecutive high-usage rounds before auto compact.
	ContextAutoCompactRounds int
	MaxToolResult            int
	// DisableShell forces shell tools off regardless of DB settings.
	DisableShell bool
	// ShellDefaultTimeout / ShellMaxTimeout for shell_execute.
	ShellDefaultTimeout time.Duration
	ShellMaxTimeout     time.Duration
	// MCP (ADR-0006)
	MCPConfig  string // --mcp-config path (empty → $MEMORY/mcp.json)
	MCPDisable bool
	MCPTimeout time.Duration
	// APIKeyEnv is the raw --api-key-env flag (comma-separated env var names). ADR-0016.
	APIKeyEnv string
	// APIKey is the resolved secret at launch (never log). Empty → no model Authorization.
	APIKey string
	// APIKeyEnvUsed is the env var name that supplied APIKey (for health/Settings display).
	APIKeyEnvUsed string
	// APIKeyEnvConfigured is true when a non-empty key was resolved.
	APIKeyEnvConfigured bool

	// Auth / OAuth (ADR-0017)
	OAuthClientID         string
	OAuthClientSecretEnv  string
	OAuthClientSecret     string // resolved; never log
	OAuthRedirectURL      string
	OAuthAllowEmails      string // comma-separated raw flag
	OAuthAllowFile        string
	// AuthMode is "open" or "google" after validation.
	AuthMode string
	// AuthAllowlist is normalized lowercase emails.
	AuthAllowlist []string

	// TLS (ADR-0017)
	TLSCertFile string
	TLSKeyFile  string
}

// Budget returns the maximum estimated tokens allowed for prompt material
// (system + tools + messages) before a completion call.
func (c Config) Budget() int {
	b := c.ContextLimit - c.MaxOutput - c.ContextReserve
	if b < 1024 {
		return 1024
	}
	return b
}

// UsageRatio returns est_prompt / budget (ADR-0005 Q5).
func (c Config) UsageRatio(estPrompt int) float64 {
	b := c.Budget()
	if b <= 0 {
		return 1
	}
	return float64(estPrompt) / float64(b)
}

// ParseFlags reads CLI flags into Config.
func ParseFlags(args []string) (Config, error) {
	fs := flag.NewFlagSet("marble-harness", flag.ContinueOnError)
	cfg := Config{}
	home, _ := os.UserHomeDir()
	defaultMemory := filepath.Join(home, ".marble")

	fs.StringVar(&cfg.BaseURL, "base-url", "http://127.0.0.1:8000/v1", "OpenAI-compatible API base URL")
	fs.StringVar(&cfg.Model, "model", "Qwen/Qwen3.5-122B-A10B-FP8", "Model id")
	fs.StringVar(&cfg.APIKeyEnv, "api-key-env", "", "Env var name(s) for model API key (comma-separated; first non-empty wins). Empty = no auth (ADR-0016)")
	fs.IntVar(&cfg.ContextLimit, "context-limit", 262144, "Total model context window (tokens)")
	fs.IntVar(&cfg.MaxOutput, "max-output", 32768, "Max generation tokens per model call")
	fs.IntVar(&cfg.ContextReserve, "context-reserve", 8192, "Reserved tokens for tool schemas / formatting")
	fs.StringVar(&cfg.Workspace, "workspace", ".", "Filesystem root for tools")
	fs.StringVar(&cfg.Memory, "memory", defaultMemory, "Memory leaf root (session/ and daily/ live here; may be outside workspace)")
	fs.DurationVar(&cfg.PersistEvery, "persist-interval", 5*time.Minute, "Background session persist interval")
	fs.StringVar(&cfg.Addr, "addr", ":8080", "HTTP listen address")
	fs.IntVar(&cfg.MaxToolIters, "max-tool-iters", 80, "Hard max tool-call rounds per user turn")
	fs.IntVar(&cfg.ToolRoundSoft, "tool-round-soft", 65, "Soft advisory tool-round threshold")
	fs.DurationVar(&cfg.SoftWall, "soft-wall", 3*time.Minute, "Soft wall-clock for continuous tool rounds without final reply")
	fs.IntVar(&cfg.MaxToolResult, "max-tool-result", 32000, "Max characters returned from a single tool call")
	fs.BoolVar(&cfg.DisableShell, "disable-shell", false, "Disable shell_execute and background shell tasks")
	fs.DurationVar(&cfg.ShellDefaultTimeout, "shell-default-timeout", 60*time.Second, "Default shell_execute timeout")
	fs.DurationVar(&cfg.ShellMaxTimeout, "shell-max-timeout", 5*time.Minute, "Max shell_execute timeout")
	fs.StringVar(&cfg.MCPConfig, "mcp-config", "", "Path to mcp.json (default: $MEMORY/mcp.json)")
	fs.BoolVar(&cfg.MCPDisable, "mcp-disable", false, "Disable MCP client entirely")
	fs.DurationVar(&cfg.MCPTimeout, "mcp-timeout", 60*time.Second, "Default timeout for MCP tool/resource/prompt calls")

	// ADR-0017 Google OAuth
	fs.StringVar(&cfg.OAuthClientID, "oauth-client-id", "", "Google OAuth client ID (enables google auth mode when fully configured)")
	fs.StringVar(&cfg.OAuthClientSecretEnv, "oauth-client-secret-env", "", "Env var name holding Google OAuth client secret")
	fs.StringVar(&cfg.OAuthRedirectURL, "oauth-redirect-url", "", "OAuth redirect URL registered in Google Console (e.g. https://host:8080/auth/callback)")
	fs.StringVar(&cfg.OAuthAllowEmails, "oauth-allow-emails", "", "Comma-separated allowlisted emails (union with --oauth-allow-file)")
	fs.StringVar(&cfg.OAuthAllowFile, "oauth-allow-file", "", "Path to allowlist file (one email per line, # comments)")
	// ADR-0017 TLS
	fs.StringVar(&cfg.TLSCertFile, "tls-cert-file", "", "Path to TLS certificate PEM (optional HTTPS)")
	fs.StringVar(&cfg.TLSKeyFile, "tls-key-file", "", "Path to TLS private key PEM")

	// Defaults not exposed as flags (ADR-0005 locked ratios)
	cfg.ContextWarnRatio = 0.60
	cfg.ContextAutoCompactRatio = 0.85
	cfg.ContextAutoCompactRounds = 3

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	cfg.resolveAPIKey()
	if err := cfg.resolveAuthAndTLS(); err != nil {
		return Config{}, err
	}
	if cfg.ContextLimit <= 0 || cfg.MaxOutput <= 0 {
		return Config{}, fmt.Errorf("context-limit and max-output must be positive")
	}
	if cfg.PersistEvery <= 0 {
		return Config{}, fmt.Errorf("persist-interval must be positive")
	}
	if cfg.MaxToolIters < 1 {
		return Config{}, fmt.Errorf("max-tool-iters must be positive")
	}
	if cfg.ToolRoundSoft < 1 {
		cfg.ToolRoundSoft = cfg.MaxToolIters
	}
	if cfg.ToolRoundSoft > cfg.MaxToolIters {
		cfg.ToolRoundSoft = cfg.MaxToolIters
	}

	absWS, err := filepath.Abs(cfg.Workspace)
	if err != nil {
		return Config{}, fmt.Errorf("workspace: %w", err)
	}
	info, err := os.Stat(absWS)
	if err != nil {
		return Config{}, fmt.Errorf("workspace: %w", err)
	}
	if !info.IsDir() {
		return Config{}, fmt.Errorf("workspace is not a directory: %s", absWS)
	}
	cfg.Workspace = absWS

	absMem, created, err := resolveMemoryDir(cfg.Memory)
	if err != nil {
		return Config{}, err
	}
	cfg.Memory = absMem
	cfg.MemoryCreated = created
	return cfg, nil
}

// resolveAPIKey reads --api-key-env names from the process environment (ADR-0016).
// Never logs the secret. First non-empty env value wins.
func (c *Config) resolveAPIKey() {
	c.APIKey = ""
	c.APIKeyEnvUsed = ""
	c.APIKeyEnvConfigured = false
	raw := strings.TrimSpace(c.APIKeyEnv)
	if raw == "" {
		return
	}
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		val := strings.TrimSpace(os.Getenv(name))
		if val == "" {
			continue
		}
		c.APIKey = val
		c.APIKeyEnvUsed = name
		c.APIKeyEnvConfigured = true
		return
	}
}

// ModelAuthPublic returns safe fields for health / Settings (never the secret).
func (c Config) ModelAuthPublic() map[string]interface{} {
	mode := "none"
	if strings.TrimSpace(c.APIKeyEnv) != "" {
		mode = "env"
	}
	out := map[string]interface{}{
		"model_auth":            mode,
		"model_auth_env":        strings.TrimSpace(c.APIKeyEnv),
		"model_auth_env_used":   c.APIKeyEnvUsed,
		"model_auth_configured": c.APIKeyEnvConfigured,
	}
	return out
}

// AuthPublicHealth fields safe for public GET /api/health (ADR-0017 Q8/Q14).
func (c Config) AuthPublicHealth() map[string]interface{} {
	mode := c.AuthMode
	if mode == "" {
		mode = "open"
	}
	out := map[string]interface{}{
		"auth_mode":     mode,
		"auth_accounts": len(c.AuthAllowlist),
		"tls_enabled":   c.TLSEnabled(),
	}
	return out
}

// AuthPublicSettings fields for authenticated Settings UI (includes allowlist emails).
func (c Config) AuthPublicSettings() map[string]interface{} {
	mode := c.AuthMode
	if mode == "" {
		mode = "open"
	}
	emails := c.AuthAllowlist
	if emails == nil {
		emails = []string{}
	}
	return map[string]interface{}{
		"auth_mode":           mode,
		"oauth_client_id":     c.OAuthClientID,
		"oauth_redirect_url":  c.OAuthRedirectURL,
		"oauth_allow_emails":  emails,
		"oauth_allow_file":    c.OAuthAllowFile,
		"auth_accounts":       len(emails),
		"tls_enabled":         c.TLSEnabled(),
		"tls_cert_file":       c.TLSCertFile,
		"tls_key_file_set":    strings.TrimSpace(c.TLSKeyFile) != "",
	}
}

// TLSEnabled reports whether both cert and key paths are set.
func (c Config) TLSEnabled() bool {
	return strings.TrimSpace(c.TLSCertFile) != "" && strings.TrimSpace(c.TLSKeyFile) != ""
}

// CookieSecure reports whether session cookies should set Secure.
func (c Config) CookieSecure() bool {
	u := strings.ToLower(strings.TrimSpace(c.OAuthRedirectURL))
	if strings.HasPrefix(u, "http://localhost") || strings.HasPrefix(u, "http://127.0.0.1") {
		return false
	}
	return strings.HasPrefix(u, "https://") || c.TLSEnabled()
}

// resolveAuthAndTLS validates OAuth + TLS flags (ADR-0017).
func (c *Config) resolveAuthAndTLS() error {
	c.AuthMode = "open"
	c.AuthAllowlist = nil
	c.OAuthClientSecret = ""

	cid := strings.TrimSpace(c.OAuthClientID)
	secEnv := strings.TrimSpace(c.OAuthClientSecretEnv)
	redir := strings.TrimSpace(c.OAuthRedirectURL)
	emailsFlag := strings.TrimSpace(c.OAuthAllowEmails)
	fileFlag := strings.TrimSpace(c.OAuthAllowFile)

	anyOAuth := cid != "" || secEnv != "" || redir != "" || emailsFlag != "" || fileFlag != ""
	if anyOAuth {
		if cid == "" || secEnv == "" || redir == "" {
			return fmt.Errorf("oauth: partial config — require --oauth-client-id, --oauth-client-secret-env, --oauth-redirect-url, and allowlist (emails and/or file)")
		}
		secret := strings.TrimSpace(os.Getenv(secEnv))
		if secret == "" {
			return fmt.Errorf("oauth: env %q is empty (set client secret)", secEnv)
		}
		c.OAuthClientSecret = secret
		list, err := loadAllowlist(emailsFlag, fileFlag)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return fmt.Errorf("oauth: allowlist is empty (use --oauth-allow-emails and/or --oauth-allow-file)")
		}
		c.AuthAllowlist = list
		c.AuthMode = "google"
		c.OAuthClientID = cid
		c.OAuthRedirectURL = redir
		c.OAuthClientSecretEnv = secEnv
	}

	cert := strings.TrimSpace(c.TLSCertFile)
	key := strings.TrimSpace(c.TLSKeyFile)
	if (cert == "") != (key == "") {
		return fmt.Errorf("tls: require both --tls-cert-file and --tls-key-file (or neither)")
	}
	if cert != "" {
		absC, err := filepath.Abs(cert)
		if err != nil {
			return fmt.Errorf("tls cert: %w", err)
		}
		absK, err := filepath.Abs(key)
		if err != nil {
			return fmt.Errorf("tls key: %w", err)
		}
		if _, err := os.Stat(absC); err != nil {
			return fmt.Errorf("tls cert: %w", err)
		}
		if _, err := os.Stat(absK); err != nil {
			return fmt.Errorf("tls key: %w", err)
		}
		c.TLSCertFile = absC
		c.TLSKeyFile = absK
	}
	return nil
}

func loadAllowlist(emailsCSV, filePath string) ([]string, error) {
	seen := map[string]struct{}{}
	var out []string
	add := func(e string) {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			return
		}
		if _, ok := seen[e]; ok {
			return
		}
		seen[e] = struct{}{}
		out = append(out, e)
	}
	for _, p := range strings.Split(emailsCSV, ",") {
		add(p)
	}
	filePath = strings.TrimSpace(filePath)
	if filePath != "" {
		b, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("oauth allow-file: %w", err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			// strip inline comments
			if i := strings.Index(line, "#"); i >= 0 {
				line = strings.TrimSpace(line[:i])
			}
			add(line)
		}
	}
	return out, nil
}

// resolveMemoryDir ensures --memory is an absolute directory.
// If it does not exist, it is created (caller should warn). If it exists but is
// not a directory, or creation fails, an error is returned.
func resolveMemoryDir(path string) (abs string, created bool, err error) {
	if strings.TrimSpace(path) == "" {
		return "", false, fmt.Errorf("memory: path is empty")
	}
	abs, err = filepath.Abs(path)
	if err != nil {
		return "", false, fmt.Errorf("memory: %w", err)
	}
	st, err := os.Stat(abs)
	if err == nil {
		if !st.IsDir() {
			return "", false, fmt.Errorf("memory: path exists but is not a directory: %s", abs)
		}
		return abs, false, nil
	}
	if !os.IsNotExist(err) {
		return "", false, fmt.Errorf("memory: cannot stat %s: %w", abs, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", false, fmt.Errorf("memory: directory does not exist and could not be created: %s: %w", abs, err)
	}
	st, err = os.Stat(abs)
	if err != nil || !st.IsDir() {
		return "", false, fmt.Errorf("memory: directory still missing after create: %s", abs)
	}
	return abs, true, nil
}
