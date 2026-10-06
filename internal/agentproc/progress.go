package agentproc

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

// Progress is a lightweight snapshot for poll UX and stuck detection.
type Progress struct {
	ElapsedSec      int    `json:"elapsed_sec"`
	CWDMtimeChanged bool   `json:"cwd_mtime_changed"`
	NewestMtime     string `json:"newest_mtime,omitempty"` // RFC3339 if found
	StuckHint       bool   `json:"stuck_hint"`
	StuckReason     string `json:"stuck_reason,omitempty"`
	StuckAfterSec   int    `json:"stuck_after_sec,omitempty"`
	// WriteSignals is a soft count of write-like tokens in captured stdout/stderr (best-effort).
	WriteSignals int `json:"write_signals,omitempty"`

	// Multi-signal liveness (ADR-0035). Absent signals degrade sensitivity, never toward stuck.
	Alive        bool                    `json:"alive"`
	AliveAgeSec  int                     `json:"alive_age_sec"`
	AliveSignal  string                  `json:"alive_signal"`
	LastSignalAt string                  `json:"last_signal_at,omitempty"`
	Phase        string                  `json:"phase,omitempty"`
	Signals      map[string]SignalStatus `json:"signals,omitempty"`
}

// buildProgress computes elapsed / cwd activity / liveness / stuck_hint for a task.
// Does not take the manager lock (caller should hold snapshot fields).
func buildProgress(t *Task, stuckAfter time.Duration, stdout, stderr string) Progress {
	now := time.Now()
	elapsed := now.Sub(t.StartedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	p := Progress{
		ElapsedSec:    int(elapsed.Seconds()),
		StuckAfterSec: int(stuckAfter.Seconds()),
		WriteSignals:  countWriteSignals(stdout) + countWriteSignals(stderr),
	}
	var newest time.Time
	if t.CWD != "" {
		n, ok := newestMtime(t.CWD, 4, 400)
		if ok {
			newest = n
			p.NewestMtime = n.UTC().Format(time.RFC3339)
			// Allow a small grace after start so "touch" of open dirs doesn't count as success.
			grace := t.StartedAt.Add(3 * time.Second)
			p.CWDMtimeChanged = n.After(grace)
		}
	}
	if t.live != nil {
		applyLiveness(&p, t, now, newest, elapsed, stuckAfter)
		return p
	}
	// No tracker (sync run, or never started): previous cwd/write-signal rule.
	if t.Status == StatusRunning && stuckAfter > 0 && elapsed >= stuckAfter {
		if !p.CWDMtimeChanged && p.WriteSignals == 0 {
			p.StuckHint = true
			p.StuckReason = fmt.Sprintf("cwd mtime unchanged and no write signals for %ds "+
				"(no process liveness tracking for this task; format=%s)", p.ElapsedSec, t.Format)
		}
	}
	return p
}

// applyLiveness merges tracker signals with the cwd signal and decides stuck_hint:
// running, older than stuckAfter, and every available signal quiet for stuckAfter.
func applyLiveness(p *Progress, t *Task, now, newestCWD time.Time, elapsed, stuckAfter time.Duration) {
	sigs, phase := t.live.observe(now)
	cwd := SignalStatus{Available: t.CWD != "", Detail: "unchanged since start"}
	if !cwd.Available {
		cwd.Detail = "no cwd"
	} else if p.CWDMtimeChanged {
		cwd.lastAdvance = newestCWD
		cwd.Detail = "newest mtime " + p.NewestMtime
	}
	sigs[SigCWD] = cwd

	var freshest time.Time
	for _, name := range signalOrder {
		s, ok := sigs[name]
		if !ok || !s.Available {
			continue
		}
		adv := s.lastAdvance
		if adv.Before(t.StartedAt) {
			adv = t.StartedAt
		}
		if adv.After(now) {
			adv = now
		}
		s.AgeSec = int(now.Sub(adv).Seconds())
		sigs[name] = s
		if p.AliveSignal == "" || adv.After(freshest) {
			freshest = adv
			p.AliveSignal = name
		}
	}
	p.Signals = sigs
	p.Phase = phase
	if p.AliveSignal == "" {
		return // nothing measurable; never infer stuck from nothing
	}
	age := now.Sub(freshest)
	p.AliveAgeSec = int(age.Seconds())
	p.LastSignalAt = freshest.UTC().Format(time.RFC3339)
	p.Alive = age <= t.live.aliveWindow

	if t.Status == StatusRunning && stuckAfter > 0 && elapsed >= stuckAfter && age >= stuckAfter {
		p.StuckHint = true
		p.StuckReason = stuckReason(p.AliveAgeSec, sigs)
	}
}

// stuckReason names every signal measured and every one that was unavailable.
func stuckReason(ageSec int, sigs map[string]SignalStatus) string {
	var measured, missing []string
	for _, name := range signalOrder {
		s, ok := sigs[name]
		if !ok {
			continue
		}
		if s.Available {
			measured = append(measured, fmt.Sprintf("%s %ds (%s)", name, s.AgeSec, s.Detail))
		} else {
			missing = append(missing, fmt.Sprintf("%s (%s)", name, s.Detail))
		}
	}
	r := fmt.Sprintf("no liveness advance for %ds on any signal: %s", ageSec, strings.Join(measured, ", "))
	if len(missing) > 0 {
		r += "; unavailable: " + strings.Join(missing, ", ")
	}
	return r
}

var errWalkDone = errors.New("walk done")

// newestMtime walks root up to maxDepth / maxFiles and returns the newest mod time.
func newestMtime(root string, maxDepth, maxFiles int) (time.Time, bool) {
	if root == "" || maxFiles <= 0 {
		return time.Time{}, false
	}
	if maxDepth <= 0 {
		maxDepth = 3
	}
	var newest time.Time
	found := false
	n := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		base := d.Name()
		if d.IsDir() {
			switch base {
			case ".git", "node_modules", "build", ".gradle", "dist", "bin", "vendor", ".idea":
				if path != root {
					return filepath.SkipDir
				}
			}
			rel, _ := filepath.Rel(root, path)
			if rel != "." && strings.Count(rel, string(filepath.Separator)) >= maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		n++
		if n > maxFiles {
			return errWalkDone
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		mt := info.ModTime()
		if !found || mt.After(newest) {
			newest = mt
			found = true
		}
		return nil
	})
	return newest, found
}

func countWriteSignals(s string) int {
	if s == "" {
		return 0
	}
	// Soft heuristics — child CLIs may not stream tool names into stdout until exit.
	keys := []string{
		"search_replace", "apply_patch", "Write", "write_file", "edit_file",
		"\"Write\"", "file_write", "Saved", "created file", "Updated ",
	}
	low := strings.ToLower(s)
	n := 0
	for _, k := range keys {
		if strings.Contains(s, k) || strings.Contains(low, strings.ToLower(k)) {
			n++
		}
	}
	return n
}
