package cron

import (
	"testing"
	"time"
)

func TestValidateAndNextCron(t *testing.T) {
	if err := ValidateSchedule("cron", "0 8 * * *", 0); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 7, 20, 7, 0, 0, 0, time.Local)
	next, err := NextRun("cron", "0 8 * * *", 0, "Local", from)
	if err != nil {
		t.Fatal(err)
	}
	if next.Hour() != 8 || next.Minute() != 0 {
		t.Fatalf("expected 08:00 got %v", next)
	}
}

func TestValidateInterval(t *testing.T) {
	if err := ValidateSchedule("interval", "", 30); err == nil {
		t.Fatal("expected min 60s")
	}
	if err := ValidateSchedule("interval", "", 120); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	next, err := NextRun("interval", "", 120, "UTC", from)
	if err != nil {
		t.Fatal(err)
	}
	if !next.Equal(from.Add(2 * time.Minute)) {
		t.Fatalf("got %v", next)
	}
}

func TestPreviewNext(t *testing.T) {
	from := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	times, err := PreviewNext("interval", "", 60, "UTC", from, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(times) != 5 {
		t.Fatalf("len %d", len(times))
	}
	if !times[0].Equal(from.Add(time.Minute)) {
		t.Fatalf("first %v", times[0])
	}
}
