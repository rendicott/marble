package session

import (
	"testing"
	"time"

	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/memory"
)

func TestSearchLiveBeforeFlush(t *testing.T) {
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
	reg := NewRegistry(nil, store, sqldb, root, "model")
	s := reg.Create("New session")

	s.mu.Lock()
	s.appendUI(Message{Role: "user", Content: "please build the apk with gradle", CreatedAt: time.Now()})
	s.mu.Unlock()

	// endTurn has not run, so SQLite does not have these terms yet.
	stored, err := sqldb.SessionSearchTermSet(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored["apk"] != 0 || stored["gradle"] != 0 {
		t.Fatalf("terms written before endTurn: %v", stored)
	}

	hits, err := reg.SearchSessions("android")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != s.ID {
		t.Fatalf("live union hits %+v", hits)
	}
	if hits[0].Description != s.Title {
		t.Fatalf("description %q title %q", hits[0].Description, s.Title)
	}

	if err := sqldb.UpsertClerkState(db.ClerkStateRow{
		SessionID: s.ID, Summary: "ship the android build", ActionItemsJSON: "[]",
	}); err != nil {
		t.Fatal(err)
	}
	hits, err = reg.SearchSessions("android")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Description != "ship the android build" {
		t.Fatalf("clerk description %+v", hits)
	}

	s.endTurn()
	stored, err = sqldb.SessionSearchTermSet(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored["apk"] == 0 || stored["gradle"] == 0 {
		t.Fatalf("endTurn did not merge terms: %v", stored)
	}

	// Drop the live copy. The SQLite row from endTurn still answers.
	reg.mu.Lock()
	delete(reg.sessions, s.ID)
	reg.mu.Unlock()
	hits, err = reg.SearchSessions("android")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != s.ID {
		t.Fatalf("sqlite hits after unload %+v", hits)
	}
}
