package tools

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	webFetchDefaultTimeout = 25 * time.Second
	webFetchDefaultMaxBody = 2 << 20 // 2 MiB download
	webFetchMaxRedirects   = 5
)

type webFetchArgs struct {
	URL      string `json:"url"`
	MaxBytes int    `json:"max_bytes"`
}

func (r *Registry) webFetch(argsJSON string, tc *TurnContext) (string, error) {
	var a webFetchArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	rawURL := strings.TrimSpace(a.URL)
	if rawURL == "" {
		return "", fmt.Errorf("url is required")
	}
	maxBody := a.MaxBytes
	if maxBody <= 0 {
		maxBody = webFetchDefaultMaxBody
	}
	if maxBody > 8<<20 {
		maxBody = 8 << 20
	}

	parent := context.Background()
	if tc != nil && tc.Ctx != nil {
		parent = tc.Ctx
	}
	ctx, cancel := context.WithTimeout(parent, webFetchDefaultTimeout)
	defer cancel()

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if err := validateFetchURL(u); err != nil {
		return "", err
	}

	client := &http.Client{
		Timeout: webFetchDefaultTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= webFetchMaxRedirects {
				return fmt.Errorf("stopped after %d redirects", webFetchMaxRedirects)
			}
			if err := validateFetchURL(req.URL); err != nil {
				return err
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Marble-web_fetch/1.0 (+local-agent; ADR-0012)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json,text/plain,text/markdown;q=0.9,*/*;q=0.5")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	finalURL := resp.Request.URL.String()
	if err := validateFetchURL(resp.Request.URL); err != nil {
		return "", fmt.Errorf("final url blocked: %w", err)
	}

	limited := io.LimitReader(resp.Body, int64(maxBody)+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	truncatedDL := false
	if len(body) > maxBody {
		body = body[:maxBody]
		truncatedDL = true
	}

	ct := resp.Header.Get("Content-Type")
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if ct == "" {
		ct = http.DetectContentType(body)
	}

	format, payload := formatFetchBody(ct, body)
	// tool result clamp happens in Execute; still note oversized payload here
	var b strings.Builder
	fmt.Fprintf(&b, "url: %s\n", rawURL)
	fmt.Fprintf(&b, "final_url: %s\n", finalURL)
	fmt.Fprintf(&b, "status: %d\n", resp.StatusCode)
	fmt.Fprintf(&b, "content_type: %s\n", ct)
	fmt.Fprintf(&b, "format: %s\n", format)
	fmt.Fprintf(&b, "bytes_read: %d", len(body))
	if truncatedDL {
		fmt.Fprintf(&b, " (download truncated at %d)", maxBody)
	}
	b.WriteByte('\n')
	b.WriteString("---\n")
	b.WriteString(payload)
	if !strings.HasSuffix(payload, "\n") {
		b.WriteByte('\n')
	}
	// Non-2xx still returns the body (status line above) so the model can react.
	return b.String(), nil
}

func formatFetchBody(contentType string, body []byte) (format, payload string) {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "json") || strings.HasSuffix(ct, "+json"):
		// Q4: JSON raw
		s := string(body)
		if !utf8.ValidString(s) {
			s = strings.ToValidUTF8(s, "�")
		}
		return "raw", s
	case strings.HasPrefix(ct, "text/plain"), strings.Contains(ct, "markdown"):
		s := string(body)
		if !utf8.ValidString(s) {
			s = strings.ToValidUTF8(s, "�")
		}
		// plain text as-is (already fine for model); light markdown leave
		return "markdown", s
	case strings.Contains(ct, "html"), strings.HasPrefix(ct, "application/xhtml"):
		s := string(body)
		if !utf8.ValidString(s) {
			s = strings.ToValidUTF8(s, "�")
		}
		return "markdown", htmlToMarkdown(s)
	default:
		// try HTML sniff
		if looksLikeHTML(body) {
			s := string(body)
			if !utf8.ValidString(s) {
				s = strings.ToValidUTF8(s, "�")
			}
			return "markdown", htmlToMarkdown(s)
		}
		if len(body) > 0 && isMostlyText(body) {
			s := string(body)
			if !utf8.ValidString(s) {
				s = strings.ToValidUTF8(s, "�")
			}
			return "markdown", s
		}
		return "error", fmt.Sprintf("(binary or unsupported content-type %q; %d bytes not shown)", contentType, len(body))
	}
}

func looksLikeHTML(b []byte) bool {
	s := strings.ToLower(string(b[:min(512, len(b))]))
	return strings.Contains(s, "<html") || strings.Contains(s, "<!doctype html") || strings.Contains(s, "<body")
}

func isMostlyText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	sample := b
	if len(sample) > 1024 {
		sample = sample[:1024]
	}
	non := 0
	for _, c := range sample {
		if c == 0 {
			return false
		}
		if c < 9 || (c > 13 && c < 32) {
			non++
		}
	}
	return non*20 < len(sample) // <5% control
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// validateFetchURL enforces http(s), blocks cloud metadata; allows LAN (ADR-0012 Q5).
func validateFetchURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("nil url")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("only http/https allowed (got %q)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("url missing host")
	}
	low := strings.ToLower(host)
	if isBlockedMetadataHost(low) {
		return fmt.Errorf("blocked metadata host %q", host)
	}
	// Resolve IPs when possible; allow private/LAN, block metadata ranges
	ips, err := net.LookupIP(host)
	if err != nil {
		// If DNS fails, still allow the request to proceed only for non-metadata hostnames;
		// dial will fail later. Block obvious IP-literal issues below.
		if ip := net.ParseIP(host); ip != nil {
			if isBlockedMetadataIP(ip) {
				return fmt.Errorf("blocked metadata address %s", ip)
			}
		}
		return nil
	}
	if len(ips) == 0 {
		return fmt.Errorf("host %q resolved to no addresses", host)
	}
	for _, ip := range ips {
		if isBlockedMetadataIP(ip) {
			return fmt.Errorf("blocked metadata address %s for host %q", ip, host)
		}
	}
	return nil
}

func isBlockedMetadataHost(host string) bool {
	switch host {
	case "metadata.google.internal",
		"metadata.goog",
		"metadata",
		"instance-data.ec2.internal":
		return true
	}
	if strings.HasSuffix(host, ".metadata.google.internal") {
		return true
	}
	return false
}

func isBlockedMetadataIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	// AWS/GCP/Azure classic metadata
	if ip4 := ip.To4(); ip4 != nil {
		// 169.254.169.254 and link-local metadata-style
		if ip4[0] == 169 && ip4[1] == 254 && ip4[2] == 169 && ip4[3] == 254 {
			return true
		}
		// Alibaba cloud metadata
		if ip4[0] == 100 && ip4[1] == 100 && ip4[2] == 100 && ip4[3] == 200 {
			return true
		}
	}
	// AWS IPv6 metadata fd00:ec2::254
	if ip.IsLinkLocalUnicast() {
		// block link-local entirely (includes fe80:: and 169.254.0.0/16)
		// Note: ADR allows LAN (RFC1918) but not link-local metadata plane
		if ip4 := ip.To4(); ip4 != nil && ip4[0] == 169 && ip4[1] == 254 {
			return true
		}
		if ip.To4() == nil {
			return true // IPv6 link-local
		}
	}
	return false
}

// --- HTML → markdown (lightweight, no extra deps) ---

var (
	reTags  = regexp.MustCompile(`(?is)<[^>]+>`)
	reWS    = regexp.MustCompile(`[ \t]+\n`)
	reBlank = regexp.MustCompile(`\n{3,}`)
)

func stripHTMLBlocks(s string, tag string) string {
	// Case-insensitive strip of <tag>...</tag> without backrefs (Go RE2).
	re := regexp.MustCompile(`(?is)<` + tag + `\b[^>]*>.*?</` + tag + `\s*>`)
	return re.ReplaceAllString(s, "")
}

func htmlToMarkdown(html string) string {
	s := html
	if i := strings.Index(strings.ToLower(s), "<body"); i >= 0 {
		s = s[i:]
	}
	for _, tag := range []string{"script", "style", "noscript", "svg", "iframe"} {
		s = stripHTMLBlocks(s, tag)
	}

	// structural replacements before stripping tags
	type pair struct {
		re *regexp.Regexp
		to string
	}
	repl := []pair{
		{regexp.MustCompile(`(?i)<br\s*/?>`), "\n"},
		{regexp.MustCompile(`(?i)</p>`), "\n\n"},
		{regexp.MustCompile(`(?i)</div>`), "\n"},
		{regexp.MustCompile(`(?i)</tr>`), "\n"},
		{regexp.MustCompile(`(?i)</h1>`), "\n\n"},
		{regexp.MustCompile(`(?i)</h2>`), "\n\n"},
		{regexp.MustCompile(`(?i)</h3>`), "\n\n"},
		{regexp.MustCompile(`(?i)</h4>`), "\n\n"},
		{regexp.MustCompile(`(?i)</li>`), "\n"},
		{regexp.MustCompile(`(?i)<li[^>]*>`), "- "},
		{regexp.MustCompile(`(?i)<h1[^>]*>`), "\n# "},
		{regexp.MustCompile(`(?i)<h2[^>]*>`), "\n## "},
		{regexp.MustCompile(`(?i)<h3[^>]*>`), "\n### "},
		{regexp.MustCompile(`(?i)<h4[^>]*>`), "\n#### "},
		{regexp.MustCompile(`(?i)<hr\s*/?>`), "\n\n---\n\n"},
		{regexp.MustCompile(`(?i)</ul>`), "\n"},
		{regexp.MustCompile(`(?i)</ol>`), "\n"},
		{regexp.MustCompile(`(?is)<pre[^>]*>(.*?)</pre>`), "\n```\n$1\n```\n"},
		{regexp.MustCompile(`(?is)<code[^>]*>(.*?)</code>`), "`$1`"},
		{regexp.MustCompile(`(?is)<a[^>]+href=["']([^"']+)["'][^>]*>(.*?)</a>`), "[$2]($1)"},
		{regexp.MustCompile(`(?is)<strong[^>]*>(.*?)</strong>`), "**$1**"},
		{regexp.MustCompile(`(?is)<b[^>]*>(.*?)</b>`), "**$1**"},
		{regexp.MustCompile(`(?is)<em[^>]*>(.*?)</em>`), "*$1*"},
		{regexp.MustCompile(`(?is)<i[^>]*>(.*?)</i>`), "*$1*"},
	}
	for _, r := range repl {
		s = r.re.ReplaceAllString(s, r.to)
	}
	s = reTags.ReplaceAllString(s, "")
	s = htmlUnescape(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = reWS.ReplaceAllString(s, "\n")
	s = reBlank.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func htmlUnescape(s string) string {
	replacer := strings.NewReplacer(
		"&nbsp;", " ",
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", "\"",
		"&#39;", "'",
		"&apos;", "'",
	)
	return replacer.Replace(s)
}
