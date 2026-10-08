package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPendingEmptyListsAreArrays(t *testing.T) {
	s := &Server{}
	b, _ := json.Marshal(s.pendingFor("nope"))
	for _, k := range []string{`"continuations":[]`, `"bg_tasks":[]`, `"agent_tasks":[]`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("UI expects arrays, got %s", b)
		}
	}
}
