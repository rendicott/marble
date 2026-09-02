package agentproc

import (
	"errors"
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
}

// buildProgress computes elapsed / cwd activity / stuck_hint for a task.
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
	if t.CWD != "" {
		newest, ok := newestMtime(t.CWD, 4, 400)
		if ok {
			p.NewestMtime = newest.UTC().Format(time.RFC3339)
			// Allow a small grace after start so "touch" of open dirs doesn't count as success.
			grace := t.StartedAt.Add(3 * time.Second)
			p.CWDMtimeChanged = newest.After(grace)
		}
	}
	if t.Status == StatusRunning && stuckAfter > 0 && elapsed >= stuckAfter {
		if !p.CWDMtimeChanged && p.WriteSignals == 0 {
			p.StuckHint = true
			p.StuckReason = "running with no cwd mtime change and no write signals for stuck_after_sec"
		}
	}
	return p
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
