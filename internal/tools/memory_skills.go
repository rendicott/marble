package tools

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/rendicott/marble/internal/memsearch"
)

type memSearchArgs struct {
	Query      string   `json:"query"`
	Since      string   `json:"since"`
	Until      string   `json:"until"`
	Tags       []string `json:"tags"`
	Scope      string   `json:"scope"` // all|session|daily|knowledge
	MaxResults int      `json:"max_results"`
}

// MemoryHits is a ranked excerpt list for subprocess context (ADR-0031).
func (r *Registry) MemoryHits(query string, max int) []string {
	if r == nil || strings.TrimSpace(r.Memory) == "" || strings.TrimSpace(query) == "" {
		return nil
	}
	if max <= 0 {
		max = agentprocMemoryTopK()
	}
	out, err := r.memorySearchHits(query, "all", max)
	if err != nil || len(out) == 0 {
		return nil
	}
	return out
}

func agentprocMemoryTopK() int { return 5 }

func (r *Registry) memorySearchHits(query, scope string, max int) ([]string, error) {
	a := memSearchArgs{Query: query, Scope: scope, MaxResults: max}
	s, _, err := r.runMemorySearch(a)
	if err != nil {
		return nil, err
	}
	if s == "no matches" || s == "" {
		return nil, nil
	}
	return strings.Split(s, "\n"), nil
}

func (r *Registry) memorySearch(argsJSON string) (string, *ResultSummary, error) {
	var a memSearchArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(a.Query) == "" {
		return "", nil, fmt.Errorf("query is required")
	}
	out, sum, err := r.runMemorySearch(a)
	if err == nil && sum != nil && sum.Data["degraded"] == true {
		// Say so in the text as well as the log row: an unranked answer, or
		// "no matches" from a walk that may have stopped early, must not
		// read like a ranked one.
		out = memSearchDegradedNote + "\n" + out
	}
	return out, sum, err
}

const memSearchDegradedNote = "[degraded: memory index unavailable; unranked linear scan, may miss documents late in the tree]"

func (r *Registry) runMemorySearch(a memSearchArgs) (string, *ResultSummary, error) {
	started := time.Now()
	scope := strings.ToLower(strings.TrimSpace(a.Scope))
	if scope == "" {
		scope = "all"
	}
	a.MaxResults = memsearch.ClampMaxResults(a.MaxResults)
	tags := make([]string, len(a.Tags))
	copy(tags, a.Tags)
	q := memsearch.BuildQuery(a.Query)
	dropped := q.Dropped
	if dropped == nil {
		dropped = []string{}
	}
	terms := q.Terms
	if terms == nil {
		terms = []string{}
	}
	var (
		hits     []string
		docs     = []map[string]interface{}{}
		scanned  int
		indexed  int
		rejected int
	)
	data := map[string]interface{}{
		"query":             a.Query,
		"scope":             scope,
		"max_results":       a.MaxResults,
		"tags":              tags,
		"since":             a.Since,
		"until":             a.Until,
		"engine":            "fts5",
		"match_expr":        q.Expr,
		"phrase_expr":       q.Phrase,
		"terms":             terms,
		"stopwords_dropped": dropped,
		"reindexed":         false,
		"index_age_sec":     int64(-1),
		"degraded":          false,
	}
	finish := func() (string, *ResultSummary, error) {
		ms := time.Since(started).Milliseconds()
		if ms < 0 {
			ms = 0
		}
		empty := len(hits) == 0
		data["docs"] = docs
		data["hits"] = len(hits)
		data["hit_count"] = len(hits)
		data["empty"] = empty
		data["search_ms"] = ms
		data["docs_scanned"] = scanned
		data["docs_indexed"] = indexed
		data["docs_rejected_tags"] = rejected
		sum := &ResultSummary{Hits: len(hits), Empty: empty, Data: data}
		if empty {
			return "no matches", sum, nil
		}
		return strings.Join(hits, "\n"), sum, nil
	}
	if len(q.Terms) == 0 {
		// Only stopwords or single characters. Matching on those is what made
		// every document a hit; say nothing was searched instead.
		data["note"] = "no searchable terms after dropping stopwords and single characters; nothing was searched"
		return finish()
	}

	ix, err := r.memoryIndex()
	var res memsearch.Result
	if err == nil {
		var reindexed bool
		var rep memsearch.ReindexReport
		reindexed, rep, err = ix.MaybeReindex()
		if err == nil {
			data["reindexed"] = reindexed
			if reindexed && rep.Partial {
				data["reindex_partial"] = true
			}
			res, err = ix.Search(q, memsearch.Options{Scope: scope, Max: a.MaxResults, Tags: a.Tags})
		}
		if age := ix.Age(); age >= 0 {
			data["index_age_sec"] = int64(age.Seconds())
		}
	}
	if err != nil {
		log.Printf("memory_search_degraded reason=%q", err.Error())
		data["engine"] = "linear"
		data["degraded"] = true
		data["degraded_reason"] = err.Error()
		hits, docs, scanned, rejected = r.memorySearchLinear(a, scope)
		return finish()
	}
	scanned, indexed, rejected = res.DocsInScope, res.DocsIndexed, res.Rejected
	for _, h := range res.Hits {
		hits = append(hits, fmt.Sprintf("%s: …%s…", h.Path, h.Excerpt))
		docs = append(docs, map[string]interface{}{
			"path":           h.Path,
			"kind":           h.Kind,
			"score":          h.Score,
			"title_hit":      h.TitleHit,
			"matched_phrase": h.MatchedPhrase,
			"matched_tokens": h.MatchedTerms,
			"doc_bytes":      h.SizeBytes,
		})
	}
	return finish()
}

