package mpub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// MaxBodyBytes is the max published body size (ADR-0009 Q5).
	MaxBodyBytes = 2 << 20 // 2 MiB
	// DirName under memory leaf.
	DirName = "mpub"
)

// Meta is stored as meta.json beside the content file.
type Meta struct {
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	ContentType string   `json:"content_type"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
	SessionID   string   `json:"session_id,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Bytes       int      `json:"bytes"`
	Filename    string   `json:"filename"` // content.html | content.md | content.txt
}

// Doc is a published document.
type Doc struct {
	Meta    Meta
	Content string
}

// Store is a jailed mpub filesystem under $MEMORY/mpub.
type Store struct {
	Root string // absolute $MEMORY/mpub
}

// New creates/ensures the mpub root under memoryLeaf.
func New(memoryLeaf string) (*Store, error) {
	abs, err := filepath.Abs(memoryLeaf)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(abs, DirName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Store{Root: root}, nil
}

var slugRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

// ValidateSlug checks slug rules (Q8, Q12).
func ValidateSlug(slug string) error {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return fmt.Errorf("slug is required")
	}
	if slug != strings.ToLower(slug) {
		return fmt.Errorf("slug must be lowercase")
	}
	if strings.Contains(slug, "/") || strings.Contains(slug, "..") {
		return fmt.Errorf("slug must be a single path segment")
	}
	reserved := map[string]bool{"api": true, "static": true, "mpub": true, ".": true, "..": true}
	if reserved[slug] {
		return fmt.Errorf("slug %q is reserved", slug)
	}
	if !slugRE.MatchString(slug) {
		return fmt.Errorf("slug must match [a-z0-9][a-z0-9-]*[a-z0-9] (1–64 chars)")
	}
	return nil
}

func (s *Store) slugDir(slug string) (string, error) {
	if err := ValidateSlug(slug); err != nil {
		return "", err
	}
	dir := filepath.Join(s.Root, slug)
	// ensure under root
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(s.Root)
	if err != nil {
		return "", err
	}
	if abs != rootAbs && !strings.HasPrefix(abs, rootAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes mpub root")
	}
	return abs, nil
}

func contentFilename(ct string) string {
	switch normalizeContentType(ct) {
	case "text/html":
		return "content.html"
	case "text/markdown":
		return "content.md"
	default:
		return "content.txt"
	}
}

func normalizeContentType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if ct == "" {
		return "text/html" // primary (Q2)
	}
	switch ct {
	case "text/html", "html":
		return "text/html"
	case "text/markdown", "markdown", "md", "text/x-markdown":
		return "text/markdown"
	case "text/plain", "plain", "txt":
		return "text/plain"
	default:
		return ct
	}
}

// Publish writes or overwrites a document.
// ifExistsFail: when true, fail if slug already exists (Q4).
func (s *Store) Publish(slug, title, content, contentType, sessionID string, tags []string, ifExistsFail bool) (*Meta, error) {
	dir, err := s.slugDir(slug)
	if err != nil {
		return nil, err
	}
	if len(content) > MaxBodyBytes {
		return nil, fmt.Errorf("content exceeds max size %d bytes", MaxBodyBytes)
	}
	ct := normalizeContentType(contentType)
	if ct != "text/html" && ct != "text/markdown" && ct != "text/plain" {
		return nil, fmt.Errorf("unsupported content_type %q (use text/html, text/markdown, text/plain)", contentType)
	}

	exists := false
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		exists = true
	}
	if exists && ifExistsFail {
		return nil, fmt.Errorf("slug %q already exists (if_exists=fail)", slug)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var created string
	if exists {
		if old, err := s.ReadMeta(slug); err == nil && old.CreatedAt != "" {
			created = old.CreatedAt
		} else {
			created = now
		}
	} else {
		created = now
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	fn := contentFilename(ct)
	// remove other content.* files on type change
	for _, name := range []string{"content.html", "content.md", "content.txt"} {
		_ = os.Remove(filepath.Join(dir, name))
	}
	if err := os.WriteFile(filepath.Join(dir, fn), []byte(content), 0o644); err != nil {
		return nil, err
	}

	if title == "" {
		title = slug
	}
	meta := Meta{
		Slug:        slug,
		Title:       title,
		ContentType: ct,
		CreatedAt:   created,
		UpdatedAt:   now,
		SessionID:   sessionID,
		Tags:        tags,
		Bytes:       len(content),
		Filename:    fn,
	}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), append(b, '\n'), 0o644); err != nil {
		return nil, err
	}
	return &meta, nil
}

// ReadMeta loads meta.json for a slug.
func (s *Store) ReadMeta(slug string) (*Meta, error) {
	dir, err := s.slugDir(slug)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Get loads meta + content.
func (s *Store) Get(slug string) (*Doc, error) {
	meta, err := s.ReadMeta(slug)
	if err != nil {
		return nil, err
	}
	dir, _ := s.slugDir(slug)
	fn := meta.Filename
	if fn == "" {
		fn = contentFilename(meta.ContentType)
	}
	data, err := os.ReadFile(filepath.Join(dir, fn))
	if err != nil {
		// fallback try all
		for _, name := range []string{"content.html", "content.md", "content.txt"} {
			if b, e := os.ReadFile(filepath.Join(dir, name)); e == nil {
				data = b
				err = nil
				break
			}
		}
		if err != nil {
			return nil, err
		}
	}
	return &Doc{Meta: *meta, Content: string(data)}, nil
}

// List returns all published metas, newest updated first.
func (s *Store) List() ([]Meta, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := s.ReadMeta(e.Name())
		if err != nil {
			continue
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, nil
}

// Unpublish removes a slug directory.
func (s *Store) Unpublish(slug string) error {
	dir, err := s.slugDir(slug)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("slug %q not found", slug)
		}
		return err
	}
	return os.RemoveAll(dir)
}

// Count returns number of published docs.
func (s *Store) Count() int {
	list, err := s.List()
	if err != nil {
		return 0
	}
	return len(list)
}

// PublicURL builds http://127.0.0.1{port}/mpub/{slug} from --addr (Q14).
func PublicURL(addr, slug string) string {
	port := ":8080"
	a := strings.TrimSpace(addr)
	if a != "" {
		if strings.HasPrefix(a, ":") {
			port = a
		} else if i := strings.LastIndex(a, ":"); i >= 0 {
			port = a[i:]
		}
	}
	return fmt.Sprintf("http://127.0.0.1%s/mpub/%s", port, slug)
}
