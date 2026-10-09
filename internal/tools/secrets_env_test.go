package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/shellpolicy"
)

// Field report 2026-10-09: an agent needed MAIL_KEY_BLAIR in a shell, tried to source
// $MEMORY/env, got blocked, and proposed pasting the key into chat or copying it into the
// repo. The secret should simply be in the shell's environment, masked in output.
func TestShellSeesSecretsAndOutputIsMasked(t *testing.T) {
	ws := t.TempDir()
	mem := t.TempDir()
	if err := os.WriteFile(filepath.Join(mem, config.RelMemoryEnv), []byte("MAIL_KEY_BLAIR=fmu1-blair-secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config.SetMemoryDirForEnv(mem)
	t.Cleanup(func() { config.SetMemoryDirForEnv(""); config.InvalidateEnvOverlay() })

	pol := shellpolicy.New(ws, mem, false, 5*time.Second, 10*time.Second)
	r := &Registry{Workspace: ws, MaxResultChars: 10000, Policy: pol}

	run := func(cmd string) string {
		return r.Execute("shell_execute", fmt.Sprintf(`{"command":%q}`, cmd), nil)
	}
	if out := run(`[ -n "$MAIL_KEY_BLAIR" ] && echo present-len-${#MAIL_KEY_BLAIR}`); !strings.Contains(out, "present-len-23") {
		t.Fatalf("secret not in shell env: %s", out)
	}
	out := run(`echo "key=$MAIL_KEY_BLAIR"`)
	if strings.Contains(out, "fmu1-blair-secret-value") || !strings.Contains(out, "key=[secret:MAIL_KEY_BLAIR]") {
		t.Fatalf("secret value must be masked in the tool result: %s", out)
	}

	// Sourcing the store stays blocked, but the error now says what to do instead.
	out = run(`. ` + filepath.Join(mem, "env"))
	if !strings.Contains(out, "blocked") || !strings.Contains(out, "$NAME") || !strings.Contains(out, "Available: MAIL_KEY_BLAIR") {
		t.Fatalf("blocked message should point at the env var: %s", out)
	}
}
