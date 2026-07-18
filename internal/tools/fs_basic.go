package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type fileReadArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (r *Registry) fileRead(argsJSON string, tc *TurnContext) (string, error) {
	var a fileReadArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	path, err := r.resolve(a.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("file is not valid UTF-8 text")
	}
	// mark read for edit_file / apply_patch
	if tc != nil {
		rel := filepath.Clean(a.Path)
		tc.ReadPaths[rel] = true
		tc.ReadPaths[a.Path] = true
	}
	text := string(data)
	if a.Offset > 0 || a.Limit > 0 {
		lines := strings.Split(text, "\n")
		start := 0
		if a.Offset > 0 {
			start = a.Offset - 1
		}
		if start > len(lines) {
			start = len(lines)
		}
		end := len(lines)
		if a.Limit > 0 && start+a.Limit < end {
			end = start + a.Limit
		}
		text = strings.Join(lines[start:end], "\n")
		return fmt.Sprintf("path=%s lines=%d-%d\n%s", a.Path, start+1, end, text), nil
	}
	return text, nil
}

type fileWriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (r *Registry) fileWrite(argsJSON string) (string, error) {
	var a fileWriteArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	path, err := r.resolve(a.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(a.Content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(a.Content), a.Path), nil
}

type listFilesArgs struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}

func (r *Registry) listFiles(argsJSON string) (string, error) {
	var a listFilesArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if a.Path == "" {
		a.Path = "."
	}
	root, err := r.resolve(a.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", a.Path)
	}

	var b strings.Builder
	if !a.Recursive {
		entries, err := os.ReadDir(root)
		if err != nil {
			return "", err
		}
		for _, e := range entries {
			typ := "file"
			if e.IsDir() {
				typ = "dir"
			}
			var size int64
			if fi, err := e.Info(); err == nil && !e.IsDir() {
				size = fi.Size()
			}
			fmt.Fprintf(&b, "%s\t%s\t%d\n", typ, e.Name(), size)
		}
		return b.String(), nil
	}

	err = filepath.WalkDir(root, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(r.Workspace, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		typ := "file"
		if d.IsDir() {
			typ = "dir"
		}
		var size int64
		if fi, err := d.Info(); err == nil && !d.IsDir() {
			size = fi.Size()
		}
		fmt.Fprintf(&b, "%s\t%s\t%d\n", typ, rel, size)
		return nil
	})
	if err != nil {
		return "", err
	}
	return b.String(), nil
}
