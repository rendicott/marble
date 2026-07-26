package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/rendicott/marble/internal/auth"
	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/cron"
	"github.com/rendicott/marble/internal/mcp"
	"github.com/rendicott/marble/internal/model"
	"github.com/rendicott/marble/internal/mpub"
	"github.com/rendicott/marble/internal/session"
	"github.com/rendicott/marble/internal/shellpolicy"
	"github.com/rendicott/marble/internal/tools"
	"github.com/rendicott/marble/internal/web"
	"github.com/rendicott/marble/internal/workspacefs"
)

// Server is the HTTP API + static UI host.
type Server struct {
	Cfg      config.Config
	Client   *model.Client
	Registry *session.Registry
	Daemon   *session.Daemon
	WS       *workspacefs.FS
	MCP      *mcp.Manager
	Policy   *shellpolicy.Policy
	Tools    *tools.Registry
	Mpub     *mpub.Store
	Cron     *cron.Manager
	Auth     *auth.Manager
	Mux      *http.ServeMux
}

// New constructs the HTTP server with routes.
func New(cfg config.Config, client *model.Client, reg *session.Registry, daemon *session.Daemon, ws *workspacefs.FS) *Server {
	s := &Server{Cfg: cfg, Client: client, Registry: reg, Daemon: daemon, WS: ws, Mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) routes() {
	if s.Auth != nil {
		s.Auth.RegisterRoutes(s.Mux)
	}
	s.Mux.HandleFunc("/api/health", s.handleHealth)
	s.Mux.HandleFunc("/api/sessions", s.handleSessions)
	s.Mux.HandleFunc("/api/sessions/", s.handleSessionSub)
	s.Mux.HandleFunc("/api/models", s.handleModels)
	s.Mux.HandleFunc("/api/models/", s.handleModels)
	s.Mux.HandleFunc("/api/workspace", s.handleWorkspace)
	s.Mux.HandleFunc("/api/workspace/", s.handleWorkspace)
	s.Mux.HandleFunc("/api/settings", s.handleSettings)
	s.Mux.HandleFunc("/api/settings/", s.handleSettings)
	s.Mux.HandleFunc("/api/prompt", s.handlePrompt)
	s.Mux.HandleFunc("/api/cron", s.handleCron)
	s.Mux.HandleFunc("/api/cron/", s.handleCron)
	// mpub before SPA catch-all (ADR-0009)
	s.Mux.HandleFunc("/mpub", s.handleMpub)
	s.Mux.HandleFunc("/mpub/", s.handleMpub)
	s.Mux.Handle("/", web.Handler())
}

// Handler returns the root handler with security headers, auth middleware + logging.
func (s *Server) Handler() http.Handler {
	var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &statusWriter{ResponseWriter: w, code: 200}
		s.Mux.ServeHTTP(lw, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, lw.code, time.Since(start).Round(time.Millisecond))
	})
	if s.Auth != nil {
		h = s.Auth.Middleware(h)
	}
	h = globalSecurityHeaders(h)
	return h
}

// globalSecurityHeaders applies baseline headers to all responses (SPA + API).
// Stricter CSP is set only on /mpub (see setMpubSecurityHeaders).
func globalSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

