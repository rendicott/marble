package sink

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rendicott/marble/internal/config"
)

const defaultWebhookTemplate = `{"text":{{json .Preview}},"link":{{json .DeepLink}}}`

// ManageTool is the manage_sinks agent tool (ADR-0028). sessionID may be empty
// for global actions; set_override/pause_all/resume_all require it.
func (m *Manager) ManageTool(ctx context.Context, argsJSON, sessionID string) (string, error) {
	if m == nil {
		return "", fmt.Errorf("sinks not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	raw := map[string]interface{}{}
	if err := parseToolArgs(argsJSON, &raw); err != nil {
		return "", err
	}
	var a toolArgs
	if err := parseToolArgs(argsJSON, &a); err != nil {
		return "", err
	}
	action := strings.ToLower(strings.TrimSpace(a.Action))
	switch action {
	case "", "list":
		return m.toolList(sessionID)
	case "get":
		return m.toolGet(a.ID)
	case "create", "add":
		return m.toolCreate(a, raw)
	case "update":
		return m.toolUpdate(a, raw)
	case "delete", "remove":
		return m.toolDelete(a.ID, sessionID)
	case "test":
		return m.toolTest(ctx, a.ID)
	case "set_override":
		return m.toolSetOverride(a.ID, a.Override, sessionID)
	case "pause_all":
		return m.toolPauseAll(sessionID)
	case "resume_all":
		return m.toolResumeAll(sessionID)
	case "set_base":
		if err := m.SetDeepLinkBase(a.GlobalDeepLinkBase); err != nil {
			return "", err
		}
		return toolJSON(map[string]interface{}{
			"ok":             true,
			"deep_link_base": m.ConfigSnapshot().DeepLinkBase,
		})
	case "env_names":
		return toolEnvNames(), nil
	default:
		return "", fmt.Errorf("unknown action %q (list|get|create|update|delete|test|set_override|pause_all|resume_all|set_base|env_names)", a.Action)
	}
}

type toolArgs struct {
	Action             string            `json:"action"`
	ID                 string            `json:"id"`
	Type               string            `json:"type"`
	Enabled            *bool             `json:"enabled"`
	DeepLinkBase       string            `json:"deep_link_base"`
	SecretEnv          string            `json:"secret_env"`
	TopicID            string            `json:"topic_id"`
	APIBase            string            `json:"api_base"`
	TitlePrefix        string            `json:"title_prefix"`
	URL                string            `json:"url"`
	Method             string            `json:"method"`
	Headers            map[string]string `json:"headers"`
	Template           string            `json:"template"`
	Channel            string            `json:"channel"`
	Username           string            `json:"username"`
	IconEmoji          string            `json:"icon_emoji"`
	AvatarURL          string            `json:"avatar_url"`
	Server             string            `json:"server"`
	Topic              string            `json:"topic"`
	Priority           int               `json:"priority"`
	Format             string            `json:"format"`
	Filters            *Filters          `json:"filters"`
	Override           string            `json:"override"`
	GlobalDeepLinkBase string            `json:"global_deep_link_base"`
}

func (m *Manager) toolList(sessionID string) (string, error) {
	cfg := m.ConfigSnapshot()
	rows := make([]map[string]interface{}, 0, len(cfg.Sinks))
	for _, s := range cfg.Sinks {
		rows = append(rows, publicSink(s))
	}
	out := map[string]interface{}{
		"sinks":          rows,
		"deep_link_base": cfg.DeepLinkBase,
		"status":         m.Status(),
		"types":          KnownTypes,
		"kinds":          KnownKinds,
		"history":        trimHistory(m.History(), 20),
		"note":           "secret_env is an env var NAME only. Put KEY=secret in $MEMORY/env (Settings → Secrets). action=create|update|delete mutates global sinks.json; set_override/pause_all/resume_all are this session only.",
	}
	if b := m.DeepLinkBaseResolved(); b != "" && b != cfg.DeepLinkBase {
		out["fallback_deep_link_base"] = b
	} else if b != "" && cfg.DeepLinkBase == "" {
		out["fallback_deep_link_base"] = b
	}
	if sessionID != "" {
		out["session_id"] = sessionID
		out["session_overrides"] = m.sessionOverrides(sessionID)
	}
	return toolJSON(out)
}

func (m *Manager) toolGet(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("id is required")
	}
	s, ok := m.GetSink(id)
	if !ok {
		return "", fmt.Errorf("sink %q not found", id)
	}
	return toolJSON(publicSink(s))
}

func (m *Manager) toolCreate(a toolArgs, raw map[string]interface{}) (string, error) {
	id := strings.TrimSpace(a.ID)
	typ := strings.ToLower(strings.TrimSpace(a.Type))
	if id == "" || typ == "" {
		return "", fmt.Errorf("create requires id and type (orb|webhook|slack|discord|ntfy|stdout)")
	}
	if _, ok := m.GetSink(id); ok {
		return "", fmt.Errorf("sink %q already exists; use action=update", id)
	}
	if err := checkSecretEnv(a.SecretEnv); err != nil {
		return "", err
	}
	spec := specFromArgs(a)
	applyCreateDefaults(&spec, raw)
	out, err := m.UpsertSink(spec)
	if err != nil {
		return "", err
	}
	return toolJSON(map[string]interface{}{
		"ok":   true,
		"sink": publicSink(out),
		"note": createNote(out),
	})
}

func (m *Manager) toolUpdate(a toolArgs, raw map[string]interface{}) (string, error) {
	id := strings.TrimSpace(a.ID)
	if id == "" {
		return "", fmt.Errorf("id is required")
	}
	cur, ok := m.GetSink(id)
	if !ok {
		return "", fmt.Errorf("sink %q not found", id)
	}
	if err := checkSecretEnv(a.SecretEnv); err != nil {
		return "", err
	}
	merged := mergeSink(cur, a, raw)
	out, err := m.UpsertSink(merged)
	if err != nil {
		return "", err
	}
	return toolJSON(map[string]interface{}{
		"ok":   true,
		"sink": publicSink(out),
		"note": createNote(out),
	})
}

func (m *Manager) toolDelete(id, sessionID string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("id is required")
	}
	if err := m.DeleteSink(id); err != nil {
		return "", err
	}
	if sessionID != "" {
		ov := m.sessionOverrides(sessionID)
		if _, ok := ov[id]; ok {
			delete(ov, id)
			_ = m.writeOverrides(sessionID, ov)
		}
	}
	return toolJSON(map[string]string{"ok": "deleted", "id": id})
}

