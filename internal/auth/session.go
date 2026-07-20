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
)

// SessionStore holds in-memory login sessions (ADR-0017 Q11).
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*sessionRec
	pending  map[string]*pendingOAuth // state → PKCE
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
		sessions: make(map[string]*sessionRec),
		pending:  make(map[string]*pendingOAuth),
	}
}

// Create issues a new session id for u.
func (s *SessionStore) Create(u User) (id string, exp time.Time) {
	id = randomID(24)
	exp = time.Now().Add(sessionTTL)
	s.mu.Lock()
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

// PutPending stores OAuth PKCE state.
func (s *SessionStore) PutPending(state, verifier, next string) {
	s.mu.Lock()
	s.pending[state] = &pendingOAuth{
		Verifier: verifier,
		Next:     next,
		Expires:  time.Now().Add(pendingTTL),
	}
	s.mu.Unlock()
}

// TakePending pops OAuth state.
func (s *SessionStore) TakePending(state string) (verifier, next string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
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

func randomID(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
