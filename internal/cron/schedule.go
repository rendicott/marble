package cron

import (
	"fmt"
	"strings"
	"time"

	cronlib "github.com/robfig/cron/v3"
)

// MinIntervalSec is ADR-0015 Q11.
const MinIntervalSec = 60

var cronParser = cronlib.NewParser(
	cronlib.Minute | cronlib.Hour | cronlib.Dom | cronlib.Month | cronlib.Dow | cronlib.Descriptor,
)

// LoadLocation resolves timezone; "Local" / "" → time.Local.
func LoadLocation(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "Local") || strings.EqualFold(name, "local") {
		return time.Local, nil
	}
	return time.LoadLocation(name)
}

// ValidateSchedule checks kind + fields.
func ValidateSchedule(kind, cronExpr string, intervalSec int) error {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "cron":
		expr := strings.TrimSpace(cronExpr)
		if expr == "" {
			return fmt.Errorf("cron_expr is required for schedule_kind=cron")
		}
		if _, err := cronParser.Parse(expr); err != nil {
			return fmt.Errorf("invalid cron_expr: %w", err)
		}
	case "interval":
		if intervalSec < MinIntervalSec {
			return fmt.Errorf("interval_sec must be >= %d", MinIntervalSec)
		}
	default:
		return fmt.Errorf("schedule_kind must be cron or interval")
	}
	return nil
}

// NextRun returns the next fire time after `from` (exclusive of exact `from` for cron).
func NextRun(kind, cronExpr string, intervalSec int, tz string, from time.Time) (time.Time, error) {
	loc, err := LoadLocation(tz)
	if err != nil {
		return time.Time{}, fmt.Errorf("timezone: %w", err)
	}
	from = from.In(loc)
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "cron":
		sched, err := cronParser.Parse(strings.TrimSpace(cronExpr))
		if err != nil {
			return time.Time{}, err
		}
		return sched.Next(from), nil
	case "interval":
		if intervalSec < MinIntervalSec {
			return time.Time{}, fmt.Errorf("interval_sec must be >= %d", MinIntervalSec)
		}
		return from.Add(time.Duration(intervalSec) * time.Second), nil
	default:
		return time.Time{}, fmt.Errorf("invalid schedule_kind")
	}
}

// PreviewNext returns up to n next fire times after from.
func PreviewNext(kind, cronExpr string, intervalSec int, tz string, from time.Time, n int) ([]time.Time, error) {
	if n <= 0 {
		n = 5
	}
	if n > 20 {
		n = 20
	}
	var out []time.Time
	cur := from
	for i := 0; i < n; i++ {
		next, err := NextRun(kind, cronExpr, intervalSec, tz, cur)
		if err != nil {
			return out, err
		}
		if next.IsZero() || !next.After(cur) {
			// interval always advances; cron should too
			if strings.ToLower(kind) == "interval" {
				next = cur.Add(time.Duration(intervalSec) * time.Second)
			} else {
				break
			}
		}
		out = append(out, next)
		cur = next
	}
	return out, nil
}

// FormatRFC3339 formats t in UTC for DB storage.
func FormatRFC3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// ParseRFC3339 parses DB timestamps.
func ParseRFC3339(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}
