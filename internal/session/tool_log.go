package session

import (
	"encoding/json"
	"time"

	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/tools"
)

// observeTool runs one tool and appends a tool_call_log row.
// The returned string is what the model sees. Logging cannot fail the turn.
func (r *Runner) observeTool(s *Session, name, callID, args string, tc *tools.TurnContext) string {
	started := time.Now()
	obs := r.Tools.ExecuteObserved(name, args, tc)
	r.recordToolCall(s, name, callID, args, obs, time.Since(started).Milliseconds())
	return obs.Text
}

// recordToolCall appends one tool_call_log row for a call the turn just ran.
// The arguments JSON is stored verbatim. A tool error is ok=0 with the error
// text, and still keeps the arguments and latency. Insert failures are ignored
// so a logging problem cannot fail the turn.
func (r *Runner) recordToolCall(s *Session, name, callID, args string, obs tools.ObservedTool, latencyMS int64) {
	if r == nil || r.Reg == nil || s == nil {
		return
	}
	database := r.Reg.DB()
	if database == nil {
		return
	}
	if latencyMS < 0 {
		latencyMS = 0
	}
	row := db.ToolCallLog{
		SessionID:   s.ID,
		TS:          time.Now().UTC().Format(time.RFC3339),
		ToolName:    name,
		ToolCallID:  callID,
		ArgsJSON:    args,
		OK:          1,
		LatencyMS:   latencyMS,
		ResultChars: len(obs.Text),
	}
	if obs.Err != nil {
		row.OK = 0
		row.Error = obs.Err.Error()
	}
	if obs.Summary != nil {
		if b, err := json.Marshal(obs.Summary); err == nil {
			row.ResultSummaryJSON = string(b)
		}
		hits := obs.Summary.Hits
		row.HitCount = &hits
	}
	_ = database.InsertToolCallLog(row)
}
