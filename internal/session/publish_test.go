package session

import (
	"testing"
	"time"
)

func TestPublishCriticalNotDroppedWhenBufferFullOfTurnNoise(t *testing.T) {
	s := newSession("pubtest01", "t")
	// Tiny buffer relative to noise volume.
	ch := make(chan Event, 4)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()

	// Flood with droppable turn events until full (and more).
	for i := 0; i < 20; i++ {
		s.publish(Event{Type: "turn", Turn: &TurnProgress{Phase: "running_tool", Iter: i}})
	}
	// Critical final message must still be accepted (by dropping oldest noise).
	msg := &Message{ID: "m-final", Role: "assistant", Content: "hello after tools"}
	s.publish(Event{Type: "message", Message: msg})
	s.publish(Event{Type: "status", Status: "idle"})

	deadline := time.After(2 * time.Second)
	var sawMsg, sawIdle bool
	for !(sawMsg && sawIdle) {
		select {
		case ev := <-ch:
			if ev.Type == "message" && ev.Message != nil && ev.Message.ID == "m-final" {
				sawMsg = true
			}
			if ev.Type == "status" && ev.Status == "idle" {
				sawIdle = true
			}
		case <-deadline:
			t.Fatalf("missing critical events: msg=%v idle=%v", sawMsg, sawIdle)
		}
	}
}

func TestPublishDroppableCanBeDropped(t *testing.T) {
	s := newSession("pubtest02", "t")
	ch := make(chan Event, 1)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()

	// Fill buffer with one event, then more turn noise should not block.
	s.publish(Event{Type: "status", Status: "running"})
	for i := 0; i < 10; i++ {
		s.publish(Event{Type: "turn", Turn: &TurnProgress{Phase: "calling_model", Iter: i}})
	}
	// Drain: should have at least the critical status; turns may be missing.
	gotStatus := false
	for {
		select {
		case ev := <-ch:
			if ev.Type == "status" {
				gotStatus = true
			}
		default:
			if !gotStatus {
				t.Fatal("expected status event retained")
			}
			return
		}
	}
}
