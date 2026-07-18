package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

type memSearchArgs struct {
	Query      string   `json:"query"`
	Since      string   `json:"since"`
	Until      string   `json:"until"`
	Tags       []string `json:"tags"`
	Scope      string   `json:"scope"` // all|session|daily|knowledge
	MaxResults int      `json:"max_results"`
}

func (r *Registry) memorySearch(argsJSON string) (string, error) {
	var a memSearchArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Query) == "" {
		return "", fmt.Errorf("query is required")
	}
	if a.MaxResults <= 0 {
		a.MaxResults = 20
	}
	scope := strings.ToLower(a.Scope)
	if scope == "" {
		scope = "all"
	}
	q := strings.ToLower(a.Query)
	var hits []string
	searchDir := func(sub string, limit int) {
		root := filepath.Join(r.Memory, sub)
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if !strings.HasSuffix(d.Name(), ".md") {
				return nil
			}
			if len(hits) >= limit {
				return fmt.Errorf("done")
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			text := string(data)
			low := strings.ToLower(text)
			if !strings.Contains(low, q) {
				// keyword tokens
				ok := false
				for _, tok := range strings.Fields(q) {
					if strings.Contains(low, tok) {
						ok = true
						break
					}
				}
				if !ok {
					return nil
				}
			}
			// tags filter
			if len(a.Tags) > 0 {
				fm := text
				if i := strings.Index(text, "---"); i >= 0 {
					if j := strings.Index(text[i+3:], "---"); j >= 0 {
						fm = text[i : i+3+j]
					}
				}
				for _, tag := range a.Tags {
					if !strings.Contains(strings.ToLower(fm), strings.ToLower(tag)) {
						return nil
					}
				}
			}
			rel, _ := filepath.Rel(r.Memory, p)
			// excerpt
			idx := strings.Index(low, strings.Fields(q)[0])
			if idx < 0 {
				idx = 0
			}
			start := idx - 40
			if start < 0 {
				start = 0
			}
			end := idx + 80
			if end > len(text) {
				end = len(text)
			}
			ex := strings.ReplaceAll(text[start:end], "\n", " ")
			hits = append(hits, fmt.Sprintf("%s: …%s…", rel, ex))
			return nil
		})
	}
	if scope == "all" || scope == "session" {
		searchDir("session", a.MaxResults)
	}
	if len(hits) < a.MaxResults && (scope == "all" || scope == "daily") {
		searchDir("daily", a.MaxResults)
	}
	if len(hits) < a.MaxResults && (scope == "all" || scope == "knowledge") {
		searchDir("knowledge", a.MaxResults)
	}
	if len(hits) == 0 {
		return "no matches", nil
	}
	return strings.Join(hits, "\n"), nil
}

type memFetchArgs struct {
	Path      string `json:"path"`
	Topic     string `json:"topic"`
	SessionID string `json:"session_id"`
}

func (r *Registry) memoryFetch(argsJSON string) (string, error) {
	var a memFetchArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	var rel string
	switch {
	case a.Path != "":
		rel = a.Path
	case a.Topic != "":
		rel = filepath.Join("knowledge", slugify(a.Topic)+".md")
	case a.SessionID != "":
		rel = filepath.Join("session", a.SessionID+".md")
	default:
		return "", fmt.Errorf("path, topic, or session_id required")
	}
	abs, err := r.resolveMemory(rel)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

type memWriteArgs struct {
	Topic   string   `json:"topic"`
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
	Mode    string   `json:"mode"`
}

func (r *Registry) memoryWrite(argsJSON string) (string, error) {
	var a memWriteArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Topic) == "" || a.Content == "" {
		return "", fmt.Errorf("topic and content required")
	}
	slug := slugify(a.Topic)
	rel := filepath.Join("knowledge", slug+".md")
	// force under knowledge only
	abs, err := r.resolveMemory(rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	now := time.Now().Format(time.RFC3339)
	tags := strings.Join(a.Tags, ", ")
	body := fmt.Sprintf("---\ntopic: %q\ntags: [%s]\nupdated_at: %s\n---\n\n# %s\n\n%s\n",
		a.Topic, tags, now, a.Topic, a.Content)
	mode := strings.ToLower(a.Mode)
	if mode == "append" {
		if prev, err := os.ReadFile(abs); err == nil {
			body = string(prev) + "\n\n---\n\n" + a.Content + "\n"
		}
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote knowledge/%s.md", slug), nil
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "note"
	}
	return out
}

func (r *Registry) skillRoots() []string {
	var roots []string
	if r.Memory != "" {
		roots = append(roots, filepath.Join(r.Memory, "skills"))
	}
	if r.Workspace != "" {
		roots = append(roots, filepath.Join(r.Workspace, ".marble", "skills"))
	}
	return roots
}

type skillSearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

func (r *Registry) skillSearch(argsJSON string) (string, error) {
	var a skillSearchArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.MaxResults <= 0 {
		a.MaxResults = 20
	}
	q := strings.ToLower(a.Query)
	var lines []string
	for _, root := range r.skillRoots() {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			skillMD := filepath.Join(root, name, "SKILL.md")
			data, err := os.ReadFile(skillMD)
			desc := ""
			if err == nil {
				desc = firstParagraph(string(data))
			}
			hay := strings.ToLower(name + " " + desc)
			if q != "" && !strings.Contains(hay, q) {
				continue
			}
			rel := skillMD
			if strings.HasPrefix(skillMD, r.Memory) {
				rel, _ = filepath.Rel(r.Memory, skillMD)
				rel = "memory:" + rel
			} else if strings.HasPrefix(skillMD, r.Workspace) {
				rel, _ = filepath.Rel(r.Workspace, skillMD)
				rel = "workspace:" + rel
			}
			lines = append(lines, fmt.Sprintf("%s\t%s\t%s", name, rel, truncate(desc, 120)))
			if len(lines) >= a.MaxResults {
				break
			}
		}
	}
	if len(lines) == 0 {
		return "no skills found", nil
	}
	return strings.Join(lines, "\n"), nil
}

