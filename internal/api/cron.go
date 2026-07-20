package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/rendicott/marble/internal/cron"
)

func (s *Server) handleCron(w http.ResponseWriter, r *http.Request) {
	if s.Cron == nil {
		http.Error(w, "cron unavailable", http.StatusServiceUnavailable)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/cron")
	path = strings.Trim(path, "/")

	// /api/cron/jobs, /api/cron/jobs/{id}, /api/cron/jobs/{id}/run, /api/cron/runs, /api/cron/preview
	if path == "" || path == "jobs" {
		s.handleCronJobs(w, r)
		return
	}
	if path == "runs" {
		s.handleCronRuns(w, r)
		return
	}
	if path == "preview" {
		s.handleCronPreview(w, r)
		return
	}
	if strings.HasPrefix(path, "jobs/") {
		rest := strings.TrimPrefix(path, "jobs/")
		parts := strings.Split(rest, "/")
		id := parts[0]
		if id == "" {
			http.Error(w, "missing job id", http.StatusBadRequest)
			return
		}
		if len(parts) == 1 {
			s.handleCronJobID(w, r, id)
			return
		}
		if len(parts) == 2 && parts[1] == "run" {
			s.handleCronJobRun(w, r, id)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Server) handleCronJobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		enabledOnly := r.URL.Query().Get("enabled") == "1" || r.URL.Query().Get("enabled") == "true"
		jobs, err := s.Cron.List(enabledOnly)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		if jobs == nil {
			jobs = []cron.Job{}
		}
		writeJSON(w, 200, map[string]interface{}{"jobs": jobs})
	case http.MethodPost:
		var body struct {
			Name         string `json:"name"`
			Enabled      *bool  `json:"enabled"`
			ScheduleKind string `json:"schedule_kind"`
			CronExpr     string `json:"cron_expr"`
			IntervalSec  int    `json:"interval_sec"`
			Timezone     string `json:"timezone"`
			SessionID    string `json:"session_id"`
			Prompt       string `json:"prompt"`
			MaxRuns      *int   `json:"max_runs"`
		}
		if err := readJSON(r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		j, err := s.Cron.Create(cron.CreateInput{
			Name:         body.Name,
			Enabled:      body.Enabled,
			ScheduleKind: body.ScheduleKind,
			CronExpr:     body.CronExpr,
			IntervalSec:  body.IntervalSec,
			Timezone:     body.Timezone,
			SessionID:    body.SessionID,
			Prompt:       body.Prompt,
			MaxRuns:      body.MaxRuns,
			CreatedBy:    "ui",
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, 201, j)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleCronJobID(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		j, runs, err := s.Cron.Get(id, true)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, 200, map[string]interface{}{"job": j, "runs": runs})
	case http.MethodPut:
		var body map[string]interface{}
		if err := readJSON(r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		in := cron.UpdateInput{}
		if v, ok := body["name"].(string); ok {
			in.Name = &v
		}
		if v, ok := body["enabled"].(bool); ok {
			in.Enabled = &v
		}
		if v, ok := body["schedule_kind"].(string); ok {
			in.ScheduleKind = &v
		}
		if v, ok := body["cron_expr"].(string); ok {
			in.CronExpr = &v
		}
		if v, ok := body["interval_sec"].(float64); ok {
			i := int(v)
			in.IntervalSec = &i
		}
		if v, ok := body["timezone"].(string); ok {
			in.Timezone = &v
		}
		if v, ok := body["session_id"].(string); ok {
			in.SessionID = &v
		}
		if v, ok := body["prompt"].(string); ok {
			in.Prompt = &v
		}
		if _, ok := body["max_runs"]; ok {
			if body["max_runs"] == nil {
				var nilMax *int
				in.MaxRuns = &nilMax
			} else if v, ok := body["max_runs"].(float64); ok {
				i := int(v)
				p := &i
				in.MaxRuns = &p
			}
		}
		j, err := s.Cron.Update(id, in)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, 200, j)
	case http.MethodDelete:
		if err := s.Cron.Delete(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, 200, map[string]string{"ok": "deleted"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleCronJobRun(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	run, err := s.Cron.RunNow(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, 200, run)
}

func (s *Server) handleCronRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}
	runs, err := s.Cron.ListRuns(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if runs == nil {
		runs = []cron.Run{}
	}
	writeJSON(w, 200, map[string]interface{}{"runs": runs})
}

func (s *Server) handleCronPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var kind, expr, tz string
	var interval, n int
	if r.Method == http.MethodGet {
		kind = r.URL.Query().Get("schedule_kind")
		expr = r.URL.Query().Get("cron_expr")
		tz = r.URL.Query().Get("timezone")
		interval, _ = strconv.Atoi(r.URL.Query().Get("interval_sec"))
		n, _ = strconv.Atoi(r.URL.Query().Get("n"))
	} else {
		var body struct {
			ScheduleKind string `json:"schedule_kind"`
			CronExpr     string `json:"cron_expr"`
			IntervalSec  int    `json:"interval_sec"`
			Timezone     string `json:"timezone"`
			N            int    `json:"n"`
		}
		if err := readJSON(r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		kind, expr, tz, interval, n = body.ScheduleKind, body.CronExpr, body.Timezone, body.IntervalSec, body.N
	}
	if n <= 0 {
		n = 5
	}
	times, err := s.Cron.Preview(kind, expr, interval, tz, n)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, 200, map[string]interface{}{"preview": times})
}

func readJSON(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
