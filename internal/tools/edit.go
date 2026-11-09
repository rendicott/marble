package tools

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

type editFileArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (r *Registry) editFile(argsJSON string, tc *TurnContext) (string, error) {
	var a editFileArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.Path == "" || a.OldString == "" {
		return "", fmt.Errorf("path and old_string are required")
	}
	if !wasRead(tc, a.Path) {
		return "", fmt.Errorf("edit_file requires a successful file_read on %q earlier in this turn", a.Path)
	}
	path, err := r.resolve(a.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text := string(data)
	// ADR-0040 D2: exact first, then glyph, then trimmed lines. The read gate
	// above stands before any of it.
	newText, count, tier, err := applyStringEdit(a.Path, text, a.OldString, a.NewString, a.ReplaceAll)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(newText), 0o644); err != nil {
		return "", err
	}
	return editReport(a.Path, text, newText, count, tier), nil
}

// editReport states what changed and shows it, so the model need not re-read.
func editReport(path, before, after string, count int, tier matchTier) string {
	how := ""
	if tier != tierExact {
		how = fmt.Sprintf("; matched after %s normalization", tier)
		log.Printf("tools: edit %s matched after %s normalization", path, tier)
	}
	msg := fmt.Sprintf("edited %s (%d replacement(s)%s)", path, count, how)
	if d := unifiedDiff(path, before, after); d != "" {
		msg += "\n" + d
	}
	return msg
}

func wasRead(tc *TurnContext, path string) bool {
	if tc == nil || tc.ReadPaths == nil {
		return false
	}
	if tc.ReadPaths[path] || tc.ReadPaths[filepath.Clean(path)] {
		return true
	}
	return false
}

type patchEdit struct {
	Op        string `json:"op"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

type applyPatchArgs struct {
	Edits []patchEdit `json:"edits"`
}

// reverseOp records enough to rollback.
type reverseOp struct {
	kind    string // restore_file | remove_file
	path    string
	content []byte // previous content if restore
	existed bool
}

func (r *Registry) applyPatch(argsJSON string, tc *TurnContext) (string, error) {
	var a applyPatchArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if len(a.Edits) == 0 {
		return "", fmt.Errorf("edits is empty")
	}

	var applied []reverseOp
	var diffs []string
	rollback := func() {
		for i := len(applied) - 1; i >= 0; i-- {
			op := applied[i]
			switch op.kind {
			case "remove_file":
				_ = os.Remove(op.path)
			case "restore_file":
				if !op.existed {
					_ = os.Remove(op.path)
				} else {
					_ = os.WriteFile(op.path, op.content, 0o644)
				}
			}
		}
	}

	for i, e := range a.Edits {
		op := strings.ToLower(strings.TrimSpace(e.Op))
		if e.Path == "" {
			rollback()
			return "", fmt.Errorf("edit[%d]: path required", i)
		}
		abs, err := r.resolve(e.Path)
		if err != nil {
			rollback()
			return "", fmt.Errorf("edit[%d]: %w", i, err)
		}
		switch op {
		case "add":
			if _, err := os.Stat(abs); err == nil {
				rollback()
				return "", fmt.Errorf("edit[%d]: add %s: already exists", i, e.Path)
			}
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				rollback()
				return "", fmt.Errorf("edit[%d]: %w", i, err)
			}
			if err := os.WriteFile(abs, []byte(e.Content), 0o644); err != nil {
				rollback()
				return "", fmt.Errorf("edit[%d]: %w", i, err)
			}
			applied = append(applied, reverseOp{kind: "remove_file", path: abs})
		case "update":
			if !wasRead(tc, e.Path) {
				rollback()
				return "", fmt.Errorf("edit[%d]: update requires prior file_read on %q", i, e.Path)
			}
			prev, err := os.ReadFile(abs)
			if err != nil {
				rollback()
				return "", fmt.Errorf("edit[%d]: %w", i, err)
			}
			var next string
			if e.OldString != "" {
				text := string(prev)
				var tier matchTier
				// ADR-0040 D2: same normalization ladder as edit_file, single
				// replacement only — a patch hunk must be unambiguous.
				next, _, tier, err = applyStringEdit(e.Path, text, e.OldString, e.NewString, false)
				if err != nil {
					rollback()
					return "", fmt.Errorf("edit[%d]: %w", i, err)
				}
				if d := unifiedDiff(e.Path, text, next); d != "" {
					diffs = append(diffs, d)
				}
				if tier != tierExact {
					diffs = append(diffs, fmt.Sprintf("(edit[%d] matched after %s normalization)\n", i, tier))
					log.Printf("tools: edit %s matched after %s normalization", e.Path, tier)
				}
			} else if e.Content != "" {
				next = e.Content
			} else {
				rollback()
				return "", fmt.Errorf("edit[%d]: update needs old_string/new_string or content", i)
			}
			if err := os.WriteFile(abs, []byte(next), 0o644); err != nil {
				rollback()
				return "", fmt.Errorf("edit[%d]: %w", i, err)
			}
			applied = append(applied, reverseOp{kind: "restore_file", path: abs, content: prev, existed: true})
		case "delete":
			prev, err := os.ReadFile(abs)
			existed := err == nil
			if err != nil && !os.IsNotExist(err) {
				rollback()
				return "", fmt.Errorf("edit[%d]: %w", i, err)
			}
			if existed {
				if err := os.Remove(abs); err != nil {
					rollback()
					return "", fmt.Errorf("edit[%d]: %w", i, err)
				}
				applied = append(applied, reverseOp{kind: "restore_file", path: abs, content: prev, existed: true})
			}
		default:
			rollback()
			return "", fmt.Errorf("edit[%d]: unknown op %q", i, e.Op)
		}
	}
	msg := fmt.Sprintf("apply_patch ok: %d edit(s)", len(a.Edits))
	if len(diffs) > 0 {
		msg += "\n" + strings.Join(diffs, "")
	}
	return msg, nil
}
