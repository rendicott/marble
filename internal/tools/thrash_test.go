package tools

import (
	"strings"
	"testing"
)

func TestIsPureSleepCommand(t *testing.T) {
	yes := []string{"sleep 2", "sleep 3", "  timeout 5  ", "sleep 1.5", "SLEEP 2"}
	no := []string{"sleep 2 && echo hi", "echo sleep 2", "python -c 'import time; time.sleep(2)'", "ls"}
	for _, c := range yes {
		if !IsPureSleepCommand(c) {
			t.Errorf("expected pure sleep: %q", c)
		}
	}
	for _, c := range no {
		if IsPureSleepCommand(c) {
			t.Errorf("expected not pure sleep: %q", c)
		}
	}
}

func TestAntiRepeatConsecutive(t *testing.T) {
	r := &Registry{
		Thrash:    ThrashPolicy{AntiRepeatN: 3, StuckEscalateK: 3, BlockSleepShell: true, EvalMutateMax: 5},
		ThrashSet: true,
	}
	tc := &TurnContext{ReadPaths: map[string]bool{}}
	args := `{"command":"echo hello"}`

	// First two OK
	for i := 0; i < 2; i++ {
		if err := r.preflightThrash("shell_execute", args, tc); err != nil {
			t.Fatalf("iter %d preflight: %v", i, err)
		}
		r.postflightThrash("shell_execute", args, "ok", tc)
	}
	// Third consecutive same fingerprint → block
	err := r.preflightThrash("shell_execute", args, tc)
	if err == nil || !strings.Contains(err.Error(), "anti-repeat") {
		t.Fatalf("expected anti-repeat, got %v", err)
	}
}

func TestSleepBlocked(t *testing.T) {
	r := &Registry{
		Thrash:    DefaultThrashPolicy(),
		ThrashSet: true,
	}
	tc := &TurnContext{ReadPaths: map[string]bool{}}
	err := r.preflightThrash("shell_execute", `{"command":"sleep 2"}`, tc)
	if err == nil || !strings.Contains(err.Error(), "sleep-only") {
		t.Fatalf("expected sleep block: %v", err)
	}
}

func TestEscalateLockBlocksClick(t *testing.T) {
	r := &Registry{
		Thrash:    DefaultThrashPolicy(),
		ThrashSet: true,
	}
	tc := &TurnContext{ReadPaths: map[string]bool{}, Thrash: &ThrashState{EscalateLock: true}}
	err := r.preflightThrash("computer_desktop_act", `{"action":"click","x":10,"y":20,"button":"1"}`, tc)
	if err == nil || !strings.Contains(err.Error(), "escalate lock") {
		t.Fatalf("expected escalate lock: %v", err)
	}
	// screenshot still allowed
	if err := r.preflightThrash("computer_screenshot", `{}`, tc); err != nil {
		t.Fatalf("screenshot should be allowed: %v", err)
	}
}

func TestComputerFailStreakEscalate(t *testing.T) {
	r := &Registry{
		Thrash:    DefaultThrashPolicy(),
		ThrashSet: true,
	}
	tc := &TurnContext{ReadPaths: map[string]bool{}}
	for i := 0; i < 3; i++ {
		// different args so anti-repeat doesn't fire first
		args := `{"action":"click_text","text":"foo` + string(rune('a'+i)) + `"}`
		r.postflightThrash("computer_browser_act", args, "error: not_found", tc)
	}
	if tc.Thrash == nil || !tc.Thrash.EscalateLock {
		t.Fatal("expected escalate lock after 3 computer failures")
	}
}

func TestIsEvalMutate(t *testing.T) {
	if !isEvalMutate(`document.querySelector('x').click()`) {
		t.Fatal("click should be mutate")
	}
	if isEvalMutate(`return {url: location.href, text: document.body.innerText}`) {
		t.Fatal("read-only should not be mutate")
	}
}

func TestFingerprintDesktopClick(t *testing.T) {
	a := ToolFingerprint("computer_desktop_act", `{"action":"click","x":100,"y":200,"button":"1","computer_id":"rinux"}`)
	b := ToolFingerprint("computer_desktop_act", `{"action":"click","x":100,"y":200,"button":"1","computer_id":"other"}`)
	if a != b {
		t.Fatalf("computer_id should be ignored: %q vs %q", a, b)
	}
}

func TestContinuePacket(t *testing.T) {
	tc := &TurnContext{Thrash: &ThrashState{
		LastURL:     "https://example.com",
		LastFailure: "not_found",
		ToolNames:   []string{"computer_screenshot", "computer_desktop_act"},
		BanList:     []string{"shell_execute\x00{}"},
		EscalateLock: true,
	}}
	p := tc.ContinuePacket("near max iters")
	for _, want := range []string{"example.com", "not_found", "computer_screenshot", "Ban", "Escalate", "near max"} {
		if !strings.Contains(p, want) {
			t.Errorf("packet missing %q:\n%s", want, p)
		}
	}
}

func TestComputerHeavyShare(t *testing.T) {
	tc := &TurnContext{Thrash: &ThrashState{
		ToolNames: []string{
			"computer_screenshot", "computer_desktop_act", "shell_execute", "computer_browser_act",
		},
	}}
	// 3/4 = 0.75
	if s := tc.ComputerHeavyShare(20); s < 0.7 || s > 0.8 {
		t.Fatalf("share %v", s)
	}
}

func TestCallAgentPollExemptFromAntiRepeat(t *testing.T) {
	// Explicitly enable anti-repeat for this test (default is off).
	pol := DefaultThrashPolicy()
	pol.AntiRepeatN = 3
	r := &Registry{Thrash: pol, ThrashSet: true}
	tc := &TurnContext{ReadPaths: map[string]bool{}}
	args := `{"task_id":"0wc72t6c2z"}`
	for i := 0; i < 10; i++ {
		if err := r.preflightThrash("call_agent_process", args, tc); err != nil {
			t.Fatalf("poll %d should be exempt: %v", i, err)
		}
		r.postflightThrash("call_agent_process", args, `{"status":"running"}`, tc)
	}
	// Starting the same agent prompt thrice should still trip when anti-repeat is on
	start := `{"format":"grok","prompt":"do the same work again please"}`
	for i := 0; i < 2; i++ {
		if err := r.preflightThrash("call_agent_process", start, tc); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		r.postflightThrash("call_agent_process", start, `{"ok":true}`, tc)
	}
	if err := r.preflightThrash("call_agent_process", start, tc); err == nil || !strings.Contains(err.Error(), "anti-repeat") {
		t.Fatalf("identical start should anti-repeat: %v", err)
	}
}

func TestAntiRepeatOffByDefault(t *testing.T) {
	r := &Registry{Thrash: DefaultThrashPolicy(), ThrashSet: true}
	if r.Thrash.AntiRepeatN != 0 {
		t.Fatalf("default AntiRepeatN want 0 got %d", r.Thrash.AntiRepeatN)
	}
	tc := &TurnContext{ReadPaths: map[string]bool{}}
	args := `{"command":"curl -sS http://127.0.0.1/health"}`
	for i := 0; i < 5; i++ {
		if err := r.preflightThrash("shell_execute", args, tc); err != nil {
			t.Fatalf("identical poll should be allowed when anti-repeat off: %v", err)
		}
		r.postflightThrash("shell_execute", args, "ok", tc)
	}
}
