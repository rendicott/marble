package memsearch

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func writeMD(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func openIndex(t *testing.T, root string) *Index {
	t.Helper()
	ix, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ix.Close() })
	return ix
}

func mustReindex(t *testing.T, ix *Index) ReindexReport {
	t.Helper()
	rep, err := ix.ReindexFull()
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func search(t *testing.T, ix *Index, query string, opt Options) Result {
	t.Helper()
	res, err := ix.Search(BuildQuery(query), opt)
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	return res
}

func TestMemIndexReindexIncremental(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "session/a.md", "# A\n\nalpha")
	writeMD(t, root, "daily/2026-11-28.md", "# Daily\n\nbeta")
	writeMD(t, root, "knowledge/k.md", "---\ntopic: \"Kestrel\"\n---\n\n# Kestrel\n\ngamma")
	writeMD(t, root, "knowledge/notes.txt", "not markdown")
	ix := openIndex(t, root)

	first := mustReindex(t, ix)
	if first.Walked != 3 || first.Added != 3 || first.Touched != 3 || first.Partial {
		t.Fatalf("first %+v", first)
	}
	second := mustReindex(t, ix)
	if second.Touched != 0 || second.Added != 0 || second.Updated != 0 || second.Deleted != 0 || second.Unchanged != 3 {
		t.Fatalf("second reindex touched files: %+v", second)
	}

	// A changed file is re-read and re-indexed; the others are not.
	writeMD(t, root, "session/a.md", "# A\n\nalpha omega changed")
	future := time.Now().Add(5 * time.Second)
	if err := os.Chtimes(filepath.Join(root, "session", "a.md"), future, future); err != nil {
		t.Fatal(err)
	}
	third := mustReindex(t, ix)
	if third.Touched != 1 || third.Updated != 1 || third.Unchanged != 2 {
		t.Fatalf("third %+v", third)
	}
	if res := search(t, ix, "omega", Options{}); len(res.Hits) != 1 || res.Hits[0].Path != "session/a.md" {
		t.Fatalf("updated text not searchable: %+v", res.Hits)
	}
	st, err := ix.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Docs != 3 || st.ByKind["session"] != 1 || st.ByKind["daily"] != 1 || st.ByKind["knowledge"] != 1 || st.AgeSec < 0 || st.LastReindexedAt == "" {
		t.Fatalf("stats %+v", st)
	}
}

func TestMemIndexDeletesMissingFiles(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "session/keep.md", "kestrel keep")
	writeMD(t, root, "session/gone.md", "kestrel gone")
	ix := openIndex(t, root)
	mustReindex(t, ix)
	if res := search(t, ix, "kestrel", Options{}); len(res.Hits) != 2 {
		t.Fatalf("hits %+v", res.Hits)
	}
	if err := os.Remove(filepath.Join(root, "session", "gone.md")); err != nil {
		t.Fatal(err)
	}
	rep := mustReindex(t, ix)
	if rep.Deleted != 1 {
		t.Fatalf("report %+v", rep)
	}
	res := search(t, ix, "kestrel", Options{})
	if len(res.Hits) != 1 || res.Hits[0].Path != "session/keep.md" || res.DocsIndexed != 1 {
		t.Fatalf("deleted file still searchable: %+v", res)
	}
	// "gone" was only in the deleted file: the FTS row went with it.
	if res := search(t, ix, "gone", Options{}); len(res.Hits) != 0 {
		t.Fatalf("stale FTS row: %+v", res.Hits)
	}
}

