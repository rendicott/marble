package session

import (
	"strings"
	"testing"
	"time"

	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/db"
)

func TestSetSessionModelAllowsBusy(t *testing.T) {
	root := t.TempDir()
	d, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	now := db.UTCNow()
	row := db.ModelCatalogRow{
		ID: "test-m", DisplayName: "T", Model: "m",
		ContextLimit: 10000, MaxOutput: 1000, CapTools: true, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.ValidateModelCatalog(&row, 512); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertModelCatalog(row); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{Cfg: config.Config{Model: "proc", BaseURL: "http://127.0.0.1:9/v1", ContextLimit: 8000, MaxOutput: 500, ContextReserve: 100}}
	reg := NewRegistry(runner, nil, d, root, "proc")
	runner.Reg = reg
	s := reg.Create("t")
	s.busy = true
	if _, _, err := reg.SetSessionModel(s.ID, "test-m"); err != nil {
		t.Fatalf("allow busy: %v", err)
	}
	if s.ModelID != "test-m" {
		t.Fatalf("model_id %q", s.ModelID)
	}
	if _, _, err := reg.SetSessionModelUI(s.ID, "test-m"); err == nil || !IsBusy(err) {
		t.Fatalf("UI should busy, got %v", err)
	}
	if _, _, err := reg.SetSessionModel(s.ID, "missing"); err == nil {
		t.Fatal("want missing error")
	}
	_ = time.Now
}

// kind=image rows are tool backends, not session models: selecting one must be
// rejected with a pointer at generate_image, and a leftover selection must not
// poison model resolution (it falls back to the process default with an advisory).
func TestImageModelCannotBeSessionModel(t *testing.T) {
	root := t.TempDir()
	d, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	now := db.UTCNow()
	img := db.ModelCatalogRow{
		ID: "gpt-image-x", DisplayName: "Image", Model: "gpt-image-2.5-sunburst",
		Kind: "image", BaseURL: "https://api.openai.com/v1", APIKeyEnv: "none",
		ContextLimit: 131072, MaxOutput: 8192, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.ValidateModelCatalog(&img, 512); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertModelCatalog(img); err != nil {
		t.Fatal(err)
	}

	runner := &Runner{Cfg: config.Config{Model: "proc", BaseURL: "http://127.0.0.1:9/v1", ContextLimit: 8000, MaxOutput: 500, ContextReserve: 100}}
	reg := NewRegistry(runner, nil, d, root, "proc")
	runner.Reg = reg
	s := reg.Create("t")

	_, _, err = reg.SetSessionModel(s.ID, "gpt-image-x")
	if err == nil || !strings.Contains(err.Error(), "generate_image") {
		t.Fatalf("want kind=image rejection mentioning generate_image, got %v", err)
	}

	// Simulate a stale selection that pre-dates the kind change.
	s.mu.Lock()
	s.ModelID = "gpt-image-x"
	s.mu.Unlock()
	em := runner.resolveEffective(s, TurnOpts{})
	if em.Kind == "image" {
		t.Fatal("an image row must never resolve as the effective chat model")
	}
	if em.Source != "process" || em.Model != "proc" {
		t.Fatalf("expected process fallback, got source=%s model=%s", em.Source, em.Model)
	}
	if !strings.Contains(em.Advisory, "image-generation model") {
		t.Fatalf("advisory missing: %q", em.Advisory)
	}
}
