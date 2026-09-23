package tools

import (
	"fmt"
	"strings"
	"time"

	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/db"
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
	return mustJSON(map[string]interface{}{
		"models":         list,
		"note":           "Use model_add / model_update to create catalog entries (never put secrets in tools — only api_key_env names). Put KEY=… in $MEMORY/env (Settings → Secrets). session_set_model only selects an existing chat id. kind=image models (gpt-image-*) are not session models — call generate_image.",
		"env_file_paths": config.EnvFilePaths(),
	}), nil
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
	if out != nil {
		out["applies"] = "next_turn"
		out["note"] = "Selects an existing enabled chat catalog id (or empty for process). kind=image rows cannot be the session model — use generate_image. Create missing entries with model_add after researching base_url / limits / caps."
	}
	return mustJSON(out), nil
}

func (r *Registry) modelGet(argsJSON string) (string, error) {
	if r.GetModel == nil {
		return "", fmt.Errorf("model get not configured")
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
	out, err := r.GetModel(id)
	if err != nil {
		return "", err
	}
	return mustJSON(out), nil
}

// modelCatalogArgs is the shared create/update payload (agent-facing).
type modelCatalogArgs struct {
	ID              string   `json:"id"`
	DisplayName     string   `json:"display_name"`
	Model           string   `json:"model"`
	Kind            string   `json:"kind"`
	BaseURL         string   `json:"base_url"`
	APIKeyEnv       string   `json:"api_key_env"`
	ContextLimit    int      `json:"context_limit"`
	MaxOutput       int      `json:"max_output"`
	ContextReserve  *int     `json:"context_reserve"`
	CapTools        *bool    `json:"cap_tools"`
	CapReasoning    *bool    `json:"cap_reasoning"`
	CapImages       *bool    `json:"cap_images"`
	CapVoice        *bool    `json:"cap_voice"`
	Enabled         *bool    `json:"enabled"`
	SortOrder       *int     `json:"sort_order"`
	Notes           string   `json:"notes"`
	CostInputPer1M  *float64 `json:"cost_input_per_1m"`
	CostOutputPer1M *float64 `json:"cost_output_per_1m"`
	CostNotes       string   `json:"cost_notes"`
}

func (r *Registry) modelAdd(argsJSON string) (string, error) {
	if r.CreateModel == nil {
		return "", fmt.Errorf("model_add not configured (database may be limp)")
	}
	var a modelCatalogArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	row, err := catalogArgsToRow(a, true)
	if err != nil {
		return "", err
	}
	out, err := r.CreateModel(row)
	if err != nil {
		return "", err
	}
	return mustJSON(enrichModelWriteResult(out, "created")), nil
}

func (r *Registry) modelUpdate(argsJSON string) (string, error) {
	if r.UpdateModel == nil {
		return "", fmt.Errorf("model_update not configured (database may be limp)")
	}
	var a modelCatalogArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.ID) == "" {
		return "", fmt.Errorf("id required")
	}
	// Track which optional fields were present so UpdateModel can merge.
	var raw map[string]interface{}
	_ = parseArgs(argsJSON, &raw)
	row, err := catalogArgsToRow(a, false)
	if err != nil {
		return "", err
	}
	// Omitted kind keeps the stored value (partial update). Empty string sent
	// explicitly still normalises to chat inside catalogArgsToRow.
	if raw == nil || raw["kind"] == nil {
		row.Kind = ""
	}
	out, err := r.UpdateModel(row)
	if err != nil {
		return "", err
	}
	_ = raw
	return mustJSON(enrichModelWriteResult(out, "updated")), nil
}

