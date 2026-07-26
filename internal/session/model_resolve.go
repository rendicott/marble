package session

import (
	"fmt"
	"strings"

	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/model"
)

// ProcessCatalogID is the synthetic id for the CLI process model (not a DB row).
const ProcessCatalogID = "process"

// EffectiveModel is the resolved model for one turn (ADR-0018).
type EffectiveModel struct {
	Source         string // process | catalog | fallthrough
	CatalogID      string // "" for pure process; slug when catalog (even if fallthrough still sets attempted)
	DisplayName    string
	Model          string // provider model id
	BaseURL        string
	APIKey         string // resolved secret; never log
	APIKeyEnv      string // config string for mode display
	APIKeyMode     string // none | inherit | env | none_forced
	APIKeyConfigured bool
	ContextLimit   int
	MaxOutput      int
	ContextReserve int
	CapReasoning   bool
	CapImages      bool
	CapVoice       bool
	CapTools       bool
	Advisory       string // optional harness note (missing/disabled/empty key)
}

// Budget returns prompt budget tokens.
func (e EffectiveModel) Budget() int {
	return config.BudgetTokens(e.ContextLimit, e.MaxOutput, e.ContextReserve)
}

// UsageRatio returns est / budget.
func (e EffectiveModel) UsageRatio(est int) float64 {
	return config.UsageRatioOf(est, e.Budget())
}

// Public maps to JSON for API (no secrets).
func (e EffectiveModel) Public() map[string]interface{} {
	return map[string]interface{}{
		"source":           e.Source,
		"catalog_id":       e.CatalogID,
		"display_name":     e.DisplayName,
		"model":            e.Model,
		"base_url":         e.BaseURL,
		"api_key_mode":     e.APIKeyMode,
		"api_key_configured": e.APIKeyConfigured,
		"context_limit":    e.ContextLimit,
		"max_output":       e.MaxOutput,
		"context_reserve":  e.ContextReserve,
		"budget":           e.Budget(),
		"capabilities": map[string]bool{
			"reasoning": e.CapReasoning,
			"images":    e.CapImages,
			"voice":     e.CapVoice,
			"tools":     e.CapTools,
		},
		"advisory": e.Advisory,
	}
}

// clientCacheKey uniquely identifies a client configuration.
func (e EffectiveModel) clientCacheKey() string {
	return strings.Join([]string{
		e.BaseURL, e.Model, fmt.Sprintf("%d", e.MaxOutput),
		e.APIKeyMode, e.APIKeyEnv, // not the secret
		// include configured flag so empty vs set differs
		fmt.Sprintf("%v", e.APIKeyConfigured),
		// hash secret length only — two different keys same length collide rarely; include used name
		fmt.Sprintf("%d", len(e.APIKey)),
	}, "|")
}

// TurnOpts carries turn-scoped options set before go runTurn (ADR-0018 KD11).
type TurnOpts struct {
	CronModelID string
}

// CatalogLookup fetches a catalog row by id (nil if missing).
type CatalogLookup func(id string) (*db.ModelCatalogRow, error)

// resolveEffective picks cron pin → session model_id → process (ADR-0018 KD3).
func (r *Runner) resolveEffective(s *Session, opts TurnOpts) EffectiveModel {
	var advisories []string
	// 1) Cron pin
	if pin := strings.TrimSpace(opts.CronModelID); pin != "" {
		em, ok, note := r.resolveCatalogID(pin, "catalog")
		if ok {
			return em
		}
		if note != "" {
			advisories = append(advisories, note)
		}
	}
	// 2) Session override
	sid := ""
	if s != nil {
		s.mu.Lock()
		sid = strings.TrimSpace(s.ModelID)
		s.mu.Unlock()
	}
	if sid != "" {
		em, ok, note := r.resolveCatalogID(sid, "catalog")
		if ok {
			if len(advisories) > 0 {
				em.Advisory = strings.Join(advisories, "; ") + "; " + em.Advisory
			}
			return em
		}
		if note != "" {
			advisories = append(advisories, note)
		}
	}
	// 3) Process
	em := r.processEffective()
	if len(advisories) > 0 {
		em.Advisory = strings.Join(advisories, "; ")
	}
	return em
}

