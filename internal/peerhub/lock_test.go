package peerhub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakePeer is the peer end of a websocket: it answers lock and action messages
// and records the lock kinds it saw.
type fakePeer struct {
	mu      sync.Mutex
	kinds   []string
	refuse  string // non-empty: refuse acquire with this error
	ws      *websocket.Conn
	writeMu sync.Mutex
}

func (p *fakePeer) lockKinds() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.kinds...)
}

func (p *fakePeer) send(env Envelope) {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.ws.WriteJSON(env)
}

func (p *fakePeer) serve() {
	for {
		var env Envelope
		if err := p.ws.ReadJSON(&env); err != nil {
			return
		}
		switch env.Type {
		case "lock":
			p.mu.Lock()
			p.kinds = append(p.kinds, env.Kind)
			refuse := p.refuse
			p.mu.Unlock()
			if env.Kind == "acquire" && refuse != "" {
				p.send(Envelope{Type: "result", ID: env.ID, OK: false, Error: refuse})
				continue
			}
			mine := env.Kind == "acquire"
			p.send(Envelope{Type: "lock_state", Meta: map[string]interface{}{"held": mine, "mine": mine}})
			p.send(Envelope{Type: "result", ID: env.ID, OK: true})
		case "action":
			p.send(Envelope{Type: "result", ID: env.ID, OK: true})
		}
	}
}

// newHubWithPeer registers one fake peer as computer "pc" and returns both ends.
func newHubWithPeer(t *testing.T, caps Caps) (*Hub, *fakePeer) {
	t.Helper()
	h := NewHub()
	up := websocket.Upgrader{}
	registered := make(chan *Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		c := h.Register("pc", "dev", caps, "linux", "test", ws)
		registered <- c
		c.ReadLoop()
	}))
	t.Cleanup(srv.Close)
	cws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cws.Close() })
	p := &fakePeer{ws: cws}
	go p.serve()
	<-registered
	return h, p
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLockSharedAcrossSessionsReleasedByLast(t *testing.T) {
	h, p := newHubWithPeer(t, Caps{Lock: true})

	if err := h.AcquireLock("pc", "s1"); err != nil {
		t.Fatal(err)
	}
	if err := h.AcquireLock("pc", "s2"); err != nil {
		t.Fatal(err)
	}
	if got := p.lockKinds(); len(got) != 1 || got[0] != "acquire" {
		t.Fatalf("want one acquire, got %v", got)
	}

	h.ReleaseLocks("s1")
	time.Sleep(100 * time.Millisecond)
	if got := p.lockKinds(); len(got) != 1 {
		t.Fatalf("released while s2 still using: %v", got)
	}

	h.ReleaseLocks("s2")
	waitFor(t, "release", func() bool {
		k := p.lockKinds()
		return len(k) == 2 && k[1] == "release"
	})
	if got := p.lockKinds(); len(got) != 2 {
		t.Fatalf("extra lock traffic: %v", got)
	}
}

func TestLockRefusedSurfacesPeerError(t *testing.T) {
	h, p := newHubWithPeer(t, Caps{Lock: true})
	p.refuse = "peer is locked by harness http://other"
	err := h.AcquireLock("pc", "s1")
	if err == nil || !strings.Contains(err.Error(), "http://other") {
		t.Fatalf("want locked error, got %v", err)
	}
	if n := h.lockUserCount("pc"); n != 0 {
		t.Fatalf("refused acquire left %d users", n)
	}
}

func TestLockRevokedByPeerIsReacquired(t *testing.T) {
	h, p := newHubWithPeer(t, Caps{Lock: true})
	if err := h.AcquireLock("pc", "s1"); err != nil {
		t.Fatal(err)
	}
	c := h.Get("pc")
	// Operator cleared the lock from the tray.
	p.send(Envelope{Type: "lock_state", Meta: map[string]interface{}{"held": false, "mine": false}})
	waitFor(t, "revoke", func() bool {
		c.lock.mu.Lock()
		defer c.lock.mu.Unlock()
		return !c.lock.held
	})
	if err := h.AcquireLock("pc", "s1"); err != nil {
		t.Fatal(err)
	}
	if got := p.lockKinds(); len(got) != 2 || got[1] != "acquire" {
		t.Fatalf("want re-acquire after revoke, got %v", got)
	}
}

func TestLegacyPeerSkipsLock(t *testing.T) {
	h, p := newHubWithPeer(t, Caps{})
	if err := h.AcquireLock("pc", "s1"); err != nil {
		t.Fatal(err)
	}
	h.ReleaseLocks("s1")
	time.Sleep(50 * time.Millisecond)
	if got := p.lockKinds(); len(got) != 0 {
		t.Fatalf("legacy peer got lock traffic: %v", got)
	}
}
