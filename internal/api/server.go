package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/rendicott/marble/internal/config"
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
	Mux      *http.ServeMux
}

// New constructs the HTTP server with routes.
func New(cfg config.Config, client *model.Client, reg *session.Registry, daemon *session.Daemon, ws *workspacefs.FS) *Server {
	s := &Server{Cfg: cfg, Client: client, Registry: reg, Daemon: daemon, WS: ws, Mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.Mux.HandleFunc("/api/health", s.handleHealth)
	s.Mux.HandleFunc("/api/sessions", s.handleSessions)
	s.Mux.HandleFunc("/api/sessions/", s.handleSessionSub)
	s.Mux.HandleFunc("/api/workspace", s.handleWorkspace)
	s.Mux.HandleFunc("/api/workspace/", s.handleWorkspace)
	s.Mux.HandleFunc("/api/settings", s.handleSettings)
	s.Mux.HandleFunc("/api/settings/", s.handleSettings)
	s.Mux.HandleFunc("/api/prompt", s.handlePrompt)
	// mpub before SPA catch-all (ADR-0009)
	s.Mux.HandleFunc("/mpub", s.handleMpub)
	s.Mux.HandleFunc("/mpub/", s.handleMpub)
	s.Mux.Handle("/", web.Handler())
}

// Handler returns the root handler with logging.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &statusWriter{ResponseWriter: w, code: 200}
		s.Mux.ServeHTTP(lw, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, lw.code, time.Since(start).Round(time.Millisecond))
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
	ctx := r.Context()
	modelOK := true
	modelErr := ""
	if err := s.Client.Health(ctx); err != nil {
		modelOK = false
		modelErr = err.Error()
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
	if store := s.Registry.Store(); store != nil {
		for k, v := range store.Health() {
			out[k] = v
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
		writeJSON(w, http.StatusOK, map[string]interface{}{"sessions": s.Registry.List()})
	case http.MethodPost:
		var body struct {
			Title string `json:"title"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sess := s.Registry.Create(body.Title)
		writeJSON(w, http.StatusCreated, sess.Summary())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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

	sess, err := s.Registry.EnsureLoaded(id)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"session":  sess.Summary(),
			"messages": sess.UIMessages(),
		})
		return
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

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		http.Error(w, "content required", http.StatusBadRequest)
		return
	}
	if err := s.Registry.PostUserMessage(id, body.Content); err != nil {
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
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := sess.Subscribe()
	defer sess.Unsubscribe(ch)

	fmt.Fprintf(w, "event: hello\ndata: {\"session_id\":%q}\n\n", sess.ID)
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
