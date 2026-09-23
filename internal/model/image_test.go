package model

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 1x1 transparent PNG.
const tinyPNGB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

func TestGenerateImageHappyPath(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"created": 1,
			"data": [{"b64_json": "` + tinyPNGB64 + `", "generation_id": "gen_1"}],
			"usage": {
				"input_tokens": 30, "output_tokens": 196, "total_tokens": 226,
				"input_tokens_details": {"image_tokens": 0, "text_tokens": 30},
				"output_tokens_details": {"image_tokens": 196, "text_tokens": 0}
			}
		}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/v1", "gpt-image-1", 8192, "sk-test", 0)
	res, err := c.GenerateImage(context.Background(), ImageRequest{
		Model:        "gpt-image-2.5-sunburst",
		Prompt:       "flat vector marble logo",
		Size:         "1024x1024",
		Quality:      "high",
		Background:   "transparent",
		OutputFormat: "png",
		N:            1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/images/generations" {
		t.Fatalf("path %q", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("auth %q", gotAuth)
	}
	// Request model override must win over the client default.
	if gotBody["model"] != "gpt-image-2.5-sunburst" {
		t.Fatalf("body model %v", gotBody["model"])
	}
	for k, want := range map[string]interface{}{
		"prompt": "flat vector marble logo", "size": "1024x1024", "quality": "high",
		"background": "transparent", "output_format": "png",
	} {
		if gotBody[k] != want {
			t.Fatalf("body[%s] = %v want %v", k, gotBody[k], want)
		}
	}
	if len(res.Images) != 1 {
		t.Fatalf("images %d", len(res.Images))
	}
	if len(res.Images[0]) == 0 || !strings.HasPrefix(string(res.Images[0]), "\x89PNG") {
		t.Fatalf("decoded image is not a PNG (%d bytes)", len(res.Images[0]))
	}
	if res.Model != "gpt-image-2.5-sunburst" {
		t.Fatalf("model %q", res.Model)
	}
	if res.Usage == nil {
		t.Fatal("nil usage")
	}
	if res.Usage.InputTextTokens != 30 || res.Usage.OutputImageTokens != 196 || res.Usage.TotalTokens != 226 {
		t.Fatalf("usage %+v", *res.Usage)
	}
}

func TestGenerateImageDefaultsModelAndN(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + tinyPNGB64 + `"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "gpt-image-1-mini", 8192, "", 0)
	if _, err := c.GenerateImage(context.Background(), ImageRequest{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if gotBody["model"] != "gpt-image-1-mini" {
		t.Fatalf("model default not applied: %v", gotBody["model"])
	}
	if gotBody["n"] != float64(1) {
		t.Fatalf("n default not applied: %v", gotBody["n"])
	}
	// Empty optional fields must be omitted, not sent as "".
	for _, k := range []string{"size", "quality", "background", "output_format"} {
		if _, present := gotBody[k]; present {
			t.Fatalf("empty %s should be omitted, got %v", k, gotBody[k])
		}
	}
}

func TestGenerateImageHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"This model is only supported in v1/responses and not in v1/chat/completions."}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "gpt-image-1", 8192, "", 0)
	_, err := c.GenerateImage(context.Background(), ImageRequest{Prompt: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "model HTTP 404") {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(err.Error(), "only supported in v1/responses") {
		t.Fatalf("provider body lost: %v", err)
	}
}

func TestGenerateImageNoImageReturned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "gpt-image-1", 8192, "", 0)
	_, err := c.GenerateImage(context.Background(), ImageRequest{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "no image") {
		t.Fatalf("err %v", err)
	}
}

func TestGenerateImageRejectsBadBase64(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"not!base64!"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "gpt-image-1", 8192, "", 0)
	_, err := c.GenerateImage(context.Background(), ImageRequest{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "decode b64_json") {
		t.Fatalf("err %v", err)
	}
}

func TestGenerateImageNilClient(t *testing.T) {
	var c *Client
	if _, err := c.GenerateImage(context.Background(), ImageRequest{Prompt: "x"}); err == nil {
		t.Fatal("expected error for nil client")
	}
}

// Guard: the shared helper still produces the same bytes the endpoint returned.
func TestGenerateImageB64MatchesBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + tinyPNGB64 + `"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "gpt-image-1", 8192, "", 0)
	res, err := c.GenerateImage(context.Background(), ImageRequest{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := base64.StdEncoding.DecodeString(tinyPNGB64)
	if string(res.Images[0]) != string(want) {
		t.Fatal("decoded bytes differ from b64 payload")
	}
	if res.B64[0] != tinyPNGB64 {
		t.Fatal("raw b64 not preserved")
	}
}
