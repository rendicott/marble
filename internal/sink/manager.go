package sink

import (
	"context"
	"log"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/session"
)

const (
	queueSize    = 64
	workers      = 2
	maxDelivered = 4096
	maxHistory   = 50
	maxAttempts  = 3
	deliveredTTL = time.Hour
)

func init() {
	resolveSecret = func(name string) (string, bool) {
		v, _, ok := config.ResolveAPIKeyEnv(name)
		return v, ok
	}
}

// DeliveryRecord is a recent delivery attempt (Settings history).
type DeliveryRecord struct {
	At         time.Time `json:"at"`
	SinkID     string    `json:"sink_id"`
	SessionID  string    `json:"session_id"`
	TurnID     string    `json:"turn_id"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"` // ok | retry | drop | skip | error
	Error      string    `json:"error,omitempty"`
	HTTPStatus int       `json:"http_status,omitempty"`
	Reason     string    `json:"reason,omitempty"`
}

type job struct {
	spec SinkSpec
	ev   TurnEvent
	key  string
}

type turnBuf struct {
	inTurn           bool
	turnID           string
	assistant        string
	sawAssistant     bool
	kindHint         string
	userContinuation bool
	lastUser         string
}

// Manager fans turn-complete events out to configured sinks (ADR-0028).
type Manager struct {
	mu           sync.Mutex
	cfg          Config
	path         string
	workspace    string
	fallbackBase func() string

	queue     chan job
	delivered map[string]time.Time
	lastFire  map[string]time.Time // sinkID\x00sessionID
	history   []DeliveryRecord
	bufs      map[string]*turnBuf
	watches   map[string]context.CancelFunc

	httpClient *http.Client
	closed     chan struct{}
	closeOnce  sync.Once
	sync       bool // tests: deliver inline
	started    bool

	enqueued int64
	dropped  int64
	okCount  int64

	sessionGet func(sessionID string) map[string]string
	sessionSet func(sessionID string, ov map[string]string) error
}

// SetSessionHooks wires per-session inherit/on/off overrides for manage_sinks.
func (m *Manager) SetSessionHooks(get func(sessionID string) map[string]string, set func(sessionID string, ov map[string]string) error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.sessionGet = get
	m.sessionSet = set
	m.mu.Unlock()
}

// NewManager builds a sink manager. Missing/empty config is a no-op.
func NewManager(cfg Config, path, workspace string) *Manager {
	cfg.Normalize()
	m := &Manager{
		cfg:       cfg,
		path:      path,
		workspace: workspace,
		queue:     make(chan job, queueSize),
		delivered: make(map[string]time.Time),
		lastFire:  make(map[string]time.Time),
		bufs:      make(map[string]*turnBuf),
		watches:   make(map[string]context.CancelFunc),
		closed:    make(chan struct{}),
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
	}
	return m
}

// SetFallbackBase supplies the global default deep_link_base (PublicOrigin).
func (m *Manager) SetFallbackBase(fn func() string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.fallbackBase = fn
	m.mu.Unlock()
}

// ConfigPath returns the sinks.json path.
func (m *Manager) ConfigPath() string {
	if m == nil {
		return ""
	}
	return m.path
}

// ConfigSnapshot returns a copy of the live config (notes computed).
func (m *Manager) ConfigSnapshot() Config {
	if m == nil {
		return DefaultConfig()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneConfig(m.cfg)
}

func cloneConfig(c Config) Config {
	out := c
	out.Sinks = make([]SinkSpec, len(c.Sinks))
	copy(out.Sinks, c.Sinks)
	for i := range out.Sinks {
		if c.Sinks[i].Headers != nil {
			h := make(map[string]string, len(c.Sinks[i].Headers))
			for k, v := range c.Sinks[i].Headers {
				h[k] = v
			}
			out.Sinks[i].Headers = h
		}
		out.Sinks[i].Filters = c.Sinks[i].Filters
		out.Sinks[i].Filters.normalize()
	}
	out.Normalize()
	return out
}

// ApplyConfig updates live config and optionally persists.
func (m *Manager) ApplyConfig(cfg Config, persist bool) error {
	if m == nil {
		return nil
	}
	cfg.Normalize()
	m.mu.Lock()
	m.cfg = cfg
	path := m.path
	m.mu.Unlock()
	if persist {
		if path == "" {
			return nil
		}
		return Save(path, cfg)
	}
	return nil
}

// Start launches workers. Idempotent.
func (m *Manager) Start() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()
	for i := 0; i < workers; i++ {
		go m.worker()
	}
}

