package session

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClientAdvertiseDecodeIgnoresUnknown(t *testing.T) {
	raw := `{"name":"wonderstand","protocol":1,"extra":true,"nested":{"x":1}}`
	var c ClientAdvertise
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	if c.Name != "wonderstand" || c.Protocol != 1 {
		t.Fatalf("got %+v", c)
	}
}

func TestPresentationDecodeRoundTrip(t *testing.T) {
	raw := `{
		"protocol_version": 1,
		"phases": [
			{
				"id": "p1",
				"speech_text": "Hello world",
				"prose_markdown": "**Hello**",
				"visual": {
					"kind": "table",
					"table": {
						"headers": ["A", "B"],
						"rows": [["1", "2"]]
					}
				}
			},
			{
				"id": "p2",
				"speech_text": "SVG beat",
				"visual": {"kind": "svg", "svg": "<svg xmlns=\"http://www.w3.org/2000/svg\"/>"}
			},
			{
				"id": "p3",
				"speech_text": "hold previous"
			}
		]
	}`
	var p Presentation
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if p.ProtocolVersion != 1 || len(p.Phases) != 3 {
		t.Fatalf("got %+v", p)
	}
	if p.Phases[0].Visual == nil || p.Phases[0].Visual.Table == nil {
		t.Fatal("expected table visual")
	}
	if p.Phases[2].Visual != nil {
		t.Fatal("expected nil visual on p3")
	}
	// Message omitempty
	m := Message{ID: "m1", Role: "assistant", Content: "hi", Presentation: &p}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"presentation"`) {
		t.Fatalf("expected presentation in JSON: %s", b)
	}
	m2 := Message{ID: "m2", Role: "assistant", Content: "hi"}
	b2, _ := json.Marshal(m2)
	if strings.Contains(string(b2), `"presentation"`) {
		t.Fatalf("expected omitempty: %s", b2)
	}
}

func TestExtractPresentationFence(t *testing.T) {
	content := "Intro\n\n```presentation\n{\"protocol_version\":1,\"phases\":[{\"id\":\"a\",\"speech_text\":\"hi\"}]}\n```\n\nMore"
	p, found := ExtractPresentationFence(content)
	if !found || p == nil {
		t.Fatalf("found=%v p=%v", found, p)
	}
	if len(p.Phases) != 1 || p.Phases[0].SpeechText != "hi" {
		t.Fatalf("got %+v", p)
	}
	// No fence
	p2, found2 := ExtractPresentationFence("just markdown")
	if found2 || p2 != nil {
		t.Fatalf("expected none, found=%v p=%v", found2, p2)
	}
	// Bad JSON still found
	_, found3 := ExtractPresentationFence("```presentation\n{not json}\n```")
	if !found3 {
		t.Fatal("expected found on bad json")
	}
}

func TestClampPresentationPhasesAndSpeech(t *testing.T) {
	phases := make([]Phase, MaxPresentationPhases+5)
	for i := range phases {
		phases[i] = Phase{ID: "p", SpeechText: strings.Repeat("x", MaxSpeechTextBytes+50)}
	}
	p := ClampPresentation(&Presentation{ProtocolVersion: 1, Phases: phases})
	if p == nil {
		t.Fatal("expected non-nil")
	}
	if len(p.Phases) != MaxPresentationPhases {
		t.Fatalf("phases %d", len(p.Phases))
	}
	if len(p.Phases[0].SpeechText) > MaxSpeechTextBytes {
		t.Fatalf("speech not clamped: %d", len(p.Phases[0].SpeechText))
	}
	// Unique ids after clamp
	ids := map[string]bool{}
	for _, ph := range p.Phases {
		if ids[ph.ID] {
			t.Fatalf("duplicate id %s", ph.ID)
		}
		ids[ph.ID] = true
	}
}

func TestClampPresentationTotalSizeDrops(t *testing.T) {
	// One huge svg exceeds total after phase clamp
	huge := strings.Repeat("a", MaxPresentationJSONSize)
	p := ClampPresentation(&Presentation{
		ProtocolVersion: 1,
		Phases: []Phase{{
			ID:         "p1",
			SpeechText: "x",
			Visual:     &Visual{Kind: "svg", SVG: huge},
		}},
	})
	// svg is clamped to 256KiB so total may still fit; ensure clamp ran
	if p != nil && p.Phases[0].Visual != nil && len(p.Phases[0].Visual.SVG) > MaxVisualPayloadBytes {
		t.Fatal("svg not clamped")
	}
}

func TestIsWonderstandProtocol(t *testing.T) {
	if !IsWonderstandProtocol("wonderstand", 1) {
		t.Fatal("expected true")
	}
	if IsWonderstandProtocol("wonderstand", 0) {
		t.Fatal("protocol 0 should not enrich")
	}
	if IsWonderstandProtocol("web", 1) {
		t.Fatal("web should not enrich")
	}
}

func TestUnknownVisualKindDropped(t *testing.T) {
	p := ClampPresentation(&Presentation{
		ProtocolVersion: 1,
		Phases: []Phase{{
			ID:         "p1",
			SpeechText: "speak",
			Visual:     &Visual{Kind: "video", SVG: "nope"},
		}},
	})
	if p == nil || p.Phases[0].Visual != nil {
		t.Fatalf("expected visual dropped: %+v", p)
	}
}
