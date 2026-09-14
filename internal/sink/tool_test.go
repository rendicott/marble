package sink

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManageToolCRUD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sinks.json")
	m := NewManager(DefaultConfig(), path, "/ws")
	ov := map[string]string{}
	m.SetSessionHooks(
		func(string) map[string]string {
			out := map[string]string{}
			for k, v := range ov {
				out[k] = v
			}
			return out
		},
		func(_ string, next map[string]string) error {
			ov = map[string]string{}
			for k, v := range next {
				ov[k] = v
			}
			return nil
		},
	)
	ctx := context.Background()
	sid := "sess1"

	out, err := m.ManageTool(ctx, `{"action":"list"}`, sid)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"sinks"`) {
		t.Fatalf("list: %s", out)
	}

	out, err = m.ManageTool(ctx, `{"action":"create","id":"dbg","type":"stdout"}`, sid)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"id": "dbg"`) {
		t.Fatalf("create: %s", out)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"id": "dbg"`) {
		t.Fatalf("not persisted: %s", b)
	}

	_, err = m.ManageTool(ctx, `{"action":"create","id":"dbg","type":"stdout"}`, sid)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("dup: %v", err)
	}

	out, err = m.ManageTool(ctx, `{"action":"update","id":"dbg","enabled":false,"format":"json"}`, sid)
	if err != nil || !strings.Contains(out, `"enabled": false`) {
		t.Fatalf("update: %s err=%v", out, err)
	}

	_, err = m.ManageTool(ctx, `{"action":"set_override","id":"dbg","override":"on"}`, sid)
	if err != nil {
		t.Fatal(err)
	}
	if ov["dbg"] != "on" {
		t.Fatalf("override %+v", ov)
	}

	_, err = m.ManageTool(ctx, `{"action":"pause_all"}`, sid)
	if err != nil {
		t.Fatal(err)
	}
	if ov["dbg"] != "off" {
		t.Fatalf("pause %+v", ov)
	}

	_, err = m.ManageTool(ctx, `{"action":"resume_all"}`, sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(ov) != 0 {
		t.Fatalf("resume %+v", ov)
	}

	_, err = m.ManageTool(ctx, `{"action":"delete","id":"dbg"}`, sid)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.GetSink("dbg"); ok {
		t.Fatal("delete did not remove")
	}
}

func TestManageToolRejectsSecretValue(t *testing.T) {
	m := NewManager(DefaultConfig(), "", "/ws")
	_, err := m.ManageTool(context.Background(), `{"action":"create","id":"orb","type":"orb","topic_id":"t_abc","secret_env":"orb_ak_notanenv"}`, "")
	if err == nil || (!strings.Contains(err.Error(), "Orb API key") && !strings.Contains(err.Error(), "env var NAME")) {
		t.Fatalf("expected secret rejection, got %v", err)
	}
	_, err = m.ManageTool(context.Background(), `{"action":"create","id":"orb","type":"orb","topic_id":"t_abc","secret_env":"not a name"}`, "")
	if err == nil || !strings.Contains(err.Error(), "env var NAME") {
		t.Fatalf("expected name rejection, got %v", err)
	}
}

func TestManageToolCreateOrbDefaults(t *testing.T) {
	m := NewManager(DefaultConfig(), "", "/ws")
	out, err := m.ManageTool(context.Background(), `{"action":"create","id":"orb","type":"orb","topic_id":"t_abc"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	var wrap struct {
		Sink map[string]interface{} `json:"sink"`
	}
	if err := json.Unmarshal([]byte(out), &wrap); err != nil {
		t.Fatal(err)
	}
	if wrap.Sink["secret_env"] != "ORB_AK" {
		t.Fatalf("%+v", wrap.Sink)
	}
	if wrap.Sink["title_prefix"] != "marble: " {
		t.Fatalf("%+v", wrap.Sink)
	}
}

func TestManageToolUnknownAction(t *testing.T) {
	m := NewManager(DefaultConfig(), "", "/ws")
	_, err := m.ManageTool(context.Background(), `{"action":"explode"}`, "")
	if err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("%v", err)
	}
}

func TestManageToolOverrideNeedsSession(t *testing.T) {
	m := NewManager(DefaultConfig(), "", "/ws")
	_, err := m.ManageTool(context.Background(), `{"action":"create","id":"dbg","type":"stdout"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.ManageTool(context.Background(), `{"action":"set_override","id":"dbg","override":"off"}`, "")
	if err == nil || !strings.Contains(err.Error(), "no current session") {
		t.Fatalf("%v", err)
	}
}
