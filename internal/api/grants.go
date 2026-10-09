package api

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/rendicott/marble/internal/db"
)

// Peer auto-enrollment via grants (ADR-0036). The operator (or automation) mints a
// single-use grant for a machine that is about to appear; the machine redeems it with
// one POST /api/computers/enroll and gets a device token. No H-code, no confirm click.

func (s *Server) grantsDB(w http.ResponseWriter) *db.DB {
	if s.Registry == nil || s.Registry.DB() == nil || !s.Registry.DB().Writable() {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return nil
	}
	return s.Registry.DB()
}

func grantView(g db.GrantRow, now time.Time) map[string]interface{} {
	m := map[string]interface{}{
		"grant_id":    g.ID,
		"device_name": g.DeviceName,
		"os":          g.OS,
		"state":       g.EffectiveState(now),
		"created_at":  time.Unix(g.CreatedAt, 0).UTC().Format(time.RFC3339),
		"expires_at":  time.Unix(g.ExpiresAt, 0).UTC().Format(time.RFC3339),
	}
	if g.Note != "" {
		m["note"] = g.Note
	}
	if len(g.AllowCIDRs) > 0 {
		m["allow_cidrs"] = g.AllowCIDRs
	}
	if g.ClaimedAt > 0 {
		m["claimed_at"] = time.Unix(g.ClaimedAt, 0).UTC().Format(time.RFC3339)
		m["claimed_ip"] = g.ClaimedIP
		m["claimed_device_name"] = g.ClaimedName
		m["peer_version"] = g.PeerVersion
		m["computer_id"] = g.ComputerID
	}
	return m
}

