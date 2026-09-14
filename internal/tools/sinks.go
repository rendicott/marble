package tools

import (
	"context"
	"fmt"
)

func (r *Registry) manageSinks(argsJSON string, tc *TurnContext) (string, error) {
	if r == nil || r.SinksExec == nil {
		return "", fmt.Errorf("sinks not configured")
	}
	sid := ""
	ctx := context.Background()
	if tc != nil {
		sid = tc.SessionID
		if tc.Ctx != nil {
			ctx = tc.Ctx
		}
	}
	return r.SinksExec(ctx, argsJSON, sid)
}
