package model

import (
	"encoding/json"
	"testing"
)

func TestToolCallExtraContentRoundTrip(t *testing.T) {
	raw := []byte(`{"role":"assistant","tool_calls":[{"id":"x","type":"function","function":{"name":"memory_search","arguments":"{\"q\":\"a\"}"},"extra_content":{"google":{"thought_signature":"SIG123"}}}]}`)
	var m Message
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.ToolCalls) != 1 || len(m.ToolCalls[0].ExtraContent) == 0 {
		t.Fatalf("missing extra: %+v", m.ToolCalls)
	}
	nm := normalizeMessage(m)
	if len(nm.ToolCalls[0].ExtraContent) == 0 {
		t.Fatal("normalize stripped extra_content")
	}
	out, err := json.Marshal(nm)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]interface{}
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	tcs := back["tool_calls"].([]interface{})
	tc0 := tcs[0].(map[string]interface{})
	ec := tc0["extra_content"].(map[string]interface{})
	g := ec["google"].(map[string]interface{})
	if g["thought_signature"] != "SIG123" {
		t.Fatalf("lost signature: %s", string(out))
	}
}
