package model

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ContextLimitError is a provider HTTP error that names a context window.
// Chat callers still see the same "model HTTP <status>: …" text as before;
// errors.As distinguishes this from a generic 400.
type ContextLimitError struct {
	Status     int
	Limit      int // zero if the body did not name the window
	Messages   int // zero if the body did not name a message-token count
	Completion int // zero if the body did not name a completion reservation
	Raw        string
}

func (e *ContextLimitError) Error() string {
	if e == nil {
		return "model HTTP 400: context length exceeded"
	}
	status := e.Status
	if status == 0 {
		status = 400
	}
	return fmt.Sprintf("model HTTP %d: %s", status, e.Raw)
}

var (
	reContextLimit = regexp.MustCompile(`(?i)maximum context length is (\d+)`)
	reContextMsgs  = regexp.MustCompile(`(?i)(\d+) in the messages`)
	reContextComp  = regexp.MustCompile(`(?i)(\d+) in the completion`)
)

// ParseContextLimit returns a typed error when body is a context-length rejection.
// A temperature or reasoning 400 does not match. status is echoed into Error().
func ParseContextLimit(status int, body string) *ContextLimitError {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	text := body
	typeHit := false
	var probe struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Error   *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &probe) == nil {
		if probe.Error != nil {
			if probe.Error.Message != "" {
				text += "\n" + probe.Error.Message
			}
			if strings.EqualFold(strings.TrimSpace(probe.Error.Type), "context_length_exceeded") {
				typeHit = true
			}
		}
		if probe.Message != "" {
			text += "\n" + probe.Message
		}
		if strings.EqualFold(strings.TrimSpace(probe.Type), "context_length_exceeded") {
			typeHit = true
		}
	}
	cle := &ContextLimitError{Status: status, Raw: body}
	if m := reContextLimit.FindStringSubmatch(text); len(m) == 2 {
		cle.Limit, _ = strconv.Atoi(m[1])
	}
	if m := reContextMsgs.FindStringSubmatch(text); len(m) == 2 {
		cle.Messages, _ = strconv.Atoi(m[1])
	}
	if m := reContextComp.FindStringSubmatch(text); len(m) == 2 {
		cle.Completion, _ = strconv.Atoi(m[1])
	}
	if !typeHit && cle.Limit == 0 && cle.Messages == 0 && cle.Completion == 0 {
		return nil
	}
	return cle
}
