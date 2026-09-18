package session

import (
	"encoding/json"
	"strings"

	"github.com/rendicott/marble/internal/agentproc"
	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/tools"
)

func (s *Session) subprocessContextSpec() agentproc.ContextSpec {
	if s == nil {
		return agentproc.ContextSpec{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subprocessContext
}

func (s *Session) setSubprocessContextLocked(spec agentproc.ContextSpec) {
	s.subprocessContext = spec
}

func encodeSubprocessContext(spec agentproc.ContextSpec) string {
	if !spec.Set {
		return ""
	}
	b, _ := json.Marshal(map[string]interface{}{
		"context":   spec.Sources,
		"max_chars": spec.MaxChars,
	})
	return string(b)
}

func decodeSubprocessContext(raw string) agentproc.ContextSpec {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return agentproc.ContextSpec{}
	}
	var m struct {
		Context  []string `json:"context"`
		MaxChars int      `json:"max_chars"`
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return agentproc.ContextSpec{}
	}
	return agentproc.ContextSpec{Sources: m.Context, MaxChars: m.MaxChars, Set: true}
}

func specFromPreset(p *db.AgentPresetRow) agentproc.ContextSpec {
	if p == nil || strings.TrimSpace(p.Context) == "" {
		return agentproc.ContextSpec{}
	}
	var src []string
	if err := json.Unmarshal([]byte(p.Context), &src); err != nil {
		return agentproc.ContextSpec{}
	}
	return agentproc.ContextSpec{Sources: src, MaxChars: p.ContextMaxChars, Set: true}
}

func sessionTurns(s *Session) []agentproc.TurnText {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]agentproc.TurnText, 0, len(s.ui))
	for _, m := range s.ui {
		switch m.Role {
		case "system", "thinking":
			continue
		}
		out = append(out, agentproc.TurnText{Role: m.Role, Content: m.Content})
	}
	return out
}

func (r *Runner) buildSubprocessBlock(s *Session, opts TurnOpts, preset *db.AgentPresetRow, prompt string, readPaths []string) agentproc.ContextBlock {
	call := opts.Context
	sessionSpec := s.subprocessContextSpec()
	fallback := agentproc.DefaultContextSpec()
	if r.Tools != nil && r.Tools.Agents != nil {
		fallback = r.Tools.Agents.Config().GlobalContextSpec()
	}
	if ps := specFromPreset(preset); ps.Set {
		fallback = ps
	}
	spec := agentproc.ResolveContextSpecWithPrompt(call, sessionSpec, fallback, prompt)
	in := agentproc.ContextInputs{
		SessionID: s.ID,
		Turns:     sessionTurns(s),
		ReadPaths: readPaths,
		Prompt:    prompt,
	}
	if containsSrc(spec.Sources, "memory") && r.Tools != nil {
		q := agentproc.MemorySeedQuery(prompt, in.Turns)
		in.MemoryHits = r.Tools.MemoryHits(q, agentproc.MemoryTopK)
	}
	return agentproc.Assemble(spec, in)
}

func specPtr(s agentproc.ContextSpec) *agentproc.ContextSpec {
	if !s.Set {
		return nil
	}
	cp := s
	return &cp
}

func containsSrc(src []string, name string) bool {
	for _, s := range src {
		if s == name {
			return true
		}
	}
	return false
}

func readPathList(tc *tools.TurnContext) []string {
	if tc == nil || tc.ReadPaths == nil {
		return nil
	}
	var out []string
	for p := range tc.ReadPaths {
		out = append(out, p)
	}
	return out
}
