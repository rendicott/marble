package tools

// ResultSummary is structured telemetry a tool may attach to one call.
// Hits and Empty are the counts a metrics query can use without parsing Data.
// Data holds tool-specific fields. A nil *ResultSummary means the log records
// only result size and latency.
type ResultSummary struct {
	Hits  int                    `json:"hits"`
	Empty bool                   `json:"empty"`
	Data  map[string]interface{} `json:"data,omitempty"`
}

// Summarizer is an optional interface a tool may implement.
// Returning a nil summary records only result size and latency.
type Summarizer interface {
	Summarize(argsJSON, result string) *ResultSummary
}

// capturedSummary returns diagnostics collected while the tool ran.
// It does not re-read files or re-run the tool.
type capturedSummary struct {
	summary *ResultSummary
}

func (c capturedSummary) Summarize(argsJSON, result string) *ResultSummary {
	return c.summary
}

// ObservedTool is the model-facing result of one execution.
// Summary is nil when the tool does not implement Summarizer.
// Err is set when the tool failed. Text is still the string the model sees.
type ObservedTool struct {
	Text    string
	Summary *ResultSummary
	Err     error
}
