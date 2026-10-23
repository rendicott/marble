package session

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rendicott/marble/internal/memory"
	"github.com/rendicott/marble/internal/model"
)

func TestProjectsToHistoryAndHarnessMarker(t *testing.T) {
	for _, role := range []string{"user", "assistant", "tool"} {
		if !projectsToHistory(role) {
			t.Errorf("%s should project", role)
		}
	}
	for _, role := range []string{"harness", "thinking", "system", ""} {
		if projectsToHistory(role) {
			t.Errorf("%s should stay out of history", role)
		}
	}
	long := strings.Repeat("m", harnessMarkerMax+40)
	msgs := []memory.TranscriptMessage{
		{Role: "harness", Content: "  older \n"},
		{Role: "user", Content: "nope"},
		{Role: "harness", Content: " \n" + long + " \n"},
	}
	got := newestHarnessMarker(msgs)
	if len(got) > harnessMarkerMax {
		t.Fatalf("len %d", len(got))
	}
	if len(got) < harnessMarkerMax-4 {
		t.Fatalf("truncated too far: %d", len(got))
	}
	if strings.HasPrefix(got, " ") || strings.HasSuffix(got, " ") {
		t.Fatalf("not trimmed: %q", got[:20])
	}
	if newestHarnessMarker(nil) != "" {
		t.Fatal("expected empty")
	}
}

func TestPlanRescueNoopIncidentNumbers(t *testing.T) {
	frame := projectionScaled([]memory.TranscriptMessage{{ID: "m", Role: "user"}}, 1)
	need := 655440 - frame
	if need <= 0 {
		t.Fatal(frame)
	}
	body := strings.Repeat("a", need*4-3)
	msgs := []memory.TranscriptMessage{{ID: "m", Role: "user", Content: body}}
	if got := projectionScaled(msgs, 1); got != 655440 {
		t.Fatalf("projection %d, want 655440", got)
	}
	plan, err := PlanRescue(msgs, 0, 1_048_576, 16_384, 1024, 1)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != "noop" || plan.OmittedMessages != 0 || plan.StubbedMessages != 0 || plan.OmittedImages != 0 {
		t.Fatalf("plan %+v", plan)
	}
	if plan.Target != 1_048_576-16_384-1024 {
		t.Fatalf("target %d", plan.Target)
	}
	if msgs[0].ModelOmit != "" || msgs[0].ModelStub != "" || msgs[0].Content != body {
		t.Fatal("input mutated")
	}
	doc := fixtureDoc(msgs)
	raw := memory.EncodeSession(doc)
	out, err := ApplyPlan(doc, plan)
	if err != nil {
		t.Fatal(err)
	}
	if memory.EncodeSession(out) != raw {
		t.Fatal("noop changed the document")
	}
}

