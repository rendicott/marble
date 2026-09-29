package session

import (
	"strings"
	"testing"

	"github.com/rendicott/marble/internal/model"
)

func imgPart(id string) model.ContentPart {
	return model.ContentPart{Type: "image_url", ImageURL: &model.ImageURL{URL: marbleAttScheme + id}}
}

func keptImageIDs(msgs []model.Message) []string {
	var ids []string
	for _, m := range msgs {
		for _, p := range m.Content.Parts {
			if p.Type == "image_url" {
				ids = append(ids, strings.TrimPrefix(p.ImageURL.URL, marbleAttScheme))
			}
		}
	}
	return ids
}

func TestApplyImageLimitKeepsNewestAndLatestUser(t *testing.T) {
	msgs := []model.Message{
		{Role: "system", Content: model.ContentFromText("sys")},
		{Role: "user", Content: model.ContentFromParts([]model.ContentPart{{Type: "text", Text: "look"}, imgPart("u1")})},
		{Role: "tool", Content: model.ContentFromParts([]model.ContentPart{{Type: "text", Text: "{}"}, imgPart("s1")})},
		{Role: "tool", Content: model.ContentFromParts([]model.ContentPart{{Type: "text", Text: "{}"}, imgPart("s2")})},
		{Role: "tool", Content: model.ContentFromParts([]model.ContentPart{{Type: "text", Text: "{}"}, imgPart("s3")})},
	}
	out, omitted := applyImageLimit(msgs, 2)
	if omitted != 2 {
		t.Fatalf("omitted=%d want 2", omitted)
	}
	got := strings.Join(keptImageIDs(out), ",")
	if got != "u1,s3" {
		t.Fatalf("kept %s want u1,s3 (latest user image + newest screenshot)", got)
	}
	if !msgs[2].Content.HasImages() {
		t.Fatal("input mutated")
	}
	// Dropped tool image collapses to plain text with a placeholder naming the id.
	if len(out[2].Content.Parts) != 0 || !strings.Contains(out[2].Content.PlainText(), "s1 omitted") {
		t.Fatalf("placeholder: %+v", out[2].Content)
	}
}

func TestApplyImageLimitUnlimitedAndUnderCap(t *testing.T) {
	msgs := []model.Message{{Role: "tool", Content: model.ContentFromParts([]model.ContentPart{imgPart("a"), imgPart("b")})}}
	if _, n := applyImageLimit(msgs, 0); n != 0 {
		t.Fatal("limit 0 must be unlimited")
	}
	if _, n := applyImageLimit(msgs, 2); n != 0 {
		t.Fatal("at cap must not trim")
	}
}

func TestParseImageLimitError(t *testing.T) {
	vllm := `model HTTP 400: {"error":{"message":"At most 2 image(s) may be provided in one prompt. (parameter=image)","type":"BadRequestError","param":"image","code":400}}`
	cases := []struct {
		msg  string
		sent int
		n    int
		ok   bool
	}{
		{vllm, 3, 2, true},
		{vllm, 2, 0, false}, // limit not below what we sent: not the cause
		{"Too many images: 101 > 100", 101, 100, true},
		{"request exceeds the maximum of 20 images", 25, 20, true},
		{"too many images in request", 5, 4, true},
		{"context length exceeded", 3, 0, false},
		{"invalid image format", 3, 0, false},
	}
	for _, c := range cases {
		n, ok := parseImageLimitError(c.msg, c.sent)
		if n != c.n || ok != c.ok {
			t.Errorf("%q sent=%d → (%d,%v) want (%d,%v)", c.msg, c.sent, n, ok, c.n, c.ok)
		}
	}
}

func TestImageLimitInfo(t *testing.T) {
	cases := []struct {
		setting, modelMax int
		mode              string
		eff               int
	}{
		{0, 0, "default", DefaultImageLimit},
		{0, 1, "default", 1},
		{5, 0, "number", 5},
		{5, 2, "number", 2},
		{ImageLimitModel, 0, "model", 0},
		{ImageLimitModel, 8, "model", 8},
	}
	for _, c := range cases {
		info := imageLimitInfo(c.setting, "", c.modelMax)
		if info.Mode != c.mode || info.Effective != c.eff {
			t.Errorf("setting=%d max=%d → %+v", c.setting, c.modelMax, info)
		}
	}
	for _, raw := range []string{"0", "-1", "101", "lots"} {
		if _, err := parseImageLimitSetting(raw); err == nil {
			t.Errorf("%q should be rejected", raw)
		}
	}
	if v, _ := parseImageLimitSetting("model"); v != ImageLimitModel {
		t.Fatal("model")
	}
	if encodeImageLimit(decodeImageLimit("7")) != "7" {
		t.Fatal("round trip")
	}
}
