package session

import (
	"context"
	"errors"
	"testing"

	"github.com/rendicott/marble/internal/config"
)

func TestNearMaxToolIters(t *testing.T) {
	r := &Runner{Cfg: config.Config{AutoContinueReserve: 10}}

	// Not near: early iters with tools
	if r.nearMaxToolIters(200, 50, 50) {
		t.Fatal("iter 50/200 should not be near max")
	}
	// Near: within reserve
	if !r.nearMaxToolIters(200, 190, 100) {
		t.Fatal("iter 190/200 with reserve 10 should be near max")
	}
	// Exact threshold hard-reserve
	if !r.nearMaxToolIters(200, 190, 1) {
		t.Fatal("iter == hard-reserve should trigger")
	}
	if r.nearMaxToolIters(200, 189, 100) {
		t.Fatal("iter 189 should not trigger with reserve 10")
	}
	// No tool work yet — do not force
	if r.nearMaxToolIters(200, 195, 0) {
		t.Fatal("no tool rounds should not force auto-continue")
	}
	// Disabled
	r.Cfg.AutoContinueReserve = 0
	if r.nearMaxToolIters(200, 199, 50) {
		t.Fatal("reserve 0 disables")
	}
}

func TestShouldAutoContinueOnErr(t *testing.T) {
	r := &Runner{}
	s := newSession("testid0001", "t")
	s.initTurnProgress(80, 65)
	s.busy = true

	if r.shouldAutoContinueOnErr(nil, s) {
		t.Fatal("nil err")
	}
	if !r.shouldAutoContinueOnErr(context.DeadlineExceeded, s) {
		t.Fatal("deadline should auto-continue")
	}
	if r.shouldAutoContinueOnErr(context.Canceled, s) {
		t.Fatal("canceled should not")
	}
	if r.shouldAutoContinueOnErr(errors.New("provider boom"), s) {
		t.Fatal("other errors should not")
	}

	s.RequestStop()
	// After stop request, even deadline should not auto-continue
	if r.shouldAutoContinueOnErr(context.DeadlineExceeded, s) {
		t.Fatal("operator stop should block auto-continue")
	}
}

func TestNearMaxWithTinyHard(t *testing.T) {
	r := &Runner{Cfg: config.Config{AutoContinueReserve: 50}}
	// reserve >= hard: after first tool round at iter>=1
	if r.nearMaxToolIters(10, 0, 1) {
		t.Fatal("iter 0 should not")
	}
	if !r.nearMaxToolIters(10, 1, 1) {
		t.Fatal("iter 1 with reserve>=hard should")
	}
}
