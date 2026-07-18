package session

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/memory"
)

func TestInfoFromDBAndLive(t *testing.T) {
	root := t.TempDir()
	sqldb, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()

	store, err := memory.New(root)
	if err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(nil, store, sqldb, "/ws", "model-a")
	s := reg.Create("hello session")
	id := s.ID

	// log a couple events
	reg.logEvent(s, "user_message", "user", "hi", "", "", "", nil, nil, nil, nil, nil, "", "")
	tin, tout, lat := 10, 5, 100
	reg.logEvent(s, "model_call", "", "", "", "", "", &tin, &tout, nil, nil, &lat, "stop", "")

	info, err := reg.Info(id)
	if err != nil {
		t.Fatal(err)
	}
	if info.Partial || info.Source != "db" {
		t.Fatalf("source=%s partial=%v", info.Source, info.Partial)
	}
	if info.Session.Title != "hello session" {
		t.Fatalf("title %q", info.Session.Title)
	}
	if info.Session.Model != "model-a" || info.Session.Workspace != "/ws" {
		t.Fatalf("model/ws %+v", info.Session)
	}
	if info.Usage.UserMessages < 1 || info.Usage.ModelCalls < 1 {
		t.Fatalf("usage %+v", info.Usage)
	}
	if info.Session.MDPath != filepath.Join("session", id+".md") {
		t.Fatalf("md_path %s", info.Session.MDPath)
	}
	if info.Session.Busy {
		t.Fatal("expected not busy")
	}
}

func TestInfoLimpMetadataOnly(t *testing.T) {
	root := t.TempDir()
	store, err := memory.New(root)
	if err != nil {
		t.Fatal(err)
	}
	// limp db (not writable)
	limp := &db.DB{Mode: db.ModeLimp, Root: root}
	reg := NewRegistry(nil, store, limp, "/ws", "model-b")
	s := reg.Create("limp sess")
	// Create still writes markdown via store
	_ = reg.PersistSession(s)

	info, err := reg.Info(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Partial {
		t.Fatal("expected partial")
	}
	if info.Usage.EventCount != 0 || info.Usage.TokensInReported != 0 {
		t.Fatalf("fake tokens? %+v", info.Usage)
	}
	if len(info.RecentEvents) != 0 {
		t.Fatalf("expected empty timeline got %d", len(info.RecentEvents))
	}
	if info.Session.Title != "limp sess" {
		t.Fatalf("title %q", info.Session.Title)
	}
}

func TestInfoNotFound(t *testing.T) {
	reg := NewRegistry(nil, nil, nil, "", "")
	_, err := reg.Info("doesnotexist")
	if err == nil {
		t.Fatal("expected not found")
	}
}

func TestInfoClosedFromDisk(t *testing.T) {
	root := t.TempDir()
	store, err := memory.New(root)
	if err != nil {
		t.Fatal(err)
	}
	sqldb, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	reg := NewRegistry(nil, store, sqldb, "/ws", "m")
	s := reg.Create("to close")
	id := s.ID
	if err := reg.Close(id); err != nil {
		t.Fatal(err)
	}
	// not live
	if _, ok := reg.Get(id); ok {
		t.Fatal("expected unloaded after close")
	}
	info, err := reg.Info(id)
	if err != nil {
		t.Fatal(err)
	}
	if info.Session.Status != "closed" {
		t.Fatalf("status %s", info.Session.Status)
	}
	if info.Session.ClosedAt == nil {
		t.Fatal("expected closed_at")
	}
	// touch UpdatedAt sanity
	_ = time.Now()
}