// memoryIndex returns the search index, opening $MEMORY/memory-index.db on
// first use when main did not set MemIndex. A failed open is retried on the
// next search rather than cached.
func (r *Registry) memoryIndex() (*memsearch.Index, error) {
	r.memIndexMu.Lock()
	defer r.memIndexMu.Unlock()
	if r.MemIndex != nil {
		return r.MemIndex, nil
	}
	if strings.TrimSpace(r.Memory) == "" {
		return nil, fmt.Errorf("no memory root")
	}
	ix, err := memsearch.Open(r.Memory)
	if err != nil {
		return nil, err
	}
	if r.MemIndexMaxAge > 0 {
		ix.MaxAge = r.MemIndexMaxAge
	}
	r.MemIndex = ix
	return ix, nil
}

// MemoryIndex is the search index for the admin endpoint (nil when it cannot
// be opened; err says why).
func (r *Registry) MemoryIndex() (*memsearch.Index, error) {
	return r.memoryIndex()
}

// memorySearchLinear is the pre-v0.4.38 search, kept only as the degraded
// fallback when the index cannot be used. It is unranked and stops at
// max_results, so it can miss documents late in the tree; callers mark the
// result degraded.
func (r *Registry) memorySearchLinear(a memSearchArgs, scope string) (hits []string, docs []map[string]interface{}, scanned, rejected int) {
	docs = []map[string]interface{}{}
	q := strings.ToLower(a.Query)
	searchDir := func(sub string, limit int) {
		root := filepath.Join(r.Memory, sub)
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if !strings.HasSuffix(d.Name(), ".md") {
				return nil
			}
			if len(hits) >= limit {
				return fmt.Errorf("done")
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			scanned++
			text := string(data)
			low := strings.ToLower(text)
			matchedPhrase := strings.Contains(low, q)
			if !matchedPhrase {
				// keyword tokens
				ok := false
				for _, tok := range strings.Fields(q) {
					if strings.Contains(low, tok) {
						ok = true
						break
					}
				}
				if !ok {
					return nil
				}
			}
			// tags filter
			if len(a.Tags) > 0 {
				fm := text
				if i := strings.Index(text, "---"); i >= 0 {
					if j := strings.Index(text[i+3:], "---"); j >= 0 {
						fm = text[i : i+3+j]
					}
				}
				for _, tag := range a.Tags {
					if !strings.Contains(strings.ToLower(fm), strings.ToLower(tag)) {
						rejected++
						return nil
					}
				}
			}
			matchedTokens := make([]string, 0)
			for _, tok := range strings.Fields(q) {
				if strings.Contains(low, tok) {
					matchedTokens = append(matchedTokens, tok)
				}
			}
			rel, _ := filepath.Rel(r.Memory, p)
			// excerpt
			idx := strings.Index(low, strings.Fields(q)[0])
			if idx < 0 {
				idx = 0
			}
			start := idx - 40
			if start < 0 {
				start = 0
			}
			end := idx + 80
			if end > len(text) {
				end = len(text)
			}
			ex := strings.ReplaceAll(text[start:end], "\n", " ")
			hits = append(hits, fmt.Sprintf("%s: …%s…", rel, ex))
			docs = append(docs, map[string]interface{}{
				"path":           rel,
				"matched_phrase": matchedPhrase,
				"matched_tokens": matchedTokens,
				"doc_bytes":      len(data),
			})
			return nil
		})
	}
	if scope == "all" || scope == "session" {
		searchDir("session", a.MaxResults)
	}
	if len(hits) < a.MaxResults && (scope == "all" || scope == "daily") {
		searchDir("daily", a.MaxResults)
	}
	if len(hits) < a.MaxResults && (scope == "all" || scope == "knowledge") {
		searchDir("knowledge", a.MaxResults)
	}
	return hits, docs, scanned, rejected
}

