package agentproc

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// staleCWD returns a cwd whose only file predates any task start.
func staleCWD(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	f := filepath.Join(dir, "old.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(f, past, past)
	return dir
}

func writeFile(t *testing.T, path, body string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

// grokFixture lays out $GROK_HOME/sessions/<enc-cwd>/<sid>/ like grok 1.0.x.
func grokFixture(t *testing.T, sid string, at time.Time) (grokHome string) {
	t.Helper()
	grokHome = t.TempDir()
	dir := filepath.Join(grokHome, "sessions", "%2Fsome%2Fcwd", sid)
	ts := at.UTC().Format(time.RFC3339Nano)
	writeFile(t, filepath.Join(dir, "events.jsonl"),
		`{"ts":"`+ts+`","type":"phase_changed","phase":"waiting_for_model"}`+"\n"+
			`{"ts":"`+ts+`","type":"first_token"}`+"\n"+
			`{"ts":"`+ts+`","type":"phase_changed","phase":"streaming_text"}`+"\n", at)
	writeFile(t, filepath.Join(dir, "summary.json"),
		`{"info":{"cwd":"/some/cwd"},"updated_at":"`+ts+`","num_messages":12}`, at)
	writeFile(t, filepath.Join(dir, "summary.json.lock"), "", at)
	return grokHome
}

// runningTask builds a task started long ago (well past any stuck window) with a tracker.
func runningTask(t *testing.T, format, sid string, env []string, cwd string) *Task {
	t.Helper()
	start := time.Now().Add(-10 * time.Minute)
	dcfg := DefaultConfig().Drivers[format]
	lt := newLiveTracker(format, sid, start, dcfg, DefaultConfig(), env, nil)
	return &Task{Format: format, Status: StatusRunning, CWD: cwd, StartedAt: start, AgentSessionID: sid, live: lt}
}

// The field failure: an agent streaming tokens but not yet writing files must not be stuck.
func TestLivenessGrokFreshSessionNotStuck(t *testing.T) {
	sid := "11111111-2222-4333-8444-555555555555"
	home := grokFixture(t, sid, time.Now())
	task := runningTask(t, "grok", sid, []string{"GROK_HOME=" + home, "HOME=" + t.TempDir()}, staleCWD(t))

	p := buildProgress(task, 5*time.Second, "", "")
	if p.StuckHint {
		t.Fatalf("stuck_hint on a fresh session: %s", p.StuckReason)
	}
	if !p.Alive || p.AliveSignal != SigSessionState || p.AliveAgeSec > 5 {
		t.Fatalf("expected alive via session_state: %+v", p)
	}
	if p.Phase != "streaming_text" {
		t.Fatalf("phase %q", p.Phase)
	}
	if p.CWDMtimeChanged {
		t.Fatal("cwd should be stale")
	}
	ss := p.Signals[SigSessionState]
	if !strings.Contains(ss.Detail, sid) || strings.Contains(ss.Detail, "4 files") {
		t.Fatalf("expected 2 files (lock skipped), got %q", ss.Detail)
	}
}

func TestLivenessClaudeFreshTranscriptNotStuck(t *testing.T) {
	sid := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	cfgDir := t.TempDir()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	// Trailing entries without a timestamp (as claude writes) must not hide the earlier one.
	writeFile(t, filepath.Join(cfgDir, "projects", "-some-cwd", sid+".jsonl"),
		`{"type":"user","timestamp":"`+ts+`","sessionId":"`+sid+`","cwd":"/some/cwd"}`+"\n"+
			`{"type":"assistant","timestamp":"`+ts+`","sessionId":"`+sid+`","cwd":"/some/cwd"}`+"\n"+
			`{"type":"atis-latch","sessionId":"`+sid+`"}`+"\n", time.Time{})
	task := runningTask(t, "claude", sid, []string{"CLAUDE_CONFIG_DIR=" + cfgDir, "HOME=" + t.TempDir()}, staleCWD(t))

	p := buildProgress(task, 5*time.Second, "", "")
	if p.StuckHint || !p.Alive || p.AliveSignal != SigSessionState {
		t.Fatalf("expected alive via transcript: %+v", p)
	}
	if !strings.Contains(p.Signals[SigSessionState].Detail, "last entry") {
		t.Fatalf("timestamp not recovered: %q", p.Signals[SigSessionState].Detail)
	}
}

func TestLivenessAllStaleIsStuckAndNamesSignals(t *testing.T) {
	sid := "11111111-2222-4333-8444-666666666666"
	old := time.Now().Add(-20 * time.Minute)
	home := grokFixture(t, sid, old)
	task := runningTask(t, "grok", sid, []string{"GROK_HOME=" + home, "HOME=" + t.TempDir()}, staleCWD(t))

	p := buildProgress(task, 5*time.Minute, "", "")
	if !p.StuckHint {
		t.Fatalf("expected stuck: %+v", p)
	}
	if p.Alive {
		t.Fatal("alive should be false past alive_window")
	}
	// Age is bounded by StartedAt (10m), not the older file mtimes.
	if p.AliveAgeSec < 590 || p.AliveAgeSec > 610 {
		t.Fatalf("alive_age_sec %d", p.AliveAgeSec)
	}
	for _, want := range []string{"no liveness advance", "session_state ", "phase=streaming_text", "cwd ", "unchanged since start", "unavailable: proc_io"} {
		if !strings.Contains(p.StuckReason, want) {
			t.Fatalf("stuck_reason missing %q: %s", want, p.StuckReason)
		}
	}
}

func TestLivenessNoSessionStateFormat(t *testing.T) {
	task := runningTask(t, "opencode", "", nil, staleCWD(t))
	p := buildProgress(task, 5*time.Minute, "", "")
	if !p.StuckHint {
		t.Fatalf("expected stuck: %+v", p)
	}
	if !strings.Contains(p.StuckReason, "session_state (no session_id_flag for format=opencode)") {
		t.Fatalf("reason should say why session_state is unavailable: %s", p.StuckReason)
	}

	// Any other live signal still rescues it: output growth.
	var out int64
	task = runningTask(t, "opencode", "", nil, staleCWD(t))
	task.live.outBytes = func() int64 { return out }
	out = 42
	p = buildProgress(task, 5*time.Minute, "", "")
	if p.StuckHint || p.AliveSignal != SigOutput || !p.Alive {
		t.Fatalf("output growth should count as alive: %+v", p)
	}
}

func TestLivenessUnresolvedStateDegradesToOtherSignals(t *testing.T) {
	// Pinned id but nothing on disk (harness changed its layout): not an error, not a kill by itself.
	sid := "11111111-2222-4333-8444-777777777777"
	task := runningTask(t, "grok", sid, []string{"GROK_HOME=" + t.TempDir(), "HOME=" + t.TempDir()}, t.TempDir())
	writeFile(t, filepath.Join(task.CWD, "new.go"), "package x", time.Time{})
	p := buildProgress(task, 5*time.Second, "", "")
	if p.StuckHint || p.AliveSignal != SigCWD {
		t.Fatalf("cwd write should keep it alive: %+v", p)
	}
	if got := p.Signals[SigSessionState]; got.Available || !strings.Contains(got.Detail, "no state matched for session "+sid) {
		t.Fatalf("%+v", got)
	}
}

func TestLivenessTruncatedLastLine(t *testing.T) {
	sid := "11111111-2222-4333-8444-888888888888"
	home := t.TempDir()
	ts := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano) // content old, mtime fresh
	writeFile(t, filepath.Join(home, "sessions", "x", sid, "events.jsonl"),
		`{"ts":"`+ts+`","type":"phase_changed","phase":"tool_execution"}`+"\n"+
			`{"ts":"2026-10-06T08:13:3`, time.Time{})
	task := runningTask(t, "grok", sid, []string{"GROK_HOME=" + home, "HOME=" + t.TempDir()}, staleCWD(t))
	p := buildProgress(task, 5*time.Second, "", "")
	if p.StuckHint || !p.Alive {
		t.Fatalf("mtime should keep it alive: %+v", p)
	}
	if p.Phase != "tool_execution" {
		t.Fatalf("phase %q", p.Phase)
	}
}

func TestTailJSONLReadsOnlyTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	var b strings.Builder
	b.WriteString(`{"ts":"2020-01-01T00:00:00Z","phase":"old"}` + "\n")
	for b.Len() < 3*stateTailBytes {
		b.WriteString(`{"type":"noise","pad":"` + strings.Repeat("x", 200) + `"}` + "\n")
	}
	b.WriteString(`{"timestamp":"2026-10-06T08:00:00Z","type":"assistant"}` + "\n")
	writeFile(t, path, b.String(), time.Time{})
	ts, phase := tailJSONL(path, int64(b.Len()))
	if ts.Format(time.RFC3339) != "2026-10-06T08:00:00Z" {
		t.Fatalf("ts %v", ts)
	}
	if phase != "" {
		t.Fatalf("phase from outside the tail leaked: %q", phase)
	}
}