// Stop unsubscribes and stops workers.
func (m *Manager) Stop() {
	if m == nil {
		return
	}
	m.closeOnce.Do(func() {
		close(m.closed)
		m.mu.Lock()
		for id, cancel := range m.watches {
			cancel()
			delete(m.watches, id)
		}
		m.mu.Unlock()
	})
}

// AttachSession subscribes to a session Event stream (ADR-0028 Q1).
func (m *Manager) AttachSession(s *session.Session) {
	if m == nil || s == nil {
		return
	}
	id := s.ID
	m.mu.Lock()
	if _, ok := m.watches[id]; ok {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.watches[id] = cancel
	m.mu.Unlock()
	ch := s.Subscribe()
	go func() {
		defer s.Unsubscribe(ch)
		defer func() {
			m.mu.Lock()
			delete(m.watches, id)
			m.mu.Unlock()
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.closed:
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				m.HandleEvent(s, ev)
			}
		}
	}()
}

// AttachAll subscribes to every currently loaded session.
func (m *Manager) AttachAll(reg *session.Registry) {
	if m == nil || reg == nil {
		return
	}
	reg.ForEachLive(m.AttachSession)
}

// HandleEvent is the stream observer. Safe to call from tests without Subscribe.
func (m *Manager) HandleEvent(s *session.Session, ev session.Event) {
	if m == nil || s == nil {
		return
	}
	sid := s.ID
	m.mu.Lock()
	buf := m.bufs[sid]
	if buf == nil {
		buf = &turnBuf{}
		m.bufs[sid] = buf
	}
	var fire *turnBuf
	switch ev.Type {
	case "status":
		switch ev.Status {
		case "running":
			if !buf.inTurn {
				resetBufLocked(buf, sid)
			}
		case "idle":
			if buf.inTurn {
				cp := *buf
				buf.inTurn = false
				fire = &cp
			}
		}
	case "message":
		if ev.Message != nil {
			switch ev.Message.Role {
			case "user":
				resetBufLocked(buf, sid)
				buf.lastUser = ev.Message.Content
				buf.userContinuation = isContinuationText(ev.Message.Content)
			case "assistant":
				if !buf.inTurn {
					resetBufLocked(buf, sid)
				}
				buf.assistant = ev.Message.Content
				buf.sawAssistant = true
			}
		}
	case "error":
		if !buf.inTurn {
			resetBufLocked(buf, sid)
		}
		buf.kindHint = "error"
		if strings.TrimSpace(buf.assistant) == "" && strings.TrimSpace(ev.Error) != "" {
			buf.assistant = ev.Error
			buf.sawAssistant = true
		}
	case "turn":
		if ev.Turn != nil {
			switch ev.Turn.Phase {
			case "error":
				buf.kindHint = "error"
			case "stopping":
				if buf.kindHint == "" {
					buf.kindHint = "stop"
				}
			}
		}
	}
	m.mu.Unlock()
	if fire != nil {
		m.finishTurn(s, fire)
	}
}

func resetBufLocked(buf *turnBuf, sid string) {
	*buf = turnBuf{
		inTurn: true,
		turnID: sid + "-" + time.Now().UTC().Format("20060102T150405.000000000"),
	}
}