var _ http.Flusher = (*statusWriter)(nil)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Google mode + anonymous: minimal public probe (no paths, model URL, or internal status).
	if s.Auth != nil && s.Auth.Enabled() && auth.UserFromContext(r.Context()) == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":          true,
			"auth_mode":   "google",
			"tls_enabled": s.Cfg.TLSEnabled(),
			"detail":      "authenticate for full health",
		})
		return
	}
	ctx := r.Context()
	modelOK := true
	modelErr := ""
	if s.Client != nil {
		if err := s.Client.Health(ctx); err != nil {
			modelOK = false
			modelErr = err.Error()
		}
	}
	out := map[string]interface{}{
		"ok":              true,
		"model_ok":        modelOK,
		"model_error":     modelErr,
		"base_url":        s.Cfg.BaseURL,
		"model":           s.Cfg.Model,
		"context_limit":   s.Cfg.ContextLimit,
		"max_output":      s.Cfg.MaxOutput,
		"context_reserve": s.Cfg.ContextReserve,
		"budget":          s.Cfg.Budget(),
		"workspace":       s.Cfg.Workspace,
		"memory":          s.Cfg.Memory,
	}
	if d := s.db(); d != nil && d.Writable() {
		if total, enabled, err := d.CountModelCatalog(); err == nil {
			out["model_catalog_count"] = total
			out["model_catalog_enabled"] = enabled
		}
	}
	for k, v := range s.Cfg.ModelAuthPublic() {
		out[k] = v
	}
	for k, v := range s.Cfg.AuthPublicHealth() {
		out[k] = v
	}
	if s.Registry != nil {
		if store := s.Registry.Store(); store != nil {
			for k, v := range store.Health() {
				out[k] = v
			}
		}
	}
	if s.Daemon != nil {
		for k, v := range s.Daemon.Health() {
			out[k] = v
		}
	}
	if s.MCP != nil {
		for k, v := range s.MCP.Health() {
			out[k] = v
		}
	}
	if s.Mpub != nil {
		out["mpub_count"] = s.Mpub.Count()
		out["mpub_path"] = "/mpub"
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list := s.Registry.List()
		s.markCronSessions(list)
		writeJSON(w, http.StatusOK, map[string]interface{}{"sessions": list})
	case http.MethodPost:
		var body struct {
			Title string `json:"title"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sess := s.Registry.Create(body.Title)
		sum := sess.Summary()
		s.markCronSession(&sum)
		writeJSON(w, http.StatusCreated, sum)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// markCronSessions sets Summary.Cron / CronJobs for list rows (ADR-0015 UI badge).
func (s *Server) markCronSessions(list []session.Summary) {
	bound := map[string][]string{}
	if s.Cron != nil {
		bound = s.Cron.BoundSessions()
	}
	for i := range list {
		if names, ok := bound[list[i].ID]; ok {
			list[i].Cron = true
			list[i].CronJobs = names
		} else if isCronTitle(list[i].Title) {
			list[i].Cron = true
		}
	}
}

func (s *Server) markCronSession(sum *session.Summary) {
	if sum == nil {
		return
	}
	if s.Cron != nil {
		if names, ok := s.Cron.BoundSessions()[sum.ID]; ok {
			sum.Cron = true
			sum.CronJobs = names
			return
		}
	}
	if isCronTitle(sum.Title) {
		sum.Cron = true
	}
}

func isCronTitle(title string) bool {
	t := strings.TrimSpace(title)
	return strings.HasPrefix(strings.ToLower(t), "cron:")
}

func (s *Server) handleSessionSub(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	path = strings.Trim(path, "/")
	if path == "" {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(path, "/")
	id := parts[0]

	if len(parts) == 2 && parts[1] == "close" {
		s.handleClose(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "info" {
		s.handleSessionInfo(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "progress" {
		s.handleSessionProgress(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "stop" {
		s.handleSessionStop(w, r, id)
		return
	}
	if len(parts) >= 2 && parts[1] == "attachments" {
		s.handleSessionAttachments(w, r, id, parts[2:])
		return
	}

	sess, err := s.Registry.EnsureLoaded(id)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			sum := sess.Summary()
			s.markCronSession(&sum)
			var me map[string]interface{}
			if s.Registry != nil {
				if em, err := s.Registry.EffectiveModelFor(id); err == nil {
					me = em.Public()
					sum.Model = em.Model
				}
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"session":         sum,
				"messages":        sess.UIMessages(),
				"model_effective": me,
			})
			return
		case http.MethodPatch:
			s.handleSessionPatch(w, r, id)
			return
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
	}

	switch parts[1] {
	case "messages":
		s.handleMessages(w, r, id)
	case "events":
		s.handleEvents(w, r, sess)
	default:
		http.NotFound(w, r)
	}
}

// handleSessionInfo serves GET /api/sessions/{id}/info (ADR-0008).
func (s *Server) handleSessionInfo(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	info, err := s.Registry.Info(id)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if s.Tools != nil {
		info.SetAvailableTools(s.Tools.Catalog())
	} else {
		info.SetAvailableTools(tools.NativeCatalog())
	}
	// Cron badge fields for session info modal
	if s.Cron != nil {
		if names, ok := s.Cron.BoundSessions()[id]; ok {
			info.Session.Cron = true
			info.Session.CronJobs = names
		}
	}
	if !info.Session.Cron && isCronTitle(info.Session.Title) {
		info.Session.Cron = true
	}
	writeJSON(w, http.StatusOK, info)
}

// handleSessionProgress serves GET /api/sessions/{id}/progress (ADR-0010).
func (s *Server) handleSessionProgress(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	prog, err := s.Registry.Progress(id)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, prog)
}

// handleSessionStop serves POST /api/sessions/{id}/stop (ADR-0010).
func (s *Server) handleSessionStop(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	u := auth.UserFromContext(r.Context())
	auth.LogAction("stop", "session="+id, u)
	if s.Registry != nil {
		email, name := "", ""
		if u != nil {
			email, name = u.Email, u.Name
		}
		s.Registry.LogActorEventEmail(id, "stop", email, name)
	}
	if err := s.Registry.Stop(id); err != nil {
		if session.IsNotBusy(err) {
			http.Error(w, "session not busy", http.StatusConflict)
			return
		}
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"status": "stopping",
		"id":     id,
	})
}

func (s *Server) handleClose(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.Registry.Close(id); err != nil {
		if session.IsBusy(err) {
			http.Error(w, "session busy", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = s.Registry.CompactToday()
	writeJSON(w, http.StatusOK, map[string]string{"status": "closed", "id": id})
}

func (s *Server) handleSessionPatch(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		ModelID *string `json:"model_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.ModelID == nil {
		http.Error(w, "model_id required", http.StatusBadRequest)
		return
	}
	// UI PATCH: reject while busy (ADR-0018 Q11). Agent tool allows busy (next-turn).
	sess, em, err := s.Registry.SetSessionModelUI(id, *body.ModelID)
	if err != nil {
		if session.IsBusy(err) {
			http.Error(w, "session busy", http.StatusConflict)
			return
		}
		if strings.Contains(err.Error(), "closed") {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	u := auth.UserFromContext(r.Context())
	auth.LogAction("session_set_model", "session="+id+" model_id="+*body.ModelID, u)
	sum := sess.Summary()
	s.markCronSession(&sum)
	sum.Model = em.Model
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":         sum,
		"model_effective": em.Public(),
	})
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Content       string   `json:"content"`
		AttachmentIDs []string `json:"attachment_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Content) == "" && len(body.AttachmentIDs) == 0 {
		http.Error(w, "content or attachment_ids required", http.StatusBadRequest)
		return
	}
	var actor *session.Actor
	if u := auth.UserFromContext(r.Context()); u != nil {
		actor = &session.Actor{Email: u.Email, Name: u.Name, Sub: u.Sub}
	}
	if err := s.Registry.PostUserMessageWithAttachments(id, body.Content, actor, body.AttachmentIDs); err != nil {
		if session.IsBusy(err) {
			http.Error(w, "session busy", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		if sw, ok2 := w.(*statusWriter); ok2 {
			if f, ok3 := sw.ResponseWriter.(http.Flusher); ok3 {
				flusher = f
				ok = true
			}
		}
	}
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Disable proxy buffering (nginx etc.) so turn events flush promptly.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := sess.Subscribe()
	defer sess.Unsubscribe(ch)

	// hello tells the client to resync transcript (SSE has no catch-up).
	fmt.Fprintf(w, "event: hello\ndata: {\"session_id\":%q,\"resync\":true}\n\n", sess.ID)
	flusher.Flush()

	notify := r.Context().Done()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-notify:
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
			flusher.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