func TestExpandStateGlobs(t *testing.T) {
	env := envLookup([]string{"HOME=/h", "GROK_HOME=/g", "EMPTY="})
	got := expandStateGlobs([]string{
		"$GROK_HOME/sessions/*/{session_id}",
		"~/.grok/sessions/*/{session_id}",
		"$EMPTY/x/{session_id}",
		"$DEFINITELY_UNSET_MARBLE_VAR/y",
		"/g/sessions/*/{session_id}", // duplicate after expansion
	}, "sid", env)
	want := []string{"/g/sessions/*/sid", "/h/.grok/sessions/*/sid"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v want %v", got, want)
	}
}

// fakeProc writes /proc-shaped stat and io files for pid.
func fakeProc(t *testing.T, root string, pid, ppid int, comm string, cpuTicks, ioBytes uint64) {
	t.Helper()
	dir := filepath.Join(root, fmt.Sprint(pid))
	stat := fmt.Sprintf("%d (%s) S %d %d 0 0 -1 0 0 0 0 0 %d 0 0 0 20 0 1 0 0 0 0", pid, comm, ppid, pid, cpuTicks)
	writeFile(t, filepath.Join(dir, "stat"), stat, time.Time{})
	writeFile(t, filepath.Join(dir, "io"), fmt.Sprintf("rchar: %d\nwchar: 0\nsyscr: 1\n", ioBytes), time.Time{})
}