func TestQueryBuilderQuotesFTS5Syntax(t *testing.T) {
	hostile := `foo" OR bar* NEAR(baz qux) AND -zap ^col:val "unterminated ( ) NOT`
	q := BuildQuery(hostile)
	if q.Expr == "" {
		t.Fatal("no expression")
	}
	// Every operand is a quoted string, optionally with a trailing * on the
	// last; the only bare words are the OR joins this builder writes.
	operand := regexp.MustCompile(`^"([^"]|"")*"\*?$`)
	parts := strings.Split(q.Expr, " OR ")
	for i, p := range parts {
		if !operand.MatchString(p) {
			t.Fatalf("operand %d %q is not a quoted literal (expr %s)", i, p, q.Expr)
		}
		if strings.HasSuffix(p, "*") && i != len(parts)-1 {
			t.Fatalf("prefix on non-last operand %q", p)
		}
	}
	for _, kw := range []string{"near", "col", "val", "foo", "bar", "zap"} {
		if !strings.Contains(q.Expr, `"`+kw+`"`) {
			t.Fatalf("%s should be a quoted literal term: %s", kw, q.Expr)
		}
	}
	if q.Phrase == "" || !operand.MatchString(q.Phrase) {
		t.Fatalf("phrase %q", q.Phrase)
	}
	// quoteFTS doubles embedded quotes, which is FTS5's only escape.
	if got := quoteFTS(`a"b`); got != `"a""b"` {
		t.Fatalf("quoteFTS %q", got)
	}

	// And the expressions run: no FTS5 syntax error, and NEAR is a word.
	root := t.TempDir()
	writeMD(t, root, "knowledge/near.md", "# Near\n\nthe cabin is near the lake")
	writeMD(t, root, "knowledge/other.md", "# Other\n\nnothing relevant")
	ix := openIndex(t, root)
	mustReindex(t, ix)
	res := search(t, ix, hostile, Options{})
	if len(res.Hits) != 1 || res.Hits[0].Path != "knowledge/near.md" {
		t.Fatalf("hostile query hits %+v", res.Hits)
	}
	for _, s := range []string{`"`, `*`, `NEAR`, `AND`, `-`, `^`, `"NEAR"`, `col:`, `(`, `a"b*`} {
		if _, err := ix.Search(BuildQuery(s), Options{}); err != nil {
			t.Fatalf("query %q broke out into FTS5 syntax: %v", s, err)
		}
	}
}

func TestSearchRanksRareTermAboveCommonTerm(t *testing.T) {
	root := t.TempDir()
	// "windows" is everywhere, as it was in the real store. The common-word
	// documents sort first and are longer on the common word.
	for i := 0; i < 40; i++ {
		writeMD(t, root, fmt.Sprintf("session/a%03d.md", i),
			"# Windows notes\n\nwindows windows windows: patched windows, rebooted windows.")
	}
	writeMD(t, root, "session/zz-rare.md", "# Release\n\nOn windows the installer needs SmartScreen reputation.")
	ix := openIndex(t, root)
	mustReindex(t, ix)
	res := search(t, ix, "windows smartscreen", Options{Max: 5})
	if len(res.Hits) == 0 || res.Hits[0].Path != "session/zz-rare.md" {
		t.Fatalf("rare-term doc must rank first: %+v", res.Hits)
	}
	if res.Hits[0].Score >= 0 {
		t.Fatalf("bm25 score should be negative: %v", res.Hits[0].Score)
	}
	for i := 1; i < len(res.Hits); i++ {
		if res.Hits[i].Score < res.Hits[i-1].Score {
			t.Fatalf("not best-first (ascending bm25): %+v", res.Hits)
		}
	}
	if !strings.Contains(strings.ToLower(res.Hits[0].Excerpt), "smartscreen") {
		t.Fatalf("excerpt should sit on the match: %q", res.Hits[0].Excerpt)
	}
}

func TestSearchTitleOutranksBody(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "knowledge/a-body.md", "# Misc\n\nsome text that mentions kestrel once among other words here")
	writeMD(t, root, "knowledge/b-title.md", "# Kestrel\n\nsome text that mentions a bird once among other words here")
	ix := openIndex(t, root)
	mustReindex(t, ix)
	res := search(t, ix, "kestrel", Options{})
	if len(res.Hits) != 2 || res.Hits[0].Path != "knowledge/b-title.md" || !res.Hits[0].TitleHit || res.Hits[1].TitleHit {
		t.Fatalf("title weight: %+v", res.Hits)
	}
}

