package tools

import (
	"fmt"
	"strings"

	"github.com/rendicott/marble/internal/agentproc"
)

func (r *Registry) sessionSetSubprocessContext(argsJSON string, tc *TurnContext) (string, error) {
	if r.SetSessionSubprocessContext == nil {
		return "", fmt.Errorf("session_set_subprocess_context not configured")
	}
	if tc == nil || strings.TrimSpace(tc.SessionID) == "" {
		return "", fmt.Errorf("no current session")
	}
	var a struct {
		Context  interface{} `json:"context"`
		MaxChars int         `json:"max_chars"`
		Clear    bool        `json:"clear"`
	}
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	spec, err := agentproc.ParseContextArg(a.Context)
	if err != nil {
		return "", err
	}
	if a.MaxChars > 0 {
		spec.MaxChars = a.MaxChars
		spec.Set = true
	}
	if a.Clear {
		spec = agentproc.ContextSpec{Set: false}
	}
	out, err := r.SetSessionSubprocessContext(tc.SessionID, spec, a.Clear)
	if err != nil {
		return "", err
	}
	return mustJSON(out), nil
}
