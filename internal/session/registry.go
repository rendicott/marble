package session

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/memory"
)

// Registry holds live sessions and coordinates persistence.
type Registry struct {
	mu        sync.RWMutex
	sessions  map[string]*Session
	runner    *Runner
	store     *memory.Store
	sqldb     *db.DB
	workspace string
	model     string
	diskIndex map[string]memory.SessionMeta

	// Optional process managers (set by main).
	OnSessionClose func(sessionID string)
}

// NewRegistry creates a registry. sqldb may be limp (non-writable).
func NewRegistry(runner *Runner, store *memory.Store, sqldb *db.DB, workspace, modelName string) *Registry {
	r := &Registry{
		sessions:  make(map[string]*Session),
		runner:    runner,
		store:     store,
		sqldb:     sqldb,
		workspace: workspace,
		model:     modelName,
		diskIndex: make(map[string]memory.SessionMeta),
	}
	if store != nil {
		r.refreshDiskIndex()
	}
	return r
}

// DB returns the sqlite wrapper (may be limp).
func (r *Registry) DB() *db.DB { return r.sqldb }

func (r *Registry) refreshDiskIndex() {
	if r.store == nil {
		return
	}
	metas, err := r.store.ListSessionMeta()
	if err != nil {
		log.Printf("memory index: %v", err)
		return
	}
	idx := make(map[string]memory.SessionMeta, len(metas))
	for _, m := range metas {
		idx[m.ID] = m
	}
	r.diskIndex = idx
}

// Create adds a new user session with a short id.
func (r *Registry) Create(title string) *Session {
	return r.create(title, "user", "")
}

// CreateSystem adds a system-agent session (ADR-0005 Q4).
func (r *Registry) CreateSystem(title, parentID string) *Session {
	return r.create(title, "system", parentID)
}

func (r *Registry) create(title, kind, parentID string) *Session {
	id := memory.NewSessionID()
	for i := 0; i < 5; i++ {
		r.mu.RLock()
		_, live := r.sessions[id]
		_, disk := r.diskIndex[id]
		r.mu.RUnlock()
		if !live && !disk {
			break
		}
		id = memory.NewSessionID()
	}
	s := newSession(id, title)
	s.Kind = kind
	if s.Kind == "" {
		s.Kind = "user"
	}
	s.ParentID = parentID
	s.dirty = true
	r.mu.Lock()
	r.sessions[id] = s
	r.mu.Unlock()
	_ = r.PersistSession(s)
	r.syncSessionRow(s)
	return s
}

// Get returns a loaded live session.
func (r *Registry) Get(id string) (*Session, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sessions[id]
	return s, ok
}

// EnsureLoaded loads a session from disk into the live map if needed.
func (r *Registry) EnsureLoaded(id string) (*Session, error) {
	if s, ok := r.Get(id); ok {
		return s, nil
	}
	if r.store == nil {
		return nil, fmt.Errorf("session not found")
	}
	doc, err := r.store.ReadSession(id)
	if err != nil {
		return nil, fmt.Errorf("session not found")
	}
	s := newSession(doc.ID, doc.Title)
	s.LoadFromDoc(doc)
	r.mu.Lock()
	if existing, ok := r.sessions[id]; ok {
		r.mu.Unlock()
		return existing, nil
	}
	r.sessions[id] = s
	r.mu.Unlock()
	r.syncSessionRow(s)
	return s, nil
}

// List returns session summaries (live + disk), newest first.
func (r *Registry) List() []Summary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool)
	out := make([]Summary, 0, len(r.sessions)+len(r.diskIndex))

	for _, s := range r.sessions {
		out = append(out, s.Summary())
		seen[s.ID] = true
	}
	for id, m := range r.diskIndex {
		if seen[id] {
			continue
		}
		kind := m.Kind
		if kind == "" {
			kind = "user"
		}
		out = append(out, Summary{
			ID:           m.ID,
			Title:        m.Title,
			Kind:         kind,
			ParentID:     m.ParentID,
			CreatedAt:    m.CreatedAt,
			UpdatedAt:    m.UpdatedAt,
			ClosedAt:     m.ClosedAt,
			Status:       m.Status,
			MessageCount: m.MessageCount,
			Busy:         false,
			Loaded:       false,
			Dirty:        false,
		})
	}
	// Prefer DB list when writable for closed flags accuracy
	if r.sqldb != nil && r.sqldb.Writable() {
		if rows, err := r.sqldb.ListSessions(true); err == nil {
			byID := make(map[string]Summary, len(out))
			for _, s := range out {
				byID[s.ID] = s
			}
			for _, row := range rows {
				sum := byID[row.ID]
				sum.ID = row.ID
				sum.Title = row.Title
				sum.Status = row.Status
				sum.MessageCount = row.MessageCount
				sum.Dirty = row.Dirty
				if t, err := time.Parse(time.RFC3339, row.CreatedAt); err == nil {
					sum.CreatedAt = t
				}
				if t, err := time.Parse(time.RFC3339, row.UpdatedAt); err == nil {
					sum.UpdatedAt = t
				}
				if row.ClosedAt.Valid {
					if t, err := time.Parse(time.RFC3339, row.ClosedAt.String); err == nil {
						sum.ClosedAt = &t
					}
				}
				// preserve busy/loaded from live
				if live, ok := r.sessions[row.ID]; ok {
					ls := live.Summary()
					sum.Busy = ls.Busy
					sum.Loaded = true
				}
				byID[row.ID] = sum
			}
			out = out[:0]
			for _, s := range byID {
				out = append(out, s)
			}
		}
	}

	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].UpdatedAt.After(out[i].UpdatedAt) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// PostUserMessage routes a message to a session via the runner.
