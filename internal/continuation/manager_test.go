package continuation

import (
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu     sync.Mutex
	fired  []Job
	notify []string
}

func (r *recorder) onFire(j Job) {
	r.mu.Lock()
	r.fired = append(r.fired, j)
	r.mu.Unlock()
}

func (r *recorder) onNotify(sid string) {
	r.mu.Lock()
	r.notify = append(r.notify, sid)
	r.mu.Unlock()
}

func (r *recorder) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.fired), len(r.notify)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDelayFireRecordsReasonAndNotifies(t *testing.T) {
	rec := &recorder{}
	m := New(rec.onFire, nil)
	defer m.Stop()
	m.Notify = rec.onNotify
	j, err := m.Schedule("s1", "go on", 1, "", "next step")
	if err != nil {
		t.Fatal(err)
	}
	if _, n := rec.counts(); n != 1 {
		t.Fatalf("schedule should notify once, got %d", n)
	}
	waitFor(t, func() bool { f, _ := rec.counts(); return f == 1 })
	got := m.List("s1")[0]
	if !got.Fired || got.FiredAt == nil || got.Reason != "delay" || got.ID != j.ID {
		t.Fatalf("%+v", got)
	}
	m.SetOutcome(j.ID, "started")
	if m.List("s1")[0].Outcome != "started" {
		t.Fatal("outcome not recorded")
	}
	if _, n := rec.counts(); n < 3 {
		t.Fatalf("schedule, fire and outcome should each notify; got %d", n)
	}
}

func TestWaitTaskFiresOnTaskDone(t *testing.T) {
	rec := &recorder{}
	var mu sync.Mutex
	done := false
	m := New(rec.onFire, func(id string) bool {
		mu.Lock()
		defer mu.Unlock()
		return id == "agent-1" && done
	})
	defer m.Stop()
	if _, err := m.Schedule("s1", "check result", 0, "agent-1", ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	if f, _ := rec.counts(); f != 0 {
		t.Fatal("fired before the task finished")
	}
	mu.Lock()
	done = true
	mu.Unlock()
	waitFor(t, func() bool { f, _ := rec.counts(); return f == 1 })
	if r := m.List("s1")[0].Reason; r != "task_done" {
		t.Fatalf("reason %q", r)
	}
}

func TestCancelSessionMarksOutcome(t *testing.T) {
	rec := &recorder{}
	m := New(rec.onFire, nil)
	defer m.Stop()
	m.Notify = rec.onNotify
	if _, err := m.Schedule("s1", "later", 3600, "", ""); err != nil {
		t.Fatal(err)
	}
	m.CancelSession("s1")
	got := m.List("s1")[0]
	if !got.Cancelled || got.Outcome != "cancelled" {
		t.Fatalf("%+v", got)
	}
	_, before := rec.counts()
	m.CancelSession("s1") // nothing left to cancel → no extra notify
	if _, after := rec.counts(); after != before {
		t.Fatal("no-op cancel should not notify")
	}
}

func TestScheduleDefaultsToAgentSource(t *testing.T) {
	rec := &recorder{}
	m := New(rec.onFire, nil)
	defer m.Stop()
	j, err := m.Schedule("s1", "later", 3600, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if j.Source != SourceAgent || j.WatchID != "" {
		t.Fatalf("%+v", j)
	}
}

func TestScheduleWatchCarriesSourceAndWatchID(t *testing.T) {
	rec := &recorder{}
	m := New(rec.onFire, nil)
	defer m.Stop()
	if _, err := m.ScheduleWatch("s1", "p", 3600, "", "", SourceAgent, "w_1"); err == nil {
		t.Fatal("agent is not a watch source")
	}
	if _, err := m.ScheduleWatch("s1", "p", 3600, "", "", SourceWatch, ""); err == nil {
		t.Fatal("watch id is required")
	}
	j, err := m.ScheduleWatch("s1", "p", 1, "", "lbl", SourceWatchBoot, "w_1")
	if err != nil {
		t.Fatal(err)
	}
	if j.Source != SourceWatchBoot || j.WatchID != "w_1" {
		t.Fatalf("%+v", j)
	}
	waitFor(t, func() bool { f, _ := rec.counts(); return f == 1 })
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if got := rec.fired[0]; got.Source != SourceWatchBoot || got.WatchID != "w_1" {
		t.Fatalf("fired job lost its link: %+v", got)
	}
}

func TestCancelSingleJob(t *testing.T) {
	rec := &recorder{}
	m := New(rec.onFire, nil)
	defer m.Stop()
	a, err := m.Schedule("s1", "a", 1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Schedule("s1", "b", 3600, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Cancel(a.ID) {
		t.Fatal("pending job should cancel")
	}
	if m.Cancel(a.ID) || m.Cancel("nope") {
		t.Fatal("repeat or unknown cancel should report false")
	}
	time.Sleep(1600 * time.Millisecond)
	if f, _ := rec.counts(); f != 0 {
		t.Fatal("cancelled job fired")
	}
	for _, j := range m.List("s1") {
		if j.ID == b.ID && j.Cancelled {
			t.Fatal("cancel touched a sibling job")
		}
	}
}
