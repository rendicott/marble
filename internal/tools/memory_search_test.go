package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rendicott/marble/internal/memsearch"
)

func seedMD(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func memRegistry(t *testing.T, root string) *Registry {
	t.Helper()
	r := &Registry{Memory: root, Workspace: t.TempDir(), MaxResultChars: 100000}
	t.Cleanup(func() {
		if r.MemIndex != nil {
			_ = r.MemIndex.Close()
		}
	})
	return r
}

func runSearch(t *testing.T, r *Registry, args map[string]any) (string, *ResultSummary) {
	t.Helper()
	b, _ := json.Marshal(args)
	out, sum, err := r.memorySearch(string(b))
	if err != nil {
		t.Fatalf("memory_search %s: %v", b, err)
	}
	if sum == nil {
		t.Fatal("nil summary")
	}
	return out, sum
}

func hitPaths(out string) []string {
	if out == "no matches" {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if p, _, ok := strings.Cut(line, ": "); ok {
			paths = append(paths, p)
		}
	}
	return paths
}

// TestSearchReachesDeepCorpusPath is the regression test for the v0.4.37
// failure: the linear walk stopped once max_results files matched any token,
// so a document late in the tree was never opened. Here 210 documents sort
// ahead of the only one that answers the query, and every one of them
// contains a query word.
func TestSearchReachesDeepCorpusPath(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 210; i++ {
		seedMD(t, root, fmt.Sprintf("session/a%04d.md", i),
			fmt.Sprintf("# Session %d\n\nRan the windows build again; windows updates applied.\n", i))
	}
	seedMD(t, root, "knowledge/zzzz-last.md", "# Release signing\n\nThe windows installer is signed with Azure Trusted Signing.\n")
	r := memRegistry(t, root)

	out, sum := runSearch(t, r, map[string]any{"query": "windows azure trusted signing", "max_results": 5})
	paths := hitPaths(out)
	if len(paths) == 0 || paths[0] != "knowledge/zzzz-last.md" {
		t.Fatalf("deep doc not returned first; got %v\n%s", paths, out)
	}
	if sum.Data["docs_scanned"] != 211 || sum.Data["docs_indexed"] != 211 {
		t.Fatalf("scanned %v indexed %v", sum.Data["docs_scanned"], sum.Data["docs_indexed"])
	}
	// Alphabetically last across the whole store, too.
	seedMD(t, root, "session/zzzz-deepest.md", "kestrel\n")
	r.MemIndex.Invalidate()
	out, _ = runSearch(t, r, map[string]any{"query": "kestrel"})
	if p := hitPaths(out); len(p) != 1 || p[0] != "session/zzzz-deepest.md" {
		t.Fatalf("deepest session file: %v", p)
	}
}

// TestMemorySearchAzureTrustedSigningBaseline replays session 0whdf247x5:
// 33 calls, none of which surfaced the three Azure Trusted Signing notes at
// ranks 183/193/196 of 223. One call must now put one of them in the top 5.
func TestMemorySearchAzureTrustedSigningBaseline(t *testing.T) {
	root := t.TempDir()
	fillers := []string{
		"# Session\n\nDebugged the windows terminal profile. Every event in the windows event log was noise.\n",
		"# Session\n\nRenewed the TLS certificate for the windows host behind tailscale; windows needed a reboot.\n",
		"# Session\n\nSet up git commit signing with ssh keys. Checked windows line endings.\n",
		"# Session\n\nwindows windows windows: the windows VM, the windows share, the windows path.\n",
		"# Session\n\nPlay Console upload; the Android signing key and the upload certificate were rotated.\n",
		"# Session\n\nEvery evening the cron job runs; events are batched. Windows clients poll.\n",
	}
	for i := 0; i < 200; i++ {
		// 0wa…–0wf… sort ahead of the answers, as in the real store.
		seedMD(t, root, fmt.Sprintf("session/0w%c%04d.md", 'a'+rune(i%6), i), fillers[i%len(fillers)])
	}
	for i := 0; i < 20; i++ {
		seedMD(t, root, fmt.Sprintf("session/0wz%04d.md", i), fillers[i%len(fillers)])
	}
	seedMD(t, root, "session/0wgd6wmss3.md", "# Windows code signing\n\nUser asked how to stop the SmartScreen warning on the windows installer. "+
		"Options: an EV certificate, or Azure Trusted Signing (cheaper, builds reputation with Microsoft). Chose Azure Trusted Signing.\n")
	seedMD(t, root, "session/0wggw9ty7a.md", "# Azure Trusted Signing setup\n\nCreated the Trusted Signing account and certificate profile; "+
		"signtool with the dlib signs the windows binaries in CI. SmartScreen reputation should follow.\n")
	seedMD(t, root, "session/0wghsjyc0c.md", "# Release\n\nWired Azure Trusted Signing into the release workflow for the windows build. "+
		"No EV certificate purchase needed.\n")
	answers := map[string]bool{
		"session/0wgd6wmss3.md": true,
		"session/0wggw9ty7a.md": true,
		"session/0wghsjyc0c.md": true,
	}
	r := memRegistry(t, root)

	out, sum := runSearch(t, r, map[string]any{"query": "windows signing certificate SmartScreen EV"})
	paths := hitPaths(out)
	top := paths
	if len(top) > 5 {
		top = top[:5]
	}
	found := false
	for _, p := range top {
		if answers[p] {
			found = true
		}
	}
	if !found {
		t.Fatalf("none of the Azure Trusted Signing notes in the top 5 of one call: %v", paths)
	}
	if sum.Data["max_results"] != memsearch.DefaultMaxResults || len(paths) > memsearch.DefaultMaxResults {
		t.Fatalf("max_results %v, %d hits", sum.Data["max_results"], len(paths))
	}
	if sum.Data["docs_scanned"] != 223 {
		t.Fatalf("docs_scanned %v want all 223", sum.Data["docs_scanned"])
	}
}

func TestQueryBuilderDropsStopwords(t *testing.T) {
	q := memsearch.BuildQuery("Is the a of it, to: a I")
	if q.Expr != "" || q.Phrase != "" || len(q.Terms) != 0 {
		t.Fatalf("stopword-only query built an expression: %+v", q)
	}
	for _, w := range []string{"is", "the", "a", "of", "it", "to", "i"} {
		found := false
		for _, d := range q.Dropped {
			found = found || d == w
		}
		if !found {
			t.Fatalf("%q not reported dropped: %v", w, q.Dropped)
		}
	}
	mixed := memsearch.BuildQuery("the windows of signing")
	if strings.Join(mixed.Terms, ",") != "windows,signing" || strings.Contains(mixed.Expr, `"the"`) || strings.Contains(mixed.Expr, `"of"`) {
		t.Fatalf("mixed %+v", mixed)
	}

	// End to end: every document contains "the"; a token-OR scan would
	// return all of them. The answer is no matches.
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		seedMD(t, root, fmt.Sprintf("knowledge/k%d.md", i), "the cat is on the mat and it was that")
	}
	r := memRegistry(t, root)
	out, sum := runSearch(t, r, map[string]any{"query": "is the a of it"})
	if out != "no matches" || !sum.Empty || sum.Hits != 0 {
		t.Fatalf("stopword-only: %q %+v", out, sum)
	}
	if sum.Data["match_expr"] != "" || sum.Data["note"] == nil {
		t.Fatalf("summary %+v", sum.Data)
	}
	if d, _ := sum.Data["stopwords_dropped"].([]string); len(d) != 5 {
		t.Fatalf("stopwords_dropped %v", sum.Data["stopwords_dropped"])
	}
}

