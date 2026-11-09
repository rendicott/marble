package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rendicott/marble/internal/bgtask"
	"github.com/rendicott/marble/internal/continuation"
	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/watch"
)

func watchRegistry(t *testing.T, d *db.DB) (*Registry, *bgtask.Manager, *continuation.Manager) {
	t.Helper()
	bg := bgtask.New(nil)
	cont := continuation.New(nil, nil)
	t.Cleanup(func() { bg.KillAllRunning(); cont.Stop() })
	m := watch.New(d, bg, cont, func(string, string) watch.WakeResult {
		return watch.WakeResult{Status: watch.WakeStarted}
	})
	return &Registry{Watch: m}, bg, cont
}

func tempDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestWatchToolIsRegistered(t *testing.T) {
	for _, s := range allSpecs() {
		if s.Function.Name == "watch" {
			return
		}
	}
	t.Fatal("watch missing from allSpecs")
}

func TestWatchToolLifecycle(t *testing.T) {
	d := tempDB(t)
	r, bg, cont := watchRegistry(t, d)
	tc := &TurnContext{SessionID: "s1"}

	var created map[string]interface{}
	out := r.Execute("watch", `{"action":"create","command":"echo hello; sleep 30","label":"long job","deadline_sec":60}`, tc)
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	id, _ := created["watch_id"].(string)
	taskID, _ := created["task_id"].(string)
	if id == "" || created["status"] != db.WatchRunning {
		t.Fatalf("%s", out)
	}
	jobs := cont.List("s1")
	if len(jobs) != 1 || jobs[0].WaitTask != taskID || jobs[0].WatchID != id {
		t.Fatalf("%+v", jobs)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		status := r.Execute("watch", `{"action":"status","id":"`+id+`"}`, tc)
		if strings.Contains(status, "hello") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no live tail: %s", status)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if list := r.Execute("watch", `{"action":"list"}`, tc); !strings.Contains(list, id) {
		t.Fatalf("%s", list)
	}
	if out := r.Execute("watch", `{"action":"cancel","id":"`+id+`"}`, tc); !strings.Contains(out, "reason is required") {
		t.Fatalf("%s", out)
	}
	if out := r.Execute("watch", `{"action":"cancel","id":"`+id+`","reason":"changed my mind"}`, tc); !strings.Contains(out, `"cancelled": true`) {
		t.Fatalf("%s", out)
	}
	row, err := d.GetWatchJob(id)
	if err != nil || row.Status != db.WatchFailed || row.EndReason != db.WatchReasonCancelled || row.Note != "changed my mind" {
		t.Fatalf("%+v %v", row, err)
	}
	if !cont.List("s1")[0].Cancelled {
		t.Fatal("continuation not cancelled")
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		if running, _ := bg.IsRunning(taskID); !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task not killed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWatchToolRefusals(t *testing.T) {
	tc := &TurnContext{SessionID: "s1"}
	cases := []struct {
		name string
		reg  *Registry
		args string
		want string
	}{
		{"unconfigured", &Registry{}, `{"action":"list"}`, "watch not configured"},
		{"limp", func() *Registry {
			r, _, _ := watchRegistry(t, &db.DB{Mode: db.ModeLimp})
			return r
		}(), `{"action":"create","command":"true"}`, "not writable"},
	}
	for _, c := range cases {
		if out := c.reg.Execute("watch", c.args, tc); !strings.Contains(out, c.want) {
			t.Errorf("%s: %s", c.name, out)
		}
	}
	r, _, _ := watchRegistry(t, tempDB(t))
	for args, want := range map[string]string{
		`{"action":"nope"}`:              "unknown action",
		`{"action":"status"}`:            "id is required",
		`{"action":"status","id":"zzz"}`: "not found",
		`{"action":"create"}`:            "command is required",
	} {
		if out := r.Execute("watch", args, tc); !strings.Contains(out, want) {
			t.Errorf("%s: %s", args, out)
		}
	}
	if out := r.Execute("watch", `{"action":"list"}`, &TurnContext{}); !strings.Contains(out, "session context") {
		t.Errorf("%s", out)
	}
}

func TestWatchStatusIsPollExempt(t *testing.T) {
	if !isPollExempt("watch", `{"action":"status","id":"w_1"}`) {
		t.Fatal("status polls must not trip anti-repeat")
	}
	if isPollExempt("watch", `{"action":"create","command":"x"}`) {
		t.Fatal("create is not a poll")
	}
}
