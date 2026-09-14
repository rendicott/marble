package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/rendicott/marble/internal/db"
)

const (
	attachURLDefaultMax   = 8 << 20 // 8 MiB (ADR-0029 Q10)
	attachURLMaxRedirects = 5       // Q3
	attachURLTimeout      = 25 * time.Second
	attachURLBatchMax     = 4 // Q6
	attachURLTurnCallMax  = 8
)

// AttachURLInput is the shared tool/HTTP payload (ADR-0029).
type AttachURLInput struct {
	URL        string   `json:"url"`
	URLs       []string `json:"urls"`
	Name       string   `json:"name"`
	Alt        string   `json:"alt"`
	SourcePage string   `json:"source_page"`
	Credit     string   `json:"credit"`
	License    string   `json:"license"`
}

// AttachURLResult is one fetch+stage outcome (tool + HTTP).
type AttachURLResult struct {
	OK           bool   `json:"ok"`
	Error        string `json:"error,omitempty"`
	Status       int    `json:"status,omitempty"`
	URL          string `json:"url,omitempty"`
	FinalURL     string `json:"final_url,omitempty"`
	AttachmentID string `json:"attachment_id,omitempty"`
	MIME         string `json:"mime,omitempty"`
	Kind         string `json:"kind,omitempty"`
	Bytes        int    `json:"bytes,omitempty"`
	Name         string `json:"name,omitempty"`
	SourceURL    string `json:"source_url,omitempty"`
	Note         string `json:"note,omitempty"`
}

func (r *Registry) attachFromURL(argsJSON string, tc *TurnContext) (string, error) {
	if tc == nil || strings.TrimSpace(tc.SessionID) == "" {
		return "", fmt.Errorf("no current session")
	}
	var a AttachURLInput
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if tc.AttachFromURLCalls >= attachURLTurnCallMax {
		return mustJSON(AttachURLResult{OK: false, Error: "rate_limited", Note: "too many attach_from_url calls this turn"}), nil
	}
	tc.AttachFromURLCalls++
	parent := context.Background()
	if tc.Ctx != nil {
		parent = tc.Ctx
	}
	ctx, cancel := context.WithTimeout(parent, attachURLTimeout)
	defer cancel()
	results, err := r.AttachURLs(ctx, tc.SessionID, a, tc.OnChatAttachment)
	if err != nil {
		return "", err
	}
	return formatAttachURLResults(results), nil
}

// AttachURLs fetches and stages image URL(s). Shared by the agent tool and HTTP API.
func (r *Registry) AttachURLs(ctx context.Context, sessionID string, a AttachURLInput, onChip func(Attachment)) ([]AttachURLResult, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("session id required")
	}
	targets, err := collectAttachURLs(a.URL, a.URLs)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	results := make([]AttachURLResult, 0, len(targets))
	for _, raw := range targets {
		name := ""
		if len(targets) == 1 {
			name = a.Name
		}
		results = append(results, r.stageOneURL(ctx, sessionID, raw, name, a, onChip))
	}
	return results, nil
}

func formatAttachURLResults(results []AttachURLResult) string {
	if len(results) == 1 {
		return mustJSON(results[0])
	}
	ids := make([]string, 0, len(results))
	okAny := false
	for _, res := range results {
		if res.OK && res.AttachmentID != "" {
			ids = append(ids, res.AttachmentID)
			okAny = true
		}
	}
	return mustJSON(map[string]interface{}{
		"ok":             okAny,
		"results":        results,
		"attachment_ids": ids,
		"note":           "chat attachments (durable); cite attachment_id when referring to an image",
	})
}

func collectAttachURLs(single string, many []string) ([]string, error) {
	single = strings.TrimSpace(single)
	var urls []string
	for _, u := range many {
		u = strings.TrimSpace(u)
		if u != "" {
			urls = append(urls, u)
		}
	}
	if single != "" && len(urls) > 0 {
		return nil, fmt.Errorf("pass url or urls, not both")
	}
	if single != "" {
		return []string{single}, nil
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("url or urls is required")
	}
	if len(urls) > attachURLBatchMax {
		return nil, fmt.Errorf("urls: max %d in one call", attachURLBatchMax)
	}
	return urls, nil
}

