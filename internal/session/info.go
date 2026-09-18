package session

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/rendicott/marble/internal/agentproc"
	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/tools"
)

// DefaultRecentEvents is the ADR-0008 Q4 default.
const DefaultRecentEvents = 30

// InfoSession is the session block of GET …/info.
type InfoSession struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	TitleCustom  bool    `json:"title_custom,omitempty"` // operator rename — not auto from messages
	Status       string  `json:"status"`
	Busy         bool    `json:"busy"`
	Dirty        bool    `json:"dirty"`
	Loaded       bool    `json:"loaded"`
	Kind         string  `json:"kind,omitempty"`
	ParentID     string  `json:"parent_session_id,omitempty"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	ClosedAt     *string `json:"closed_at"`
	Model        string  `json:"model"`
	Workspace    string  `json:"workspace"`
	MDPath       string  `json:"md_path"`
	MDPathAbs    string  `json:"md_path_abs"`
	MessageCount int     `json:"message_count"`
	System       bool    `json:"system"`
	// LastPeerAction is the latest computer_* blurb (in-memory; empty if never used).
	LastPeerAction   string  `json:"last_peer_action,omitempty"`
	LastPeerActionAt *string `json:"last_peer_action_at,omitempty"`
	// Cron marks sessions used by durable cron jobs (ADR-0015); set by API.
	Cron     bool     `json:"cron,omitempty"`
	CronJobs []string `json:"cron_jobs,omitempty"`
	// SubprocessContext is the ADR-0031 session override (null = inherit default).
	SubprocessContext *agentproc.ContextSpec `json:"subprocess_context,omitempty"`
}

// InfoTTSArtifact is one session audio attachment produced by TTS (ADR-0027).
type InfoTTSArtifact struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	MIME      string `json:"mime,omitempty"`
	Bytes     int64  `json:"bytes"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	Voice     string `json:"voice,omitempty"`
	PhaseID   string `json:"phase_id,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	URL       string `json:"url,omitempty"`
}

// InfoTTS is process TTS status plus this session's latest audio artifact.
type InfoTTS struct {
	Enabled       bool             `json:"enabled"`
	Ready         bool             `json:"ready"`
	Configured    bool             `json:"configured"`
	Provider      string           `json:"provider,omitempty"`
	DefaultVoice  string           `json:"default_voice,omitempty"`
	DefaultModel  string           `json:"default_model,omitempty"`
	ReadyHint     string           `json:"ready_hint,omitempty"`
	LastError     string           `json:"last_error,omitempty"`
	SynthCalls    int64            `json:"synth_calls,omitempty"`
	SynthErrors   int64            `json:"synth_errors,omitempty"`
	ArtifactCount int              `json:"artifact_count"`
	LastArtifact  *InfoTTSArtifact `json:"last_artifact,omitempty"`
}

// InfoResponse is GET /api/sessions/{id}/info (ADR-0008).
type InfoResponse struct {
	Session        InfoSession          `json:"session"`
	Usage          db.SessionUsage      `json:"usage"`
	Tools          []db.ToolStat        `json:"tools"` // usage histogram (calls this session)
	AvailableTools []tools.CatalogEntry `json:"available_tools"`
	RecentEvents   []db.EventSummary    `json:"recent_events"`
	TTS            *InfoTTS             `json:"tts,omitempty"`
	Source         string               `json:"source"` // db | memory | markdown
	Partial        bool                 `json:"partial"`
}

// Info returns structured session diagnostics (ADR-0008).
// Prefer SQLite aggregates; limp/missing DB → metadata only with partial=true.
func (r *Registry) Info(id string) (*InfoResponse, error) {
	if id == "" {
		return nil, fmt.Errorf("session not found")
	}

	out := &InfoResponse{
		Tools:        []db.ToolStat{},
		RecentEvents: []db.EventSummary{},
		Source:       "memory",
		Partial:      true,
	}

	// Live session (optional load)
	var live *Session
	if s, ok := r.Get(id); ok {
		live = s
	} else if r.store != nil {
		if s, err := r.EnsureLoaded(id); err == nil {
			live = s
		}
	}

	// Disk index / markdown meta
	var diskTitle, diskStatus, diskModel, diskWS string
	var diskCreated, diskUpdated time.Time
	var diskClosed *time.Time
	var diskMsgs int
	var diskKind, diskParent string
	var diskTitleCustom bool
	r.mu.RLock()
	if m, ok := r.diskIndex[id]; ok {
		diskTitle = m.Title
		diskTitleCustom = m.TitleCustom
		diskStatus = m.Status
		diskModel = m.Model
		diskWS = m.Workspace
		diskCreated = m.CreatedAt
		diskUpdated = m.UpdatedAt
		diskClosed = m.ClosedAt
		diskMsgs = m.MessageCount
		diskKind = m.Kind
		diskParent = m.ParentID
	}
	r.mu.RUnlock()

	// If not in index, try reading markdown once
	if diskTitle == "" && r.store != nil && live == nil {
		if doc, err := r.store.ReadSession(id); err == nil {
			diskTitle = doc.Title
			diskTitleCustom = doc.TitleCustom
			diskStatus = doc.Status
			diskModel = doc.Model
			diskWS = doc.Workspace
			diskCreated = doc.CreatedAt
			diskUpdated = doc.UpdatedAt
			diskClosed = doc.ClosedAt
			diskMsgs = doc.MessageCount
			diskKind = doc.Kind
			diskParent = doc.ParentID
			out.Source = "markdown"
		}
	}

	mdRel := filepath.Join("session", id+".md")
	mdAbs := ""
	if r.store != nil {
		mdAbs = r.store.SessionPath(id)
	}

	// Base metadata from live or disk
	if live != nil {
		sum := live.Summary()
		out.Session = InfoSession{
			ID:                sum.ID,
			Title:             sum.Title,
			TitleCustom:       sum.TitleCustom,
			Status:            sum.Status,
			Busy:              sum.Busy,
			Dirty:             sum.Dirty,
			Loaded:            true,
			Kind:              sum.Kind,
			ParentID:          sum.ParentID,
			CreatedAt:         sum.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:         sum.UpdatedAt.UTC().Format(time.RFC3339),
			MessageCount:      sum.MessageCount,
			System:            sum.Kind == "system",
			Model:             r.model,
			Workspace:         r.workspace,
			MDPath:            mdRel,
			MDPathAbs:         mdAbs,
			LastPeerAction:    sum.LastPeerAction,
			SubprocessContext: sum.SubprocessContext,
		}
		if sum.LastPeerActionAt != nil {
			t := sum.LastPeerActionAt.UTC().Format(time.RFC3339)
			out.Session.LastPeerActionAt = &t
		}
		if sum.ClosedAt != nil {
			c := sum.ClosedAt.UTC().Format(time.RFC3339)
			out.Session.ClosedAt = &c
		}
		if diskModel != "" {
			out.Session.Model = diskModel
		}
		if diskWS != "" {
			out.Session.Workspace = diskWS
		}
	} else if diskTitle != "" || diskStatus != "" {
		out.Session = InfoSession{
			ID:           id,
			Title:        diskTitle,
			TitleCustom:  diskTitleCustom,
			Status:       diskStatus,
			Busy:         false,
			Dirty:        false,
			Loaded:       false,
			Kind:         diskKind,
			ParentID:     diskParent,
			MessageCount: diskMsgs,
			System:       diskKind == "system",
			Model:        firstNonEmpty(diskModel, r.model),
			Workspace:    firstNonEmpty(diskWS, r.workspace),
			MDPath:       mdRel,
			MDPathAbs:    mdAbs,
		}
		if !diskCreated.IsZero() {
			out.Session.CreatedAt = diskCreated.UTC().Format(time.RFC3339)
		}
		if !diskUpdated.IsZero() {
			out.Session.UpdatedAt = diskUpdated.UTC().Format(time.RFC3339)
		}
		if diskClosed != nil {
			c := diskClosed.UTC().Format(time.RFC3339)
			out.Session.ClosedAt = &c
		}
		if out.Source != "markdown" {
			out.Source = "memory"
		}
	}

	// DB enrich
	if r.sqldb != nil && r.sqldb.Writable() {
		bundle, err := r.sqldb.LoadSessionInfo(id, DefaultRecentEvents)
		if err != nil {
			return nil, err
		}
		out.Usage = bundle.Usage
		if bundle.Tools != nil {
			out.Tools = bundle.Tools
		}
		if bundle.Recent != nil {
			out.RecentEvents = bundle.Recent
		}
		out.Partial = false
		out.Source = "db"

		if row := bundle.Row; row != nil {
			if out.Session.ID == "" {
				out.Session.ID = row.ID
			}
			if row.Title != "" {
				out.Session.Title = row.Title
			}
			if row.Status != "" {
				out.Session.Status = row.Status
			}
			out.Session.MessageCount = row.MessageCount
			// Prefer live busy/dirty
			if live == nil {
				out.Session.Dirty = row.Dirty
			}
			if row.CreatedAt != "" {
				out.Session.CreatedAt = row.CreatedAt
			}
			if row.UpdatedAt != "" {
				out.Session.UpdatedAt = row.UpdatedAt
			}
			if row.ClosedAt.Valid {
				c := row.ClosedAt.String
				out.Session.ClosedAt = &c
			}
			if row.Model != "" {
				out.Session.Model = row.Model
			}
			if row.Workspace != "" {
				out.Session.Workspace = row.Workspace
			}
			if row.MDPath != "" {
				out.Session.MDPath = row.MDPath
			}
		}
		// Live always wins for busy/loaded
		if live != nil {
			sum := live.Summary()
			out.Session.Busy = sum.Busy
			out.Session.Dirty = sum.Dirty
			out.Session.Loaded = true
			out.Session.System = sum.Kind == "system"
			if sum.Kind != "" {
				out.Session.Kind = sum.Kind
			}
		}
	} else {
		// Limp: metadata only, no fake tokens (Q9)
		out.Partial = true
		if out.Source == "" || out.Source == "db" {
			if live != nil {
				out.Source = "memory"
			} else {
				out.Source = "markdown"
			}
		}
	}

	if out.Session.ID == "" {
		return nil, fmt.Errorf("session not found")
	}
	if out.Session.Status == "" {
		out.Session.Status = "active"
	}
	if out.Session.Title == "" {
		out.Session.Title = out.Session.ID
	}
	if out.Session.MDPath == "" {
		out.Session.MDPath = mdRel
	}
	if out.Session.MDPathAbs == "" {
		out.Session.MDPathAbs = mdAbs
	}
	if out.Tools == nil {
		out.Tools = []db.ToolStat{}
	}
	if out.AvailableTools == nil {
		out.AvailableTools = []tools.CatalogEntry{}
	}
	if out.RecentEvents == nil {
		out.RecentEvents = []db.EventSummary{}
	}
	return out, nil
}

// SetAvailableTools attaches the process tool catalog (native + MCP).
func (info *InfoResponse) SetAvailableTools(cat []tools.CatalogEntry) {
	if info == nil {
		return
	}
	if cat == nil {
		info.AvailableTools = []tools.CatalogEntry{}
		return
	}
	info.AvailableTools = cat
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
