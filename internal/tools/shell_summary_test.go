package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rendicott/marble/internal/shellpolicy"
)

func testShellReg(t *testing.T) *Registry {
	t.Helper()
	dir := t.TempDir()
	pol := shellpolicy.New(dir, dir, false, 30*time.Second, 60*time.Second)
	return &Registry{Workspace: dir, MaxResultChars: 10000, Policy: pol}
}

func dataInt(t *testing.T, sum *ResultSummary, key string) int {
	t.Helper()
	v, ok := sum.Data[key]
	if !ok {
		t.Fatalf("missing %s in %+v", key, sum.Data)
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	default:
		t.Fatalf("%s type %T", key, v)
		return 0
	}
}

func dataBool(t *testing.T, sum *ResultSummary, key string) bool {
	t.Helper()
	v, ok := sum.Data[key].(bool)
	if !ok {
		t.Fatalf("%s type %T", key, sum.Data[key])
	}
	return v
}

// TestShellExecuteSummaryTimeoutAndSuccess checks the outcome summary against
// a command that hits its timeout and one that returns immediately.
func TestShellExecuteSummaryTimeoutAndSuccess(t *testing.T) {
	r := testShellReg(t)

	start := time.Now()
	out, sum, err := r.shellExecute(`{"command":"sleep 5","timeout_sec":1}`, nil)
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Fatalf("timeout case took %s; want under 3s", elapsed)
	}
	if err != nil {
		t.Fatal(err)
	}
	if sum == nil {
		t.Fatal("nil summary")
	}
	if sum.Hits != 0 {
		t.Fatalf("hits %d", sum.Hits)
	}
	if !strings.Contains(out, "exit=-1") || !strings.Contains(out, "killed_timeout=true") || !strings.Contains(out, "killed_stop=false") {
		t.Fatalf("model text %q", out)
	}
	if dataInt(t, sum, "exit_code") != -1 {
		t.Fatalf("exit %+v", sum.Data)
	}
	if !dataBool(t, sum, "killed_timeout") || !dataBool(t, sum, "timed_out") || dataBool(t, sum, "killed_stop") {
		t.Fatalf("flags %+v", sum.Data)
	}
	if dataInt(t, sum, "timeout_sec") != 1 {
		t.Fatalf("timeout_sec %+v", sum.Data["timeout_sec"])
	}
	stdoutN := dataInt(t, sum, "stdout_bytes")
	stderrN := dataInt(t, sum, "stderr_bytes")
	if sum.Empty != (stdoutN == 0 && stderrN == 0) {
		t.Fatalf("empty %v stdout %d stderr %d", sum.Empty, stdoutN, stderrN)
	}
	if dataInt(t, sum, "duration_ms") < 500 {
		t.Fatalf("duration_ms %d", dataInt(t, sum, "duration_ms"))
	}
	raw, err := json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"timed_out":true`) || !strings.Contains(string(raw), `"killed_timeout":true`) {
		t.Fatalf("summary json %s", raw)
	}

	out, sum, err = r.shellExecute(`{"command":"true","timeout_sec":1}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sum == nil {
		t.Fatal("nil summary")
	}
	if !strings.HasPrefix(out, "exit=0 ") || !strings.Contains(out, "killed_timeout=false") || !strings.Contains(out, "killed_stop=false") {
		t.Fatalf("model text %q", out)
	}
	if !strings.Contains(out, "--- stdout ---") {
		t.Fatalf("model text %q", out)
	}
	if dataInt(t, sum, "exit_code") != 0 || dataBool(t, sum, "timed_out") || dataBool(t, sum, "killed_timeout") {
		t.Fatalf("success %+v", sum.Data)
	}
	if sum.Hits != 0 {
		t.Fatalf("hits %d", sum.Hits)
	}
	if dataInt(t, sum, "timeout_sec") != 1 {
		t.Fatalf("timeout_sec %+v", sum.Data["timeout_sec"])
	}
	if !sum.Empty || dataInt(t, sum, "stdout_bytes") != 0 || dataInt(t, sum, "stderr_bytes") != 0 {
		t.Fatalf("empty streams %+v empty=%v", sum.Data, sum.Empty)
	}
	raw, err = json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"timed_out":false`) || strings.Contains(string(raw), `"timed_out":true`) || !strings.Contains(string(raw), `"exit_code":0`) {
		t.Fatalf("summary json %s", raw)
	}
}
