package session

import (
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