func TestSearchScopeAndTags(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "session/s.md", "kestrel in a session")
	writeMD(t, root, "knowledge/tagged.md", "---\ntopic: \"x\"\ntags: [birds]\n---\n\nkestrel tagged")
	writeMD(t, root, "knowledge/untagged.md", "---\ntopic: \"y\"\ntags: [cars]\n---\n\nkestrel untagged")
	ix := openIndex(t, root)
	mustReindex(t, ix)
	res := search(t, ix, "kestrel", Options{Scope: "knowledge"})
	if len(res.Hits) != 2 || res.DocsInScope != 2 || res.DocsIndexed != 3 {
		t.Fatalf("scope %+v", res)
	}
	res = search(t, ix, "kestrel", Options{Scope: "all", Tags: []string{"Birds"}})
	if len(res.Hits) != 1 || res.Hits[0].Path != "knowledge/tagged.md" || res.Rejected != 2 {
		t.Fatalf("tags %+v", res)
	}
}

func TestSearchPhraseFirst(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "knowledge/a.md", "# Signing\n\ntrusted people do signing, signing, signing of trusted things, trusted")
	writeMD(t, root, "knowledge/b.md", "# Notes\n\nwe moved to azure trusted signing last week")
	ix := openIndex(t, root)
	mustReindex(t, ix)
	res := search(t, ix, "trusted signing", Options{})
	if len(res.Hits) != 2 || res.Hits[0].Path != "knowledge/b.md" || !res.Hits[0].MatchedPhrase || res.Hits[1].MatchedPhrase {
		t.Fatalf("phrase first: %+v", res.Hits)
	}
}

func TestSearchPrefixOnLastTerm(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "knowledge/a.md", "notarization of the dmg")
	ix := openIndex(t, root)
	mustReindex(t, ix)
	if res := search(t, ix, "dmg notariz", Options{}); len(res.Hits) != 1 {
		t.Fatalf("prefix: %+v", res.Hits)
	}
}

func TestMaybeReindexHonoursAgeAndInvalidate(t *testing.T) {
	root := t.TempDir()
	writeMD(t, root, "knowledge/a.md", "alpha")
	ix := openIndex(t, root)
	ix.MaxAge = time.Hour
	ran, _, err := ix.MaybeReindex()
	if err != nil || !ran {
		t.Fatalf("first search must index: ran=%v err=%v", ran, err)
	}
	writeMD(t, root, "knowledge/b.md", "kestrel")
	if ran, _, _ := ix.MaybeReindex(); ran {
		t.Fatal("fresh index reindexed before MaxAge")
	}
	ix.Invalidate()
	ran, rep, err := ix.MaybeReindex()
	if err != nil || !ran || rep.Added != 1 {
		t.Fatalf("invalidate: ran=%v rep=%+v err=%v", ran, rep, err)
	}
}

func TestReindexBudgetResumes(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 30; i++ {
		writeMD(t, root, fmt.Sprintf("session/f%02d.md", i), "kestrel")
	}
	ix := openIndex(t, root)
	// A budget already spent: the first file is read, the rest left.
	rep, err := ix.Reindex(time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Partial || rep.Remaining == 0 || rep.Touched+rep.Remaining != 30 {
		t.Fatalf("partial %+v", rep)
	}
	if !ix.stale() {
		t.Fatal("partial reindex must leave the index stale")
	}
	rep2 := mustReindex(t, ix)
	if rep2.Partial || rep2.Added != rep.Remaining {
		t.Fatalf("resume %+v after %+v", rep2, rep)
	}
}

func TestParseDocHeadings(t *testing.T) {
	text := "---\ntopic: \"Release Signing\"\ntags: [a]\n---\n\n# Top\n\n## Windows\n\n```sh\n# not a heading\n```\n\n### EV cert ###\n#nospace\n"
	d := ParseDoc("knowledge/x.md", text)
	if d.Title != "Release Signing" {
		t.Fatalf("title %q", d.Title)
	}
	if d.Headings != "Top > Windows > EV cert" {
		t.Fatalf("headings %q", d.Headings)
	}
	if ParseDoc("session/abc.md", "no headings").Title != "abc" {
		t.Fatal("filename title")
	}
	if ParseDoc("session/abc.md", "## Sub\n# Main\n").Title != "Main" {
		t.Fatal("first H1 title")
	}
}
