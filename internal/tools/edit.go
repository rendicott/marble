package tools

import (
	"fmt"
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
	count := strings.Count(text, a.OldString)
	if count == 0 {
		return "", fmt.Errorf("old_string not found in %s", a.Path)
	}
	if count > 1 && !a.ReplaceAll {
		return "", fmt.Errorf("old_string matches %d times; set replace_all=true or provide a unique string", count)
	}
	var newText string
	if a.ReplaceAll {
		newText = strings.ReplaceAll(text, a.OldString, a.NewString)
	} else {
		newText = strings.Replace(text, a.OldString, a.NewString, 1)
	}
	if err := os.WriteFile(path, []byte(newText), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("edited %s (%d replacement(s))", a.Path, count), nil
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
				if !strings.Contains(text, e.OldString) {
					rollback()
					return "", fmt.Errorf("edit[%d]: old_string not found in %s", i, e.Path)
				}
				if strings.Count(text, e.OldString) > 1 {
					rollback()
					return "", fmt.Errorf("edit[%d]: old_string not unique in %s", i, e.Path)
				}
				next = strings.Replace(text, e.OldString, e.NewString, 1)
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
	return fmt.Sprintf("apply_patch ok: %d edit(s)", len(a.Edits)), nil
}
