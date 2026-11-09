package agentproc

import (
	"strings"
	"testing"

	"github.com/rendicott/marble/internal/config"
)

// TestNormalizeEnvMode pins the parser: default is subscription, aliases resolve to the
// caller's intent, and a typo is an error (never a silent inherit).
func TestNormalizeEnvMode(t *testing.T) {
	cases := map[string]string{
		"":             EnvModeSubscription,
		"subscription": EnvModeSubscription,
		"SUBSCRIPTION": EnvModeSubscription,
		" login ":      EnvModeSubscription,
		"isolated":     EnvModeSubscription,
		"inherit":      EnvModeInherit,
		"all":          EnvModeInherit,
		"api_keys":     EnvModeInherit,
	}
	for in, want := range cases {
		got, err := NormalizeEnvMode(in)
		if err != nil {
			t.Fatalf("%q: unexpected error %v", in, err)
		}
		if got != want {
			t.Fatalf("%q: got %q want %q", in, got, want)
		}
	}
	for _, bad := range []string{"keysplease", "everythng", "yolo"} {
		if _, err := NormalizeEnvMode(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		} else if !strings.Contains(err.Error(), "inherit") || !strings.Contains(err.Error(), "subscription") {
			t.Fatalf("%q: error should name both modes: %v", bad, err)
		}
	}
}

// TestMergeEnvModeKeepsKeysOptional is the point of the flag: withholding is the default,
// but an explicit inherit restores the full environment so a child can bill an API key.
func TestMergeEnvModeKeepsKeysOptional(t *testing.T) {
	config.SetMemoryDirForEnv(t.TempDir())
	t.Cleanup(func() { config.SetMemoryDirForEnv(""); config.InvalidateEnvOverlay() })
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-optional-0001")
	t.Setenv("XAI_API_KEY", "xai-optional-0002")
	t.Setenv("ANTHROPIC_BASE_URL", "https://proxy.invalid")

	sub := envSliceMap(mergeEnv(nil, EnvModeSubscription))
	if _, ok := sub["ANTHROPIC_API_KEY"]; ok {
		t.Fatal("subscription mode must withhold ANTHROPIC_API_KEY")
	}
	if _, ok := sub["XAI_API_KEY"]; ok {
		t.Fatal("subscription mode must withhold XAI_API_KEY")
	}
	if _, ok := sub["ANTHROPIC_BASE_URL"]; ok {
		t.Fatal("subscription mode must withhold endpoint overrides")
	}

	inh := envSliceMap(mergeEnv(nil, EnvModeInherit))
	if got := inh["ANTHROPIC_API_KEY"]; got != "sk-ant-optional-0001" {
		t.Fatalf("inherit mode must pass API keys: %q", got)
	}
	if got := inh["XAI_API_KEY"]; got != "xai-optional-0002" {
		t.Fatalf("inherit mode must pass API keys: %q", got)
	}

	// Driver extras win in both modes: naming a key in Settings → Agents is an opt-in.
	explicit := envSliceMap(mergeEnv(map[string]string{"ANTHROPIC_API_KEY": "explicit-key"}, EnvModeSubscription))
	if got := explicit["ANTHROPIC_API_KEY"]; got != "explicit-key" {
		t.Fatalf("driver extras must apply verbatim: %q", got)
	}
}

func envSliceMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}

// TestAgentEnvModeFallback: an invalid persisted value must degrade to subscription.
func TestAgentEnvModeFallback(t *testing.T) {
	if got := (Config{EnvMode: "inherit"}).AgentEnvMode(); got != EnvModeInherit {
		t.Fatalf("got %q", got)
	}
	for _, in := range []string{"", "nonsense"} {
		if got := (Config{EnvMode: in}).AgentEnvMode(); got != EnvModeSubscription {
			t.Fatalf("%q: got %q want subscription", in, got)
		}
	}
	if got := DefaultConfig().AgentEnvMode(); got != EnvModeSubscription {
		t.Fatalf("default config must be subscription, got %q", got)
	}
}