func (m *Manager) toolTest(ctx context.Context, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("id is required")
	}
	rec, err := m.TestSink(ctx, id)
	if err != nil {
		return "", err
	}
	return toolJSON(rec)
}

func (m *Manager) toolSetOverride(id, override, sessionID string) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", fmt.Errorf("no current session")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("id is required")
	}
	if _, ok := m.GetSink(id); !ok {
		return "", fmt.Errorf("sink %q not found", id)
	}
	v, err := normalizeOverride(override)
	if err != nil {
		return "", err
	}
	ov := m.sessionOverrides(sessionID)
	if v == "" {
		delete(ov, id)
	} else {
		ov[id] = v
	}
	if err := m.writeOverrides(sessionID, ov); err != nil {
		return "", err
	}
	return toolJSON(map[string]interface{}{
		"ok":         true,
		"session_id": sessionID,
		"id":         id,
		"override":   overrideOrInherit(v),
		"overrides":  ov,
	})
}

func (m *Manager) toolPauseAll(sessionID string) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", fmt.Errorf("no current session")
	}
	cfg := m.ConfigSnapshot()
	ov := map[string]string{}
	for _, s := range cfg.Sinks {
		if s.ID != "" {
			ov[s.ID] = "off"
		}
	}
	if err := m.writeOverrides(sessionID, ov); err != nil {
		return "", err
	}
	return toolJSON(map[string]interface{}{
		"ok":         true,
		"session_id": sessionID,
		"overrides":  ov,
		"note":       "all sinks forced off for this session; resume_all clears overrides",
	})
}

func (m *Manager) toolResumeAll(sessionID string) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", fmt.Errorf("no current session")
	}
	if err := m.writeOverrides(sessionID, map[string]string{}); err != nil {
		return "", err
	}
	return toolJSON(map[string]interface{}{
		"ok":         true,
		"session_id": sessionID,
		"overrides":  map[string]string{},
		"note":       "session overrides cleared; sinks follow their global enabled flag",
	})
}

func (m *Manager) sessionOverrides(sessionID string) map[string]string {
	m.mu.Lock()
	get := m.sessionGet
	m.mu.Unlock()
	if get == nil {
		return map[string]string{}
	}
	ov := get(sessionID)
	if ov == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(ov))
	for k, v := range ov {
		out[k] = v
	}
	return out
}

