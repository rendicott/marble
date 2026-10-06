package shellpolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDenyCatastrophicButAllowNormalRm(t *testing.T) {
	p := New("/tmp/ws", "/tmp/mem", false, 60*time.Second, 300*time.Second)
	if err := p.Check("rm file.txt", "."); err != nil {
		t.Fatalf("normal rm should be allowed: %v", err)
	}
	if err := p.Check("rm -rf /", "."); err == nil {
		t.Fatal("expected deny for rm -rf /")
	}
	if err := p.Check("sudo ls", "."); err == nil {
		t.Fatal("expected deny for sudo")
	}
}

func TestDisableCLI(t *testing.T) {
	p := New("/tmp/ws", "/tmp/mem", true, 60*time.Second, 300*time.Second)
	if err := p.Check("echo hi", "."); err == nil {
		t.Fatal("expected disabled")
	}
}

func TestClampTimeout(t *testing.T) {
	p := New("/tmp/ws", "/tmp/mem", false, 60*time.Second, 300*time.Second)
	d, _ := p.ClampTimeout(0)
	if d != 60*time.Second {
		t.Fatalf("default: %v", d)
	}
	d, hint := p.ClampTimeout(600)
	if d != 300*time.Second {
		t.Fatalf("clamp: %v", d)
	}
	if hint == "" {
		t.Fatal("expected hint for clamp")
	}
}

func TestMemoryRootForms(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	root := filepath.Join(home, ".marble-policytest")
	p := New("/tmp/ws", root, false, 60*time.Second, 300*time.Second)
	blocked := []string{
		"cat " + root + "/env",
		"cat ~/.marble-policytest/env",
		"cat $HOME/.marble-policytest/env",
		"cat ${HOME}/.marble-policytest/env",
		`base64 < "$HOME/.marble-""policytest/env"`,
		`cat ~/.marble-'policy'test/env`,
		`cat ~/.marble-\policytest/env`,
	}
	for _, c := range blocked {
		err := p.Check(c, ".")
		if err == nil {
			t.Errorf("expected memory block: %s", c)
			continue
		}
		if !strings.Contains(err.Error(), "not retryable") {
			t.Errorf("block should say not retryable: %v", err)
		}
	}
	for _, c := range []string{"cat ~/.marble-other/env", "ls $HOME", "echo hi"} {
		if err := p.Check(c, "."); err != nil {
			t.Errorf("unexpected block for %q: %v", c, err)
		}
	}
}

func TestPolicyBlocksSayNotRetryable(t *testing.T) {
	p := New("/tmp/ws", "/tmp/mem", false, 60*time.Second, 300*time.Second)
	if err := p.Check("sudo ls", "."); err == nil || !strings.Contains(err.Error(), "not retryable") {
		t.Fatalf("deny block: %v", err)
	}
}
