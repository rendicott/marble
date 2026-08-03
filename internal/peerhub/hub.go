// Package peerhub manages desktop peer WebSocket connections and action RPCs (ADR-0020).
package peerhub

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const ProtocolVersion = 1

// Caps reported by a peer.
type Caps struct {
	Browser bool `json:"browser"`
	Desktop bool `json:"desktop"`
	Confirm bool `json:"confirm"`
}

// Envelope is a wire message (both directions).
type Envelope struct {
	Type            string          `json:"type"`
	ProtocolVersion int             `json:"protocol_version,omitempty"`
	ID              string          `json:"id,omitempty"`
	DeviceID        string          `json:"device_id,omitempty"`
	Token           string          `json:"token,omitempty"`
	ComputerID      string          `json:"computer_id,omitempty"`
	Kind            string          `json:"kind,omitempty"`
	DeadlineMS      int64           `json:"deadline_ms,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	OK              bool            `json:"ok,omitempty"`
	Error           string          `json:"error,omitempty"`
	Caps            *Caps           `json:"caps,omitempty"`
	OS              string          `json:"os,omitempty"`
	PeerVersion     string          `json:"peer_version,omitempty"`
	ScreenshotB64   string          `json:"screenshot_b64,omitempty"`
	Text            string          `json:"text,omitempty"`
	Meta            map[string]interface{} `json:"meta,omitempty"`
}

// Conn is one online peer.
type Conn struct {
	ComputerID string
	DeviceID   string
	Caps       Caps
	OS         string
	PeerVer    string
	LastSeen   time.Time

	mu      sync.Mutex
	ws      *websocket.Conn
	pending map[string]chan Envelope
	closed  bool
}

// PendingConfirm is a high-risk peer action waiting for human Accept/Deny.
// Can be resolved from the peer mini-UI/tray OR from the Marble harness UI.
type PendingConfirm struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"session_id,omitempty"`
	ComputerID string    `json:"computer_id"`
	Prompt     string    `json:"prompt"`
	Risk       string    `json:"risk,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// Hub tracks online peers by computer id.
type Hub struct {
	mu       sync.Mutex
	peers    map[string]*Conn // computer_id
	confirms map[string]*PendingConfirm
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{
		peers:    make(map[string]*Conn),
		confirms: make(map[string]*PendingConfirm),
	}
}

// Register attaches a websocket for a computer.
// If another peer is already online for this computer_id, the old connection is closed
// (last writer wins). Running two marble-peer processes for the same device causes a
// reconnect fight — peer enforces a single-instance lock.
func (h *Hub) Register(computerID, deviceID string, caps Caps, os, peerVer string, ws *websocket.Conn) *Conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.peers[computerID]; ok {
		log.Printf("peerhub: replacing connection computer_id=%s (old device=%s)", computerID, old.DeviceID)
		old.closeLocked()
	}
	c := &Conn{
		ComputerID: computerID,
		DeviceID:   deviceID,
		Caps:       caps,
		OS:         os,
		PeerVer:    peerVer,
		LastSeen:   time.Now(),
		ws:         ws,
		pending:    make(map[string]chan Envelope),
	}
	h.peers[computerID] = c
	log.Printf("peerhub: online computer_id=%s device=%s os=%s", computerID, deviceID, os)
	c.startKeepAlive()
	return c
}

// Unregister removes a peer if still the same conn.
func (h *Hub) Unregister(computerID string, c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cur, ok := h.peers[computerID]; ok && cur == c {
		delete(h.peers, computerID)
		c.closeLocked()
		log.Printf("peerhub: offline computer_id=%s", computerID)
	}
}

// Get returns online conn or nil.
func (h *Hub) Get(computerID string) *Conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.peers[computerID]
}

// ListOnline returns public status maps.
func (h *Hub) ListOnline() map[string]map[string]interface{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]map[string]interface{})
	for id, c := range h.peers {
		out[id] = map[string]interface{}{
			"online":      true,
			"os":          c.OS,
			"caps":        c.Caps,
			"last_seen":   c.LastSeen.UTC().Format(time.RFC3339),
			"peer_version": c.PeerVer,
		}
	}
	return out
}

func (c *Conn) closeLocked() {
	if c.closed {
		return
	}
	c.closed = true
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	_ = c.ws.Close()
}

// startKeepAlive sends application-level pings so the peer's read deadline
// (typically 90s) does not fire during idle periods.
func (c *Conn) startKeepAlive() {
	go func() {
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for range t.C {
			c.mu.Lock()
			if c.closed {
				c.mu.Unlock()
				return
			}
			err := c.ws.WriteJSON(Envelope{Type: "ping"})
			c.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
}

// ReadLoop processes peer messages until disconnect.
func (c *Conn) ReadLoop() {
	defer func() {
		c.mu.Lock()
		c.closeLocked()
		c.mu.Unlock()
	}()
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		c.LastSeen = time.Now()
		switch env.Type {
		case "result", "error":
			c.mu.Lock()
			ch := c.pending[env.ID]
			if ch != nil {
				delete(c.pending, env.ID)
				select {
				case ch <- env:
				default:
				}
			}
			c.mu.Unlock()
		case "pong", "hello_ack":
			// ignore keepalives
		case "ping":
			// peer-initiated keepalive — reply
			c.mu.Lock()
			if !c.closed {
				_ = c.ws.WriteJSON(Envelope{Type: "pong"})
			}
			c.mu.Unlock()
		default:
			// peer-initiated (confirm response etc.)
			if env.Type == "confirm_result" {
				c.mu.Lock()
				ch := c.pending[env.ID]
				if ch != nil {
					delete(c.pending, env.ID)
					select {
					case ch <- env:
					default:
					}
				}
				c.mu.Unlock()
			}
		}
	}
}

// Call sends an action and waits for result up to deadline.
func (c *Conn) Call(kind string, payload interface{}, deadline time.Duration) (Envelope, error) {
	return c.CallWithID(uuid.NewString(), kind, payload, deadline)
}

// CallWithID is Call with a caller-chosen action id (used so computer_confirm
// can share the same id with harness UI Accept/Deny).
func (c *Conn) CallWithID(id, kind string, payload interface{}, deadline time.Duration) (Envelope, error) {
	if deadline <= 0 {
		deadline = 120 * time.Second
	}
	if deadline > 5*time.Minute {
		deadline = 5 * time.Minute
	}
	if strings.TrimSpace(id) == "" {
		id = uuid.NewString()
	}
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return Envelope{}, err
		}
		raw = b
	}
	env := Envelope{
		Type:       "action",
		ID:         id,
		Kind:       kind,
		DeadlineMS: deadline.Milliseconds(),
		Payload:    raw,
	}
	ch := make(chan Envelope, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return Envelope{}, fmt.Errorf("peer offline")
	}
	c.pending[id] = ch
	err := c.ws.WriteJSON(env)
	c.mu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return Envelope{}, err
	}
	timer := time.NewTimer(deadline + 2*time.Second)
	defer timer.Stop()
	select {
	case res, ok := <-ch:
		if !ok {
			return Envelope{}, fmt.Errorf("peer disconnected")
		}
		if res.Type == "error" || !res.OK && res.Error != "" {
			if res.Error == "" {
				res.Error = "peer action failed"
			}
			return res, fmt.Errorf("%s", res.Error)
		}
		return res, nil
	case <-timer.C:
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return Envelope{}, fmt.Errorf("peer action timeout")
	}
}

// PutConfirm registers a pending human confirmation (harness UI + peer mini-UI).
func (h *Hub) PutConfirm(p PendingConfirm) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.confirms == nil {
		h.confirms = make(map[string]*PendingConfirm)
	}
	cp := p
	h.confirms[p.ID] = &cp
}

// DeleteConfirm removes a pending confirmation.
func (h *Hub) DeleteConfirm(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.confirms, id)
}

// GetConfirm returns a copy of a pending confirmation or nil.
func (h *Hub) GetConfirm(id string) *PendingConfirm {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.confirms[id]
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

// ListConfirms returns pending confirms, optionally filtered by session_id.
func (h *Hub) ListConfirms(sessionID string) []PendingConfirm {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	out := make([]PendingConfirm, 0)
	for id, p := range h.confirms {
		if p == nil {
			continue
		}
		if !p.ExpiresAt.IsZero() && now.After(p.ExpiresAt) {
			delete(h.confirms, id)
			continue
		}
		if sessionID != "" && p.SessionID != sessionID {
			continue
		}
		out = append(out, *p)
	}
	return out
}

// ResolveConfirmFromHarness tells the peer the human accepted/denied via Marble UI.
// The peer unblocks waitConfirm; the original Call then returns.
func (h *Hub) ResolveConfirmFromHarness(id string, accept bool) error {
	h.mu.Lock()
	p := h.confirms[id]
	var computerID string
	if p != nil {
		computerID = p.ComputerID
	}
	h.mu.Unlock()
	if computerID == "" {
		// Still try to find an online peer? Fail clearly.
		return fmt.Errorf("unknown or expired confirm id %q", id)
	}
	c := h.Get(computerID)
	if c == nil {
		return fmt.Errorf("computer %q offline", computerID)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("computer %q offline", computerID)
	}
	return c.ws.WriteJSON(Envelope{
		Type: "confirm_resolve",
		ID:   id,
		OK:   accept,
	})
}

// Cancel asks peer to stop current action (best effort).
func (c *Conn) Cancel() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	_ = c.ws.WriteJSON(Envelope{Type: "cancel"})
}