func (m *Manager) writeOverrides(sessionID string, ov map[string]string) error {
	m.mu.Lock()
	set := m.sessionSet
	m.mu.Unlock()
	if set == nil {
		return fmt.Errorf("session sink overrides not configured")
	}
	return set(sessionID, ov)
}

func specFromArgs(a toolArgs) SinkSpec {
	spec := SinkSpec{
		ID:           a.ID,
		Type:         a.Type,
		DeepLinkBase: a.DeepLinkBase,
		SecretEnv:    a.SecretEnv,
		TopicID:      a.TopicID,
		APIBase:      a.APIBase,
		TitlePrefix:  a.TitlePrefix,
		URL:          a.URL,
		Method:       a.Method,
		Headers:      a.Headers,
		Template:     a.Template,
		Channel:      a.Channel,
		Username:     a.Username,
		IconEmoji:    a.IconEmoji,
		AvatarURL:    a.AvatarURL,
		Server:       a.Server,
		Topic:        a.Topic,
		Priority:     a.Priority,
		Format:       a.Format,
	}
	if a.Enabled != nil {
		spec.Enabled = *a.Enabled
	}
	if a.Filters != nil {
		spec.Filters = *a.Filters
	}
	return spec
}

func applyCreateDefaults(spec *SinkSpec, raw map[string]interface{}) {
	if _, ok := raw["enabled"]; !ok {
		spec.Enabled = true
	}
	if _, ok := raw["filters"]; !ok {
		t := true
		spec.Filters = Filters{
			Kinds:     []string{"complete", "error"},
			SkipEmpty: &t,
			SkipCron:  true,
		}
	}
	switch spec.Type {
	case "orb":
		if spec.SecretEnv == "" {
			spec.SecretEnv = "ORB_AK"
		}
		if spec.TitlePrefix == "" {
			if _, ok := raw["title_prefix"]; !ok {
				spec.TitlePrefix = "marble: "
			}
		}
	case "slack":
		if spec.SecretEnv == "" {
			spec.SecretEnv = "SLACK_WEBHOOK_URL"
		}
		if spec.Username == "" {
			if _, ok := raw["username"]; !ok {
				spec.Username = "marble"
			}
		}
	case "discord":
		if spec.SecretEnv == "" {
			spec.SecretEnv = "DISCORD_WEBHOOK_URL"
		}
		if spec.Username == "" {
			if _, ok := raw["username"]; !ok {
				spec.Username = "marble"
			}
		}
	case "webhook":
		if spec.Method == "" {
			spec.Method = "POST"
		}
		if spec.Template == "" {
			spec.Template = defaultWebhookTemplate
		}
		if spec.Headers == nil {
			spec.Headers = map[string]string{"Content-Type": "application/json"}
		}
	case "ntfy":
		if spec.Server == "" {
			spec.Server = "https://ntfy.sh"
		}
		if spec.Priority == 0 {
			spec.Priority = 3
		}
	case "stdout":
		if spec.Format == "" {
			spec.Format = "plain"
		}
	}
}

func mergeSink(cur SinkSpec, a toolArgs, raw map[string]interface{}) SinkSpec {
	out := cur
	if _, ok := raw["type"]; ok && strings.TrimSpace(a.Type) != "" {
		out.Type = a.Type
	}
	if _, ok := raw["enabled"]; ok && a.Enabled != nil {
		out.Enabled = *a.Enabled
	}
	if _, ok := raw["deep_link_base"]; ok {
		out.DeepLinkBase = a.DeepLinkBase
	}
	if _, ok := raw["secret_env"]; ok {
		out.SecretEnv = a.SecretEnv
	}
	if _, ok := raw["topic_id"]; ok {
		out.TopicID = a.TopicID
	}
	if _, ok := raw["api_base"]; ok {
		out.APIBase = a.APIBase
	}
	if _, ok := raw["title_prefix"]; ok {
		out.TitlePrefix = a.TitlePrefix
	}
	if _, ok := raw["url"]; ok {
		out.URL = a.URL
	}
	if _, ok := raw["method"]; ok {
		out.Method = a.Method
	}
	if _, ok := raw["headers"]; ok {
		out.Headers = a.Headers
	}
	if _, ok := raw["template"]; ok {
		out.Template = a.Template
	}
	if _, ok := raw["channel"]; ok {
		out.Channel = a.Channel
	}
	if _, ok := raw["username"]; ok {
		out.Username = a.Username
	}
	if _, ok := raw["icon_emoji"]; ok {
		out.IconEmoji = a.IconEmoji
	}
	if _, ok := raw["avatar_url"]; ok {
		out.AvatarURL = a.AvatarURL
	}
	if _, ok := raw["server"]; ok {
		out.Server = a.Server
	}
	if _, ok := raw["topic"]; ok {
		out.Topic = a.Topic
	}
	if _, ok := raw["priority"]; ok {
		out.Priority = a.Priority
	}
	if _, ok := raw["format"]; ok {
		out.Format = a.Format
	}
	if _, ok := raw["filters"]; ok && a.Filters != nil {
		out.Filters = *a.Filters
	}
	return out
}

