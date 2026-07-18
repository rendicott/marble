package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCreateAndEvent(t *testing.T) {
	root := t.TempDir()
	d, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Mode != ModeNormal {
		t.Fatalf("mode %s: %s", d.Mode, d.Reason)
	}
	if err := d.UpsertSession(SessionRow{
		ID: "testid1234", Title: "t", Status: "active",
		CreatedAt: UTCNow(), UpdatedAt: UTCNow(),
		MDPath: "session/testid1234.md",
	}); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 40000)
	for i := range big {
		big[i] = 'a'
	}
	if err := d.AppendEvent(Event{
		SessionID: "testid1234",
		Seq:       1,
		TS:        UTCNow(),
		Kind:      "user_message",
		Role:      "user",
		Content:   string(big),
	}); err != nil {
		t.Fatal(err)
	}
	// blob should exist
	entries, _ := os.ReadDir(filepath.Join(root, "blobs"))
	if len(entries) < 1 {
		t.Fatal("expected blob spill")
	}
}

func TestLockBlocksSecondOpen(t *testing.T) {
	root := t.TempDir()
	d1, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d1.Close()
	_, err = Open(root)
	if err == nil {
		t.Fatal("expected lock error")
	}
}

func TestSessionInfoAggregates(t *testing.T) {
	root := t.TempDir()
	d, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	id := "infoaggtest1"
	now := UTCNow()
	if err := d.UpsertSession(SessionRow{
		ID: id, Title: "info", Status: "active",
		CreatedAt: now, UpdatedAt: now,
		MDPath: "session/" + id + ".md", Model: "test-model", Workspace: "/tmp/ws",
		MessageCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	tin, tout := 100, 40
	lat := 500
	events := []Event{
		{SessionID: id, Seq: 1, TS: now, Kind: "user_message", Role: "user", Content: "hi"},
		{SessionID: id, Seq: 2, TS: now, Kind: "model_call", TokensInReported: &tin, TokensOutReported: &tout, LatencyMs: &lat},
		{SessionID: id, Seq: 3, TS: now, Kind: "tool_call", ToolName: "shell_execute", ToolArgsJSON: `{"cmd":"ls"}`},
		{SessionID: id, Seq: 4, TS: now, Kind: "tool_result", ToolName: "shell_execute", Content: "ok"},
		{SessionID: id, Seq: 5, TS: now, Kind: "tool_call", ToolName: "file_read"},
		{SessionID: id, Seq: 6, TS: now, Kind: "tool_result", ToolName: "file_read", Error: "not found"},
		{SessionID: id, Seq: 7, TS: now, Kind: "error", Error: "boom " + string(make([]byte, 600))},
	}
	// spill one blob
	big := make([]byte, 40000)
	for i := range big {
		big[i] = 'x'
	}
	events = append(events, Event{
		SessionID: id, Seq: 8, TS: now, Kind: "assistant_message", Role: "assistant", Content: string(big),
	})
	for _, ev := range events {
		if err := d.AppendEvent(ev); err != nil {
			t.Fatal(err)
		}
	}

	bundle, err := d.LoadSessionInfo(id, 30)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Partial {
		t.Fatal("expected full db info")
	}
	if bundle.Row == nil || bundle.Row.Title != "info" {
		t.Fatalf("row: %+v", bundle.Row)
	}
	u := bundle.Usage
	if u.EventCount != 8 {
		t.Fatalf("event_count %d", u.EventCount)
	}
	if u.UserMessages != 1 || u.ModelCalls != 1 || u.ToolCalls != 2 {
		t.Fatalf("counts user=%d model=%d tools=%d", u.UserMessages, u.ModelCalls, u.ToolCalls)
	}
	if u.TokensInReported != 100 || u.TokensOutReported != 40 {
		t.Fatalf("tokens in=%d out=%d", u.TokensInReported, u.TokensOutReported)
	}
	if u.LatencyMsAvg != 500 || u.LatencyMsMax != 500 {
		t.Fatalf("latency avg=%d max=%d", u.LatencyMsAvg, u.LatencyMsMax)
	}
	if u.BlobCount < 1 {
		t.Fatalf("expected blob_count >= 1 got %d", u.BlobCount)
	}
	if len(bundle.Tools) < 2 {
		t.Fatalf("tools: %+v", bundle.Tools)
	}
	if len(bundle.Recent) != 8 {
		t.Fatalf("recent len %d", len(bundle.Recent))
	}
	// newest first
	if bundle.Recent[0].Seq != 8 {
		t.Fatalf("expected seq 8 first got %d", bundle.Recent[0].Seq)
	}
	// error truncation
	var foundErr bool
	for _, e := range bundle.Recent {
		if e.Kind == "error" {
			foundErr = true
			if len([]rune(e.Error)) > 501 { // 500 + ellipsis
				t.Fatalf("error not truncated: %d runes", len([]rune(e.Error)))
			}
		}
	}
	if !foundErr {
		t.Fatal("expected error event in recent")
	}
}

func TestSessionInfoLimp(t *testing.T) {
	// Writable false → partial empty
	d := &DB{Mode: ModeLimp}
	b, err := d.LoadSessionInfo("x", 30)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Partial || b.Row != nil || b.Usage.EventCount != 0 {
		t.Fatalf("limp bundle: %+v", b)
	}
}
