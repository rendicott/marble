package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/rendicott/marble/internal/clerk"
)

// Clerk is set from main (ADR-0023).
// (field on Server)

func (s *Server) handleClerk(w http.ResponseWriter, r *http.Request) {
	if s.Clerk == nil {
		http.Error(w, "clerk unavailable", http.StatusServiceUnavailable)
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/clerk"), "/")
	switch {
	case path == "" && r.Method == http.MethodGet:
		includeClosed := r.URL.Query().Get("include_closed") == "1" ||
			r.URL.Query().Get("include_closed") == "true"
		// Default: hide snoozed so they don't clutter the top. ?include_snoozed=1 shows them (still sorted last).
		includeSnoozed := r.URL.Query().Get("include_snoozed") == "1" ||
			r.URL.Query().Get("include_snoozed") == "true"
		rows, err := s.Clerk.ListFiltered(includeClosed, includeSnoozed)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if rows == nil {
			rows = []clerk.Row{}
		}
		snoozedN := s.Clerk.CountSnoozed(includeClosed)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"sessions":       rows,
			"count":          len(rows),
			"snoozed_count":  snoozedN,
			"include_snoozed": includeSnoozed,
		})
	case path == "refresh" && r.Method == http.MethodPost:
		var body struct {
			SessionIDs []string `json:"session_ids"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		// If empty, refresh all currently idle (non-closed, non-system) from list
		ids := body.SessionIDs
		if len(ids) == 0 {
			rows, err := s.Clerk.ListFiltered(false, true)
			if err == nil {
				for _, row := range rows {
					if !row.Busy {
						ids = append(ids, row.SessionID)
					}
				}
			}
		}
		n, err := s.Clerk.EnqueueRefresh(ids)
		if err != nil {
			http.Error(w, err.Error(), http.StatusTooManyRequests)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"queued": n,
		})
	case path == "snooze" && r.Method == http.MethodPost:
		var body struct {
			SessionID string `json:"session_id"`
			Duration  string `json:"duration"` // 1h|4h|1d|3d|1w|tomorrow|clear
			Until     string `json:"until"`    // optional RFC3339 override
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		until, err := s.Clerk.Snooze(body.SessionID, body.Duration, body.Until)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"session_id":    body.SessionID,
			"snoozed_until": until,
			"cleared":       until == "",
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
