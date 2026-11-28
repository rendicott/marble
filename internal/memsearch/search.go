package memsearch

import (
	"fmt"
	"strings"
	"time"
)

// BM25 column weights, one per mem_fts column in declaration order,
// including the UNINDEXED path. A title hit counts 12× a body hit and a
// heading hit 6×.
const (
	WeightPath     = 0.0
	WeightTitle    = 12.0
	WeightHeadings = 6.0
	WeightBody     = 1.0
)

const (
	// DefaultMaxResults is memory_search's default result count.
	DefaultMaxResults = 10
	// MaxMaxResults caps max_results. Ranked results are worth more few
	// than many, and each one costs prompt tokens.
	MaxMaxResults = 25
	// snippetTokens is the excerpt window around the best body match.
	snippetTokens = 20
	// tagScanLimit is how many ranked rows are read when a tag filter will
	// discard some of them after the query.
	tagScanLimit = 1000
)

// ClampMaxResults applies the default and the 1..25 range.
func ClampMaxResults(n int) int {
	if n <= 0 {
		return DefaultMaxResults
	}
	if n > MaxMaxResults {
		return MaxMaxResults
	}
	return n
}

// Options narrow a search.
type Options struct {
	Scope string   // "", "all", "session", "daily", "knowledge"
	Max   int      // already clamped by the caller; <=0 uses the default
	Tags  []string // every tag must appear in the file's front matter
}

// Hit is one ranked document.
type Hit struct {
	Path          string
	Kind          string
	Title         string
	SizeBytes     int64
	Score         float64 // bm25: negative, more negative is better
	Excerpt       string  // snippet() around the best body match, one line
	TitleHit      bool    // a content term is in the title
	MatchedPhrase bool    // the whole query matched as a phrase
	MatchedTerms  []string
}

// Result is a ranked search outcome plus what the caller logs.
type Result struct {
	Hits        []Hit
	DocsInScope int
	DocsIndexed int
	Rejected    int // dropped by the tag filter
}

func scopeKind(scope string) string {
	s := strings.ToLower(strings.TrimSpace(scope))
	if s == "all" {
		return ""
	}
	return s
}

// Search ranks documents for q. Phrase matches come first, each group in
// bm25 order (ORDER BY score ASC, because bm25 is negative). A query with no
// content terms returns no hits; there is no unranked fallback here.
func (ix *Index) Search(q Query, opt Options) (Result, error) {
	var res Result
	kind := scopeKind(opt.Scope)
	max := ClampMaxResults(opt.Max)
	inScope, total, err := ix.countDocs(kind)
	if err != nil {
		return res, err
	}
	res.DocsInScope, res.DocsIndexed = inScope, total
	if len(q.Terms) == 0 {
		return res, nil
	}
	limit := max
	if len(opt.Tags) > 0 {
		limit = tagScanLimit
	}
	seen := map[string]bool{}
	add := func(expr string, phrase bool) error {
		if len(res.Hits) >= max {
			return nil
		}
		hits, err := ix.query(expr, kind, limit)
		if err != nil {
			return err
		}
		for _, h := range hits {
			if len(res.Hits) >= max {
				break
			}
			if seen[h.Path] {
				continue
			}
			seen[h.Path] = true
			if len(opt.Tags) > 0 && !hasTags(h.body, opt.Tags) {
				res.Rejected++
				continue
			}
			h.MatchedPhrase = phrase
			h.MatchedTerms = matchedTerms(q.Terms, h.Title, h.headings, h.body)
			h.TitleHit = titleHit(q.Terms, h.Title)
			res.Hits = append(res.Hits, h.Hit)
		}
		return nil
	}
	if q.Phrase != "" {
		if err := add(q.Phrase, true); err != nil {
			return res, err
		}
	}
	if err := add(q.Expr, false); err != nil {
		return res, err
	}
	return res, nil
}

type rawHit struct {
	Hit
	headings string
	body     string
}

func (ix *Index) query(expr, kind string, limit int) ([]rawHit, error) {
	sqlText := fmt.Sprintf(`
		SELECT d.path, d.kind, d.title, d.headings, d.body, d.size_bytes,
		       bm25(mem_fts, %g, %g, %g, %g) AS score,
		       snippet(mem_fts, 3, '', '', '…', %d)
		FROM mem_fts JOIN mem_doc d ON d.id = mem_fts.rowid
		WHERE mem_fts MATCH ?
		  AND (? = '' OR d.kind = ?)
		ORDER BY score ASC, d.path ASC
		LIMIT ?`, WeightPath, WeightTitle, WeightHeadings, WeightBody, snippetTokens)
	rows, err := ix.sql.Query(sqlText, expr, kind, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rawHit
	for rows.Next() {
		var h rawHit
		var snip string
		if err := rows.Scan(&h.Path, &h.Kind, &h.Title, &h.headings, &h.body, &h.SizeBytes, &h.Score, &snip); err != nil {
			return nil, err
		}
		h.Excerpt = oneLine(snip)
		if h.Excerpt == "" {
			h.Excerpt = oneLine(h.Title)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.Trim(s, "…")
}

func frontMatter(text string) string {
	if i := strings.Index(text, "---"); i >= 0 {
		if j := strings.Index(text[i+3:], "---"); j >= 0 {
			return text[i : i+3+j]
		}
	}
	return text
}

// hasTags keeps the filter the linear search used: each tag, lowercased,
// must be a substring of the front matter.
func hasTags(body string, tags []string) bool {
	fm := strings.ToLower(frontMatter(body))
	for _, tag := range tags {
		if !strings.Contains(fm, strings.ToLower(tag)) {
			return false
		}
	}
	return true
}

func tokenSet(parts ...string) map[string]bool {
	set := map[string]bool{}
	for _, p := range parts {
		for _, t := range Tokenize(p) {
			set[t] = true
		}
	}
	return set
}

func matchedTerms(terms []string, parts ...string) []string {
	set := tokenSet(parts...)
	out := []string{}
	for _, t := range terms {
		if set[t] {
			out = append(out, t)
		}
	}
	return out
}

func titleHit(terms []string, title string) bool {
	set := tokenSet(title)
	for _, t := range terms {
		if set[t] {
			return true
		}
	}
	return false
}

// Age is how long ago the last complete reindex finished; -1s when never.
func (ix *Index) Age() time.Duration {
	last := ix.LastReindexed()
	if last.IsZero() {
		return -time.Second
	}
	return time.Since(last)
}