func TestSearchNoMatchSaysNoMatch(t *testing.T) {
	root := t.TempDir()
	seedMD(t, root, "knowledge/a.md", "# Alpha\n\nnothing to see")
	seedMD(t, root, "session/b.md", "beta session")
	r := memRegistry(t, root)
	out, sum := runSearch(t, r, map[string]any{"query": "kestrel zeppelin"})
	if out != "no matches" || !sum.Empty || sum.Hits != 0 || sum.Data["empty"] != true || sum.Data["hit_count"] != 0 {
		t.Fatalf("got %q %+v", out, sum)
	}
	if sum.Data["degraded"] != false || sum.Data["engine"] != "fts5" {
		t.Fatalf("summary %+v", sum.Data)
	}
	if hits := r.MemoryHits("kestrel zeppelin", 5); hits != nil {
		t.Fatalf("MemoryHits %v", hits)
	}
}

func TestMemorySearchDegradesVisiblyWhenIndexUnavailable(t *testing.T) {
	root := t.TempDir()
	seedMD(t, root, "knowledge/a.md", "# Alpha\n\nkestrel nests")
	// A directory where the index file belongs: SQLite cannot open it.
	if err := os.MkdirAll(filepath.Join(root, "memory-index.db"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := memRegistry(t, root)
	out, sum := runSearch(t, r, map[string]any{"query": "kestrel"})
	if !strings.HasPrefix(out, memSearchDegradedNote+"\n") || !strings.Contains(out, "knowledge/a.md: ") {
		t.Fatalf("degraded text must say so and still answer: %q", out)
	}
	if sum.Data["degraded"] != true || sum.Data["engine"] != "linear" || sum.Data["degraded_reason"] == "" || sum.Hits != 1 {
		t.Fatalf("summary %+v", sum.Data)
	}
	// The tool-log row stores this summary as JSON.
	b, _ := json.Marshal(sum)
	if !strings.Contains(string(b), `"degraded":true`) {
		t.Fatalf("summary json %s", b)
	}
	out, sum = runSearch(t, r, map[string]any{"query": "zeppelin"})
	if out != memSearchDegradedNote+"\nno matches" || !sum.Empty {
		t.Fatalf("degraded empty: %q", out)
	}
	// The subprocess-context path gets hits without the note line.
	if hits := r.MemoryHits("kestrel", 5); len(hits) != 1 || !strings.HasPrefix(hits[0], "knowledge/a.md: ") {
		t.Fatalf("MemoryHits %v", hits)
	}
}

func TestMemorySearchSummaryKeys(t *testing.T) {
	root := t.TempDir()
	seedMD(t, root, "knowledge/a.md", "# Kestrel\n\nkestrel nests")
	r := memRegistry(t, root)
	_, sum := runSearch(t, r, map[string]any{"query": "the kestrel", "max_results": 99})
	// v0.4.35 keys the stats endpoint and audit scripts read: kept.
	for _, k := range []string{"query", "scope", "max_results", "docs", "hit_count", "empty", "search_ms", "docs_scanned", "docs_rejected_tags", "tags", "since", "until"} {
		if _, ok := sum.Data[k]; !ok {
			t.Fatalf("missing existing key %q", k)
		}
	}
	for _, k := range []string{"match_expr", "phrase_expr", "hits", "docs_indexed", "index_age_sec", "reindexed", "stopwords_dropped", "degraded", "engine"} {
		if _, ok := sum.Data[k]; !ok {
			t.Fatalf("missing new key %q", k)
		}
	}
	if sum.Data["max_results"] != memsearch.MaxMaxResults {
		t.Fatalf("max_results not clamped: %v", sum.Data["max_results"])
	}
	if sum.Data["reindexed"] != true {
		t.Fatal("first search on a fresh index should reindex")
	}
	docs := sum.Data["docs"].([]map[string]interface{})
	if len(docs) != 1 || docs[0]["title_hit"] != true {
		t.Fatalf("docs %+v", docs)
	}
	if s, _ := docs[0]["score"].(float64); s >= 0 {
		t.Fatalf("score %v", docs[0]["score"])
	}
}

func TestMemoryWriteIsSearchableImmediately(t *testing.T) {
	root := t.TempDir()
	r := memRegistry(t, root)
	if out, _ := runSearch(t, r, map[string]any{"query": "kestrel"}); out != "no matches" {
		t.Fatalf("before write: %q", out)
	}
	if _, err := r.memoryWrite(`{"topic":"Kestrel facts","content":"kestrels hover"}`); err != nil {
		t.Fatal(err)
	}
	out, _ := runSearch(t, r, map[string]any{"query": "kestrel"})
	if p := hitPaths(out); len(p) != 1 || p[0] != "knowledge/kestrel-facts.md" {
		t.Fatalf("after write: %q", out)
	}
}
