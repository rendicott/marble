package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func attachReg(t *testing.T) *Registry {
	t.Helper()
	png := tinyPNG(t)
	return &Registry{
		MaxResultChars: 50000,
		StageChatAttachment: func(sessionID, name string, data []byte, metaJSON string) (string, string, string, error) {
			if len(data) < 8 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
				// still allow if sniff would; tests pass png
			}
			_ = png
			id := "aabbccddeeff00112233445566778899"
			if metaJSON != "" && !strings.Contains(metaJSON, "source_url") {
				t.Errorf("expected provenance meta, got %s", metaJSON)
			}
			return id, "image/png", "image", nil
		},
	}
}

func TestAttachFromURLHappyPath(t *testing.T) {
	png := tinyPNG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer srv.Close()

	var chips int
	r := attachReg(t)
	tc := &TurnContext{SessionID: "sess1", OnChatAttachment: func(Attachment) { chips++ }}
	out := r.Execute("attach_from_url", fmt.Sprintf(`{"url":%q,"name":"tiger.png","alt":"tiger","credit":"me"}`, srv.URL+"/tiger.png"), tc)
	if strings.HasPrefix(out, "error:") {
		t.Fatal(out)
	}
	var res AttachURLResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.AttachmentID == "" || res.MIME != "image/png" {
		t.Fatalf("%+v / %s", res, out)
	}
	if chips != 1 {
		t.Fatalf("chips=%d", chips)
	}
}

func TestAttachFromURLBatchPartial(t *testing.T) {
	png := tinyPNG(t)
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png)
			return
		}
		http.NotFound(w, r)
	}))
	defer ok.Close()

	r := attachReg(t)
	tc := &TurnContext{SessionID: "sess1"}
	body := fmt.Sprintf(`{"urls":[%q,%q]}`, ok.URL+"/ok.png", ok.URL+"/missing.png")
	out := r.Execute("attach_from_url", body, tc)
	if strings.HasPrefix(out, "error:") {
		t.Fatal(out)
	}
	var wrap struct {
		OK      bool              `json:"ok"`
		Results []AttachURLResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &wrap); err != nil {
		t.Fatal(err)
	}
	if !wrap.OK || len(wrap.Results) != 2 {
		t.Fatalf("%s", out)
	}
	if !wrap.Results[0].OK || wrap.Results[1].OK || wrap.Results[1].Error != "http_status" {
		t.Fatalf("%+v", wrap.Results)
	}
}

func TestAttachFromURLRejectsSVG(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		fmt.Fprint(w, `<svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	}))
	defer srv.Close()
	r := attachReg(t)
	out := r.Execute("attach_from_url", fmt.Sprintf(`{"url":%q}`, srv.URL), &TurnContext{SessionID: "s"})
	var res AttachURLResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Error != "unsupported_type" {
		t.Fatalf("%+v %s", res, out)
	}
}

func TestAttachFromURLRejectsNonImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "hello")
	}))
	defer srv.Close()
	r := attachReg(t)
	out := r.Execute("attach_from_url", fmt.Sprintf(`{"url":%q}`, srv.URL), &TurnContext{SessionID: "s"})
	var res AttachURLResult
	_ = json.Unmarshal([]byte(out), &res)
	if res.OK || res.Error != "unsupported_type" {
		t.Fatalf("%+v %s", res, out)
	}
}

func TestAttachFromURLTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", 9<<20))
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()
	r := attachReg(t)
	out := r.Execute("attach_from_url", fmt.Sprintf(`{"url":%q}`, srv.URL), &TurnContext{SessionID: "s"})
	var res AttachURLResult
	_ = json.Unmarshal([]byte(out), &res)
	if res.OK || res.Error != "too_large" {
		t.Fatalf("%+v %s", res, out)
	}
}

func TestAttachFromURLBlockedMetadata(t *testing.T) {
	r := attachReg(t)
	out := r.Execute("attach_from_url", `{"url":"http://169.254.169.254/latest/meta-data/"}`, &TurnContext{SessionID: "s"})
	var res AttachURLResult
	_ = json.Unmarshal([]byte(out), &res)
	if res.OK || res.Error != "url_blocked" {
		t.Fatalf("%+v %s", res, out)
	}
}

func TestAttachFromURLBatchMax(t *testing.T) {
	r := attachReg(t)
	out := r.Execute("attach_from_url", `{"urls":["https://a/1","https://a/2","https://a/3","https://a/4","https://a/5"]}`, &TurnContext{SessionID: "s"})
	if !strings.Contains(out, "max 4") {
		t.Fatalf("%s", out)
	}
}

func TestAttachFromURLRateLimit(t *testing.T) {
	png := tinyPNG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer srv.Close()
	r := attachReg(t)
	tc := &TurnContext{SessionID: "s"}
	args := fmt.Sprintf(`{"url":%q}`, srv.URL)
	for i := 0; i < attachURLTurnCallMax; i++ {
		out := r.Execute("attach_from_url", args, tc)
		if strings.HasPrefix(out, "error:") {
			t.Fatalf("call %d: %s", i, out)
		}
	}
	out := r.Execute("attach_from_url", args, tc)
	if !strings.Contains(out, "rate_limited") {
		t.Fatalf("%s", out)
	}
}

func TestAttachURLsShared(t *testing.T) {
	png := tinyPNG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer srv.Close()
	r := attachReg(t)
	res, err := r.AttachURLs(context.Background(), "sess", AttachURLInput{URL: srv.URL + "/x.png"}, nil)
	if err != nil || len(res) != 1 || !res[0].OK {
		t.Fatalf("%v %+v", err, res)
	}
}