func (r *Runner) processEffective() EffectiveModel {
	mode := "none"
	if strings.TrimSpace(r.Cfg.APIKeyEnv) != "" {
		mode = "env"
	}
	return EffectiveModel{
		Source:           "process",
		CatalogID:        "",
		DisplayName:      "Process default (CLI)",
		Model:            r.Cfg.Model,
		BaseURL:          r.Cfg.BaseURL,
		APIKey:           r.Cfg.APIKey,
		APIKeyEnv:        r.Cfg.APIKeyEnv,
		APIKeyMode:       mode,
		APIKeyConfigured: r.Cfg.APIKeyEnvConfigured,
		ContextLimit:     r.Cfg.ContextLimit,
		MaxOutput:        r.Cfg.MaxOutput,
		ContextReserve:   r.Cfg.ContextReserve,
		CapReasoning:     true,
		CapImages:        false,
		CapVoice:         false,
		CapTools:         true,
	}
}

// resolveCatalogID loads a catalog row; returns false if missing/disabled (caller falls through).
func (r *Runner) resolveCatalogID(id string, source string) (em EffectiveModel, ok bool, advisory string) {
	id = strings.TrimSpace(id)
	if id == "" || id == ProcessCatalogID {
		return EffectiveModel{}, false, ""
	}
	var row *db.ModelCatalogRow
	var err error
	if r.CatalogGet != nil {
		row, err = r.CatalogGet(id)
	} else if r.Reg != nil && r.Reg.sqldb != nil && r.Reg.sqldb.Writable() {
		row, err = r.Reg.sqldb.GetModelCatalog(id)
	} else {
		return EffectiveModel{}, false, fmt.Sprintf("[harness] model_id %q unavailable (no catalog); using process default", id)
	}
	if err != nil || row == nil {
		return EffectiveModel{}, false, fmt.Sprintf("[harness] model_id %q not found; using process default", id)
	}
	if !row.Enabled {
		return EffectiveModel{}, false, fmt.Sprintf("[harness] model_id %q is disabled; using process default", id)
	}
	em = r.effectiveFromRow(row, source)
	return em, true, ""
}

func (r *Runner) effectiveFromRow(row *db.ModelCatalogRow, source string) EffectiveModel {
	base := strings.TrimSpace(row.BaseURL)
	if base == "" {
		base = r.Cfg.BaseURL
	}
	reserve := row.ContextReserve
	if reserve == 0 {
		reserve = r.Cfg.ContextReserve
		if reserve <= 0 {
			reserve = 8192
		}
	}
	em := EffectiveModel{
		Source:         source,
		CatalogID:      row.ID,
		DisplayName:    row.DisplayName,
		Model:          row.Model,
		BaseURL:        base,
		ContextLimit:   row.ContextLimit,
		MaxOutput:      row.MaxOutput,
		ContextReserve: reserve,
		CapReasoning:   row.CapReasoning,
		CapImages:      row.CapImages,
		CapVoice:       row.CapVoice,
		CapTools:       row.CapTools,
		APIKeyEnv:      row.APIKeyEnv,
	}
	// Auth (Q15)
	envSpec := strings.TrimSpace(row.APIKeyEnv)
	switch {
	case envSpec == "":
		em.APIKey = r.Cfg.APIKey
		em.APIKeyEnv = r.Cfg.APIKeyEnv
		em.APIKeyMode = "inherit"
		em.APIKeyConfigured = r.Cfg.APIKeyEnvConfigured
	case strings.EqualFold(envSpec, "none"):
		em.APIKey = ""
		em.APIKeyMode = "none_forced"
		em.APIKeyConfigured = false
	default:
		key, used, configured := config.ResolveAPIKeyEnv(envSpec)
		em.APIKey = key
		em.APIKeyEnv = envSpec
		em.APIKeyMode = "env"
		em.APIKeyConfigured = configured
		if !configured {
			em.Advisory = fmt.Sprintf("[harness] model %q api_key_env %q unset; calling without Authorization", row.ID, envSpec)
		}
		_ = used
	}
	return em
}

