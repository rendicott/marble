package tools

import (
	"fmt"
	"strings"

	"github.com/rendicott/marble/internal/cron"
)

func (r *Registry) cronMgr() (*cron.Manager, error) {
	if r.Cron == nil {
		return nil, fmt.Errorf("cron not configured")
	}
	return r.Cron, nil
}

func (r *Registry) cronList(argsJSON string) (string, error) {
	m, err := r.cronMgr()
	if err != nil {
		return "", err
	}
	var a struct {
		EnabledOnly bool   `json:"enabled_only"`
		SessionID   string `json:"session_id"`
	}
	_ = parseArgs(argsJSON, &a)
	jobs, err := m.List(a.EnabledOnly)
	if err != nil {
		return "", err
	}
	if a.SessionID != "" {
		var filtered []cron.Job
		for _, j := range jobs {
			if j.SessionID == a.SessionID {
				filtered = append(filtered, j)
			}
		}
		jobs = filtered
	}
	return mustJSON(jobs), nil
}

func (r *Registry) cronGet(argsJSON string) (string, error) {
	m, err := r.cronMgr()
	if err != nil {
		return "", err
	}
	var a struct {
		ID string `json:"id"`
	}
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.ID) == "" {
		return "", fmt.Errorf("id is required")
	}
	j, runs, err := m.Get(a.ID, true)
	if err != nil {
		return "", err
	}
	return mustJSON(map[string]interface{}{"job": j, "runs": runs}), nil
}

func (r *Registry) cronCreate(argsJSON string) (string, error) {
	m, err := r.cronMgr()
	if err != nil {
		return "", err
	}
	var a struct {
		Name         string `json:"name"`
		Enabled      *bool  `json:"enabled"`
		ScheduleKind string `json:"schedule_kind"`
		CronExpr     string `json:"cron_expr"`
		IntervalSec  int    `json:"interval_sec"`
		Timezone     string `json:"timezone"`
		SessionID    string `json:"session_id"`
		Prompt       string `json:"prompt"`
		MaxRuns      *int   `json:"max_runs"`
		ModelID      string `json:"model_id"`
	}
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	j, err := m.Create(cron.CreateInput{
		Name:         a.Name,
		Enabled:      a.Enabled,
		ScheduleKind: a.ScheduleKind,
		CronExpr:     a.CronExpr,
		IntervalSec:  a.IntervalSec,
		Timezone:     a.Timezone,
		SessionID:    a.SessionID,
		Prompt:       a.Prompt,
		MaxRuns:      a.MaxRuns,
		ModelID:      a.ModelID,
		CreatedBy:    "agent",
	})
	if err != nil {
		return "", err
	}
	return mustJSON(j), nil
}

func (r *Registry) cronUpdate(argsJSON string) (string, error) {
	m, err := r.cronMgr()
	if err != nil {
		return "", err
	}
	// Decode loosely so we know which fields were set
	raw := map[string]interface{}{}
	if err := parseArgs(argsJSON, &raw); err != nil {
		return "", err
	}
	id, _ := raw["id"].(string)
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("id is required")
	}
	in := cron.UpdateInput{}
	if v, ok := raw["name"].(string); ok {
		in.Name = &v
	}
	if v, ok := raw["enabled"].(bool); ok {
		in.Enabled = &v
	}
	if v, ok := raw["schedule_kind"].(string); ok {
		in.ScheduleKind = &v
	}
	if v, ok := raw["cron_expr"].(string); ok {
		in.CronExpr = &v
	}
	if v, ok := raw["interval_sec"].(float64); ok {
		i := int(v)
		in.IntervalSec = &i
	}
	if v, ok := raw["timezone"].(string); ok {
		in.Timezone = &v
	}
	if v, ok := raw["session_id"].(string); ok {
		in.SessionID = &v
	}
	if v, ok := raw["prompt"].(string); ok {
		in.Prompt = &v
	}
	if v, ok := raw["max_runs"].(float64); ok {
		i := int(v)
		p := &i
		in.MaxRuns = &p
	}
	if v, ok := raw["model_id"].(string); ok {
		in.ModelID = &v
	}
	j, err := m.Update(id, in)
	if err != nil {
		return "", err
	}
	return mustJSON(j), nil
}

func (r *Registry) cronDelete(argsJSON string) (string, error) {
	m, err := r.cronMgr()
	if err != nil {
		return "", err
	}
	var a struct {
		ID string `json:"id"`
	}
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.ID) == "" {
		return "", fmt.Errorf("id is required")
	}
	if err := m.Delete(a.ID); err != nil {
		return "", err
	}
	return mustJSON(map[string]string{"ok": "deleted", "id": a.ID}), nil
}

func (r *Registry) cronRun(argsJSON string) (string, error) {
	m, err := r.cronMgr()
	if err != nil {
		return "", err
	}
	var a struct {
		ID string `json:"id"`
	}
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.ID) == "" {
		return "", fmt.Errorf("id is required")
	}
	run, err := m.RunNow(a.ID)
	if err != nil {
		return "", err
	}
	return mustJSON(run), nil
}
