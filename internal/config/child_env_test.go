package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withManagedEnv(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, RelMemoryEnv)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	SetMemoryDirForEnv(dir)
	t.Cleanup(func() { SetMemoryDirForEnv(""); InvalidateEnvOverlay() })
	return p
}

func envMap(env []string) map[string][]string {
	m := map[string][]string{}
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		m[kv[:i]] = append(m[kv[:i]], kv[i+1:])
	}
	return m
}

// The field case: a key added in Settings → Secrets after the harness started must reach
// shells without a restart, and a rotated key must replace the boot-time copy.
func TestChildEnvAppliesManagedSecretsLive(t *testing.T) {
	t.Setenv("MAIL_KEY_ROTATED", "old-value-from-boot")
	p := withManagedEnv(t, "MAIL_KEY_ROTATED=new-value-1234\n")

	m := envMap(ChildEnv(nil))
	if got := m["MAIL_KEY_ROTATED"]; len(got) != 1 || got[0] != "new-value-1234" {
		t.Fatalf("managed value must replace the stale process copy exactly once: %v", got)
	}
	if len(m["PATH"]) != 1 {
		t.Fatal("parent env (PATH) must pass through")
	}

	if err := os.WriteFile(p, []byte("MAIL_KEY_ROTATED=new-value-1234\nMAIL_KEY_BLAIR=added-after-start\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	InvalidateEnvOverlay() // stands in for the 2s overlay TTL
	m = envMap(ChildEnv(map[string]string{"MAIL_KEY_ROTATED": "driver-extra"}))
	if got := m["MAIL_KEY_BLAIR"]; len(got) != 1 || got[0] != "added-after-start" {
		t.Fatalf("secret added after start: %v", got)
	}
	if got := m["MAIL_KEY_ROTATED"]; len(got) != 1 || got[0] != "driver-extra" {
		t.Fatalf("explicit extras win over the overlay: %v", got)
	}
	if names := SecretNames(); strings.Join(names, ",") != "MAIL_KEY_BLAIR,MAIL_KEY_ROTATED" {
		t.Fatalf("names %v", names)
	}
}

// Agent subprocesses must fall back to their own logged-in subscription credentials. A key
// in the environment flips Claude Code to metered API billing ("Credit balance is too low")
// and xAI's CLI reads a different name than Marble exports, so it reports "Not signed in".
func TestChildEnvForAgentWithholdsCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-from-systemd-0001")
	t.Setenv("XAI_API_KEY", "xai-from-systemd-0002")
	t.Setenv("ANTHROPIC_BASE_URL", "https://proxy.invalid")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-sub-0007")
	t.Setenv("HARMLESS_FLAG", "keep-me")
	withManagedEnv(t, "GROK_API_KEY=grok-managed-0003\nGEMINI_API_KEY=gem-managed-0004\nSESSION_OAUTH_TOKEN=oauth-0005\nMAIL_KEY=fmu1-managed-0006\n")

	agent := envMap(ChildEnvForAgent(nil))
	for _, banned := range []string{"ANTHROPIC_API_KEY", "XAI_API_KEY", "ANTHROPIC_BASE_URL",
		"CLAUDE_CODE_USE_BEDROCK", "GROK_API_KEY", "GEMINI_API_KEY", "SESSION_OAUTH_TOKEN"} {
		if _, ok := agent[banned]; ok {
			t.Fatalf("agent env must not carry %s", banned)
		}
	}
	if got := agent["HARMLESS_FLAG"]; len(got) != 1 || got[0] != "keep-me" {
		t.Fatalf("non-credential vars pass through: %v", got)
	}
	// A subscription credential is not key billing: a setup-token login has nothing else.
	if got := agent["CLAUDE_CODE_OAUTH_TOKEN"]; len(got) != 1 || got[0] != "oauth-sub-0007" {
		t.Fatalf("subscription OAuth token must reach the agent: %v", got)
	}
	if got := agent["MAIL_KEY"]; len(got) != 1 || got[0] != "fmu1-managed-0006" {
		t.Fatalf("credential carrying an unrelated name still reaches the agent (it is not key-shaped): %v", got)
	}
	if len(agent["PATH"]) != 1 {
		t.Fatal("PATH must pass through so the CLI is found")
	}

	// Shells and background tasks keep the live overlay untouched.
	shell := envMap(ChildEnv(nil))
	if got := shell["ANTHROPIC_API_KEY"]; len(got) != 1 || got[0] != "sk-ant-from-systemd-0001" {
		t.Fatalf("shell env keeps secrets: %v", got)
	}
	if got := shell["GROK_API_KEY"]; len(got) != 1 {
		t.Fatal("shell env keeps managed secrets")
	}

	// An explicit per-driver extra is an operator opt-in and wins.
	explicit := envMap(ChildEnvForAgent(map[string]string{"XAI_API_KEY": "explicit-driver-key"}))
	if got := explicit["XAI_API_KEY"]; len(got) != 1 || got[0] != "explicit-driver-key" {
		t.Fatalf("driver extras are applied verbatim: %v", got)
	}
}

func TestScrubSecrets(t *testing.T) {
	t.Setenv("SYSTEMD_ONLY_API_KEY", "sk-from-systemd-xyz")
	t.Setenv("HARMLESS_PATHISH", "/usr/local/share/thing") // not credential-named, not managed
	withManagedEnv(t, "MAIL_KEY_BLAIR=fmu1-abcdef123456\nLONGER_TOKEN=fmu1-abcdef123456-extended\nFLAG=on\n")

	in := "token=fmu1-abcdef123456 long=fmu1-abcdef123456-extended sys=sk-from-systemd-xyz flag=on path=/usr/local/share/thing"
	got := ScrubSecrets(in)
	want := "token=[secret:MAIL_KEY_BLAIR] long=[secret:LONGER_TOKEN] sys=[secret:SYSTEMD_ONLY_API_KEY] flag=on path=/usr/local/share/thing"
	if got != want {
		t.Fatalf("scrub:\n got %s\nwant %s", got, want)
	}
	if ScrubSecrets("short") != "short" {
		t.Fatal("short strings pass through")
	}
}