type skillLoadArgs struct {
	Name string `json:"name"`
	Ref  string `json:"ref"`
}

func (r *Registry) skillLoad(argsJSON string) (string, error) {
	var a skillLoadArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	ref := a.Ref
	if ref == "" {
		ref = "SKILL.md"
	}
	if strings.Contains(ref, "..") {
		return "", fmt.Errorf("invalid ref")
	}
	for _, root := range r.skillRoots() {
		p := filepath.Join(root, a.Name, ref)
		data, err := os.ReadFile(p)
		if err == nil {
			return string(data), nil
		}
	}
	return "", fmt.Errorf("skill %q not found", a.Name)
}

func firstParagraph(md string) string {
	// skip front matter
	s := md
	if strings.HasPrefix(s, "---") {
		if i := strings.Index(s[3:], "---"); i >= 0 {
			s = s[3+i+3:]
		}
	}
	s = strings.TrimSpace(s)
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

type attachArgs struct {
	Path   string `json:"path"`
	As     string `json:"as"`
	Inline *bool  `json:"inline"`
}

func (r *Registry) attachFile(argsJSON string, tc *TurnContext) (string, error) {
	var a attachArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	abs, err := r.resolve(a.Path)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", fmt.Errorf("cannot attach directory")
	}
	name := a.As
	if name == "" {
		name = filepath.Base(a.Path)
	}
	inline := true
	if a.Inline != nil {
		inline = *a.Inline
	}
	preview := ""
	if fi.Size() <= 1<<20 && inline {
		if data, err := os.ReadFile(abs); err == nil && utf8Printable(data) {
			preview = string(data)
			if len(preview) > 2000 {
				preview = preview[:2000] + "…"
			}
		} else {
			inline = false
		}
	} else if fi.Size() > 1<<20 {
		inline = false
	}
	att := Attachment{
		Path:    a.Path,
		Name:    name,
		Inline:  inline,
		Size:    fi.Size(),
		Preview: preview,
	}
	if tc != nil && tc.OnAttachment != nil {
		tc.OnAttachment(att)
	}
	return mustJSON(map[string]interface{}{
		"attached": true,
		"path":     a.Path,
		"name":     name,
		"inline":   inline,
		"size":     fi.Size(),
		"note":     "UI attachment only; not added to model context",
	}), nil
}

func utf8Printable(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	for i := 0; i < len(b) && i < 8192; i++ {
		if b[i] == 0 {
			return false
		}
	}
	return true
}

func (r *Registry) getContextUsage(tc *TurnContext) (string, error) {
	if tc == nil || tc.GetUsage == nil {
		return "", fmt.Errorf("context usage not available")
	}
	return mustJSON(tc.GetUsage()), nil
}

type compactArgs struct {
	Style     string `json:"style"`
	KeepLastN int    `json:"keep_last_n"`
}

func (r *Registry) sessionCompact(argsJSON string, tc *TurnContext) (string, error) {
	var a compactArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.KeepLastN <= 0 {
		a.KeepLastN = 12
	}
	if tc == nil || tc.Compact == nil {
		return "", fmt.Errorf("session_compact not available in this context")
	}
	return tc.Compact(a.Style, a.KeepLastN)
}