func (m *Manager) finishTurn(s *session.Session, buf *turnBuf) {
	title, modelID, model := s.MetaSnapshot()
	kind := classify(buf, title)
	// Q8: skip tool-only turns unless this is an error.
	if kind != "error" && !buf.sawAssistant {
		m.record(DeliveryRecord{
			At: time.Now(), SinkID: "*", SessionID: s.ID, TurnID: buf.turnID,
			Kind: kind, Status: "skip", Reason: "tool-only",
		})
		return
	}
	msg := buf.assistant
	if kind == "error" && strings.TrimSpace(msg) == "" {
		msg = "turn error"
	}
	ev := TurnEvent{
		SessionID:    s.ID,
		SessionTitle: title,
		Workspace:    m.workspace,
		ModelID:      modelID,
		TurnID:       buf.turnID,
		Kind:         kind,
		Message:      msg,
		Preview:      PreviewOf(msg),
		Timestamp:    time.Now(),
	}
	if ev.ModelID == "" {
		ev.ModelID = model
	}
	m.dispatch(s, ev)
}

func classify(buf *turnBuf, title string) string {
	if buf.kindHint == "error" {
		return "error"
	}
	if buf.kindHint == "stop" {
		return "stop"
	}
	if buf.userContinuation {
		return "continuation"
	}
	if isCronTitle(title) {
		return "cron"
	}
	return "complete"
}

func (m *Manager) dispatch(s *session.Session, ev TurnEvent) {
	m.mu.Lock()
	cfg := cloneConfig(m.cfg)
	fallback := ""
	if m.fallbackBase != nil {
		fallback = strings.TrimRight(strings.TrimSpace(m.fallbackBase()), "/")
	}
	m.mu.Unlock()

	overrides := s.SinkOverridesCopy()
	now := time.Now()
	for _, spec := range cfg.Sinks {
		if spec.ValidationNote != "" {
			continue
		}
		ov := overrides[spec.ID]
		enabled := spec.Enabled
		switch ov {
		case session.SinkOn:
			enabled = true
		case session.SinkOff:
			enabled = false
		}
		if !enabled {
			continue
		}
		ok, reason := spec.Filters.Match(ev)
		if !ok {
			m.record(DeliveryRecord{
				At: now, SinkID: spec.ID, SessionID: ev.SessionID, TurnID: ev.TurnID,
				Kind: ev.Kind, Status: "skip", Reason: reason,
			})
			continue
		}
		if spec.Filters.MinIntervalSec > 0 {
			ck := spec.ID + "\x00" + ev.SessionID
			m.mu.Lock()
			last := m.lastFire[ck]
			m.mu.Unlock()
			if !last.IsZero() && now.Sub(last) < time.Duration(spec.Filters.MinIntervalSec)*time.Second {
				m.record(DeliveryRecord{
					At: now, SinkID: spec.ID, SessionID: ev.SessionID, TurnID: ev.TurnID,
					Kind: ev.Kind, Status: "skip", Reason: "coalesce",
				})
				continue
			}
		}
		base := spec.DeepLinkBase
		if base == "" {
			base = cfg.DeepLinkBase
		}
		if base == "" {
			base = fallback
		}
		tev := ev
		if base != "" {
			tev.DeepLink = strings.TrimRight(base, "/") + "/s/" + ev.SessionID
		} else {
			tev.DeepLink = "/s/" + ev.SessionID
		}
		tev.IdempotencyKey = IdempotencyKey(spec.ID, ev.SessionID, ev.TurnID)
		m.enqueue(job{spec: spec, ev: tev, key: tev.IdempotencyKey})
	}
}

