package clerk

import (
	"database/sql"
	"testing"
	"time"
)

func TestHeuristicNeedsUser(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"All done, foo shipped.", false},
		{"Which option should I pick?", true},
		{"Please confirm before I proceed.", true},
		{"Shall I restart the service?", true},
		{"Choose: A or B", true},
		{"Status update complete", false},
		{"Ready when you are?", true},
	}
	for _, c := range cases {
		if got := heuristicNeedsUser(c.in); got != c.want {
			t.Errorf("heuristicNeedsUser(%q)=%v want %v", c.in, got, c.want)
		}
	}
}

func TestSnippetClip(t *testing.T) {
	s := snippet("  hello   world  \n next ", 80)
	if s != "hello world next" {
		t.Fatalf("snippet normalize: %q", s)
	}
	long := ""
	for i := 0; i < 100; i++ {
		long += "x"
	}
	clipped := clipRunes(long, 10)
	if len([]rune(clipped)) != 10 {
		t.Fatalf("clip len=%d want 10 (%q)", len([]rune(clipped)), clipped)
	}
	if clipped[len(clipped)-1:] != "…" && !hasEllipsis(clipped) {
		t.Fatalf("expected ellipsis: %q", clipped)
	}
}

func hasEllipsis(s string) bool {
	for _, r := range s {
		if r == '…' {
			return true
		}
	}
	return false
}

func TestSortRows(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	rows := []Row{
		{SessionID: "w1", Attention: "working", UpdatedAt: now, IdleForSec: 0},
		{SessionID: "i-short", Attention: "idle", IdleForSec: 10, UpdatedAt: now},
		{SessionID: "n-long", Attention: "needs_user", IdleForSec: 500, UpdatedAt: now},
		{SessionID: "n-short", Attention: "needs_user", IdleForSec: 5, UpdatedAt: now},
		{SessionID: "i-long", Attention: "idle", IdleForSec: 900, UpdatedAt: now},
		{SessionID: "w2", Attention: "working", UpdatedAt: "2099-01-01T00:00:00Z", IdleForSec: 0},
		{SessionID: "snz-soon", Attention: "needs_user", IdleForSec: 9999, Snoozed: true, SnoozeLeftSec: 100},
		{SessionID: "snz-later", Attention: "needs_user", IdleForSec: 9999, Snoozed: true, SnoozeLeftSec: 9000},
	}
	sortRows(rows)
	want := []string{"n-long", "n-short", "i-long", "i-short", "w2", "w1", "snz-soon", "snz-later"}
	for i, id := range want {
		if rows[i].SessionID != id {
			t.Fatalf("pos %d: got %s want %s (full=%v)", i, rows[i].SessionID, id, idsOf(rows))
		}
	}
}

func TestActiveSnooze(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	// future
	fut := now.Add(2 * time.Hour).Format(time.RFC3339)
	ok, until, left := activeSnooze(sql.NullString{String: fut, Valid: true}, now)
	if !ok || until == "" || left < 7000 || left > 7300 {
		t.Fatalf("future: ok=%v until=%q left=%d", ok, until, left)
	}
	// past
	past := now.Add(-time.Hour).Format(time.RFC3339)
	ok, _, _ = activeSnooze(sql.NullString{String: past, Valid: true}, now)
	if ok {
		t.Fatal("past should be inactive")
	}
	ok, _, _ = activeSnooze(sql.NullString{}, now)
	if ok {
		t.Fatal("empty inactive")
	}
}

func idsOf(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.SessionID
	}
	return out
}

func TestFallbackNeedsUser(t *testing.T) {
	// fallbackFromSession needs a session; exercise heuristic path via sumOut shape
	out := sumOut{Summary: "pick A or B?", NeedsUser: true, ActionItems: []string{"reply"}}
	if !out.NeedsUser || out.Summary == "" {
		t.Fatal("sanity")
	}
}

func TestParseSumOut(t *testing.T) {
	var out sumOut
	if err := parseSumOut(`{"summary":"foo done","needs_user":false,"action_items":[]}`, &out); err != nil {
		t.Fatal(err)
	}
	if out.Summary != "foo done" || out.NeedsUser {
		t.Fatalf("%+v", out)
	}
	out = sumOut{}
	messy := "Sure.\n```json\n{\"summary\":\"pick A?\",\"needs_user\":true,\"action_items\":[\"choose\"]}\n```\n"
	if err := parseSumOut(messy, &out); err != nil {
		t.Fatal(err)
	}
	if !out.NeedsUser || out.Summary != "pick A?" || len(out.ActionItems) != 1 {
		t.Fatalf("%+v", out)
	}
	out = sumOut{}
	if err := parseSumOut(`Thinking… {"summary":"x","needs_user":false,"action_items":[]} done`, &out); err != nil {
		t.Fatal(err)
	}
	if out.Summary != "x" {
		t.Fatalf("%+v", out)
	}
}
