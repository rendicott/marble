package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store is the on-disk memory root (--memory leaf).
// Layout:
//
//	$MEMORY/session/<id>.md
//	$MEMORY/daily/YYYY-MM-DD.md
type Store struct {
	Root string

	mu           sync.Mutex
	lastFlushErr error
	lastFlushAt  time.Time
	lastDailyAt  time.Time
}

// New creates a store and ensures session/ and daily/ exist.
func New(root string) (*Store, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	s := &Store{Root: abs}
	if err := s.EnsureLayout(); err != nil {
		return nil, err
	}
	return s, nil
}

// EnsureLayout creates session/ and daily/ under the memory root.
func (s *Store) EnsureLayout() error {
	for _, sub := range []string{"session", "daily"} {
		if err := os.MkdirAll(filepath.Join(s.Root, sub), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}
	return nil
}

// SessionPath returns $MEMORY/session/<id>.md
func (s *Store) SessionPath(id string) string {
	return filepath.Join(s.Root, "session", id+".md")
}

// DailyPath returns $MEMORY/daily/YYYY-MM-DD.md
func (s *Store) DailyPath(day time.Time) string {
	return filepath.Join(s.Root, "daily", day.In(time.Local).Format("2006-01-02")+".md")
}

// WriteSession persists a full session transcript atomically.
func (s *Store) WriteSession(doc *SessionDoc) error {
	if doc == nil || doc.ID == "" {
		return fmt.Errorf("session doc missing id")
	}
	body := EncodeSession(doc)
	path := s.SessionPath(doc.ID)
	if err := atomicWrite(path, []byte(body)); err != nil {
		s.setFlushErr(err)
		return err
	}
	s.setFlushOK()
	return nil
}

// ReadSession loads a session file by id.
func (s *Store) ReadSession(id string) (*SessionDoc, error) {
	b, err := os.ReadFile(s.SessionPath(id))
	if err != nil {
		return nil, err
	}
	return DecodeSession(string(b))
}

// ListSessionMeta returns front-matter summaries for all session files (newest first).
func (s *Store) ListSessionMeta() ([]SessionMeta, error) {
	dir := filepath.Join(s.Root, "session")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]SessionMeta, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".md")
		doc, err := s.ReadSession(id)
		if err != nil {
			continue
		}
		out = append(out, doc.SessionMeta)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

// CompactDailyIfNeeded rebuilds daily/YYYY-MM-DD.md when there was activity that day.
// If no sessions were created/updated that local calendar day, it does nothing.
func (s *Store) CompactDailyIfNeeded(day time.Time) error {
	metas, err := s.ListSessionMeta()
	if err != nil {
		return err
	}

	var docs []*SessionDoc
	var latest time.Time
	for _, m := range metas {
		if !sameLocalDay(m.UpdatedAt, day) && !sameLocalDay(m.CreatedAt, day) {
			continue
		}
		doc, err := s.ReadSession(m.ID)
		if err != nil {
			continue
		}
		docs = append(docs, doc)
		if doc.UpdatedAt.After(latest) {
			latest = doc.UpdatedAt
		}
	}
	if len(docs) == 0 {
		return nil // no activity — skip
	}

	path := s.DailyPath(day)
	if st, err := os.Stat(path); err == nil && !st.ModTime().Before(latest) {
		return nil // already up to date
	}

	body := EncodeDaily(day, docs)
	if err := atomicWrite(path, []byte(body)); err != nil {
		return err
	}
	s.mu.Lock()
	s.lastDailyAt = time.Now()
	s.mu.Unlock()
	return nil
}

// Health snapshot for /api/health.
func (s *Store) Health() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	errStr := ""
	if s.lastFlushErr != nil {
		errStr = s.lastFlushErr.Error()
	}
	return map[string]interface{}{
		"memory_path":    s.Root,
		"last_flush_at":  nullTime(s.lastFlushAt),
		"last_flush_err": errStr,
		"last_daily_at":  nullTime(s.lastDailyAt),
	}
}

func nullTime(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t
}

func (s *Store) setFlushErr(err error) {
	s.mu.Lock()
	s.lastFlushErr = err
	s.lastFlushAt = time.Now()
	s.mu.Unlock()
}

func (s *Store) setFlushOK() {
	s.mu.Lock()
	s.lastFlushErr = nil
	s.lastFlushAt = time.Now()
	s.mu.Unlock()
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func sameLocalDay(a, ref time.Time) bool {
	a = a.In(time.Local)
	r := ref.In(time.Local)
	return a.Year() == r.Year() && a.Month() == r.Month() && a.Day() == r.Day()
}