func (r *Registry) stageOneURL(ctx context.Context, sessionID, rawURL, name string, meta AttachURLInput, onChip func(Attachment)) AttachURLResult {
	out := AttachURLResult{URL: rawURL, SourceURL: rawURL}
	data, finalURL, _, ferr := fetchImageBytes(ctx, rawURL, attachURLDefaultMax)
	if ferr != nil {
		out.Error = ferr.Code
		out.Status = ferr.Status
		out.OK = false
		if ferr.FinalURL != "" {
			out.FinalURL = ferr.FinalURL
		}
		return out
	}
	out.FinalURL = finalURL
	if name == "" {
		name = nameFromImageURL(finalURL)
	}
	name = strings.TrimSpace(name)
	if looksLikeSVG(data, name) {
		out.OK = false
		out.Error = "unsupported_type"
		out.Note = "SVG is rejected on attach_from_url"
		return out
	}
	mime, kind, err := db.SniffAttachment(name, data)
	if err != nil {
		out.OK = false
		out.Error = "unsupported_type"
		out.Note = err.Error()
		return out
	}
	if kind != "image" || strings.Contains(strings.ToLower(mime), "svg") {
		out.OK = false
		out.Error = "unsupported_type"
		out.Note = "images only (png/jpeg/webp/gif); SVG rejected"
		return out
	}
	if r.StageChatAttachment == nil {
		out.OK = false
		out.Error = "fetch_failed"
		out.Note = "attachment store not configured"
		return out
	}
	prov := attachProvenance(rawURL, finalURL, mime, name, meta)
	id, gotMIME, gotKind, err := r.StageChatAttachment(sessionID, name, data, prov)
	if err != nil {
		out.OK = false
		out.Error = "fetch_failed"
		out.Note = err.Error()
		return out
	}
	if onChip != nil {
		onChip(Attachment{
			Path:      id,
			Name:      name,
			Inline:    true,
			Mime:      gotMIME,
			Size:      int64(len(data)),
			SourceURL: finalURL,
			Alt:       strings.TrimSpace(meta.Alt),
			Credit:    strings.TrimSpace(meta.Credit),
		})
	}
	out.OK = true
	out.AttachmentID = id
	out.MIME = gotMIME
	out.Kind = gotKind
	out.Bytes = len(data)
	out.Name = name
	out.SourceURL = rawURL
	out.Note = "chat attachment (durable); cite attachment_id in reply if showing the image"
	return out
}

type fetchErr struct {
	Code     string
	Status   int
	FinalURL string
}

func fetchImageBytes(ctx context.Context, rawURL string, maxBytes int) (data []byte, finalURL, contentType string, ferr *fetchErr) {
	if maxBytes <= 0 || maxBytes > attachURLDefaultMax {
		maxBytes = attachURLDefaultMax
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", "", &fetchErr{Code: "url_blocked"}
	}
	if err := validateFetchURL(u); err != nil {
		return nil, "", "", &fetchErr{Code: "url_blocked"}
	}
	client := &http.Client{
		Timeout: attachURLTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= attachURLMaxRedirects {
				return fmt.Errorf("stopped after %d redirects", attachURLMaxRedirects)
			}
			if err := validateFetchURL(req.URL); err != nil {
				return err
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", "", &fetchErr{Code: "fetch_failed"}
	}
	req.Header.Set("User-Agent", "Marble-attach_from_url/1.0 (+local-agent; ADR-0029)")
	req.Header.Set("Accept", "image/png,image/jpeg,image/webp,image/gif;q=0.9,*/*;q=0.5")

	resp, err := client.Do(req)
	if err != nil {
		code := "fetch_failed"
		if strings.Contains(strings.ToLower(err.Error()), "blocked") || strings.Contains(err.Error(), "metadata") {
			code = "url_blocked"
		}
		return nil, "", "", &fetchErr{Code: code}
	}
	defer resp.Body.Close()

	final := ""
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
		if err := validateFetchURL(resp.Request.URL); err != nil {
			return nil, final, "", &fetchErr{Code: "url_blocked", FinalURL: final}
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, final, "", &fetchErr{Code: "http_status", Status: resp.StatusCode, FinalURL: final}
	}
	if cl := strings.TrimSpace(resp.Header.Get("Content-Length")); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil && n > int64(maxBytes) {
			return nil, final, "", &fetchErr{Code: "too_large", FinalURL: final}
		}
	}
	limited := io.LimitReader(resp.Body, int64(maxBytes)+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, final, "", &fetchErr{Code: "fetch_failed", FinalURL: final}
	}
	if len(body) > maxBytes {
		return nil, final, "", &fetchErr{Code: "too_large", FinalURL: final}
	}
	ct := resp.Header.Get("Content-Type")
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return body, final, ct, nil
}

func looksLikeSVG(data []byte, name string) bool {
	if strings.EqualFold(path.Ext(name), ".svg") {
		return true
	}
	head := strings.ToLower(string(data[:min(len(data), 512)]))
	if strings.Contains(head, "<svg") || strings.Contains(head, "image/svg") {
		return true
	}
	return false
}

func nameFromImageURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "image"
	}
	base := path.Base(u.Path)
	base = strings.TrimSpace(base)
	if base == "" || base == "." || base == "/" {
		return "image"
	}
	return base
}

func attachProvenance(sourceURL, finalURL, mime, name string, meta AttachURLInput) string {
	m := map[string]string{
		"source_url":   sourceURL,
		"final_url":    finalURL,
		"fetched_at":   time.Now().UTC().Format(time.RFC3339),
		"content_type": mime,
	}
	if name != "" {
		m["name"] = name
	}
	if s := strings.TrimSpace(meta.Alt); s != "" {
		m["alt"] = s
	}
	if s := strings.TrimSpace(meta.Credit); s != "" {
		m["credit"] = s
	}
	if s := strings.TrimSpace(meta.License); s != "" {
		m["license"] = s
	}
	if s := strings.TrimSpace(meta.SourcePage); s != "" {
		m["source_page"] = s
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}
