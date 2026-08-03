package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/peerhub"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // private harness; peers may be on tailnet
}

func (s *Server) handleComputers(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/computers")
	path = strings.Trim(path, "/")

	switch {
	case path == "" && r.Method == http.MethodGet:
		s.listComputers(w, r)
	case path == "pair/start" && r.Method == http.MethodPost:
		s.pairStart(w, r)
	case path == "pair/join" && r.Method == http.MethodPost:
		s.pairJoin(w, r)
	case path == "pair/confirm" && r.Method == http.MethodPost:
		s.pairConfirm(w, r)
	case path == "pair/status" && r.Method == http.MethodGet:
		s.pairStatus(w, r)
	case path == "ws" && r.Method == http.MethodGet:
		s.peerWS(w, r)
	case path == "confirms" && r.Method == http.MethodGet:
		s.listConfirms(w, r)
		return
	case strings.HasPrefix(path, "confirms/") && r.Method == http.MethodPost:
		id := strings.TrimPrefix(path, "confirms/")
		id = strings.Trim(id, "/")
		s.resolveConfirm(w, r, id)
		return
	case strings.HasSuffix(path, "/rpc") && r.Method == http.MethodPost:
		id := strings.TrimSuffix(path, "/rpc")
		id = strings.Trim(id, "/")
		s.computerRPC(w, r, id)
		return
	case strings.HasPrefix(path, "") && r.Method == http.MethodDelete:
		// /api/computers/{id}
		id := path
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		s.revokeComputer(w, r, id)
	default:
		// DELETE /api/computers/x or GET one
		parts := strings.Split(path, "/")
		if len(parts) == 1 && parts[0] != "" && r.Method == http.MethodDelete {
			s.revokeComputer(w, r, parts[0])
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (s *Server) listComputers(w http.ResponseWriter, r *http.Request) {
	if s.Registry == nil || s.Registry.DB() == nil || !s.Registry.DB().Writable() {
		writeJSON(w, http.StatusOK, map[string]interface{}{"computers": []interface{}{}})
		return
	}
	d := s.Registry.DB()
	rows, err := d.ListComputers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	online := map[string]map[string]interface{}{}
	if s.PeerHub != nil {
		online = s.PeerHub.ListOnline()
	}
	out := make([]map[string]interface{}, 0, len(rows))
	for _, c := range rows {
		m := map[string]interface{}{
			"id":           c.ID,
			"display_name": c.DisplayName,
			"device_id":    c.DeviceID,
			"os":           c.OS,
			"enabled":      c.Enabled,
			"caps_json":    c.CapsJSON,
			"online":       false,
		}
		if c.LastSeenAt.Valid {
			m["last_seen_at"] = c.LastSeenAt.Int64
		}
		if o, ok := online[c.ID]; ok {
			m["online"] = true
			m["live"] = o
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"computers": out})
}

func (s *Server) pairStart(w http.ResponseWriter, r *http.Request) {
	if s.Registry == nil || s.Registry.DB() == nil || !s.Registry.DB().Writable() {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	d := s.Registry.DB()
	d.CleanupExpiredPairings()
	n, _ := d.CountComputers()
	if n >= 8 {
		http.Error(w, "max 8 computers", http.StatusBadRequest)
		return
	}
	hCode, err := db.RandomCode(6)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id := uuid.NewString()
	if err := d.CreatePairing(id, hCode, 10*time.Minute); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	hint := s.publicHarnessURL(r)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"pairing_id":       id,
		"h_code":           hCode,
		"expires_in_sec":   600,
		"harness_url_hint": hint,
	})
}

func (s *Server) publicHarnessURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = strings.TrimPrefix(s.Cfg.Addr, ":")
		if !strings.Contains(host, ":") && strings.HasPrefix(s.Cfg.Addr, ":") {
			host = "127.0.0.1" + s.Cfg.Addr
		}
	}
	return scheme + "://" + host
}

