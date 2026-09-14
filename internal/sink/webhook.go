package sink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"
)

const (
	maxBodyBytes     = 32 * 1024
	maxHeaderRuneCap = 512
	maxRespLogBytes  = 240
)

var tmplFuncs = template.FuncMap{
	"json": func(v interface{}) string {
		b, err := json.Marshal(v)
		if err != nil {
			return "null"
		}
		return string(b)
	},
	"truncate": func(s string, n int) string {
		return truncateRunes(s, n)
	},
	"hasPrefix": strings.HasPrefix,
}

func parseTemplate(name, body string) (*template.Template, error) {
	return template.New(name).Funcs(tmplFuncs).Option("missingkey=zero").Parse(body)
}

type webhookPlan struct {
	URL      string
	Method   string
	Headers  map[string]string
	Template string
	BodyType string // raw template vs empty
}

func execTemplate(name, tmpl string, data any) (string, error) {
	t, err := parseTemplate(name, tmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func deliverHTTP(ctx context.Context, client *http.Client, plan webhookPlan, data view, secret string) (int, error) {
	if client == nil {
		client = http.DefaultClient
	}
	method := plan.Method
	if method == "" {
		method = http.MethodPost
	}
	body, err := execTemplate("body", plan.Template, data)
	if err != nil {
		return 0, fmt.Errorf("template: %w", err)
	}
	if utf8.RuneCountInString(body) > maxBodyBytes {
		// byte cap: keep request bounded even if runes are 1:1 for ascii
		if len(body) > maxBodyBytes {
			body = string([]rune(body)[:maxBodyBytes/4]) + "…"
			if len(body) > maxBodyBytes {
				body = body[:maxBodyBytes]
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, plan.URL, strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	for k, raw := range plan.Headers {
		val := raw
		if strings.Contains(raw, "{{") {
			rendered, err := execTemplate("hdr-"+k, raw, data)
			if err != nil {
				return 0, fmt.Errorf("header %s: %w", k, err)
			}
			val = rendered
		}
		val = strings.ReplaceAll(val, "\r", " ")
		val = strings.ReplaceAll(val, "\n", " ")
		if k != "Authorization" && !strings.EqualFold(k, "X-Orb-Return-Url") {
			val = maxHeaderRunes(val, maxHeaderRuneCap)
		}
		if strings.TrimSpace(val) == "" {
			continue
		}
		// Skip junk schemes so a bad template cannot 400 the whole publish.
		if strings.EqualFold(k, "X-Orb-Return-Url") && !ValidOrbReturnURL(val) {
			continue
		}
		req.Header.Set(k, val)
	}
	if secret != "" && req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	if req.Header.Get("Idempotency-Key") == "" && data.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", data.IdempotencyKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	lim := io.LimitReader(resp.Body, maxRespLogBytes+1)
	rb, _ := io.ReadAll(lim)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, nil
	}
	snippet := strings.TrimSpace(string(rb))
	if len(snippet) > maxRespLogBytes {
		snippet = snippet[:maxRespLogBytes] + "…"
	}
	return resp.StatusCode, fmt.Errorf("http %d %s", resp.StatusCode, snippet)
}

func retryable(status int, err error) bool {
	if err == nil {
		return false
	}
	if status == 429 || status >= 500 {
		return true
	}
	if status == 0 {
		return true // network
	}
	return false
}

func backoff(attempt int) time.Duration {
	// 200ms, 800ms, 2s with jitter from caller
	d := 200 * time.Millisecond
	for i := 0; i < attempt; i++ {
		d *= 4
	}
	if d > 3*time.Second {
		d = 3 * time.Second
	}
	return d
}