func publicSink(s SinkSpec) map[string]interface{} {
	configured := false
	if s.SecretEnv != "" {
		_, _, configured = config.ResolveAPIKeyEnv(s.SecretEnv)
	}
	return map[string]interface{}{
		"id":                s.ID,
		"type":              s.Type,
		"enabled":           s.Enabled,
		"effective_enabled": s.EffectiveEnabled(),
		"deep_link_base":    s.DeepLinkBase,
		"secret_env":        s.SecretEnv,
		"secret_configured": configured,
		"topic_id":          s.TopicID,
		"api_base":          s.APIBase,
		"title_prefix":      s.TitlePrefix,
		"url":               s.URL,
		"method":            s.Method,
		"headers":           s.Headers,
		"template":          s.Template,
		"channel":           s.Channel,
		"username":          s.Username,
		"icon_emoji":        s.IconEmoji,
		"avatar_url":        s.AvatarURL,
		"server":            s.Server,
		"topic":             s.Topic,
		"priority":          s.Priority,
		"format":            s.Format,
		"filters":           s.Filters,
		"validation_note":   s.ValidationNote,
	}
}

func createNote(s SinkSpec) string {
	if s.ValidationNote != "" {
		return "saved but invalid: " + s.ValidationNote + " — fix with action=update (invalid sinks do not deliver)"
	}
	if s.SecretEnv != "" {
		_, _, ok := config.ResolveAPIKeyEnv(s.SecretEnv)
		if !ok {
			return "saved; secret_env " + s.SecretEnv + " is not set in process env or $MEMORY/env — add it in Settings → Secrets"
		}
	}
	if !s.Enabled {
		return "saved but disabled; set enabled=true or action=set_override override=on for this session"
	}
	return "saved; delivers on the next finished turn whose filters pass"
}

func checkSecretEnv(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if err := config.ValidateEnvKey(name); err != nil {
		return fmt.Errorf("secret_env must be an env var NAME (e.g. ORB_AK), never the secret: %w", err)
	}
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "orb_ak_") || strings.HasPrefix(lower, "orb_pk_") {
		return fmt.Errorf("secret_env looks like an Orb API key; pass the env var NAME (e.g. ORB_AK)")
	}
	return nil
}

func normalizeOverride(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "", "inherit":
		return "", nil
	case "on", "off":
		return v, nil
	default:
		return "", fmt.Errorf("override must be inherit, on, or off")
	}
}

func overrideOrInherit(v string) string {
	if v == "" {
		return "inherit"
	}
	return v
}

func toolEnvNames() string {
	_, paths, entries, err := config.ListManagedEnv()
	names := []map[string]interface{}{}
	if err == nil {
		for _, e := range entries {
			if e.Name == "" {
				continue
			}
			names = append(names, map[string]interface{}{
				"name":        e.Name,
				"configured":  strings.TrimSpace(e.Value) != "",
				"in_process":  e.InProcess,
				"in_env_file": e.InManagedFile,
			})
		}
	}
	s, _ := toolJSON(map[string]interface{}{
		"names":          names,
		"env_file_paths": paths,
		"note":           "Use these names as secret_env. Never put the secret value in manage_sinks.",
	})
	return s
}

func trimHistory(in []DeliveryRecord, n int) []DeliveryRecord {
	if n <= 0 || len(in) <= n {
		return in
	}
	return in[len(in)-n:]
}

func parseToolArgs(argsJSON string, dest interface{}) error {
	if argsJSON == "" || argsJSON == "null" {
		return nil
	}
	return json.Unmarshal([]byte(argsJSON), dest)
}

func toolJSON(v interface{}) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
