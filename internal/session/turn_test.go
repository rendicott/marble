package session

import (
	"context"
	"testing"
	"time"
)

func TestRequestStopCancelsAndProgress(t *testing.T) {
	s := newSession("testid0001", "t")
	s.initTurnProgress(80, 65)
	ctx, cancel := context.WithCancel(context.Background())
	s.setTurnCancel(cancel)
	s.busy = true
	s.setPhase("calling_model")
	s.setIter(3)
	s.setToolRounds(2)

	if !s.RequestStop() {
		t.Fatal("expected stop accepted")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("context not cancelled")
	}
	p := s.Progress()
	if !p.StopRequested {
		t.Fatal("expected stop_requested")
	}
	if p.Phase != "stopping" {
		t.Fatalf("phase %s", p.Phase)
	}
	if p.Iter != 3 || p.ToolRounds != 2 {
		t.Fatalf("counters %+v", p)
	}
	found := false
	for _, st := range p.Steps {
		if st.Kind == "stop" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected stop step")
	}

	// idle stop rejected
	s.busy = false
	if s.RequestStop() {
		t.Fatal("expected stop rejected when idle")
	}
}

func TestFinalizeCollapse(t *testing.T) {
	s := newSession("testid0002", "t")
	s.initTurnProgress(80, 65)
	s.appendStep(TurnStep{Kind: "model_call", Iter: 0, Detail: "ok"})
	s.setCurrentTool("c1", "shell_execute", `{"command":"echo hi"}`, "start")
	s.finishCurrentTool("exit=0\nok")
	s.finalizeTurnProgress("complete", "")
	p := s.Progress()
	if p.Active {
		t.Fatal("expected inactive")
	}
	if !p.Collapsed {
		t.Fatal("expected collapsed")
	}
	if p.LastTool == nil || p.LastTool.Name != "shell_execute" {
		t.Fatalf("last tool %+v", p.LastTool)
	}
	if p.CurrentTool != nil {
		t.Fatal("current tool should clear")
	}
	if len(p.LastTool.ArgsPreview) == 0 {
		t.Fatal("expected args preview")
	}
}

func TestTruncateOneLine(t *testing.T) {
	s := truncateOneLine("a\nb\nc", 10)
	if len(s) > 10 {
		t.Fatalf("%q", s)
	}
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}
	s = truncateOneLine(string(long), argsPreviewMax)
	if len([]rune(s)) > argsPreviewMax {
		t.Fatalf("runes %d", len([]rune(s)))
	}
}