// POST /api/computers/grants (operator)
func (s *Server) createGrant(w http.ResponseWriter, r *http.Request) {
	d := s.grantsDB(w)
	if d == nil {
		return
	}
	var body struct {
		DeviceName string   `json:"device_name"`
		OS         string   `json:"os"`
		TTLSec     int      `json:"ttl_sec"`
		Note       string   `json:"note"`
		AllowCIDRs []string `json:"allow_cidrs"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
	}
	if body.TTLSec < 0 || (body.TTLSec > 0 && body.TTLSec < 60) {
		http.Error(w, "ttl_sec must be ≥ 60", http.StatusBadRequest)
		return
	}
	g, secret, err := d.CreateGrant(body.DeviceName, body.OS, body.Note, body.AllowCIDRs, time.Duration(body.TTLSec)*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	harness := s.publicHarnessURL(r)
	log.Printf("computers: grant %s created device_name=%q os=%q expires=%s note=%q allow=%v",
		g.ID, g.DeviceName, g.OS, time.Unix(g.ExpiresAt, 0).UTC().Format(time.RFC3339), g.Note, g.AllowCIDRs)
	out := grantView(g, time.Now())
	out["grant_secret"] = secret
	out["harness_url_hint"] = harness
	out["enroll_command"] = "marble-peer enroll --harness " + harness + " --grant " + secret
	writeJSON(w, http.StatusCreated, out)
}

// GET /api/computers/grants (operator)
func (s *Server) listGrants(w http.ResponseWriter, r *http.Request) {
	if s.Registry == nil || s.Registry.DB() == nil || !s.Registry.DB().Writable() {
		writeJSON(w, http.StatusOK, map[string]interface{}{"grants": []interface{}{}})
		return
	}
	rows, err := s.Registry.DB().ListGrants()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now()
	out := make([]map[string]interface{}, 0, len(rows))
	for _, g := range rows {
		out = append(out, grantView(g, now))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"grants": out})
}

// DELETE /api/computers/grants/{id} (operator)
func (s *Server) revokeGrant(w http.ResponseWriter, r *http.Request, id string) {
	d := s.grantsDB(w)
	if d == nil {
		return
	}
	if err := d.RevokeGrant(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	log.Printf("computers: grant %s revoked", id)
	writeJSON(w, http.StatusOK, map[string]interface{}{"revoked": id})
}

// enrollLimiter caps failed claims per source address. Secrets carry 192 bits, so this
// is about log noise and abuse, not guessability.
type enrollLimiter struct {
	mu    sync.Mutex
	fails map[string][]time.Time
}

const (
	enrollFailWindow = 10 * time.Minute
	enrollFailMax    = 10
)

func (l *enrollLimiter) blocked(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.pruneLocked(ip, now)) >= enrollFailMax
}

func (l *enrollLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fails == nil {
		l.fails = map[string][]time.Time{}
	}
	l.fails[ip] = append(l.pruneLocked(ip, now), now)
}

func (l *enrollLimiter) pruneLocked(ip string, now time.Time) []time.Time {
	ts := l.fails[ip]
	i := 0
	for i < len(ts) && now.Sub(ts[i]) > enrollFailWindow {
		i++
	}
	ts = ts[i:]
	if len(ts) == 0 {
		delete(l.fails, ip)
	} else {
		l.fails[ip] = ts
	}
	return ts
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// POST /api/computers/enroll (peer — public; the grant secret is the credential)
func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	if s.Registry == nil || s.Registry.DB() == nil || !s.Registry.DB().Writable() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	d := s.Registry.DB()
	// Default off: dark unless an operator has minted a grant that can still be claimed.
	if !d.HasClaimableGrant() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	ip := remoteIP(r)
	now := time.Now()
	if s.enrollLimit.blocked(ip, now) {
		writeJSON(w, http.StatusTooManyRequests, map[string]interface{}{"error": "too many failed enroll attempts; retry later"})
		return
	}
	var body struct {
		GrantSecret string                 `json:"grant_secret"`
		DeviceName  string                 `json:"device_name"`
		DeviceID    string                 `json:"device_id"`
		OS          string                 `json:"os"`
		PeerVersion string                 `json:"peer_version"`
		Caps        map[string]interface{} `json:"caps"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	capsJSON := "{}"
	if body.Caps != nil {
		b, _ := json.Marshal(body.Caps)
		capsJSON = string(b)
	}
	res, err := d.ClaimGrant(db.ClaimInput{
		Secret:      body.GrantSecret,
		DeviceName:  body.DeviceName,
		DeviceID:    body.DeviceID,
		OS:          body.OS,
		PeerVersion: body.PeerVersion,
		CapsJSON:    capsJSON,
		RemoteIP:    ip,
	})
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, db.ErrGrantInvalid):
			status = http.StatusUnauthorized
		case errors.Is(err, db.ErrGrantUsed):
			status = http.StatusConflict
		case errors.Is(err, db.ErrGrantExpired), errors.Is(err, db.ErrGrantRevoked):
			status = http.StatusGone
		case errors.Is(err, db.ErrGrantIP):
			status = http.StatusForbidden
		case errors.Is(err, db.ErrComputerLimit):
			status = http.StatusConflict
		}
		if status != http.StatusInternalServerError && status != http.StatusConflict {
			s.enrollLimit.fail(ip, now)
		}
		log.Printf("computers: enroll rejected from %s device_name=%q grant=%s: %v", ip, body.DeviceName, res.Grant.ID, err)
		writeJSON(w, status, map[string]interface{}{"error": err.Error()})
		return
	}
	log.Printf("computers: grant %s claimed from %s device_name=%q peer=%s → computer %s (reenrolled=%v)",
		res.Grant.ID, ip, body.DeviceName, body.PeerVersion, res.ComputerID, res.Reenrolled)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"computer_id":  res.ComputerID,
		"display_name": res.DisplayName,
		"device_id":    res.DeviceID,
		"device_token": res.DeviceToken,
		"harness_url":  s.publicHarnessURL(r),
		"grant_id":     res.Grant.ID,
		"reenrolled":   res.Reenrolled,
	})
}
