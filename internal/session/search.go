package session

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/memory"
	"github.com/rendicott/marble/internal/search"
)

// ErrSearchUnavailable is returned when marble.db cannot take the index write
// the search endpoint needs. The HTTP layer maps it to 503.
var ErrSearchUnavailable = errors.New("search unavailable")

const searchHitCap = 20

// SearchSessions ranks sessions for the raw query q. It unions SQLite term
// rows with each loaded session's in-memory term set and never reads a
// transcript. Queries shorter than two characters match nothing.
func (r *Registry) SearchSessions(q string) ([]db.SearchHit, error) {
	if r == nil || r.sqldb == nil || !r.sqldb.Writable() {
		return nil, ErrSearchUnavailable
	}
	q = strings.TrimSpace(q)
	if len(q) < 2 {
		return nil, nil
	}
	expanded := search.ExpandQuery(q)
	hits, err := r.sqldb.SearchSessions(expanded)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]db.SearchHit, len(hits))
	for _, h := range hits {
		byID[h.ID] = h
	}

	r.mu.RLock()
	live := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		live = append(live, s)
	}
	r.mu.RUnlock()
	for _, s := range live {
		score, title, updated, indexed := s.searchMatch(expanded)
		if !indexed {
			continue
		}
		if score <= 0 {
			// The loaded transcript is the whole index for this session.
			// A stale SQLite row must not keep it in the results.
			delete(byID, s.ID)
			continue
		}
		prev := byID[s.ID]
		byID[s.ID] = db.SearchHit{
			ID:          s.ID,
			Title:       title,
			Description: prev.Description,
			UpdatedAt:   updated,
			Score:       score,
		}
	}

	out := make([]db.SearchHit, 0, len(byID))
	for _, h := range byID {
		h.Description = r.searchDescription(h.ID, h.Title, h.Description)
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > searchHitCap {
		out = out[:searchHitCap]
	}
	return out, nil
}

// searchDescription is the clerk summary when one exists, otherwise the title.
func (r *Registry) searchDescription(id, title, stored string) string {
	if r != nil && r.sqldb != nil && r.sqldb.Writable() {
		st, err := r.sqldb.GetClerkState(id)
		if err == nil && st != nil {
			if sum := strings.TrimSpace(st.Summary); sum != "" {
				return sum
			}
		}
	}
	if t := strings.TrimSpace(title); t != "" {
		return t
	}
	if s := strings.TrimSpace(stored); s != "" {
		return s
	}
	return id
}

// flushSearch writes the live term set. Called from endTurn.
// source_hash is preserved so the sweep still notices when the file changes.
func (r *Registry) flushSearch(s *Session) {
	if r == nil || s == nil || r.sqldb == nil || !r.sqldb.Writable() {
		return
	}
	title, updated, terms := s.searchSnapshot()
	desc := r.searchDescription(s.ID, title, "")
	err := r.sqldb.ReplaceSessionSearch(db.SessionSearchRow{
		SessionID:   s.ID,
		Title:       title,
		Description: desc,
		UpdatedAt:   updated.UTC().Format(time.RFC3339),
	}, terms, true)
	if err != nil {
		log.Printf("search index %s: %v", s.ID, err)
	}
}

// ReindexSearch indexes $MEMORY/session/*.md whose bytes differ from source_hash.
// Loaded dirty sessions are left to endTurn so an older file cannot replace
// terms that are already ahead of disk.
func (r *Registry) ReindexSearch() error {
	if r == nil || r.store == nil || r.sqldb == nil || !r.sqldb.Writable() {
		return nil
	}
	r.mu.RLock()
	live := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		live = append(live, s)
	}
	r.mu.RUnlock()
	dirty := map[string]bool{}
	liveIDs := map[string]bool{}
	for _, s := range live {
		liveIDs[s.ID] = true
		if s.IsDirty() {
			dirty[s.ID] = true
		}
	}

	dir := filepath.Join(r.store.Root, "session")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	hashes, err := r.sqldb.SessionSearchHashes()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			log.Printf("search index read %s: %v", e.Name(), err)
			continue
		}
		doc, err := memory.DecodeSession(string(body))
		if err != nil {
			log.Printf("search index decode %s: %v", e.Name(), err)
			continue
		}
		id := strings.TrimSpace(doc.ID)
		if id == "" {
			id = strings.TrimSuffix(e.Name(), ".md")
		}
		seen[id] = true
		if dirty[id] {
			continue
		}
		sum := search.Hash(body)
		if prev, ok := hashes[id]; ok && prev == sum {
			continue
		}
		title := strings.TrimSpace(doc.Title)
		updated := doc.UpdatedAt.UTC().Format(time.RFC3339)
		if doc.UpdatedAt.IsZero() {
			updated = time.Now().UTC().Format(time.RFC3339)
		}
		desc := r.searchDescription(id, title, "")
		terms := search.TermsFromTranscript(doc.Messages)
		if err := r.sqldb.ReplaceSessionSearch(db.SessionSearchRow{
			SessionID:   id,
			Title:       title,
			Description: desc,
			UpdatedAt:   updated,
			SourceHash:  sum,
		}, terms, false); err != nil {
			log.Printf("search index write %s: %v", id, err)
			continue
		}
		n++
	}
	for id := range hashes {
		if seen[id] || liveIDs[id] {
			continue
		}
		if err := r.sqldb.DeleteSessionSearch(id); err != nil {
			log.Printf("search index drop %s: %v", id, err)
		}
	}
	if n > 0 {
		log.Printf("search index: reindexed %d session(s)", n)
	}
	return nil
}

func (s *Session) searchSnapshot() (title string, updated time.Time, terms map[string]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	title = s.Title
	updated = s.UpdatedAt
	terms = make(map[string]int, len(s.searchTerms))
	for k, v := range s.searchTerms {
		terms[k] = v
	}
	return title, updated, terms
}

// searchMatch scores expanded terms against the live set.
// indexed is false when this session has no in-memory index.
func (s *Session) searchMatch(expanded []string) (score int, title, updated string, indexed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.searchTerms == nil {
		return 0, "", "", false
	}
	seen := map[string]struct{}{}
	for _, term := range expanded {
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		score += s.searchTerms[term]
	}
	return score, s.Title, s.UpdatedAt.UTC().Format(time.RFC3339), true
}
