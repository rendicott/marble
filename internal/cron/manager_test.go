package cron

import (
	"testing"
	"time"

	"github.com/rendicott/marble/internal/db"
)

func TestManagerCRUDAndFire(t *testing.T) {
	root := t.TempDir()
	d, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Mode != db.ModeNormal {
		t.Fatalf("mode %s %s", d.Mode, d.Reason)
	}

	fired := make(chan FireResult, 4)
	m := New(d, func(jobID, jobName, sessionID, prompt, modelID string) FireResult {
		if sessionID == "" {
			res := FireResult{SessionID: "newsess1", CreatedSession: true, Status: "created_session"}
			fired <- res
			return res
		}
		res := FireResult{SessionID: sessionID, Status: "ok"}
		fired <- res
		return res
	}, func() bool { return true })
	defer m.Stop()

	j, err := m.Create(CreateInput{
		Name:         "t1",
		ScheduleKind: "interval",
		IntervalSec:  60,
		Timezone:     "UTC",
		Prompt:       "hello cron",
		CreatedBy:    "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if j.ID == "" || j.NextRunAt == "" {
		t.Fatalf("job %+v", j)
	}

	// Force due by setting next_run_at in the past
	row, err := d.GetCronJob(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	row.NextRunAt = FormatRFC3339(time.Now().Add(-time.Minute))
	if err := d.UpdateCronJob(*row); err != nil {
		t.Fatal(err)
	}

	// Manual run-now
	run, err := m.RunNow(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "created_session" && run.Status != "ok" {
		t.Fatalf("run status %s err=%s", run.Status, run.Error)
	}

	// Rebind should have session id
	got, _, err := m.Get(j.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID == "" {
		t.Fatal("expected session rebind")
	}

	// Update disable
	en := false
	_, err = m.Update(j.ID, UpdateInput{Enabled: &en})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(j.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaV2OnOpen(t *testing.T) {
	root := t.TempDir()
	d, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var v int
	if err := d.SQL.QueryRow(`SELECT schema_version FROM schema_meta WHERE id=1`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != db.CurrentSchemaVersion {
		t.Fatalf("schema %d want %d", v, db.CurrentSchemaVersion)
	}
	// cron tables exist
	var n int
	if err := d.SQL.QueryRow(`SELECT COUNT(*) FROM cron_jobs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
}