type memFetchArgs struct {
	Path      string `json:"path"`
	Topic     string `json:"topic"`
	SessionID string `json:"session_id"`
}

func (r *Registry) memoryFetch(argsJSON string) (string, error) {
	var a memFetchArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	var rel string
	switch {
	case a.Path != "":
		rel = a.Path
	case a.Topic != "":
		rel = filepath.Join("knowledge", slugify(a.Topic)+".md")
	case a.SessionID != "":
		rel = filepath.Join("session", a.SessionID+".md")
	default:
		return "", fmt.Errorf("path, topic, or session_id required")
	}
	abs, err := r.resolveMemory(rel)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

type memWriteArgs struct {
	Topic   string   `json:"topic"`
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
	Mode    string   `json:"mode"`
}

func (r *Registry) memoryWrite(argsJSON string) (string, error) {
	var a memWriteArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Topic) == "" || a.Content == "" {
		return "", fmt.Errorf("topic and content required")
	}
	slug := slugify(a.Topic)
	rel := filepath.Join("knowledge", slug+".md")
	// force under knowledge only
	abs, err := r.resolveMemory(rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	now := time.Now().Format(time.RFC3339)
	tags := strings.Join(a.Tags, ", ")
	body := fmt.Sprintf("---\ntopic: %q\ntags: [%s]\nupdated_at: %s\n---\n\n# %s\n\n%s\n",
		a.Topic, tags, now, a.Topic, a.Content)
	mode := strings.ToLower(a.Mode)
	if mode == "append" {
		if prev, err := os.ReadFile(abs); err == nil {
			body = string(prev) + "\n\n---\n\n" + a.Content + "\n"
		}
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		return "", err
	}
	// A search right after the write should find it, not wait for the
	// reindex age to pass.
	r.memIndexMu.Lock()
	ix := r.MemIndex
	r.memIndexMu.Unlock()
	ix.Invalidate()
	return fmt.Sprintf("wrote knowledge/%s.md", slug), nil
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "note"
	}
	return out
}

func (r *Registry) skillRoots() []string {
	var roots []string
	if r.Memory != "" {
		roots = append(roots, filepath.Join(r.Memory, "skills"))
	}
	if r.Workspace != "" {
		roots = append(roots, filepath.Join(r.Workspace, ".marble", "skills"))
	}
	return roots
}

type skillSearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

func (r *Registry) skillSearch(argsJSON string) (string, error) {
	var a skillSearchArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.MaxResults <= 0 {
		a.MaxResults = 20
	}
	q := strings.ToLower(a.Query)
	var lines []string
	for _, root := range r.skillRoots() {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			skillMD := filepath.Join(root, name, "SKILL.md")
			data, err := os.ReadFile(skillMD)
			desc := ""
			if err == nil {
				desc = firstParagraph(string(data))
			}
			hay := strings.ToLower(name + " " + desc)
			if q != "" && !strings.Contains(hay, q) {
				continue
			}
			rel := skillMD
			if strings.HasPrefix(skillMD, r.Memory) {
				rel, _ = filepath.Rel(r.Memory, skillMD)
				rel = "memory:" + rel
			} else if strings.HasPrefix(skillMD, r.Workspace) {
				rel, _ = filepath.Rel(r.Workspace, skillMD)
				rel = "workspace:" + rel
			}
			lines = append(lines, fmt.Sprintf("%s\t%s\t%s", name, rel, truncate(desc, 120)))
			if len(lines) >= a.MaxResults {
				break
			}
		}
	}
	if len(lines) == 0 {
		return "no skills found", nil
	}
	return strings.Join(lines, "\n"), nil
}

type skillLoadArgs struct {
	Name string `json:"name"`
	Ref  string `json:"ref"`
}

func (r *Registry) skillLoad(argsJSON string) (string, error) {
	var a skillLoadArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	ref := a.Ref
	if ref == "" {
		ref = "SKILL.md"
	}
	if strings.Contains(ref, "..") {
		return "", fmt.Errorf("invalid ref")
	}
	for _, root := range r.skillRoots() {
		p := filepath.Join(root, a.Name, ref)
		data, err := os.ReadFile(p)
		if err == nil {
			return string(data), nil
		}
	}
	return "", fmt.Errorf("skill %q not found", a.Name)
}

