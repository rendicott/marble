package tools

import (
	"context"
	"strings"
	"testing"
)

func TestManageSinksDispatches(t *testing.T) {
	var gotArgs, gotSID string
	r := &Registry{
		SinksExec: func(_ context.Context, argsJSON, sessionID string) (string, error) {
			gotArgs, gotSID = argsJSON, sessionID
			return `{"ok":true}`, nil
		},
	}
	tc := &TurnContext{SessionID: "sess1"}
	out := r.Execute("manage_sinks", `{"action":"list"}`, tc)
	if strings.HasPrefix(out, "error:") {
		t.Fatal(out)
	}
	if gotSID != "sess1" || !strings.Contains(gotArgs, "list") {
		t.Fatalf("args=%q sid=%q", gotArgs, gotSID)
	}
}

func TestManageSinksUnavailable(t *testing.T) {
	r := &Registry{}
	out := r.Execute("manage_sinks", `{"action":"list"}`, nil)
	if !strings.Contains(out, "sinks not configured") {
		t.Fatalf("%s", out)
	}
}