func (r *Registry) PostUserMessage(id, text string) error {
	s, err := r.EnsureLoaded(id)
	if err != nil {
		return err
	}
	if s.Status == "closed" {
		return fmt.Errorf("session is closed")
	}
	if !r.runner.PostUserMessage(s, text) {
		return errBusy
	}
	return nil
}

// Stop requests cancel of the in-flight turn (ADR-0010).
// Returns errBusy-style not-busy when session is idle.
func (r *Registry) Stop(id string) error {
	s, err := r.EnsureLoaded(id)
	if err != nil {
		return err
	}
	if !s.RequestStop() {
		return errNotBusy
	}
	return nil
}

// Progress returns turn progress for a session (active or last completed).
func (r *Registry) Progress(id string) (TurnProgress, error) {
	s, err := r.EnsureLoaded(id)
	if err != nil {
		return TurnProgress{}, err
	}
	return s.Progress(), nil
}

var errNotBusy = fmt.Errorf("session not busy")

// IsNotBusy reports whether err means stop was rejected because idle.
func IsNotBusy(err error) bool {
	return err == errNotBusy
}

// Close flushes the session, marks closed, updates DB.
func (r *Registry) Close(id string) error {
	s, err := r.EnsureLoaded(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return errBusy
	}
	now := time.Now()
	s.Status = "closed"
	s.ClosedAt = &now
	s.UpdatedAt = now
	s.dirty = true
	s.mu.Unlock()

	if r.OnSessionClose != nil {
		r.OnSessionClose(id)
	}

	if err := r.PersistSession(s); err != nil {
		return err
	}
	if r.sqldb != nil && r.sqldb.Writable() {
		_ = r.sqldb.MarkClosed(id, now)
		r.syncSessionRow(s)
	}

	r.mu.Lock()
	delete(r.sessions, id)
	if r.store != nil {
		if doc, err := r.store.ReadSession(id); err == nil {
			r.diskIndex[id] = doc.SessionMeta
		}
	}
	r.mu.Unlock()

	s.publish(Event{Type: "status", Status: "closed"})
	return nil
}

// PersistSession writes markdown session file.
func (r *Registry) PersistSession(s *Session) error {
	if r.store == nil || s == nil {
		return nil
	}
	doc := s.SnapshotDoc(r.workspace, r.model)
	if err := r.store.WriteSession(doc); err != nil {
		return err
	}
	s.clearDirty()
	r.mu.Lock()
	r.diskIndex[s.ID] = doc.SessionMeta
	r.mu.Unlock()
	r.syncSessionRow(s)
	return nil
}

// PersistDirty flushes all dirty live sessions to Markdown.
func (r *Registry) PersistDirty() (flushed int, err error) {
	r.mu.RLock()
	list := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		if s.IsDirty() {
			list = append(list, s)
		}
	}
	r.mu.RUnlock()

	var firstErr error
	for _, s := range list {
		if e := r.PersistSession(s); e != nil {
			if firstErr == nil {
				firstErr = e
			}
			log.Printf("persist %s: %v", s.ID, e)
			continue
		}
		flushed++
	}
	return flushed, firstErr
}

// DirtyCount returns number of dirty live sessions.
func (r *Registry) DirtyCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, s := range r.sessions {
		if s.IsDirty() {
			n++
		}
	}
	return n
}

// LiveCount returns loaded session count.
func (r *Registry) LiveCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.sessions)
}

// CompactToday runs daily compaction for the local calendar day if activity exists.
func (r *Registry) CompactToday() error {
	if r.store == nil {
		return nil
	}
	return r.store.CompactDailyIfNeeded(time.Now())
}

// Store returns the markdown store.
func (r *Registry) Store() *memory.Store {
	return r.store
}

// RunMaintenance prune + blob GC (normal mode only).
func (r *Registry) RunMaintenance() (pruned, blobs int, err error) {
	if r.sqldb == nil || !r.sqldb.Writable() {
		return 0, 0, nil
	}
	maxClosed := r.sqldb.SettingInt("closed_session_max_age_days", 4)
	ids, err := r.sqldb.PruneClosed(maxClosed)
	if err != nil {
		return 0, 0, err
	}
	pruned = len(ids)
	for _, id := range ids {
		n, e := r.sqldb.DeleteSessionBlobs(id)
		blobs += n
		if e != nil && err == nil {
			err = e
		}
		// remove from disk index but keep md files
		r.mu.Lock()
		delete(r.diskIndex, id)
		r.mu.Unlock()
	}
	maxBlob := r.sqldb.SettingInt("blob_max_age_days", 4)
	n, e := r.sqldb.GCBlobs(maxBlob)
	blobs += n
	if e != nil && err == nil {
		err = e
	}
	return pruned, blobs, err
}

var errBusy = fmt.Errorf("session busy")

// IsBusy reports whether err is the busy error.
func IsBusy(err error) bool {
	return err == errBusy
}