func firstParagraph(md string) string {
	// skip front matter
	s := md
	if strings.HasPrefix(s, "---") {
		if i := strings.Index(s[3:], "---"); i >= 0 {
			s = s[3+i+3:]
		}
	}
	s = strings.TrimSpace(s)
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

type attachArgs struct {
	Path   string `json:"path"`
	As     string `json:"as"`
	Inline *bool  `json:"inline"`
}

func (r *Registry) attachFile(argsJSON string, tc *TurnContext) (string, error) {
	var a attachArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	abs, err := r.resolve(a.Path)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", fmt.Errorf("cannot attach directory")
	}
	name := a.As
	if name == "" {
		name = filepath.Base(a.Path)
	}
	inline := true
	if a.Inline != nil {
		inline = *a.Inline
	}
	preview := ""
	if fi.Size() <= 1<<20 && inline {
		if data, err := os.ReadFile(abs); err == nil && utf8Printable(data) {
			preview = string(data)
			if len(preview) > 2000 {
				preview = preview[:2000] + "…"
			}
		} else {
			inline = false
		}
	} else if fi.Size() > 1<<20 {
		inline = false
	}
	att := Attachment{
		Path:    a.Path,
		Name:    name,
		Inline:  inline,
		Size:    fi.Size(),
		Preview: preview,
	}
	if tc != nil && tc.OnAttachment != nil {
		tc.OnAttachment(att)
	}
	return mustJSON(map[string]interface{}{
		"attached": true,
		"path":     a.Path,
		"name":     name,
		"inline":   inline,
		"size":     fi.Size(),
		"note":     "UI attachment only; not added to model context",
	}), nil
}

// messageAttach stores a chat-scoped attachment from a workspace path (ADR-0019).
func (r *Registry) messageAttach(argsJSON string, tc *TurnContext) (string, error) {
	var a struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", fmt.Errorf("path is required")
	}
	if tc == nil || strings.TrimSpace(tc.SessionID) == "" {
		return "", fmt.Errorf("no current session")
	}
	// Store via session runner is injected as callback path — read workspace file then OnChatAttachment.
	// Tools package cannot import session; use path resolve under workspace.
	abs, err := r.resolve(a.Path)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", fmt.Errorf("cannot attach directory")
	}
	if fi.Size() > 8<<20 {
		return "", fmt.Errorf("file too large (max 8 MiB)")
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	name := a.Name
	if name == "" {
		name = filepath.Base(a.Path)
	}
	// Sniff via simple extension; full sniff is on StageAttachment side if we had access.
	// Prefer OnChatAttachment with bytes staged through a hook if available.
	if r.StageChatAttachment != nil {
		id, mime, kind, err := r.StageChatAttachment(tc.SessionID, name, data, "")
		if err != nil {
			return "", err
		}
		preview := ""
		if kind == "document" && len(data) > 0 {
			preview = string(data)
			if len(preview) > 2048 {
				preview = preview[:2048] + "…"
			}
		}
		if tc.OnChatAttachment != nil {
			tc.OnChatAttachment(Attachment{
				Path: id, Name: name, Inline: kind == "image", Mime: mime, Size: fi.Size(), Preview: preview,
			})
		}
		return mustJSON(map[string]interface{}{
			"attached": true,
			"id":       id,
			"name":     name,
			"mime":     mime,
			"kind":     kind,
			"size":     fi.Size(),
			"preview":  preview,
			"note":     "chat attachment (durable); not re-injected as tool result to model",
		}), nil
	}
	return "", fmt.Errorf("message_attach not configured")
}

func utf8Printable(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	for i := 0; i < len(b) && i < 8192; i++ {
		if b[i] == 0 {
			return false
		}
	}
	return true
}

func (r *Registry) getContextUsage(tc *TurnContext) (string, error) {
	if tc == nil || tc.GetUsage == nil {
		return "", fmt.Errorf("context usage not available")
	}
	return mustJSON(tc.GetUsage()), nil
}

type compactArgs struct {
	Style     string `json:"style"`
	KeepLastN int    `json:"keep_last_n"`
}

func (r *Registry) sessionCompact(argsJSON string, tc *TurnContext) (string, error) {
	var a compactArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.KeepLastN <= 0 {
		a.KeepLastN = 12
	}
	if tc == nil || tc.Compact == nil {
		return "", fmt.Errorf("session_compact not available in this context")
	}
	return tc.Compact(a.Style, a.KeepLastN)
}
