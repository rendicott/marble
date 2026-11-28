package memsearch

import (
	"strings"
	"unicode"
)

// stopwords are dropped from the query before it reaches FTS5. The default
// unicode61 tokenizer keeps them, so without this a query word such as "the"
// matches almost every document.
var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "of": true,
	"to": true, "in": true, "for": true, "on": true, "with": true, "is": true,
	"are": true, "was": true, "were": true, "be": true, "been": true, "it": true,
	"this": true, "that": true, "at": true, "by": true, "from": true, "as": true,
	"not": true, "no": true,
}

// Query is a user query turned into FTS5 MATCH expressions.
// Terms empty means nothing is searchable: the caller must report no matches
// and must not fall back to a token-OR scan.
type Query struct {
	Raw     string
	Terms   []string // content terms, lowercased, deduplicated, in query order
	Dropped []string // stopwords and single characters removed, deduplicated
	// Phrase is the whole query as one quoted FTS5 phrase. Empty when the
	// query has fewer than two tokens or no content terms.
	Phrase string
	// Expr is "t1" OR "t2" OR … OR "tn"*: every content term quoted, and the
	// last one also as a prefix so a half-typed word still matches.
	Expr string
}

// Tokenize lowercases s and splits it on every rune that is not a letter or
// a digit. That is the same boundary the unicode61 tokenizer uses, so a term
// produced here is a term FTS5 indexed.
func Tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// BuildQuery strips stopwords and builds the MATCH expressions. Every term is
// emitted inside double quotes with any embedded quote doubled, so FTS5
// operators in user text (NEAR, AND, OR, NOT, *, ^, -, :, parentheses) are
// literals, never syntax. The expressions are bound as SQL parameters.
func BuildQuery(raw string) Query {
	q := Query{Raw: raw}
	toks := Tokenize(raw)
	seenTerm := map[string]bool{}
	seenDrop := map[string]bool{}
	for _, t := range toks {
		if stopwords[t] || len([]rune(t)) < 2 {
			if !seenDrop[t] {
				seenDrop[t] = true
				q.Dropped = append(q.Dropped, t)
			}
			continue
		}
		if !seenTerm[t] {
			seenTerm[t] = true
			q.Terms = append(q.Terms, t)
		}
	}
	if len(q.Terms) == 0 {
		return q
	}
	parts := make([]string, 0, len(q.Terms)+1)
	for _, t := range q.Terms {
		parts = append(parts, quoteFTS(t))
	}
	last := q.Terms[len(q.Terms)-1]
	parts = append(parts, quoteFTS(last)+"*")
	q.Expr = strings.Join(parts, " OR ")
	// The phrase keeps stopwords: the documents still contain them, and FTS5
	// phrase matching needs the tokens to be adjacent as written.
	if len(toks) >= 2 {
		q.Phrase = quoteFTS(strings.Join(toks, " "))
	}
	return q
}

func quoteFTS(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