func (m *Manager) enqueue(j job) {
	m.mu.Lock()
	if t, ok := m.delivered[j.key]; ok && time.Since(t) < deliveredTTL {
		m.mu.Unlock()
		m.record(DeliveryRecord{
			At: time.Now(), SinkID: j.spec.ID, SessionID: j.ev.SessionID, TurnID: j.ev.TurnID,
			Kind: j.ev.Kind, Status: "skip", Reason: "idempotent",
		})
		return
	}
	m.delivered[j.key] = time.Now()
	if len(m.delivered) > maxDelivered {
		cutoff := time.Now().Add(-deliveredTTL)
		for k, t := range m.delivered {
			if t.Before(cutoff) {
				delete(m.delivered, k)
			}
		}
	}
	sync := m.sync
	m.enqueued++
	m.mu.Unlock()
	if sync {
		m.deliverJob(j)
		return
	}
	select {
	case m.queue <- j:
	default:
		m.mu.Lock()
		m.dropped++
		m.mu.Unlock()
		log.Printf("sink: drop overflow sink=%s session=%s turn=%s", j.spec.ID, j.ev.SessionID, j.ev.TurnID)
		m.record(DeliveryRecord{
			At: time.Now(), SinkID: j.spec.ID, SessionID: j.ev.SessionID, TurnID: j.ev.TurnID,
			Kind: j.ev.Kind, Status: "drop", Reason: "overflow",
		})
	}
}

func (m *Manager) worker() {
	for {
		select {
		case <-m.closed:
			return
		case j := <-m.queue:
			m.deliverJob(j)
		}
	}
}

func (m *Manager) deliverJob(j job) {
	secret := ""
	if j.spec.SecretEnv != "" {
		v, ok := resolveSecret(j.spec.SecretEnv)
		if !ok || v == "" {
			m.record(DeliveryRecord{
				At: time.Now(), SinkID: j.spec.ID, SessionID: j.ev.SessionID, TurnID: j.ev.TurnID,
				Kind: j.ev.Kind, Status: "error", Error: "secret_env empty: " + j.spec.SecretEnv,
			})
			return
		}
		secret = v
	}
	var lastStatus int
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
		st, err := deliverSpec(ctx, m.httpClient, j.spec, j.ev, secret, nil)
		cancel()
		lastStatus, lastErr = st, err
		if err == nil {
			if j.spec.Filters.MinIntervalSec > 0 {
				ck := j.spec.ID + "\x00" + j.ev.SessionID
				m.mu.Lock()
				m.lastFire[ck] = time.Now()
				m.okCount++
				m.mu.Unlock()
			} else {
				m.mu.Lock()
				m.okCount++
				m.mu.Unlock()
			}
			m.record(DeliveryRecord{
				At: time.Now(), SinkID: j.spec.ID, SessionID: j.ev.SessionID, TurnID: j.ev.TurnID,
				Kind: j.ev.Kind, Status: "ok", HTTPStatus: st,
			})
			return
		}
		if !retryable(st, err) || attempt == maxAttempts-1 {
			break
		}
		m.record(DeliveryRecord{
			At: time.Now(), SinkID: j.spec.ID, SessionID: j.ev.SessionID, TurnID: j.ev.TurnID,
			Kind: j.ev.Kind, Status: "retry", HTTPStatus: st, Error: err.Error(),
		})
		d := backoff(attempt)
		jitter := time.Duration(rand.IntN(int(d/2)+1)) + d/2
		t := time.NewTimer(jitter)
		select {
		case <-m.closed:
			t.Stop()
			return
		case <-t.C:
		}
	}
	errStr := ""
	if lastErr != nil {
		errStr = lastErr.Error()
	}
	log.Printf("sink: drop sink=%s session=%s turn=%s err=%s", j.spec.ID, j.ev.SessionID, j.ev.TurnID, errStr)
	m.record(DeliveryRecord{
		At: time.Now(), SinkID: j.spec.ID, SessionID: j.ev.SessionID, TurnID: j.ev.TurnID,
		Kind: j.ev.Kind, Status: "drop", HTTPStatus: lastStatus, Error: errStr,
	})
}

func (m *Manager) record(r DeliveryRecord) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.history = append(m.history, r)
	if len(m.history) > maxHistory {
		m.history = m.history[len(m.history)-maxHistory:]
	}
}

// History returns a copy of recent delivery records (newest last).
func (m *Manager) History() []DeliveryRecord {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]DeliveryRecord, len(m.history))
	copy(out, m.history)
	return out
}

