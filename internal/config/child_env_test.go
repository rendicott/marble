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
