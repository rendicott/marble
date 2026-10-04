package peerhub

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Peer lock (protocol v2). A peer may be paired with several harnesses; only
// the harness holding the peer's lock may send actions. This harness acquires
// the lock on a session's first peer call and releases it when the last
// session using the peer ends its turn (Hub.ReleaseLocks). If the harness dies
// without releasing, the peer drops the lock when we reconnect with a new
// InstanceID, or the operator clears it from the peer tray.

const lockRPCTimeout = 15 * time.Second

type connLock struct {
	op   sync.Mutex // serializes acquire/release round trips with the peer
	mu   sync.Mutex
	held bool                   // peer confirmed we hold the lock on this connection
	peer map[string]interface{} // last lock_state from the peer
}

// AcquireLock makes sure this harness holds computerID's peer lock on behalf of
// holder (a session id). No-op for peers that predate locking.
func (h *Hub) AcquireLock(computerID, holder string) error {
	c := h.Get(computerID)
	if c == nil {
		return fmt.Errorf("computer %q offline", computerID)
	}
	if !c.Caps.Lock {
		return nil
	}
	c.lock.op.Lock()
	defer c.lock.op.Unlock()
	c.lock.mu.Lock()
	held := c.lock.held
	c.lock.mu.Unlock()
	if !held {
		if _, err := c.roundTrip(Envelope{Type: "lock", ID: uuid.NewString(), Kind: "acquire"}, lockRPCTimeout); err != nil {
			return fmt.Errorf("acquire peer lock on %q: %w", computerID, err)
		}
		c.lock.mu.Lock()
		c.lock.held = true
		c.lock.mu.Unlock()
		log.Printf("peerhub: lock acquired computer_id=%s for %s", computerID, holder)
	}
	h.lockMu.Lock()
	if h.lockUsers[computerID] == nil {
		h.lockUsers[computerID] = make(map[string]bool)
	}
	h.lockUsers[computerID][holder] = true
	h.lockMu.Unlock()
	return nil
}

// ReleaseLocks drops holder from every peer lock it uses; a peer whose last
// user is gone is told to release. Does not block on the peer.
func (h *Hub) ReleaseLocks(holder string) {
	var idle []string
	h.lockMu.Lock()
	for cid, users := range h.lockUsers {
		if !users[holder] {
			continue
		}
		delete(users, holder)
		if len(users) == 0 {
			delete(h.lockUsers, cid)
			idle = append(idle, cid)
		}
	}
	h.lockMu.Unlock()
	for _, cid := range idle {
		if c := h.Get(cid); c != nil {
			go c.releaseIfUnused()
		}
	}
}

func (h *Hub) lockUserCount(computerID string) int {
	h.lockMu.Lock()
	defer h.lockMu.Unlock()
	return len(h.lockUsers[computerID])
}

// releaseIfUnused tells the peer to release our lock when no session uses it.
func (c *Conn) releaseIfUnused() {
	c.lock.op.Lock()
	defer c.lock.op.Unlock()
	if c.hub.lockUserCount(c.ComputerID) > 0 {
		return
	}
	c.lock.mu.Lock()
	ours := c.lock.held || c.lock.peer["mine"] == true
	c.lock.held = false
	c.lock.mu.Unlock()
	if !ours {
		return
	}
	if _, err := c.roundTrip(Envelope{Type: "lock", ID: uuid.NewString(), Kind: "release"}, lockRPCTimeout); err != nil {
		log.Printf("peerhub: release lock computer_id=%s: %v", c.ComputerID, err)
		return
	}
	log.Printf("peerhub: lock released computer_id=%s", c.ComputerID)
}

// MarkLockLost forgets our lock after the peer refused an action for lack of it.
func (c *Conn) MarkLockLost() {
	c.lock.mu.Lock()
	c.lock.held = false
	c.lock.mu.Unlock()
}

// onLockState records the peer's lock broadcast. Called from ReadLoop, so it
// must not wait on a round trip.
func (c *Conn) onLockState(meta map[string]interface{}) {
	mine := meta["mine"] == true
	c.lock.mu.Lock()
	c.lock.peer = meta
	revoked := c.lock.held && !mine
	if revoked {
		c.lock.held = false
	}
	c.lock.mu.Unlock()
	if revoked {
		log.Printf("peerhub: lock on computer_id=%s revoked by peer (now %v)", c.ComputerID, meta["holder"])
	}
	if mine && c.hub != nil {
		// Peer says we hold it (e.g. reconnect after the last session finished
		// while offline) — release unless a session is using it.
		go c.releaseIfUnused()
	}
}

// LockStatus is the peer's last reported lock state (nil before any report).
func (c *Conn) LockStatus() map[string]interface{} {
	c.lock.mu.Lock()
	defer c.lock.mu.Unlock()
	if c.lock.peer == nil {
		return nil
	}
	out := make(map[string]interface{}, len(c.lock.peer))
	for k, v := range c.lock.peer {
		out[k] = v
	}
	return out
}
