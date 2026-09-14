package sink

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebhookTemplateAndHeaders(t *testing.T) {
	var gotBody, gotTitle, gotLink, gotIdem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotTitle = r.Header.Get("X-Title")
		gotLink = r.Header.Get("X-Link")
		gotIdem = r.Header.Get("Idempotency-Key")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	spec := SinkSpec{
		ID:       "wh",
		Type:     "webhook",
		Enabled:  true,
		URL:      srv.URL,
		Method:   "POST",
		Template: `{"text":{{json .Preview}},"link":{{json .DeepLink}}}`,
		Headers: map[string]string{
			"Content-Type": "application/json",
			"X-Title":      "{{.Preview}}",
			"X-Link":       "{{.DeepLink}}",
		},
	}
	spec.normalize()
	if spec.ValidationNote != "" {
		t.Fatal(spec.ValidationNote)
	}
	ev := TurnEvent{
		SessionID:      "s1",
		Preview:        "hello \"x\"",
		Message:        "hello \"x\"\nmore",
		DeepLink:       "https://example/s/s1",
		IdempotencyKey: "abc",
		Kind:           "complete",
	}
	st, err := deliverSpec(context.Background(), srv.Client(), spec, ev, "", nil)
	if err != nil || st != 200 {
		t.Fatalf("st=%d err=%v", st, err)
	}
	if !strings.Contains(gotBody, `"text":"hello \"x\""`) {
		t.Fatalf("body %s", gotBody)
	}
	if gotTitle != `hello "x"` || gotLink != ev.DeepLink || gotIdem != "abc" {
		t.Fatalf("headers title=%q link=%q idem=%q", gotTitle, gotLink, gotIdem)
	}
}

func TestStdoutJSON(t *testing.T) {
	var buf strings.Builder
	spec := SinkSpec{ID: "dbg", Type: "stdout", Format: "json"}
	ev := TurnEvent{SessionID: "s", Kind: "complete", Preview: "p"}
	if err := deliverStdout(&buf, spec, ev); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"session_id":"s"`) {
		t.Fatalf("%s", buf.String())
	}
}

func TestConfigLoadMissing(t *testing.T) {
	c, err := Load("/no/such/sinks.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Sinks) != 0 {
		t.Fatalf("%+v", c)
	}
}

func TestOrbSendsHTTPReturnURL(t *testing.T) {
	var gotReturn, gotRunID, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReturn = r.Header.Get("X-Orb-Return-Url")
		gotRunID = r.Header.Get("X-Orb-Run-Id")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(202)
	}))
	defer srv.Close()

	spec := SinkSpec{
		ID:      "orb",
		Type:    "orb",
		Enabled: true,
		TopicID: "t_abc",
		APIBase: srv.URL,
	}
	spec.normalize()
	link := "http://rinux.example:8080/s/abc"
	ev := TurnEvent{
		SessionID:      "0wdvhcj0q0",
		Message:        "done",
		DeepLink:       link,
		IdempotencyKey: "k",
		Kind:           "complete",
	}
	st, err := deliverSpec(context.Background(), srv.Client(), spec, ev, "", nil)
	if err != nil || st != 202 {
		t.Fatalf("st=%d err=%v", st, err)
	}
	if gotReturn != link {
		t.Fatalf("http return_url should be sent, got %q", gotReturn)
	}
	if gotRunID != ev.SessionID {
		t.Fatalf("run_id should be session id, got %q", gotRunID)
	}
	if !strings.Contains(gotBody, link) {
		t.Fatalf("body should still carry the session link: %s", gotBody)
	}
}

func TestOrbOmitsEmptyRunID(t *testing.T) {
	var gotRunID string
	saw := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, saw = r.Header["X-Orb-Run-Id"]
		gotRunID = r.Header.Get("X-Orb-Run-Id")
		w.WriteHeader(202)
	}))
	defer srv.Close()
	spec := SinkSpec{ID: "orb", Type: "orb", Enabled: true, TopicID: "t_abc", APIBase: srv.URL}
	spec.normalize()
	_, err := deliverSpec(context.Background(), srv.Client(), spec, TurnEvent{
		Message: "done", DeepLink: "https://rinux.example/s/abc", IdempotencyKey: "k",
	}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if saw || gotRunID != "" {
		t.Fatalf("empty session id should omit X-Orb-Run-Id, saw=%v val=%q", saw, gotRunID)
	}
}

func TestOrbSendsHTTPSReturnURL(t *testing.T) {
	var gotReturn string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReturn = r.Header.Get("X-Orb-Return-Url")
		w.WriteHeader(202)
	}))
	defer srv.Close()
	spec := SinkSpec{ID: "orb", Type: "orb", Enabled: true, TopicID: "t_abc", APIBase: srv.URL}
	spec.normalize()
	link := "https://rinux.example/s/abc"
	_, err := deliverSpec(context.Background(), srv.Client(), spec, TurnEvent{
		Message: "done", DeepLink: link, IdempotencyKey: "k",
	}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotReturn != link {
		t.Fatalf("got %q", gotReturn)
	}
}

func TestOrbPlanURL(t *testing.T) {
	spec := SinkSpec{Type: "orb", TopicID: "t_abc", APIBase: "https://api.example"}
	p, err := spec.planOrb()
	if err != nil {
		t.Fatal(err)
	}
	if p.URL != "https://api.example/v1/topics/t_abc/messages" {
		t.Fatalf("url %s", p.URL)
	}
	if p.Headers["X-Orb-Return-Url"] != "{{.DeepLink}}" {
		t.Fatalf("headers %+v", p.Headers)
	}
	if p.Headers["X-Orb-Run-Id"] != "{{.SessionID}}" {
		t.Fatalf("run_id header %+v", p.Headers)
	}
}

func TestSlackAndDiscordTemplatesJSON(t *testing.T) {
	ev := TurnEvent{
		Preview:  "hi",
		Message:  "hello **world**",
		DeepLink: "https://ex/s/1",
	}
	for _, spec := range []SinkSpec{
		{Type: "slack", Channel: "#ops", Username: "marble", IconEmoji: ":robot_face:"},
		{Type: "discord", Username: "marble"},
	} {
		plan, err := spec.plan("https://example.invalid/hook")
		if err != nil {
			t.Fatal(err)
		}
		body, err := execTemplate(spec.Type, plan.Template, viewOf(ev, spec))
		if err != nil {
			t.Fatalf("%s template: %v", spec.Type, err)
		}
		if !json.Valid([]byte(body)) {
			t.Fatalf("%s body not json: %s", spec.Type, body)
		}
	}
}

func TestInvalidSinkNote(t *testing.T) {
	spec := SinkSpec{ID: "x", Type: "orb", Enabled: true}
	spec.normalize()
	if spec.ValidationNote == "" || spec.EffectiveEnabled() {
		t.Fatalf("%+v", spec)
	}
}
