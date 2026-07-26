package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rendicott/marble/internal/auth"
	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/session"
)

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/models")
	path = strings.Trim(path, "/")
	if path == "" {
		switch r.Method {
		case http.MethodGet:
			s.listModels(w, r)
		case http.MethodPost:
			s.createModel(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	parts := strings.Split(path, "/")
	id := parts[0]
	if len(parts) == 2 && parts[1] == "health" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.healthModel(w, r, id)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getModel(w, r, id)
	case http.MethodPut:
		s.updateModel(w, r, id)
	case http.MethodDelete:
		s.deleteModel(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	out := []map[string]interface{}{}
	if s.Registry != nil && s.Registry.Runner() != nil {
		out = append(out, s.Registry.Runner().ProcessPublic())
	}
	if d := s.db(); d != nil && d.Writable() {
		rows, err := d.ListModelCatalog()
		if err == nil {
			for i := range rows {
				out = append(out, s.Registry.Runner().CatalogRowPublic(&rows[i]))
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"models": out})
}

func (s *Server) getModel(w http.ResponseWriter, r *http.Request, id string) {
	if id == session.ProcessCatalogID {
		if s.Registry == nil || s.Registry.Runner() == nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, s.Registry.Runner().ProcessPublic())
		return
	}
	d := s.db()
	if d == nil || !d.Writable() {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	row, err := d.GetModelCatalog(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, s.Registry.Runner().CatalogRowPublic(row))
}

func (s *Server) createModel(w http.ResponseWriter, r *http.Request) {
	if s.db() == nil || !s.db().Writable() {
		http.Error(w, "database not writable", http.StatusServiceUnavailable)
		return
	}
	row, err := decodeCatalogBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := db.ValidateModelCatalog(row, s.Cfg.ContextReserve); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	row.CreatedAt = now
	row.UpdatedAt = now
	if err := s.db().InsertModelCatalog(*row); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.Registry != nil && s.Registry.Runner() != nil {
		s.Registry.Runner().InvalidateClientCache()
	}
	auth.LogAction("model_create", "id="+row.ID, auth.UserFromContext(r.Context()))
	writeJSON(w, http.StatusCreated, s.Registry.Runner().CatalogRowPublic(row))
}

func (s *Server) updateModel(w http.ResponseWriter, r *http.Request, id string) {
	if id == session.ProcessCatalogID {
		http.Error(w, "cannot modify process model", http.StatusBadRequest)
		return
	}
	if s.db() == nil || !s.db().Writable() {
		http.Error(w, "database not writable", http.StatusServiceUnavailable)
		return
	}
	row, err := decodeCatalogBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	row.ID = id
	if err := db.ValidateModelCatalog(row, s.Cfg.ContextReserve); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// preserve created_at
	if existing, err := s.db().GetModelCatalog(id); err == nil && existing != nil {
		row.CreatedAt = existing.CreatedAt
	}
	row.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.db().UpdateModelCatalog(*row); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.Registry != nil && s.Registry.Runner() != nil {
		s.Registry.Runner().InvalidateClientCache()
	}
	auth.LogAction("model_update", "id="+id, auth.UserFromContext(r.Context()))
	writeJSON(w, http.StatusOK, s.Registry.Runner().CatalogRowPublic(row))
}

func (s *Server) deleteModel(w http.ResponseWriter, r *http.Request, id string) {
	if id == session.ProcessCatalogID {
		http.Error(w, "cannot delete process model", http.StatusBadRequest)
		return
	}
	if s.db() == nil || !s.db().Writable() {
		http.Error(w, "database not writable", http.StatusServiceUnavailable)
		return
	}
	if err := s.db().DeleteModelCatalog(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if s.Registry != nil && s.Registry.Runner() != nil {
		s.Registry.Runner().InvalidateClientCache()
	}
	auth.LogAction("model_delete", "id="+id, auth.UserFromContext(r.Context()))
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
}

func (s *Server) healthModel(w http.ResponseWriter, r *http.Request, id string) {
	var em session.EffectiveModel
	if id == session.ProcessCatalogID {
		if s.Registry == nil || s.Registry.Runner() == nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		em = s.Registry.Runner().ProcessPublicAsEM()
	} else {
		d := s.db()
		if d == nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		row, err := d.GetModelCatalog(id)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		em = s.Registry.Runner().EffectiveFromRow(row)
	}
	client := s.Registry.Runner().ClientFor(em)
	ctx := r.Context()
	err := client.Health(ctx)
	ok := err == nil
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":      ok,
		"id":      id,
		"model":   em.Model,
		"base_url": em.BaseURL,
		"error":   msg,
	})
}

func (s *Server) db() *db.DB {
	if s.Registry == nil {
		return nil
	}
	return s.Registry.DB()
}

func decodeCatalogBody(r *http.Request) (*db.ModelCatalogRow, error) {
	var body struct {
		ID              string   `json:"id"`
		DisplayName     string   `json:"display_name"`
		Model           string   `json:"model"`
		BaseURL         string   `json:"base_url"`
		APIKeyEnv       string   `json:"api_key_env"`
		CostInputPer1M  *float64 `json:"cost_input_per_1m"`
		CostOutputPer1M *float64 `json:"cost_output_per_1m"`
		CostNotes       string   `json:"cost_notes"`
		CapReasoning    *bool    `json:"cap_reasoning"`
		CapImages       *bool    `json:"cap_images"`
		CapVoice        *bool    `json:"cap_voice"`
		CapTools        *bool    `json:"cap_tools"`
		ContextLimit    int      `json:"context_limit"`
		MaxOutput       int      `json:"max_output"`
		ContextReserve  int      `json:"context_reserve"`
		Enabled         *bool    `json:"enabled"`
		SortOrder       int      `json:"sort_order"`
		Notes           string   `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("bad json")
	}
	row := &db.ModelCatalogRow{
		ID:              body.ID,
		DisplayName:     body.DisplayName,
		Model:           body.Model,
		BaseURL:         body.BaseURL,
		APIKeyEnv:       body.APIKeyEnv,
		CostInputPer1M:  body.CostInputPer1M,
		CostOutputPer1M: body.CostOutputPer1M,
		CostNotes:       body.CostNotes,
		ContextLimit:    body.ContextLimit,
		MaxOutput:       body.MaxOutput,
		ContextReserve:  body.ContextReserve,
		SortOrder:       body.SortOrder,
		Notes:           body.Notes,
		CapTools:        true,
		Enabled:         true,
	}
	if body.CapReasoning != nil {
		row.CapReasoning = *body.CapReasoning
	}
	if body.CapImages != nil {
		row.CapImages = *body.CapImages
	}
	if body.CapVoice != nil {
		row.CapVoice = *body.CapVoice
	}
	if body.CapTools != nil {
		row.CapTools = *body.CapTools
	}
	if body.Enabled != nil {
		row.Enabled = *body.Enabled
	}
	return row, nil
}
