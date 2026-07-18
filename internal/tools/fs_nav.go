package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

var defaultIgnore = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, ".marble": true,
	"__pycache__": true, ".venv": true, "dist": true, "build": true,
}

type summaryArgs struct {
	Path       string `json:"path"`
	MaxDepth   int    `json:"max_depth"`
	MaxEntries int    `json:"max_entries"`
}

func (r *Registry) codebaseSummary(argsJSON string) (string, error) {
	var a summaryArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.Path == "" {
		a.Path = "."
	}
	if a.MaxDepth <= 0 {
		a.MaxDepth = 4
	}
	if a.MaxDepth > 12 {
		a.MaxDepth = 12
	}
	if a.MaxEntries <= 0 {
		a.MaxEntries = 2000
	}
	root, err := r.resolve(a.Path)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "codebase_summary path=%s max_depth=%d\n", a.Path, a.MaxDepth)
	count := 0
	var walk func(dir string, prefix string, depth int) error
	walk = func(dir string, prefix string, depth int) error {
		if count >= a.MaxEntries {
			return fmt.Errorf("max_entries")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for i, e := range entries {
			if count >= a.MaxEntries {
				fmt.Fprintf(&b, "%s… (truncated at %d entries)\n", prefix, a.MaxEntries)
				return fmt.Errorf("max_entries")
			}
			name := e.Name()
			if defaultIgnore[name] {
				continue
			}
			connector := "├── "
			nextPref := prefix + "│   "
			if i == len(entries)-1 {
				connector = "└── "
				nextPref = prefix + "    "
			}
			full := filepath.Join(dir, name)
			if e.IsDir() {
				fmt.Fprintf(&b, "%s%s%s/\n", prefix, connector, name)
				count++
				if depth < a.MaxDepth {
					_ = walk(full, nextPref, depth+1)
				}
				continue
			}
			var size int64
			if fi, err := e.Info(); err == nil {
				size = fi.Size()
			}
			fmt.Fprintf(&b, "%s%s%s (%s)\n", prefix, connector, name, humanSize(size))
			count++
		}
		return nil
	}
	_ = walk(root, "", 1)
	return b.String(), nil
}

func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1fKiB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1fMiB", float64(n)/(1024*1024))
}

type grepArgs struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path"`
	Glob            string `json:"glob"`
	CaseInsensitive bool   `json:"case_insensitive"`
	MaxMatches      int    `json:"max_matches"`
	ContextLines    int    `json:"context_lines"`
}

func (r *Registry) grep(argsJSON string) (string, error) {
	var a grepArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if a.Path == "" {
		a.Path = "."
	}
	if a.MaxMatches <= 0 {
		a.MaxMatches = 50
	}
	if a.MaxMatches > 500 {
		a.MaxMatches = 500
	}
	if a.ContextLines < 0 {
		a.ContextLines = 0
	}
	if a.ContextLines > 5 {
		a.ContextLines = 5
	}
	flags := ""
	if a.CaseInsensitive {
		flags = "(?i)"
	}
	re, err := regexp.Compile(flags + a.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regex: %w", err)
	}
	root, err := r.resolve(a.Path)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	matches := 0
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			if d != nil && d.IsDir() && defaultIgnore[d.Name()] {
				return filepath.SkipDir
			}
			return walkErr
		}
		if matches >= a.MaxMatches {
			return fmt.Errorf("done")
		}
		if a.Glob != "" {
			ok, _ := filepath.Match(a.Glob, d.Name())
			if !ok {
				// also try full rel
				rel, _ := filepath.Rel(r.Workspace, p)
				ok2, _ := filepath.Match(a.Glob, rel)
				if !ok2 {
					return nil
				}
			}
		}
		data, err := os.ReadFile(p)
		if err != nil || !utf8.Valid(data) || bytesHasNUL(data) {
			return nil
		}
		// skip huge files
		if len(data) > 2<<20 {
			return nil
		}
		rel, _ := filepath.Rel(r.Workspace, p)
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if matches >= a.MaxMatches {
				return fmt.Errorf("done")
			}
			if !re.MatchString(line) {
				continue
			}
			matches++
			fmt.Fprintf(&b, "%s:%d: %s\n", rel, i+1, line)
			for c := 1; c <= a.ContextLines; c++ {
				if i-c >= 0 {
					fmt.Fprintf(&b, "%s:%d- %s\n", rel, i-c+1, lines[i-c])
				}
			}
			for c := 1; c <= a.ContextLines; c++ {
				if i+c < len(lines) {
					fmt.Fprintf(&b, "%s:%d+ %s\n", rel, i+c+1, lines[i+c])
				}
			}
		}
		return nil
	})
	if err != nil && err.Error() != "done" {
		return "", err
	}
	if matches == 0 {
		return "no matches", nil
	}
	fmt.Fprintf(&b, "\n(%d matches)\n", matches)
	return b.String(), nil
}

func bytesHasNUL(b []byte) bool {
	for i := 0; i < len(b) && i < 8192; i++ {
		if b[i] == 0 {
			return true
		}
	}
	return false
}

type globArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	MaxResults int    `json:"max_results"`
}

func (r *Registry) glob(argsJSON string) (string, error) {
	var a globArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if a.Path == "" {
		a.Path = "."
	}
	if a.MaxResults <= 0 {
		a.MaxResults = 500
	}
	root, err := r.resolve(a.Path)
	if err != nil {
		return "", err
	}
	// Support ** by walking and matching
	var out []string
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() && defaultIgnore[d.Name()] {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(r.Workspace, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Match pattern against rel using path/filepath and simple ** expansion
		if matchGlob(a.Pattern, rel) {
			if !d.IsDir() {
				out = append(out, rel)
			}
			if len(out) >= a.MaxResults {
				return fmt.Errorf("done")
			}
		}
		return nil
	})
	if err != nil && err.Error() != "done" {
		return "", err
	}
	if len(out) == 0 {
		return "no matches", nil
	}
	return strings.Join(out, "\n"), nil
}

// matchGlob supports * , ? , and ** across path segments.
func matchGlob(pattern, path string) bool {
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)
	// Fast path: standard match without **
	if !strings.Contains(pattern, "**") {
		ok, _ := filepath.Match(pattern, path)
		if ok {
			return true
		}
		ok, _ = filepath.Match(pattern, filepath.Base(path))
		return ok
	}
	// Convert ** to regex
	var b strings.Builder
	b.WriteString("^")
	i := 0
	for i < len(pattern) {
		if i+1 < len(pattern) && pattern[i] == '*' && pattern[i+1] == '*' {
			b.WriteString(".*")
			i += 2
			if i < len(pattern) && pattern[i] == '/' {
				i++
			}
			continue
		}
		c := pattern[i]
		switch c {
		case '*':
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
		i++
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(path)
}
