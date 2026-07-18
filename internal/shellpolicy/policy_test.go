package shellpolicy

import (
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