// clientFor returns a model client for em (cached; process client when source=process).
func (r *Runner) clientFor(em EffectiveModel) *model.Client {
	if em.Source == "process" || (em.CatalogID == "" && em.BaseURL == r.Cfg.BaseURL && em.Model == r.Cfg.Model && em.MaxOutput == r.Cfg.MaxOutput && em.APIKey == r.Cfg.APIKey) {
		// Prefer process client for pure process
		if r.Client != nil && em.Source == "process" {
			return r.Client
		}
	}
	r.ensureClientCache()
	key := em.clientCacheKey() + "|" + em.APIKey // include secret in key under lock only
	r.clientMu.Lock()
	defer r.clientMu.Unlock()
	if c, ok := r.clientCache[key]; ok {
		return c
	}
	c := model.New(em.BaseURL, em.Model, em.MaxOutput, em.APIKey)
	r.clientCache[key] = c
	return c
}

func (r *Runner) ensureClientCache() {
	if r.clientCache == nil {
		r.clientCache = make(map[string]*model.Client)
	}
}

// InvalidateClientCache clears cached clients (after catalog write).
func (r *Runner) InvalidateClientCache() {
	r.clientMu.Lock()
	r.clientCache = make(map[string]*model.Client)
	r.clientMu.Unlock()
}

// ApplyCapabilityFilter strips unsupported modalities on a clone (ADR-0019 Q11).
// Does not mutate caller's slice contents if they already shared clones from trimHistory.
// Returns whether any strip occurred (caller may advisory).
func ApplyCapabilityFilter(msgs []model.Message, em EffectiveModel) ([]model.Message, bool) {
	if em.CapImages {
		return msgs, false
	}
	stripped := false
	out := make([]model.Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		out[i].Content = model.CloneContent(m.Content)
		c, dropped := dropImageParts(out[i].Content)
		if dropped {
			stripped = true
			out[i].Content = c
		}
	}
	return out, stripped
}

// ProcessPublic builds the synthetic process list entry for /api/models.
func (r *Runner) ProcessPublic() map[string]interface{} {
	em := r.processEffective()
	m := em.Public()
	m["id"] = ProcessCatalogID
	m["enabled"] = true
	m["read_only"] = true
	return m
}

// ProcessPublicAsEM returns process EffectiveModel.
func (r *Runner) ProcessPublicAsEM() EffectiveModel { return r.processEffective() }

// EffectiveFromRow builds EffectiveModel from a catalog row.
func (r *Runner) EffectiveFromRow(row *db.ModelCatalogRow) EffectiveModel {
	return r.effectiveFromRow(row, "catalog")
}

// ClientFor returns a client for em (exported for health probes).
func (r *Runner) ClientFor(em EffectiveModel) *model.Client { return r.clientFor(em) }

// CatalogRowPublic maps a DB row to public API shape.
func (r *Runner) CatalogRowPublic(row *db.ModelCatalogRow) map[string]interface{} {
	em := r.effectiveFromRow(row, "catalog")
	m := em.Public()
	m["id"] = row.ID
	m["enabled"] = row.Enabled
	m["sort_order"] = row.SortOrder
	m["notes"] = row.Notes
	m["cost_input_per_1m"] = row.CostInputPer1M
	m["cost_output_per_1m"] = row.CostOutputPer1M
	m["cost_notes"] = row.CostNotes
	m["base_url_configured"] = row.BaseURL
	m["api_key_env"] = row.APIKeyEnv
	m["context_reserve_configured"] = row.ContextReserve
	m["read_only"] = false
	return m
}

