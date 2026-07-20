package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const (
	CookieName   = "marble_session"
	CSRFHeader   = "X-Marble-Requested-With"
	CSRFValue    = "fetch"
	sessionTTL   = 7 * 24 * time.Hour
	pendingTTL   = 10 * time.Minute

	// maxPendingOAuth caps in-flight PKCE states (login DoS / memory).
	maxPendingOAuth = 256
	// loginRateLimit / loginRateWindow limit /auth/login starts per client key.
	loginRateLimit  = 20
	loginRateWindow = 10 * time.Minute
)

// SessionStore holds in-memory login sessions (ADR-0017 Q11).
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*sessionRec
	pending  map[string]*pendingOAuth // state → PKCE
	// loginHits tracks login starts by client key (IP) for rate limiting.
	loginHits map[string][]time.Time
}

type sessionRec struct {
	User    User
	Expires time.Time
}

type pendingOAuth struct {
	Verifier string
	Next     string
	Expires  time.Time
}

// NewSessionStore creates an empty store.
func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions:  make(map[string]*sessionRec),
		pending:   make(map[string]*pendingOAuth),
		loginHits: make(map[string][]time.Time),
	}
}

// Create issues a new session id for u.
func (s *SessionStore) Create(u User) (id string, exp time.Time) {
	id = randomID(24)
	exp = time.Now().Add(sessionTTL)
	s.mu.Lock()
	s.gcSessionsLocked(time.Now())
	s.sessions[id] = &sessionRec{User: u, Expires: exp}
	s.mu.Unlock()
	return id, exp
}

// Get returns the user for a session id, or nil if missing/expired.
func (s *SessionStore) Get(id string) *User {
	if id == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.sessions[id]
	if !ok {
		return nil
	}
	if time.Now().After(rec.Expires) {
		delete(s.sessions, id)
		return nil
	}
	cp := rec.User
	return &cp
}

// Delete removes a session.
func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// AllowLoginStart rate-limits OAuth login starts for clientKey (typically RemoteAddr host).
// Returns false when the client should receive 429.
func (s *SessionStore) AllowLoginStart(clientKey string) bool {
	if clientKey == "" {
		clientKey = "unknown"
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	// prune window
	cutoff := now.Add(-loginRateWindow)
	hits := s.loginHits[clientKey]
	kept := hits[:0]
	for _, t := range hits {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= loginRateLimit {
		s.loginHits[clientKey] = kept
		return false
	}
	s.loginHits[clientKey] = append(kept, now)
	// opportunistic GC of empty-ish maps / pending
	s.gcPendingLocked(now)
	return true
}

// PutPending stores OAuth PKCE state. Returns false if the pending map is at capacity
// after garbage-collecting expired entries.
func (s *SessionStore) PutPending(state, verifier, next string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.gcPendingLocked(now)
	if len(s.pending) >= maxPendingOAuth {
		return false
	}
	s.pending[state] = &pendingOAuth{
		Verifier: verifier,
		Next:     next,
		Expires:  now.Add(pendingTTL),
	}
	return true
}

// TakePending pops OAuth state.
func (s *SessionStore) TakePending(state string) (verifier, next string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcPendingLocked(time.Now())
	p, ok := s.pending[state]
	if !ok {
		return "", "", false
	}
	delete(s.pending, state)
	if time.Now().After(p.Expires) {
		return "", "", false
	}
	return p.Verifier, p.Next, true
}

// PendingCount is for tests/metrics.
func (s *SessionStore) PendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcPendingLocked(time.Now())
	return len(s.pending)
}

func (s *SessionStore) gcPendingLocked(now time.Time) {
	for k, p := range s.pending {
		if now.After(p.Expires) {
			delete(s.pending, k)
		}
	}
}

func (s *SessionStore) gcSessionsLocked(now time.Time) {
	for k, rec := range s.sessions {
		if now.After(rec.Expires) {
			delete(s.sessions, k)
		}
	}
}

func randomID(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		// Extremely rare; fall back still better than empty id.
		for i := range b {
			b[i] = byte(time.Now().UnixNano() >> (i % 8))
		}
	}
	return hex.EncodeToString(b)
}