func TestProcSignalsThresholdsAndTree(t *testing.T) {
	root := t.TempDir()
	old := procRoot
	procRoot = root
	defer func() { procRoot = old }()

	fakeProc(t, root, 100, 1, "grok", 0, 0)
	fakeProc(t, root, 999, 1, "unrelated", 0, 0)
	start := time.Now().Add(-time.Hour)
	lt := newLiveTracker("opencode", "", start, DriverConfig{}, DefaultConfig(), nil, nil)
	lt.setPID(100)

	t0 := start.Add(time.Minute)
	lt.sampleProc(t0)
	if !lt.procOK || !lt.ioAdv.IsZero() || !lt.cpuAdv.IsZero() || !lt.treeAdv.IsZero() {
		t.Fatalf("idle baseline should not advance: %+v", lt)
	}

	// Idle noise (claude-like): ~700 B/s and 8.5% CPU over 10s → below thresholds.
	fakeProc(t, root, 100, 1, "grok", 85, 7000)
	t1 := t0.Add(10 * time.Second)
	lt.sampleProc(t1)
	if !lt.ioAdv.IsZero() || !lt.cpuAdv.IsZero() {
		t.Fatalf("noise counted as activity: io=%.0f cpu=%.1f", lt.ioRate, lt.cpuPct)
	}

	// Streaming + compute: 50 KB and 300 ticks (30%) in 10s → both advance.
	fakeProc(t, root, 100, 1, "grok", 385, 57000)
	t2 := t1.Add(10 * time.Second)
	lt.sampleProc(t2)
	if !lt.ioAdv.Equal(t2) || !lt.cpuAdv.Equal(t2) {
		t.Fatalf("activity missed: io=%.0f cpu=%.1f", lt.ioRate, lt.cpuPct)
	}

	// Tool started in its own process group (ppid chain, not pgid): tree advances.
	fakeProc(t, root, 101, 100, "bash", 0, 0)
	fakeProc(t, root, 102, 101, "sleep", 0, 0)
	t3 := t2.Add(10 * time.Second)
	lt.sampleProc(t3)
	if !lt.treeAdv.Equal(t3) || len(lt.kids) != 2 || lt.kids[102] != "sleep" {
		t.Fatalf("descendants not found: %v", lt.kids)
	}
	if _, ok := lt.kids[999]; ok {
		t.Fatal("unrelated process included")
	}

	// Same tree, no change → no new tree advance.
	t4 := t3.Add(10 * time.Second)
	lt.sampleProc(t4)
	if !lt.treeAdv.Equal(t3) {
		t.Fatal("unchanged tree should not advance")
	}

	// Root gone → process signals unavailable, never "stuck" on that basis.
	if err := os.RemoveAll(filepath.Join(root, "100")); err != nil {
		t.Fatal(err)
	}
	lt.sampleProc(t4.Add(10 * time.Second))
	if lt.procOK {
		t.Fatal("expected proc signals unavailable")
	}
}