func enrichModelWriteResult(out map[string]interface{}, action string) map[string]interface{} {
	if out == nil {
		out = map[string]interface{}{}
	}
	out["action"] = action
	out["env_file_paths"] = config.EnvFilePaths()
	if cfg, _ := out["api_key_configured"].(bool); !cfg {
		if env, _ := out["api_key_env"].(string); strings.TrimSpace(env) != "" && !strings.EqualFold(strings.TrimSpace(env), "none") {
			if hint, _ := out["auth_hint"].(string); hint == "" {
				out["auth_hint"] = config.AuthHintForAPIKeyEnv(env)
			}
			out["next_step"] = "Add KEY=secret to an env file listed in env_file_paths (no restart for catalog). Do not put the secret in model_add/model_update."
		}
	}
	note := "Never store API secrets in the catalog — only api_key_env names. Prefer OpenAI-compatible base_url (e.g. Gemini: https://generativelanguage.googleapis.com/v1beta/openai)."
	if k, _ := out["kind"].(string); k == "image" {
		note += " This row is kind=image (Images API). It cannot be the session model — call generate_image."
	}
	out["note"] = note
	return out
}

func catalogArgsToRow(a modelCatalogArgs, isCreate bool) (db.ModelCatalogRow, error) {
	id := strings.TrimSpace(a.ID)
	if id == "" {
		return db.ModelCatalogRow{}, fmt.Errorf("id required (slug, e.g. grok-4.5 or gemini-3.6)")
	}
	// Reject accidental secret material in api_key_env early with a clear message.
	env := strings.TrimSpace(a.APIKeyEnv)
	if strings.Contains(env, "sk-") || strings.Contains(env, "AIza") || (strings.Contains(env, "=") && !strings.EqualFold(env, "none")) {
		return db.ModelCatalogRow{}, fmt.Errorf("api_key_env must be an environment variable NAME only (e.g. GEMINI_API_KEY), never the secret value")
	}

	display := strings.TrimSpace(a.DisplayName)
	if display == "" {
		display = id
	}
	model := strings.TrimSpace(a.Model)
	if model == "" {
		return db.ModelCatalogRow{}, fmt.Errorf("model (provider model string) required")
	}
	ctxLim := a.ContextLimit
	if ctxLim <= 0 && isCreate {
		ctxLim = 131072
	}
	maxOut := a.MaxOutput
	if maxOut <= 0 && isCreate {
		maxOut = 8192
	}
	// On update, 0 means “keep existing” (merged in main).
	reserve := 0
	if a.ContextReserve != nil {
		reserve = *a.ContextReserve
	}
	capTools := true
	if a.CapTools != nil {
		capTools = *a.CapTools
	} else if !isCreate {
		// sentinel: UpdateModel treats CapTools=false+CapReasoning=false+… carefully via mergeFlags
		capTools = true
	}
	capReason, capImg, capVoice := false, false, false
	if a.CapReasoning != nil {
		capReason = *a.CapReasoning
	}
	if a.CapImages != nil {
		capImg = *a.CapImages
	}
	if a.CapVoice != nil {
		capVoice = *a.CapVoice
	}
	kind := db.NormalizeModelKind(a.Kind)
	if kind != "chat" && kind != "image" {
		return db.ModelCatalogRow{}, fmt.Errorf("kind must be chat or image")
	}
	// Image models are not chat agents. Defaults keep context_limit/max_output
	// so catalog validation still passes.
	if kind == "image" {
		if a.CapTools == nil {
			capTools = false
		}
		if a.CapReasoning == nil {
			capReason = false
		}
	}
	enabled := true
	if a.Enabled != nil {
		enabled = *a.Enabled
	}
	sortOrder := 0
	if a.SortOrder != nil {
		sortOrder = *a.SortOrder
	}
	now := time.Now().UTC().Format(time.RFC3339)
	return db.ModelCatalogRow{
		ID:              id,
		DisplayName:     display,
		Model:           model,
		Kind:            kind,
		BaseURL:         strings.TrimSpace(a.BaseURL),
		APIKeyEnv:       env,
		CostInputPer1M:  a.CostInputPer1M,
		CostOutputPer1M: a.CostOutputPer1M,
		CostNotes:       strings.TrimSpace(a.CostNotes),
		CapReasoning:    capReason,
		CapImages:       capImg,
		CapVoice:        capVoice,
		CapTools:        capTools,
		ContextLimit:    ctxLim,
		MaxOutput:       maxOut,
		ContextReserve:  reserve,
		Enabled:         enabled,
		SortOrder:       sortOrder,
		Notes:           strings.TrimSpace(a.Notes),
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}
