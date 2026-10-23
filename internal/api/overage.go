package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rendicott/marble/internal/session"
)

// handleOverageRescue serves POST /api/sessions/{id}/overage-rescue.
// It does not accept message text and does not start a turn.
func (s *Server) handleOverageRescue(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		DryRun bool `json:"dry_run"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
	}
	if s.Registry == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	res, err := s.Registry.OverageRescue(id, req.DryRun, time.Time{})
	if err != nil {
		if session.IsBusy(err) {
			http.Error(w, "session busy", http.StatusConflict)
			return
		}
		msg := err.Error()
		switch {
		case strings.Contains(msg, "not found"):
			http.Error(w, "session not found", http.StatusNotFound)
		case strings.Contains(msg, "closed"):
			http.Error(w, "session is closed", http.StatusBadRequest)
		default:
			http.Error(w, msg, http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, http.StatusOK, rescueJSON(res))
}

func rescueJSON(res session.RescueResult) map[string]interface{} {
	out := map[string]interface{}{
		"action":           res.Action,
		"context_limit":    res.ContextLimit,
		"margin":           res.Margin,
		"scale":            res.Scale,
		"before_est":       res.BeforeEst,
		"after_est":        res.AfterEst,
		"target":           res.Target,
		"omitted_messages": res.OmittedMessages,
		"stubbed_messages": res.StubbedMessages,
		"omitted_images":   res.OmittedImages,
	}
	if res.DryRun {
		out["dry_run"] = true
		return out
	}
	out["archive"] = res.Archive
	if res.EventSeq > 0 {
		out["event_seq"] = res.EventSeq
	}
	if res.Error != "" {
		out["error"] = res.Error
	}
	return out
}
