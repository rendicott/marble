package tools

import (
	"fmt"
	"strings"
)

func (r *Registry) modelList(_ string) (string, error) {
	if r.ListModels == nil {
		return "", fmt.Errorf("model list not configured")
	}
	list, err := r.ListModels()
	if err != nil {
		return "", err
	}
	if list == nil {
		list = []map[string]interface{}{}
	}
	return mustJSON(list), nil
}

func (r *Registry) sessionSetModel(argsJSON string, tc *TurnContext) (string, error) {
	if r.SetSessionModel == nil {
		return "", fmt.Errorf("session_set_model not configured")
	}
	if tc == nil || strings.TrimSpace(tc.SessionID) == "" {
		return "", fmt.Errorf("no current session")
	}
	var a struct {
		ModelID string `json:"model_id"`
	}
	_ = parseArgs(argsJSON, &a)
	out, err := r.SetSessionModel(tc.SessionID, a.ModelID)
	if err != nil {
		return "", err
	}
	// Clarify next-turn semantics for the model.
	if out != nil {
		out["applies"] = "next_turn"
		out["note"] = "Catalog entries must be created in Settings → Models; this tool only selects an existing enabled id."
	}
	return mustJSON(out), nil
}