func (s *Server) pairJoin(w http.ResponseWriter, r *http.Request) {
	if s.Registry == nil || s.Registry.DB() == nil || !s.Registry.DB().Writable() {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		HCode    string                 `json:"h_code"`
		DeviceID string                 `json:"device_id"`
		OS       string                 `json:"os"`
		Caps     map[string]interface{} `json:"caps"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	body.HCode = strings.ToUpper(strings.TrimSpace(body.HCode))
	body.DeviceID = strings.TrimSpace(body.DeviceID)
	if body.HCode == "" || body.DeviceID == "" {
		http.Error(w, "h_code and device_id required", http.StatusBadRequest)
		return
	}
	d := s.Registry.DB()
	p, err := d.GetPairingByHCode(body.HCode)
	if err != nil || p == nil {
		http.Error(w, "invalid or expired h_code", http.StatusBadRequest)
		return
	}
	if p.Status != "pending" {
		http.Error(w, "pairing already joined", http.StatusConflict)
		return
	}
	pCode, err := db.RandomCode(6)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	token, err := db.RandomToken(32)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	capsJSON, _ := json.Marshal(body.Caps)
	if err := d.UpdatePairingJoin(p.ID, pCode, body.DeviceID, token, body.OS, string(capsJSON)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"pairing_id": p.ID,
		"p_code":     pCode,
		"status":     "joined",
	})
}

func (s *Server) pairStatus(w http.ResponseWriter, r *http.Request) {
	if s.Registry == nil || s.Registry.DB() == nil || !s.Registry.DB().Writable() {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	pid := r.URL.Query().Get("pairing_id")
	did := r.URL.Query().Get("device_id")
	p, err := s.Registry.DB().GetPairing(pid)
	if err != nil || p == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if did != "" && p.DeviceID != "" && p.DeviceID != did {
		http.Error(w, "device mismatch", http.StatusForbidden)
		return
	}
	out := map[string]interface{}{
		"pairing_id": p.ID,
		"status":     p.Status,
		"p_code":     p.PCode,
	}
	if p.Status == "sealed" {
		// token only returned once via sealed poll — we need to store sealed computer
		// After seal, peer looks up by reading sealed response fields set at confirm time.
		// We keep token in memory... For simplicity re-fetch computer by device.
		c, _ := s.Registry.DB().GetComputerByDevice(p.DeviceID)
		if c != nil {
			out["computer_id"] = c.ID
			out["display_name"] = c.DisplayName
		}
		// device_token is only available if we still have it — store sealed token map on Server
		if s.sealedTokens != nil {
			if tok, ok := s.sealedTokens[p.ID]; ok {
				out["device_token"] = tok
				delete(s.sealedTokens, p.ID)
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) pairConfirm(w http.ResponseWriter, r *http.Request) {
	if s.Registry == nil || s.Registry.DB() == nil || !s.Registry.DB().Writable() {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		PairingID   string `json:"pairing_id"`
		PCode       string `json:"p_code"`
		DisplayName string `json:"display_name"`
		ID          string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	d := s.Registry.DB()
	p, err := d.GetPairing(body.PairingID)
	if err != nil || p == nil {
		http.Error(w, "pairing not found", http.StatusNotFound)
		return
	}
	if p.Status != "joined" {
		http.Error(w, "pairing not ready (peer must join first)", http.StatusBadRequest)
		return
	}
	if strings.ToUpper(strings.TrimSpace(body.PCode)) != p.PCode {
		http.Error(w, "p_code mismatch", http.StatusBadRequest)
		return
	}
	if time.Now().Unix() > p.ExpiresAt {
		http.Error(w, "pairing expired", http.StatusBadRequest)
		return
	}
	slug := strings.TrimSpace(body.ID)
	if slug == "" {
		slug = slugify(body.DisplayName)
	}
	if slug == "" || slug == "process" {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.DisplayName)
	if name == "" {
		name = slug
	}
	now := time.Now().Unix()
	token := p.DeviceTokenPlain
	row := db.ComputerRow{
		ID:          slug,
		DisplayName: name,
		DeviceID:    p.DeviceID,
		TokenHash:   db.HashDeviceToken(token),
		OS:          p.OS,
		CapsJSON:    p.CapsJSON,
		PolicyJSON:  `{}`,
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := d.InsertComputer(row); err != nil {
		http.Error(w, "insert: "+err.Error(), http.StatusConflict)
		return
	}
	_ = d.SealPairing(p.ID)
	if s.sealedTokens == nil {
		s.sealedTokens = make(map[string]string)
	}
	s.sealedTokens[p.ID] = token
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"computer": map[string]interface{}{
			"id":           slug,
			"display_name": name,
			"device_id":    p.DeviceID,
			"os":           p.OS,
		},
	})
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else if r == ' ' || r == '_' {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func (s *Server) revokeComputer(w http.ResponseWriter, r *http.Request, id string) {
	if s.Registry == nil || s.Registry.DB() == nil || !s.Registry.DB().Writable() {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.Registry.DB().RevokeComputer(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"revoked": id})
}

func (s *Server) peerWS(w http.ResponseWriter, r *http.Request) {
	if s.PeerHub == nil || s.Registry == nil || s.Registry.DB() == nil {
		http.Error(w, "peer hub unavailable", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	deviceID := q.Get("device_id")
	token := q.Get("token")
	if deviceID == "" || token == "" {
		http.Error(w, "device_id and token required", http.StatusBadRequest)
		return
	}
	crow, err := s.Registry.DB().GetComputerByDevice(deviceID)
	if err != nil || crow == nil || !crow.Enabled || crow.RevokedAt.Valid {
		http.Error(w, "unknown or revoked device", http.StatusUnauthorized)
		return
	}
	if crow.TokenHash != db.HashDeviceToken(token) {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	// wait for hello
	_ = ws.SetReadDeadline(time.Now().Add(30 * time.Second))
	_, data, err := ws.ReadMessage()
	if err != nil {
		_ = ws.Close()
		return
	}
	var hello peerhub.Envelope
	if err := json.Unmarshal(data, &hello); err != nil || hello.Type != "hello" {
		_ = ws.WriteJSON(peerhub.Envelope{Type: "error", Error: "expected hello"})
		_ = ws.Close()
		return
	}
	caps := peerhub.Caps{}
	if hello.Caps != nil {
		caps = *hello.Caps
	}
	_ = ws.SetReadDeadline(time.Time{})
	conn := s.PeerHub.Register(crow.ID, deviceID, caps, hello.OS, hello.PeerVersion, ws)
	_ = ws.WriteJSON(peerhub.Envelope{
		Type:            "hello_ack",
		ComputerID:      crow.ID,
		ProtocolVersion: peerhub.ProtocolVersion,
	})
	capsB, _ := json.Marshal(caps)
	_ = s.Registry.DB().TouchComputer(crow.ID, string(capsB), peerRemote(r))
	go func() {
		conn.ReadLoop()
		s.PeerHub.Unregister(crow.ID, conn)
	}()
}

func peerRemote(r *http.Request) string {
	return r.RemoteAddr
}

// computerRPC is an operator debug endpoint: proxy one action to an online peer.
func (s *Server) computerRPC(w http.ResponseWriter, r *http.Request, id string) {
	if s.PeerHub == nil {
		http.Error(w, "no hub", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Kind      string                 `json:"kind"`
		Payload   map[string]interface{} `json:"payload"`
		SessionID string                 `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	conn := s.PeerHub.Get(id)
	if conn == nil {
		http.Error(w, "offline", http.StatusConflict)
		return
	}
	// For confirm: use shared id so harness UI can Accept/Deny the same wait.
	deadline := 60 * time.Second
	actionID := ""
	if body.Kind == "confirm" {
		deadline = 125 * time.Second
		actionID = uuid.NewString()
		prompt, _ := body.Payload["prompt"].(string)
		risk, _ := body.Payload["risk"].(string)
		s.PeerHub.PutConfirm(peerhub.PendingConfirm{
			ID:         actionID,
			SessionID:  body.SessionID,
			ComputerID: id,
			Prompt:     prompt,
			Risk:       risk,
			CreatedAt:  time.Now(),
			ExpiresAt:  time.Now().Add(120 * time.Second),
		})
		defer s.PeerHub.DeleteConfirm(actionID)
	}
	var res peerhub.Envelope
	var err error
	if actionID != "" {
		res, err = conn.CallWithID(actionID, body.Kind, body.Payload, deadline)
	} else {
		res, err = conn.Call(body.Kind, body.Payload, deadline)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	out := map[string]interface{}{
		"ok":   res.OK,
		"text": res.Text,
		"meta": res.Meta,
		"error": res.Error,
	}
	if actionID != "" {
		out["confirm_id"] = actionID
	}
	if res.ScreenshotB64 != "" {
		out["screenshot_b64_len"] = len(res.ScreenshotB64)
		// include prefix only
		if len(res.ScreenshotB64) > 80 {
			out["screenshot_b64_prefix"] = res.ScreenshotB64[:80]
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// listConfirms returns peer computer_confirm actions waiting for human Accept/Deny.
// Optional query session_id= filters to one chat session.
func (s *Server) listConfirms(w http.ResponseWriter, r *http.Request) {
	if s.PeerHub == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"confirms": []interface{}{}})
		return
	}
	sid := strings.TrimSpace(r.URL.Query().Get("session_id"))
	list := s.PeerHub.ListConfirms(sid)
	writeJSON(w, http.StatusOK, map[string]interface{}{"confirms": list})
}

// resolveConfirm Accept/Deny a pending computer_confirm from the Marble harness UI.
// Body: {"accept": true|false}
func (s *Server) resolveConfirm(w http.ResponseWriter, r *http.Request, id string) {
	if s.PeerHub == nil {
		http.Error(w, "no hub", http.StatusServiceUnavailable)
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		http.Error(w, "missing confirm id", http.StatusBadRequest)
		return
	}
	var body struct {
		Accept *bool `json:"accept"`
		OK     *bool `json:"ok"` // alias
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	accept := false
	if body.Accept != nil {
		accept = *body.Accept
	} else if body.OK != nil {
		accept = *body.OK
	} else {
		http.Error(w, `need {"accept":true|false}`, http.StatusBadRequest)
		return
	}
	if err := s.PeerHub.ResolveConfirmFromHarness(id, accept); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":         true,
		"confirm_id": id,
		"accepted":   accept,
		"source":     "harness",
	})
}

// handleSessionComputer binds a session to a computer.
func (s *Server) handleSessionComputer(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ComputerID string `json:"computer_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(body.ComputerID)
	if id != "" {
		c, err := s.Registry.DB().GetComputer(id)
		if err != nil || c == nil || c.RevokedAt.Valid {
			http.Error(w, "unknown computer", http.StatusBadRequest)
			return
		}
	}
	if err := s.Registry.SetComputerID(sessionID, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"computer_id": id})
}

