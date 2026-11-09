package bgtask

import (
	"strings"
	"testing"
	"time"
)

func waitDone(t *testing.T, m *Manager, id string) *Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got, ok := m.Get(id); ok && got.Status != StatusRunning {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("task did not finish")
	return nil
}

func TestGetReturnsOutputWhileRunning(t *testing.T) {
	m := New(nil)
	task, err := m.Start("s", "echo early; echo warn >&2; sleep 30", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(task.ID, true)
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, _ := m.Get(task.ID)
		if strings.Contains(got.Stdout, "early") && strings.Contains(got.Stderr, "warn") && strings.Contains(got.Combined, "warn") {
			if got.Status != StatusRunning {
				t.Fatalf("status %s", got.Status)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no live output: %+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOutputKeptAfterExit(t *testing.T) {
	m := New(nil)
	task, err := m.Start("s", "echo out; echo err >&2; exit 2", "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := waitDone(t, m, task.ID)
	if got.Status != StatusExited || got.ExitCode == nil || *got.ExitCode != 2 {
		t.Fatalf("%+v", got)
	}
	for _, s := range []struct{ name, text, want string }{
		{"stdout", got.Stdout, "out"}, {"stderr", got.Stderr, "err"},
		{"combined", got.Combined, "out"}, {"combined", got.Combined, "err"},
	} {
		if !strings.Contains(s.text, s.want) {
			t.Errorf("%s = %q, want %q", s.name, s.text, s.want)
		}
	}
}

// A noisy command's last lines survive; the old limitedWriter kept only the start.
func TestNoisyCommandKeepsEndOfOutput(t *testing.T) {
	m := New(nil)
	m.maxOut = 4096
	task, err := m.Start("s", `i=0; while [ $i -lt 3000 ]; do echo "line-$i"; i=$((i+1)); done`, "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := waitDone(t, m, task.ID)
	if !strings.Contains(got.Stdout, "line-0\n") {
		t.Fatal("head lost")
	}
	if !strings.HasSuffix(got.Stdout, "line-2999\n") {
		t.Fatalf("tail is not the end of output: ...%q", got.Stdout[len(got.Stdout)-40:])
	}
	if !strings.Contains(got.Stdout, "bytes omitted") {
		t.Fatal("no omission marker")
	}
	if len(got.Stdout) > 4096+64 {
		t.Fatalf("capture not bounded: %d", len(got.Stdout))
	}
}

func TestOutputBuffer(t *testing.T) {
	cases := []struct {
		name       string
		head, tail int
		writes     []string
		want       string
	}{
		{"fits", 4, 4, []string{"abcdef"}, "abcdef"},
		{"oversized write", 2, 3, []string{"ab", "cdefghij\n"}, "ab\n…[6 bytes omitted]…\nij\n"},
		{"ring wraps", 0, 6, []string{"a\nbb\n", "c\nd\n"}, "c\nd\n"},
		{"tail only, one long line", 0, 4, []string{"abcdefgh"}, "efgh"},
		{"keeps complete tail lines", 2, 6, []string{"xx", "1\n2\n3\n4\n"}, "xx\n…[2 bytes omitted]…\n3\n4\n"},
	}
	for _, c := range cases {
		b := newOutputBuffer(c.head, c.tail)
		for _, w := range c.writes {
			if n, err := b.Write([]byte(w)); n != len(w) || err != nil {
				t.Fatalf("%s: write %d %v", c.name, n, err)
			}
		}
		if got := b.String(); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// R5: a finished task frees its slot; only running tasks count against the cap.
func TestFinishedTaskFreesCapSlot(t *testing.T) {
	m := New(nil)
	m.maxPerS = 2
	first, err := m.Start("s", "exit 0", "", "")
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, m, first.ID)
	var live []string
	defer func() {
		for _, id := range live {
			_ = m.Kill(id, true)
		}
	}()
	for i := 0; i < 2; i++ {
		task, err := m.Start("s", "sleep 30", "", "")
		if err != nil {
			t.Fatalf("slot %d not free: %v", i, err)
		}
		live = append(live, task.ID)
	}
	if _, err := m.Start("s", "sleep 30", "", ""); err == nil || !strings.Contains(err.Error(), "max concurrent") {
		t.Fatalf("cap not enforced: %v", err)
	}
	if _, err := m.Start("other", "exit 0", "", ""); err != nil {
		t.Fatalf("cap is per session: %v", err)
	}
}