func TestPlanRescueDropsOldestRounds(t *testing.T) {
	var msgs []memory.TranscriptMessage
	for i := 0; i < 40; i++ {
		body := strings.Repeat("a", 180)
		msgs = append(msgs,
			memory.TranscriptMessage{ID: fmt.Sprintf("u%d", i), Role: "user", Content: "user " + body},
			memory.TranscriptMessage{ID: fmt.Sprintf("a%d", i), Role: "assistant", Content: "asst " + body},
			memory.TranscriptMessage{ID: fmt.Sprintf("t%d", i), Role: "tool", ToolName: "bash", ToolCallID: fmt.Sprintf("c%d", i), Content: "tool " + body},
		)
	}
	one := projectionScaled(msgs[:3], 1)
	reserve := rescueMarkerReserve()
	fitRoom := 3*one + one/2
	room := fitRoom + reserve
	plan, err := PlanRescue(msgs, 0, room, 0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != "rescued" {
		t.Fatalf("action %s err %s before %d after %d room %d one %d", plan.Action, plan.Error, plan.BeforeEst, plan.AfterEst, plan.RoomForHistory, one)
	}
	if plan.OmittedMessages != 37*3 {
		t.Fatalf("omitted %d", plan.OmittedMessages)
	}
	out := applyMust(t, msgs, plan)
	for i, m := range out.Messages[:37*3] {
		if !pureOmit(m) {
			t.Fatalf("msg %d not omitted: %+v", i, m.ModelOmit)
		}
		if m.Content != msgs[i].Content || m.ToolCallID != msgs[i].ToolCallID {
			t.Fatalf("msg %d body changed", i)
		}
	}
	for i, m := range out.Messages[37*3 : 40*3] {
		if m.ModelOmit != "" || m.ModelStub != "" {
			t.Fatalf("newest round touched at %d omit %q stub %q", i, m.ModelOmit, m.ModelStub)
		}
	}
	assertClustersWhole(t, out.Messages[:len(msgs)])
	again, err := PlanRescue(out.Messages, 0, room, 0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if again.Action != "noop" {
		t.Fatalf("second run %s omitted %d stubbed %d", again.Action, again.OmittedMessages, again.StubbedMessages)
	}
	second := applyMust(t, out.Messages, again)
	if len(second.Messages) != len(out.Messages) {
		t.Fatalf("noop added a marker: %d → %d", len(out.Messages), len(second.Messages))
	}
}

func TestPlanRescueStubsMonsterToolResult(t *testing.T) {
	secret := strings.Repeat("SECRET", 4000)
	msgs := []memory.TranscriptMessage{
		{ID: "u", Role: "user", Content: "hi"},
		{ID: "a", Role: "assistant", Content: "running"},
		{ID: "t", Role: "tool", ToolName: "bash", ToolCallID: "call-1", Content: secret},
	}
	full := projectionScaled(msgs, 1)
	trial := append([]memory.TranscriptMessage(nil), msgs...)
	applyToolOmitStub(&trial[2])
	stubbed := projectionScaled(trial, 1)
	reserve := rescueMarkerReserve()
	fitRoom := stubbed + (full-stubbed)/2
	room := fitRoom + reserve
	plan, err := PlanRescue(msgs, 0, room, 0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != "rescued" {
		t.Fatalf("%s %s", plan.Action, plan.Error)
	}
	out := applyMust(t, msgs, plan)
	tool := out.Messages[2]
	if tool.Content != secret || tool.ToolCallID != "call-1" || tool.ToolName != "bash" {
		t.Fatalf("tool identity changed: %+v", tool)
	}
	if tool.ModelOmit != "" || !strings.Contains(tool.ModelStub, "omitted tool result") || !strings.Contains(tool.ModelStub, "call_id=call-1") || !strings.Contains(tool.ModelStub, "name=bash") {
		t.Fatalf("stub %q omit %q", tool.ModelStub, tool.ModelOmit)
	}
	if out.Messages[0].ModelOmit != "" || out.Messages[0].ModelStub != "" || out.Messages[0].Content != "hi" {
		t.Fatalf("user changed: %+v", out.Messages[0])
	}
	if projectionScaled(out.Messages[:3], 1) > plan.RoomForHistory {
		t.Fatalf("scaled %d room %d", projectionScaled(out.Messages[:3], 1), plan.RoomForHistory)
	}
}

func TestPlanRescueKeepsToolClustersWhole(t *testing.T) {
	var tools []memory.TranscriptMessage
	for i := 1; i <= 3; i++ {
		tools = append(tools, memory.TranscriptMessage{
			ID:         fmt.Sprintf("t%d", i),
			Role:       "tool",
			ToolName:   "bash",
			ToolCallID: fmt.Sprintf("c%d", i),
			Content:    strings.Repeat(fmt.Sprintf("R%d", i), 3000),
		})
	}
	msgs := []memory.TranscriptMessage{
		{ID: "u", Role: "user", Content: "go"},
		{ID: "a", Role: "assistant", Content: "calling"},
	}
	msgs = append(msgs, tools...)

	full := projectionScaled(msgs, 1)
	trial := append([]memory.TranscriptMessage(nil), msgs...)
	for i := range trial {
		if trial[i].Role == "tool" {
			applyToolOmitStub(&trial[i])
		}
	}
	stubbed := projectionScaled(trial, 1)
	reserve := rescueMarkerReserve()
	room := stubbed + (full-stubbed)/2 + reserve
	plan, err := PlanRescue(msgs, 0, room, 0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	out := applyMust(t, msgs, plan)
	body := out.Messages[:len(msgs)]
	assertClustersWhole(t, body)
	var calls []model.ToolCall
	var results []model.Message
	for _, m := range body {
		if m.Role != "tool" {
			continue
		}
		if m.ModelStub == "" || m.ModelOmit != "" {
			t.Fatalf("expected every result stubbed, got omit %q stub %q", m.ModelOmit, m.ModelStub)
		}
		calls = append(calls, model.ToolCall{ID: m.ToolCallID, Type: "function", Function: model.FunctionCall{Name: m.ToolName, Arguments: "{}"}})
		results = append(results, model.Message{Role: "tool", Name: m.ToolName, ToolCallID: m.ToolCallID, Content: model.ContentFromText(m.ModelStub)})
	}
	live := append([]model.Message{{
		Role:      "assistant",
		Content:   model.ContentFromText("calling"),
		ToolCalls: calls,
	}}, results...)
	assertSanitizedPairs(t, live)

	// Reload shape has no ToolCalls. sanitize demotes the stub rows.
	doc := fixtureDoc(body)
	s := newSession(doc.ID, doc.Title)
	s.LoadFromDoc(doc)
	var reloaded []model.Message
	for _, m := range s.history {
		if len(m.ToolCalls) != 0 {
			t.Fatal("reload restored tool calls")
		}
		if m.Role == "system" {
			continue
		}
		reloaded = append(reloaded, m)
		if m.Role == "tool" && strings.Contains(m.Content.PlainText(), "R1R1") {
			t.Fatal("history kept the tool body")
		}
	}
	assertSanitizedPairs(t, sanitizeToolCallHistory(reloaded))
}

func TestPlanRescueOmitsWholeClusterWhenStubsStillOverflow(t *testing.T) {
	msgs := []memory.TranscriptMessage{
		{ID: "u", Role: "user", Content: "hi"},
		{ID: "a", Role: "assistant", Content: "ok"},
	}
	for i := 1; i <= 3; i++ {
		msgs = append(msgs, memory.TranscriptMessage{
			ID: fmt.Sprintf("t%d", i), Role: "tool", ToolName: "bash", ToolCallID: fmt.Sprintf("c%d", i),
			Content: strings.Repeat("Z", 8000),
		})
	}
	kept := projectionScaled(msgs[:2], 1)
	trial := append([]memory.TranscriptMessage(nil), msgs...)
	for i := range trial {
		if trial[i].Role == "tool" {
			applyToolOmitStub(&trial[i])
		}
	}
	withStubs := projectionScaled(trial, 1)
	if withStubs <= kept {
		t.Fatal("stubs did not cost more than the user turn")
	}
	reserve := rescueMarkerReserve()
	fitRoom := kept + (withStubs-kept)/2
	if fitRoom <= kept || fitRoom >= withStubs {
		t.Fatalf("fitRoom %d kept %d stubs %d", fitRoom, kept, withStubs)
	}
	room := fitRoom + reserve
	plan, err := PlanRescue(msgs, 0, room, 0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	out := applyMust(t, msgs, plan)
	body := out.Messages[:len(msgs)]
	assertClustersWhole(t, body)
	for _, m := range body {
		if m.Role != "tool" {
			if m.ModelOmit != "" || m.ModelStub != "" {
				t.Fatalf("non-tool changed: %+v", m)
			}
			continue
		}
		if !pureOmit(m) {
			t.Fatalf("tool not omitted together: omit %q stub %q", m.ModelOmit, m.ModelStub)
		}
	}
	live := []model.Message{{
		Role:    "assistant",
		Content: model.ContentFromText("ok"),
		// Pass 3 cleared the tool-call list because every result was omitted.
	}}
	assertSanitizedPairs(t, live)
}

func TestPlanRescueDropsImagesBeforeText(t *testing.T) {
	var atts []memory.TranscriptAttachment
	for i := 0; i < 30; i++ {
		atts = append(atts, memory.TranscriptAttachment{ID: fmt.Sprintf("img%02d", i), Kind: "image", Name: "a.png"})
	}
	msgs := []memory.TranscriptMessage{
		{ID: "old", Role: "user", Content: "round one stays as text", Attachments: atts},
		{ID: "new", Role: "user", Content: "round two stays", Attachments: []memory.TranscriptAttachment{{ID: "newimg", Kind: "image", Name: "b.png"}}},
	}
	scale := 4.0
	full := projectionScaled(msgs, scale)
	trial := append([]memory.TranscriptMessage(nil), msgs...)
	trial[0].ModelStub = imageOmittedStub(trial[0], messageImageIDs(trial[0]))
	after := projectionScaled(trial, scale)
	if after >= full {
		t.Fatal("dropping images did not shrink the projection")
	}
	reserve := rescueMarkerReserve()
	fitRoom := after + 50
	if fitRoom >= full {
		fitRoom = after
	}
	room := fitRoom + reserve
	plan, err := PlanRescue(msgs, 0, int(room), 0, 0, scale)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != "rescued" || plan.OmittedImages != 30 {
		t.Fatalf("%+v", plan)
	}
	out := applyMust(t, msgs, plan)
	old, newest := out.Messages[0], out.Messages[1]
	if old.ModelOmit != "" || !strings.Contains(old.ModelStub, "[image omitted img00]") {
		t.Fatalf("old stub %q omit %q", old.ModelStub, old.ModelOmit)
	}
	if len(old.Attachments) != 30 || old.Attachments[0].ID != "img00" || old.Content != msgs[0].Content {
		t.Fatal("attachment bytes or text changed")
	}
	pm, ok := projectedMessage(old)
	if !ok || pm.Content.HasImages() {
		t.Fatal("old images still in the projection")
	}
	if newest.ModelOmit != "" || newest.ModelStub != "" || newest.Content != "round two stays" {
		t.Fatalf("newest text round changed: %+v", newest)
	}
	if len(newest.Attachments) != 1 || newest.Attachments[0].ID != "newimg" {
		t.Fatal("newest attachment dropped")
	}
	if pm, ok := projectedMessage(newest); !ok || !pm.Content.HasImages() {
		t.Fatal("newest image should still project")
	}
}

func TestPlanRescueTailsMonsterUserMessage(t *testing.T) {
	body := strings.Repeat("abcd", 20000)
	msgs := []memory.TranscriptMessage{{ID: "monster", Role: "user", Content: body}}
	reserve := rescueMarkerReserve()
	room := reserve + 400
	plan, err := PlanRescue(msgs, 0, room, 0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != "rescued" {
		t.Fatalf("%s %s", plan.Action, plan.Error)
	}
	out := applyMust(t, msgs, plan)
	m := out.Messages[0]
	if m.Content != body || m.ModelOmit != "" {
		t.Fatalf("content/omit changed omit %q", m.ModelOmit)
	}
	if !strings.HasPrefix(m.ModelStub, overageTailPrefix) {
		t.Fatalf("stub %q", m.ModelStub)
	}
	if strings.Count(m.ModelStub, overageTailPrefix) != 1 {
		t.Fatal("nested rescue prefix")
	}
	suffix := tailBody(m.ModelStub)
	prefix := strings.TrimSuffix(m.ModelStub, suffix)
	want := fmt.Sprintf("…[overage-rescue kept the last %d chars of monster]\n", len(suffix))
	if prefix != want {
		t.Fatalf("prefix %q", prefix)
	}
	if !strings.HasSuffix(body, suffix) {
		t.Fatal("suffix is not a tail of the body")
	}
	if projectionScaled(out.Messages[:1], 1) > plan.RoomForHistory {
		t.Fatalf("stub scaled %d room %d", projectionScaled(out.Messages[:1], 1), plan.RoomForHistory)
	}
}

func TestPlanRescueScaleCutsWhenRawEstimateFits(t *testing.T) {
	msgs := []memory.TranscriptMessage{{ID: "m", Role: "user", Content: strings.Repeat("b", 8000)}}
	raw := projectionScaled(msgs, 1)
	scale := 655440.0 / 600000.0
	scaled := projectionScaled(msgs, scale)
	if scaled <= raw {
		t.Fatalf("raw %d scaled %d", raw, scaled)
	}
	room := raw + (scaled-raw)/2
	if room < raw || scaled <= room {
		t.Fatalf("room %d raw %d scaled %d", room, raw, scaled)
	}
	plan, err := PlanRescue(msgs, 0, room, 0, 0, scale)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != "rescued" || plan.StubbedMessages != 1 {
		t.Fatalf("%+v", plan)
	}
	out := applyMust(t, msgs, plan)
	if out.Messages[0].Content != msgs[0].Content || !strings.HasPrefix(out.Messages[0].ModelStub, overageTailPrefix) {
		t.Fatal("expected a tail stub")
	}
}

func TestPlanRescueUnrecoverableLeavesDocument(t *testing.T) {
	msgs := []memory.TranscriptMessage{{ID: "m", Role: "user", Content: "hello"}}
	doc := fixtureDoc(msgs)
	raw := memory.EncodeSession(doc)
	plan, err := PlanRescue(msgs, 10_000, 5_000, 0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != "unrecoverable" || plan.Error == "" {
		t.Fatalf("%+v", plan)
	}
	out, err := ApplyPlan(doc, plan)
	if err != nil {
		t.Fatal(err)
	}
	if memory.EncodeSession(out) != raw {
		t.Fatal("unrecoverable changed the document")
	}
	if doc.Messages[0].Content != "hello" {
		t.Fatal("input mutated")
	}
}

func TestPlanRescueAlreadyOmittedIsNoop(t *testing.T) {
	msgs := []memory.TranscriptMessage{
		{ID: "old", Role: "user", Content: strings.Repeat("z", 5000), ModelOmit: overageRescueTag},
		{ID: "new", Role: "user", Content: "kept"},
	}
	plan, err := PlanRescue(msgs, 0, 100_000, 0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != "noop" {
		t.Fatalf("%+v", plan)
	}
}

func TestApplyPlanFixtureFileRoundTrip(t *testing.T) {
	raw, err := os.ReadFile("testdata/overage_fixture.md")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := memory.DecodeSession(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Messages) != 2 || doc.Messages[0].Content == "" {
		t.Fatalf("fixture decoded %+v", doc.Messages)
	}
	oldCost := projectionScaled(doc.Messages[:1], 1)
	reserve := rescueMarkerReserve()
	// Fit the newest message only: room sits under two rounds and over one.
	one := projectionScaled(doc.Messages[1:2], 1)
	two := projectionScaled(doc.Messages, 1)
	if two <= one || oldCost <= 0 {
		t.Fatalf("one %d two %d", one, two)
	}
	fitRoom := one + (two-one)/2
	room := fitRoom + reserve
	plan, err := PlanRescue(doc.Messages, 0, room, 0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != "rescued" {
		t.Fatalf("%s %s", plan.Action, plan.Error)
	}
	plan.At = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	out, err := ApplyPlan(doc, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 3 || out.Messages[2].Role != "harness" || out.Messages[2].ModelOmit != overageRescueTag {
		t.Fatalf("marker %+v", out.Messages[len(out.Messages)-1])
	}
	if !pureOmit(out.Messages[0]) || out.Messages[1].ModelOmit != "" {
		t.Fatalf("flags old %q/%q new %q/%q", out.Messages[0].ModelOmit, out.Messages[0].ModelStub, out.Messages[1].ModelOmit, out.Messages[1].ModelStub)
	}
	encoded := memory.EncodeSession(out)
	decoded, err := memory.DecodeSession(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Messages[0].ModelOmit != overageRescueTag || decoded.Messages[2].Role != "harness" {
		t.Fatal("encode/decode lost rescue flags")
	}
	s := newSession(decoded.ID, decoded.Title)
	s.LoadFromDoc(decoded)
	var hist []string
	for _, m := range s.history {
		if m.Role == "harness" {
			t.Fatal("harness row entered history as its own turn")
		}
		hist = append(hist, m.Role+":"+m.Content.PlainText())
	}
	joined := strings.Join(hist, "\n")
	if strings.Contains(joined, "OLDROUND") {
		t.Fatal("omitted round is in history")
	}
	if !strings.Contains(joined, "NEWROUND") {
		t.Fatal("newest round missing from history")
	}
	if !strings.Contains(s.history[0].Content.PlainText(), "Context rescue") {
		t.Fatal("marker was not appended to the system message")
	}
}

func TestOverageFlagsRoundTripThroughLoad(t *testing.T) {
	stub := "kept tail --> still here"
	doc := fixtureDoc([]memory.TranscriptMessage{
		{ID: "omit", Role: "user", Content: "hidden body", ModelOmit: overageRescueTag},
		{ID: "stub", Role: "user", Content: "full body", ModelStub: stub},
		{ID: "h", Role: "harness", Content: "  rescue note  "},
	})
	decoded, err := memory.DecodeSession(memory.EncodeSession(doc))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Messages[0].ModelOmit != overageRescueTag || decoded.Messages[0].ModelStub != "" {
		t.Fatalf("omit: %+v", decoded.Messages[0])
	}
	if decoded.Messages[1].ModelStub != stub || decoded.Messages[1].Content != "full body" {
		t.Fatalf("stub: %+v", decoded.Messages[1])
	}
	s := newSession(doc.ID, doc.Title)
	s.LoadFromDoc(decoded)
	var sawStub bool
	for _, m := range s.history {
		if strings.Contains(m.Content.PlainText(), "hidden body") || strings.Contains(m.Content.PlainText(), "full body") {
			t.Fatalf("raw body in history: %s", m.Content.PlainText())
		}
		if strings.Contains(m.Content.PlainText(), stub) {
			sawStub = true
		}
	}
	if !sawStub {
		t.Fatal("history missing stub")
	}
	if !strings.Contains(s.history[0].Content.PlainText(), "rescue note") {
		t.Fatal("harness marker missing from system")
	}
}

func fixtureDoc(msgs []memory.TranscriptMessage) *memory.SessionDoc {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	cp := append([]memory.TranscriptMessage(nil), msgs...)
	for i := range cp {
		if cp[i].CreatedAt.IsZero() {
			cp[i].CreatedAt = now
		}
	}
	return &memory.SessionDoc{
		SessionMeta: memory.SessionMeta{
			ID:        "ovtest01",
			Title:     "t",
			Kind:      "user",
			CreatedAt: now,
			UpdatedAt: now,
			Status:    "active",
		},
		Messages: cp,
	}
}

func applyMust(t *testing.T, msgs []memory.TranscriptMessage, plan RescuePlan) *memory.SessionDoc {
	t.Helper()
	plan.At = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	doc := fixtureDoc(msgs)
	out, err := ApplyPlan(doc, plan)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action == "rescued" {
		last := out.Messages[len(out.Messages)-1]
		if last.Role != "harness" || last.ModelOmit != overageRescueTag || !strings.Contains(last.Content, "Context rescue") {
			t.Fatalf("marker %+v", last)
		}
	}
	return out
}

func assertClustersWhole(t *testing.T, msgs []memory.TranscriptMessage) {
	t.Helper()
	// Group consecutive tool rows in file order. splitRounds drops pure omits,
	// which would hide a cluster that omitted only some of its results.
	var tools []memory.TranscriptMessage
	flush := func() {
		if len(tools) == 0 {
			return
		}
		omitted, kept := 0, 0
		for _, m := range tools {
			if pureOmit(m) {
				omitted++
			} else {
				kept++
			}
		}
		if omitted > 0 && kept > 0 {
			t.Fatalf("tool cluster split: omitted %d kept %d", omitted, kept)
		}
		tools = nil
	}
	for _, m := range msgs {
		if m.Role == "tool" {
			tools = append(tools, m)
			continue
		}
		flush()
	}
	flush()
}

func assertSanitizedPairs(t *testing.T, msgs []model.Message) {
	t.Helper()
	out := sanitizeToolCallHistory(msgs)
	for i, m := range out {
		if m.Role == "tool" {
			if i == 0 || out[i-1].Role != "tool" && out[i-1].Role != "assistant" {
				t.Fatalf("orphan tool at %d", i)
			}
			ok := false
			for j := i - 1; j >= 0; j-- {
				if out[j].Role == "assistant" {
					for _, tc := range out[j].ToolCalls {
						if tc.ID == m.ToolCallID {
							ok = true
						}
					}
					break
				}
				if out[j].Role != "tool" {
					break
				}
			}
			if !ok {
				t.Fatalf("tool %s has no matching call", m.ToolCallID)
			}
		}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			need := map[string]bool{}
			for _, tc := range m.ToolCalls {
				need[tc.ID] = true
			}
			for j := i + 1; j < len(out) && out[j].Role == "tool"; j++ {
				delete(need, out[j].ToolCallID)
			}
			if len(need) > 0 {
				t.Fatalf("assistant tool calls missing results: %v", need)
			}
		}
	}
}
