package tools

import (
	"fmt"
	"strings"

	"github.com/rendicott/marble/internal/db"
)

func (r *Registry) agentPresetList(_ string) (string, error) {
	if r.ListAgentPresets == nil {
		return "", fmt.Errorf("agent presets not configured")
	}
	list, err := r.ListAgentPresets()
	if err != nil {
		return "", err
	}
	if list == nil {
		list = []map[string]interface{}{}
	}
	return mustJSON(map[string]interface{}{
		"presets": list,
		"drivers": db.KnownAgentDrivers,
		"note":    "Use session_set_agent_preset to lock this session to a detected preset. Create/edit presets in Settings → Agents.",
	}), nil
}

func (r *Registry) agentPresetGet(argsJSON string) (string, error) {
	if r.GetAgentPreset == nil {
		return "", fmt.Errorf("agent presets not configured")
	}
	var a struct {
		ID string `json:"id"`
	}
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	id := strings.TrimSpace(a.ID)
	if id == "" {
		return "", fmt.Errorf("id required")
	}
	out, err := r.GetAgentPreset(id)
	if err != nil {
		return "", err
	}
	return mustJSON(out), nil
}

func (r *Registry) sessionSetAgentPreset(argsJSON string, tc *TurnContext) (string, error) {
	if r.SetSessionAgentPreset == nil {
		return "", fmt.Errorf("session_set_agent_preset not configured")
	}
	if tc == nil || strings.TrimSpace(tc.SessionID) == "" {
		return "", fmt.Errorf("no current session")
	}
	var a struct {
		PresetID string `json:"preset_id"`
	}
	_ = parseArgs(argsJSON, &a)
	out, err := r.SetSessionAgentPreset(tc.SessionID, a.PresetID)
	if err != nil {
		return "", err
	}
	return mustJSON(out), nil
}