// Status is a public probe (no secrets).
func (m *Manager) Status() map[string]interface{} {
	if m == nil {
		return map[string]interface{}{"configured": false, "sinks": 0}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.cfg.Sinks)
	en := 0
	for _, s := range m.cfg.Sinks {
		if s.EffectiveEnabled() {
			en++
		}
	}
	return map[string]interface{}{
		"configured":  n > 0,
		"sinks":       n,
		"enabled":     en,
		"enqueued":    m.enqueued,
		"dropped":     m.dropped,
		"ok":          m.okCount,
		"queue":       len(m.queue),
		"config_path": m.path,
	}
}

// PublicSinks is the compact list for the session popover.
func (m *Manager) PublicSinks() []map[string]interface{} {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]interface{}, 0, len(m.cfg.Sinks))
	for _, s := range m.cfg.Sinks {
		out = append(out, map[string]interface{}{
			"id":              s.ID,
			"type":            s.Type,
			"enabled":         s.Enabled,
			"valid":           s.ValidationNote == "",
			"validation_note": s.ValidationNote,
		})
	}
	return out
}

// TestSink sends a synthetic complete event, bypassing filters/overrides/coalesce.
func (m *Manager) TestSink(ctx context.Context, id string) (DeliveryRecord, error) {
	rec := DeliveryRecord{At: time.Now(), SinkID: id, SessionID: "test", TurnID: "test", Kind: "complete", Status: "error"}
	if m == nil {
		rec.Error = "sinks unavailable"
		return rec, nil
	}
	m.mu.Lock()
	var spec SinkSpec
	found := false
	cfg := cloneConfig(m.cfg)
	fallback := ""
	if m.fallbackBase != nil {
		fallback = strings.TrimRight(strings.TrimSpace(m.fallbackBase()), "/")
	}
	m.mu.Unlock()
	for _, s := range cfg.Sinks {
		if s.ID == id {
			spec = s
			found = true
			break
		}
	}
	if !found {
		rec.Error = "sink not found"
		return rec, nil
	}
	if spec.ValidationNote != "" {
		rec.Error = spec.ValidationNote
		return rec, nil
	}
	base := spec.DeepLinkBase
	if base == "" {
		base = cfg.DeepLinkBase
	}
	if base == "" {
		base = fallback
	}
	link := "/s/test"
	if base != "" {
		link = strings.TrimRight(base, "/") + "/s/test"
	}
	ev := TurnEvent{
		SessionID:      "test",
		SessionTitle:   "Sink test",
		Workspace:      m.workspace,
		ModelID:        "test",
		TurnID:         "test-" + time.Now().UTC().Format("20060102T150405"),
		Kind:           "complete",
		Message:        "This is a Marble sink test.",
		Preview:        "This is a Marble sink test.",
		DeepLink:       link,
		Timestamp:      time.Now(),
		IdempotencyKey: IdempotencyKey(spec.ID, "test", "test-manual"),
	}
	secret := ""
	if spec.SecretEnv != "" {
		v, ok := resolveSecret(spec.SecretEnv)
		if !ok || v == "" {
			rec.Error = "secret_env empty: " + spec.SecretEnv
			return rec, nil
		}
		secret = v
	}
	st, err := deliverSpec(ctx, m.httpClient, spec, ev, secret, nil)
	rec.HTTPStatus = st
	rec.TurnID = ev.TurnID
	if err != nil {
		rec.Status = "error"
		rec.Error = err.Error()
		m.record(rec)
		return rec, nil
	}
	rec.Status = "ok"
	m.record(rec)
	return rec, nil
}

// DeepLinkBaseResolved is the global default used when a sink omits its own.
func (m *Manager) DeepLinkBaseResolved() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.DeepLinkBase != "" {
		return m.cfg.DeepLinkBase
	}
	if m.fallbackBase != nil {
		return strings.TrimRight(strings.TrimSpace(m.fallbackBase()), "/")
	}
	return ""
}
