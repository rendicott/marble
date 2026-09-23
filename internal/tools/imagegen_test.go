package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rendicott/marble/internal/model"
)

const tinyPNGB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

// imageServer serves a minimal Images API response with usage.
func imageServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Model string `json:"model"`
			N     int    `json:"n"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.N <= 0 {
			body.N = 1
		}
		data := make([]map[string]string, 0, body.N)
		for i := 0; i < body.N; i++ {
			data = append(data, map[string]string{"b64_json": tinyPNGB64})
		}
		out := map[string]interface{}{
			"data": data,
			"usage": map[string]interface{}{
				"input_tokens": 30, "output_tokens": 196, "total_tokens": 226,
				"input_tokens_details":  map[string]int{"image_tokens": 0, "text_tokens": 30},
				"output_tokens_details": map[string]int{"image_tokens": 196, "text_tokens": 0},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
}

func imageRow(id string) map[string]interface{} {
	return map[string]interface{}{
		"id": id, "kind": "image", "enabled": true,
		"model": "gpt-image-2.5-sunburst", "display_name": "GPT Image 2.5 Sunburst",
	}
}

func imageRegistry(t *testing.T, srvURL string) *Registry {
	t.Helper()
	return &Registry{
		Workspace: t.TempDir(),
		ImageModelDefault: func() (map[string]interface{}, error) {
			return imageRow("gpt-image-2.5-sunburst"), nil
		},
		ImageModelGet: func(id string) (map[string]interface{}, error) {
			switch id {
			case "gpt-image-2.5-sunburst":
				return imageRow(id), nil
			case "chat-only":
				return map[string]interface{}{"id": id, "kind": "chat", "enabled": true}, nil
			default:
				return nil, os.ErrNotExist
			}
		},
		ImageClientFor: func(id string) (*model.Client, string, error) {
			return model.New(srvURL+"/v1", id, 8192, "sk-test", 0), "gpt-image-2.5-sunburst", nil
		},
	}
}

func decodeImageResult(t *testing.T, out string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("bad json %q: %v", out, err)
	}
	return m
}

func TestGenerateImageWritesFileAndAttaches(t *testing.T) {
	srv := imageServer(t)
	defer srv.Close()
	r := imageRegistry(t, srv.URL)

	var attached []Attachment
	tc := &TurnContext{
		SessionID: "sess1",
		OnChatAttachment: func(a Attachment) {
			attached = append(attached, a)
		},
	}
	r.StageChatAttachment = func(sessionID, name string, data []byte, metaJSON string) (string, string, string, error) {
		if sessionID != "sess1" {
			t.Errorf("stage session %q", sessionID)
		}
		if len(data) == 0 {
			t.Error("staged empty data")
		}
		if !strings.Contains(metaJSON, "generate_image") {
			t.Errorf("meta missing provenance: %s", metaJSON)
		}
		return "att_123", "image/png", "image", nil
	}
	defer func() { tc.OnChatAttachment = nil }()

	out, err := r.generateImage(`{"prompt":"flat vector marble logo","path":"brand/logo.png"}`, tc)
	if err != nil {
		t.Fatal(err)
	}
	m := decodeImageResult(t, out)
	if m["ok"] != true {
		t.Fatalf("ok=%v out=%s", m["ok"], out)
	}
	files, _ := m["files"].([]interface{})
	if len(files) != 1 {
		t.Fatalf("files %v", m["files"])
	}
	f0, _ := files[0].(map[string]interface{})
	if f0["path"] != "brand/logo.png" {
		t.Fatalf("path %v", f0["path"])
	}
	abs := filepath.Join(r.Workspace, "brand", "logo.png")
	raw, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if !strings.HasPrefix(string(raw), "\x89PNG") {
		t.Fatal("file is not a PNG")
	}
	if ids, _ := m["attachment_ids"].([]interface{}); len(ids) != 1 || ids[0] != "att_123" {
		t.Fatalf("attachment_ids %v", m["attachment_ids"])
	}
	if len(attached) != 1 || attached[0].Name != "logo.png" || !attached[0].Inline {
		t.Fatalf("chip %+v", attached)
	}
	if m["model_id"] != "gpt-image-2.5-sunburst" {
		t.Fatalf("model_id %v", m["model_id"])
	}
	// 30 text-in + 0 image-in + 196 image-out => 30*5e-6 + 196*30e-6 = 0.00603
	if cost, _ := m["est_cost_usd"].(float64); cost < 0.006 || cost > 0.0061 {
		t.Fatalf("est_cost_usd %v", cost)
	}
	if tc.ImageGenCalls != 1 {
		t.Fatalf("ImageGenCalls %d", tc.ImageGenCalls)
	}
}

func TestGenerateImageDefaultPathAndMultiple(t *testing.T) {
	srv := imageServer(t)
	defer srv.Close()
	r := imageRegistry(t, srv.URL)

	tc := &TurnContext{SessionID: "sess1"}
	r.StageChatAttachment = func(sessionID, name string, data []byte, metaJSON string) (string, string, string, error) {
		return "att_" + name, "image/png", "image", nil
	}
	out, err := r.generateImage(`{"prompt":"two variants","n":3}`, tc)
	if err != nil {
		t.Fatal(err)
	}
	m := decodeImageResult(t, out)
	files, _ := m["files"].([]interface{})
	if len(files) != 3 {
		t.Fatalf("want 3 files, got %v", m["files"])
	}
	var paths []string
	for _, f := range files {
		fm, _ := f.(map[string]interface{})
		p, _ := fm["path"].(string)
		paths = append(paths, p)
		if _, err := os.Stat(filepath.Join(r.Workspace, p)); err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
	}
	if !strings.HasPrefix(paths[0], "generated/image-") || !strings.HasSuffix(paths[0], ".png") {
		t.Fatalf("default path %q", paths[0])
	}
	// Suffixes keep all three distinct.
	if paths[0] == paths[1] || paths[1] == paths[2] {
		t.Fatalf("paths collide: %v", paths)
	}
}

func TestGenerateImageRejectsNonImageModel(t *testing.T) {
	srv := imageServer(t)
	defer srv.Close()
	r := imageRegistry(t, srv.URL)

	_, err := r.generateImage(`{"prompt":"x","model_id":"chat-only"}`, &TurnContext{SessionID: "s"})
	if err == nil || !strings.Contains(err.Error(), "kind must be image") {
		t.Fatalf("err %v", err)
	}
}

func TestGenerateImageNoModelConfigured(t *testing.T) {
	r := &Registry{
		Workspace:         t.TempDir(),
		ImageModelDefault: func() (map[string]interface{}, error) { return nil, nil },
		ImageClientFor:    func(string) (*model.Client, string, error) { return nil, "", nil },
	}
	_, err := r.generateImage(`{"prompt":"x"}`, &TurnContext{SessionID: "s"})
	if err == nil || !strings.Contains(err.Error(), "no image model configured") {
		t.Fatalf("err %v", err)
	}
}

func TestGenerateImageNotConfigured(t *testing.T) {
	r := &Registry{Workspace: t.TempDir()}
	_, err := r.generateImage(`{"prompt":"x"}`, &TurnContext{SessionID: "s"})
	if err == nil || !strings.Contains(err.Error(), "image models not configured") {
		t.Fatalf("err %v", err)
	}
}

func TestGenerateImageRequiresPromptAndJail(t *testing.T) {
	srv := imageServer(t)
	defer srv.Close()
	r := imageRegistry(t, srv.URL)

	if _, err := r.generateImage(`{}`, &TurnContext{SessionID: "s"}); err == nil || !strings.Contains(err.Error(), "prompt is required") {
		t.Fatalf("prompt err %v", err)
	}
	if _, err := r.generateImage(`{"prompt":"x","n":9}`, &TurnContext{SessionID: "s"}); err == nil || !strings.Contains(err.Error(), "n must be 1..4") {
		t.Fatalf("n err %v", err)
	}
	// Path must stay inside the workspace jail.
	if _, err := r.generateImage(`{"prompt":"x","path":"../../escape.png"}`, &TurnContext{SessionID: "s"}); err == nil {
		t.Fatal("expected jail error for escaping path")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(r.Workspace), "escape.png")); err == nil {
		t.Fatal("file escaped the workspace")
	}
}

func TestGenerateImageTurnCallCap(t *testing.T) {
	srv := imageServer(t)
	defer srv.Close()
	r := imageRegistry(t, srv.URL)

	tc := &TurnContext{SessionID: "s", ImageGenCalls: imageGenTurnCallMax}
	_, err := r.generateImage(`{"prompt":"x"}`, tc)
	if err == nil || !strings.Contains(err.Error(), "too many generate_image calls") {
		t.Fatalf("err %v", err)
	}
}

func TestGenerateImageVectorize(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	if err := exec.Command("python3", "-c", "import vtracer").Run(); err != nil {
		t.Skip("vtracer not installed")
	}
	srv := imageServer(t)
	defer srv.Close()
	r := imageRegistry(t, srv.URL)

	out, err := r.generateImage(`{"prompt":"logo","path":"logo.png","vectorize":true}`, &TurnContext{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	m := decodeImageResult(t, out)
	if m["vectorized"] != true {
		t.Fatalf("vectorized=%v notes=%v", m["vectorized"], m["notes"])
	}
	files, _ := m["files"].([]interface{})
	f0, _ := files[0].(map[string]interface{})
	svg, _ := f0["vectorized_path"].(string)
	if svg != "logo.svg" {
		t.Fatalf("vectorized_path %v", f0["vectorized_path"])
	}
	raw, err := os.ReadFile(filepath.Join(r.Workspace, "logo.svg"))
	if err != nil {
		t.Fatalf("svg not written: %v", err)
	}
	if !strings.Contains(string(raw), "<svg") {
		t.Fatal("not an svg")
	}
}

// The tool must be reachable through the normal dispatch path.
func TestGenerateImageDispatch(t *testing.T) {
	srv := imageServer(t)
	defer srv.Close()
	r := imageRegistry(t, srv.URL)

	out := r.Execute("generate_image", `{"prompt":"dispatched"}`, &TurnContext{SessionID: "s"})
	if strings.HasPrefix(out, "error:") {
		t.Fatalf("dispatch failed: %s", out)
	}
	if !strings.Contains(out, `"ok": true`) {
		t.Fatalf("unexpected output %s", out)
	}
}

// The spec must be visible to the model.
func TestGenerateImageSpecRegistered(t *testing.T) {
	var found bool
	for _, s := range allSpecs() {
		if s.Function.Name == "generate_image" {
			found = true
			if s.Function.Description == "" {
				t.Fatal("empty description")
			}
			props, _ := s.Function.Parameters["properties"].(map[string]interface{})
			for _, k := range []string{"prompt", "path", "model_id", "size", "quality", "background", "output_format", "n", "vectorize", "attach"} {
				if _, ok := props[k]; !ok {
					t.Fatalf("spec missing property %q", k)
				}
			}
			if req, _ := s.Function.Parameters["required"].([]string); len(req) != 1 || req[0] != "prompt" {
				t.Fatalf("required %v", s.Function.Parameters["required"])
			}
		}
	}
	if !found {
		t.Fatal("generate_image spec not registered")
	}
}
