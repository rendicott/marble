package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/session"
)

func grantServer(t *testing.T) *Server {
	t.Helper()
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return &Server{Registry: session.NewRegistry(nil, nil, d, "", "")}
}

func call(s *Server, method, path, body, remote string) (*httptest.ResponseRecorder, map[string]interface{}) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "harness:8080"
	if remote != "" {
		r.RemoteAddr = remote
	}
	w := httptest.NewRecorder()
	s.handleComputers(w, r)
	var m map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w, m
}

func TestEnrollEndToEnd(t *testing.T) {
	s := grantServer(t)
	enroll := func(secret, name, remote string) (*httptest.ResponseRecorder, map[string]interface{}) {
		b, _ := json.Marshal(map[string]interface{}{
			"grant_secret": secret, "device_name": name, "os": "windows", "peer_version": "v0.2.3",
			"caps": map[string]bool{"desktop": true},
		})
		return call(s, http.MethodPost, "/api/computers/enroll", string(b), remote)
	}

	// Dark until a grant exists.
	if w, _ := enroll("mgrant_x", "a", ""); w.Code != http.StatusNotFound {
		t.Fatalf("enroll without grants: %d", w.Code)
	}

	w, g := call(s, http.MethodPost, "/api/computers/grants", `{"device_name":"orb-win-test","os":"windows","ttl_sec":86400}`, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("create grant: %d %s", w.Code, w.Body)
	}
	secret, _ := g["grant_secret"].(string)
	if !strings.HasPrefix(secret, "mgrant_") || g["state"] != "pending" ||
		!strings.Contains(g["enroll_command"].(string), "--harness http://harness:8080 --grant "+secret) {
		t.Fatalf("grant response %v", g)
	}

	w, out := enroll(secret, "orb-win-test", "100.64.0.9:5555")
	if w.Code != http.StatusOK || out["computer_id"] != "orb-win-test" || out["device_token"] == "" ||
		out["harness_url"] != "http://harness:8080" || out["device_id"] == "" {
		t.Fatalf("enroll: %d %v", w.Code, out)
	}
	// The token works for the peer WebSocket lookup.
	c, _ := s.Registry.DB().GetComputerByDevice(out["device_id"].(string))
	if c == nil || c.TokenHash != db.HashDeviceToken(out["device_token"].(string)) {
		t.Fatalf("computer row %+v", c)
	}

	// The list never leaks the secret and shows the claim audit.
	w, l := call(s, http.MethodGet, "/api/computers/grants", "", "")
	if strings.Contains(w.Body.String(), secret) {
		t.Fatal("grant list must not include the secret")
	}
	row := l["grants"].([]interface{})[0].(map[string]interface{})
	if row["state"] != "claimed" || row["claimed_ip"] != "100.64.0.9" || row["computer_id"] != "orb-win-test" {
		t.Fatalf("grant row %v", row)
	}
}

func TestEnrollStatusCodesAndLimiter(t *testing.T) {
	s := grantServer(t)
	_, g := call(s, http.MethodPost, "/api/computers/grants", `{}`, "")
	secret := g["grant_secret"].(string)
	_, g2 := call(s, http.MethodPost, "/api/computers/grants", `{"device_name":"spare"}`, "")

	body := func(sec string) string { return `{"grant_secret":"` + sec + `","device_name":"m"}` }
	if w, _ := call(s, http.MethodPost, "/api/computers/enroll", body(secret), "10.1.1.1:1"); w.Code != 200 {
		t.Fatalf("first claim %d", w.Code)
	}
	if w, _ := call(s, http.MethodPost, "/api/computers/enroll", body(secret), "10.1.1.1:1"); w.Code != http.StatusConflict {
		t.Fatalf("reuse should be 409, got %d", w.Code)
	}
	if w, _ := call(s, http.MethodDelete, "/api/computers/grants/"+g2["grant_id"].(string), "", ""); w.Code != 200 {
		t.Fatalf("revoke %d %s", w.Code, w.Body)
	}
	// Revoking the last pending grant turns enroll dark again.
	if w, _ := call(s, http.MethodPost, "/api/computers/enroll", body(g2["grant_secret"].(string)), "10.1.1.1:1"); w.Code != http.StatusNotFound {
		t.Fatalf("revoked + no pending: %d", w.Code)
	}

	_, _ = call(s, http.MethodPost, "/api/computers/grants", `{}`, "")
	for i := 0; i < 10; i++ {
		if w, _ := call(s, http.MethodPost, "/api/computers/enroll", body("mgrant_wrong"), "10.9.9.9:1"); w.Code != http.StatusUnauthorized {
			t.Fatalf("bad secret %d: %d", i, w.Code)
		}
	}
	if w, _ := call(s, http.MethodPost, "/api/computers/enroll", body("mgrant_wrong"), "10.9.9.9:1"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("limiter: %d", w.Code)
	}
	// Other addresses are unaffected.
	if w, _ := call(s, http.MethodPost, "/api/computers/enroll", body("mgrant_wrong"), "10.9.9.8:1"); w.Code != http.StatusUnauthorized {
		t.Fatalf("other ip: %d", w.Code)
	}
	if w, _ := call(s, http.MethodPost, "/api/computers/grants", `{"ttl_sec":5}`, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("tiny ttl: %d", w.Code)
	}
}
