package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func metaJSON(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestScreenshotPrecision(t *testing.T) {
	// The field report's full shot: 1920×1080 display at 1280×720.
	full := metaJSON(t, `{"w":1280,"h":720,"scale":1.5,"screen_w":1920,"screen_h":1080,
		"region":{"x":0,"y":0,"w":1920,"h":1080},"zoom":0.667,"downscaled":true}`)
	p := screenshotPrecision(full, true)
	if !strings.Contains(p, "1.50× downscaled") || !strings.Contains(p, "region={x,y,w,h}") || strings.Contains(p, "zoomed view") {
		t.Fatalf("full shot: %q", p)
	}
	if p := screenshotPrecision(full, false); !strings.Contains(p, "upgrade marble-desktop-peer") {
		t.Fatalf("old peer should be told to upgrade: %q", p)
	}

	// Native region crop: zoomed, not downscaled.
	crop := metaJSON(t, `{"w":800,"h":1000,"scale":0.5,"screen_w":1920,"screen_h":1080,
		"region":{"x":640,"y":200,"w":400,"h":500},"zoom":2,"downscaled":false}`)
	p = screenshotPrecision(crop, true)
	if !strings.Contains(p, "zoomed view: display region x=640 y=200 w=400 h=500 of 1920×1080 at 2.00") || strings.Contains(p, "downscaled") {
		t.Fatalf("crop: %q", p)
	}

	// Older peer meta (no downscaled/region keys): inferred from scale.
	if p := screenshotPrecision(metaJSON(t, `{"w":1280,"h":720,"scale":1.5}`), false); !strings.Contains(p, "1.50× downscaled") {
		t.Fatalf("legacy meta: %q", p)
	}
	if p := screenshotPrecision(metaJSON(t, `{"w":1920,"h":1080,"scale":1}`), true); p != "" {
		t.Fatalf("1:1 full shot needs no precision note: %q", p)
	}
}

func TestRegionRequiresPeerCap(t *testing.T) {
	r := &Registry{}
	tc := &TurnContext{ReadPaths: map[string]bool{}}
	_, err := r.computerScreenshot(`{"region":{"x":1,"y":2,"w":30,"h":40}}`, tc)
	if err == nil || !strings.Contains(err.Error(), "caps.region") {
		t.Fatalf("region on a peer without caps.region must explain the upgrade: %v", err)
	}
}

func TestScreenshotFingerprintIncludesRegion(t *testing.T) {
	a := ToolFingerprint("computer_screenshot", `{"region":{"x":1,"y":2,"w":30,"h":40}}`)
	b := ToolFingerprint("computer_screenshot", `{"region":{"x":5,"y":2,"w":30,"h":40}}`)
	if a == b || a == ToolFingerprint("computer_screenshot", `{}`) {
		t.Fatalf("regions must fingerprint distinctly: %q %q", a, b)
	}
}