func TestReadProcTreeRealDescendantOutsideGroup(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid")
	}
	cmd := exec.Command("sh", "-c", "setsid sleep 30 & wait")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { killProcessGroupTree(cmd.Process.Pid); _ = cmd.Wait() }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tree, err := readProcTree(cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range tree.kids {
			if c == "sleep" {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("setsid'd sleep not found among descendants")
}

// killProcessGroupTree kills root and its descendants (test cleanup).
func killProcessGroupTree(root int) {
	tree, _ := readProcTree(root)
	for pid := range tree.kids {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	}
	if p, err := os.FindProcess(root); err == nil {
		_ = p.Kill()
	}
}

func TestBuildArgvPinsSessionIDOnce(t *testing.T) {
	tt := true
	sid := "0f0f0f0f-1111-4222-8333-444444444444"
	cases := []struct {
		d    Driver
		cfg  DriverConfig
		flag string
	}{
		{grokDriver{}, DriverConfig{Command: "/bin/true", AutoApprove: &tt, SessionIDFlag: "-s",
			DefaultArgs: []string{"-s", "stale-id", "--effort", "medium"}}, "-s"},
		{claudeDriver{}, DriverConfig{Command: "/bin/true", AutoApprove: &tt, SessionIDFlag: "--session-id",
			DefaultArgs: []string{"--session-id=stale-id"}}, "--session-id"},
		{opencodeDriver{}, DriverConfig{Command: "/bin/true", AutoApprove: &tt, SessionIDFlag: "--session"}, "--session"},
	}
	for _, c := range cases {
		argv, err := c.d.BuildArgv(Request{Prompt: "-s is not a flag here", CWD: "/tmp", OutputFormat: "json",
			AgentSessionID: sid, ExtraArgs: []string{"--max-turns", "5"}}, c.cfg)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for i, a := range argv {
			if strings.HasPrefix(a, c.flag+"=") || strings.Contains(a, "stale-id") {
				t.Fatalf("%s: stale session flag survived: %q", c.d.Name(), argv)
			}
			if a == c.flag {
				n++
				if i+1 >= len(argv) || argv[i+1] != sid {
					t.Fatalf("%s: flag without id: %q", c.d.Name(), argv)
				}
			}
		}
		if n != 1 {
			t.Fatalf("%s: want exactly one %s, got %d: %q", c.d.Name(), c.flag, n, argv)
		}
		if !strings.Contains(strings.Join(argv, "\x00"), "-s is not a flag here") {
			t.Fatalf("%s: prompt damaged: %q", c.d.Name(), argv)
		}
	}

	// Disabled / unconfigured → no flag.
	for _, flag := range []string{"", "none"} {
		argv, _ := grokDriver{}.BuildArgv(Request{Prompt: "p", AgentSessionID: sid},
			DriverConfig{Command: "/bin/true", SessionIDFlag: flag})
		if strings.Contains(strings.Join(argv, " "), sid) {
			t.Fatalf("flag %q should not pin: %q", flag, argv)
		}
	}
}

func TestLoadInheritsSessionDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent_process.json")
	// An agent_process.json written before ADR-0035: drivers present, no session keys.
	old := `{"drivers":{"grok":{"enabled":true,"command":"grok"},
	                    "claude":{"enabled":true,"command":"claude","session_id_flag":"none"}}}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if g := cfg.Drivers["grok"]; g.SessionIDFlag != "-s" || len(g.StateGlobs) == 0 {
		t.Fatalf("grok should inherit defaults: %+v", g)
	}
	if c := cfg.Drivers["claude"]; c.SessionPinned() {
		t.Fatalf("explicit none must stick: %+v", c)
	}
	if cfg.AliveWindowSec != 120 || cfg.Liveness.MinIOBytesPerSec != 4096 || cfg.Liveness.MinCPUPercent != 25 || cfg.Liveness.SampleSec != 5 {
		t.Fatalf("liveness defaults: %+v %+v", cfg.AliveWindowSec, cfg.Liveness)
	}
}

// End to end through the manager: pinned id → fake CLI writes state under it →
// a backdated task with a 5s stuck window is still alive (the field failure).
func TestStartBackgroundPinsAndTracksSession(t *testing.T) {
	ws := t.TempDir()
	mem := t.TempDir()
	grokHome := t.TempDir()
	script := filepath.Join(t.TempDir(), "fake-grok")
	body := `#!/bin/sh
sid=""
while [ $# -gt 0 ]; do [ "$1" = "-s" ] && sid="$2"; shift; done
d="$GROK_HOME/sessions/enc/$sid"
mkdir -p "$d"
echo '{"ts":"2026-10-06T08:00:00Z","type":"phase_changed","phase":"streaming_reasoning"}' >> "$d/events.jsonl"
sleep 30
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	g := cfg.Drivers["grok"]
	g.Command = script
	g.Env = map[string]string{"GROK_HOME": grokHome}
	cfg.Drivers["grok"] = g
	cfg.StuckAfterSec = 5
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(ConfigPath(mem), b, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := New(mem, ws)
	if err != nil {
		t.Fatal(err)
	}
	task, err := m.StartBackground("s1", Request{Format: "grok", Prompt: "think", CWD: ws, Background: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Kill(task.ID, true) }()
	if len(task.AgentSessionID) != 36 {
		t.Fatalf("agent_session_id %q", task.AgentSessionID)
	}
	if !strings.Contains(strings.Join(task.Command, " "), "-s "+task.AgentSessionID) {
		t.Fatalf("argv not pinned: %q", task.Command)
	}

	// Let the fake CLI write first; otherwise the first glob misses and re-globs are throttled to 5s.
	evPath := filepath.Join(grokHome, "sessions", "enc", task.AgentSessionID, "events.jsonl")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(evPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake CLI never wrote session state")
		}
		time.Sleep(20 * time.Millisecond)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		got, _ := m.Get(task.ID)
		if got.Progress != nil && got.Progress.Phase != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session state never resolved: %+v", got.Progress)
		}
		time.Sleep(100 * time.Millisecond)
	}

	m.mu.Lock()
	m.tasks[task.ID].StartedAt = time.Now().Add(-10 * time.Minute)
	m.mu.Unlock()
	got, _ := m.Get(task.ID)
	p := got.Progress
	if p.StuckHint || !p.Alive || p.Phase != "streaming_reasoning" {
		t.Fatalf("thinking agent reported stuck: %+v", p)
	}
	if got.AgentSessionID != task.AgentSessionID {
		t.Fatal("agent_session_id not exposed on poll")
	}
	if _, err := os.Stat("/proc/self/stat"); err == nil {
		// The child may still be between fork and exec (named fake-grok); samples are
		// throttled to 1s, so allow a few.
		deadline := time.Now().Add(4 * time.Second)
		for {
			got, _ := m.Get(task.ID)
			s := got.Progress.Signals[SigProcTree]
			if s.Available && strings.Contains(s.Detail, "sleep") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("proc_tree should see the sleep child: %+v", s)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
}
