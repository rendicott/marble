// Package memsearch is the ranked memory search behind memory_search: an
// SQLite FTS5 index of $MEMORY/{session,daily,knowledge}/**/*.md, scored with
// BM25. It replaces a first-come linear walk that stopped at max_results and
// so never reached documents late in the tree.
package memsearch

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rendicott/marble/internal/db"
)

// Kinds are the memory subtrees that are indexed, in search order.
var Kinds = []string{"session", "daily", "knowledge"}

const (
	// DefaultMaxAge is how old the last reindex may be before a search
	// triggers an incremental one.
	DefaultMaxAge = 60 * time.Second
	// DefaultBudget caps one reindex invocation on the search path. Work
	// left over is resumed by the next invocation.
	DefaultBudget = 2 * time.Second
)

// Index is the memory search index for one memory root. It is owned by the
// harness process. Methods are safe for concurrent use.
type Index struct {
	Root   string
	MaxAge time.Duration // <=0 uses DefaultMaxAge
	Budget time.Duration // <=0 uses DefaultBudget

	sql *sql.DB

	reindexMu sync.Mutex // one reindex at a time

	mu           sync.Mutex
	lastReindex  time.Time
	lastComplete bool
	dirty        bool
}

// Open opens or creates $MEMORY/memory-index.db. It does not reindex.
func Open(root string) (*Index, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	s, err := db.OpenMemIndex(abs)
	if err != nil {
		return nil, err
	}
	ix := &Index{Root: abs, sql: s}
	var last string
	if err := s.QueryRow(`SELECT last_reindexed_at FROM mem_index_meta WHERE id = 1`).Scan(&last); err == nil && last != "" {
		if t, err := time.Parse(time.RFC3339Nano, last); err == nil {
			ix.lastReindex = t
		}
	}
	// A previous process may have stopped mid-reindex; the first search in
	// this one checks the tree regardless of age.
	ix.lastComplete = false
	return ix, nil
}

// Close closes the index database.
func (ix *Index) Close() error {
	if ix == nil || ix.sql == nil {
		return nil
	}
	return ix.sql.Close()
}

// Invalidate makes the next MaybeReindex run regardless of age. Call it after
// writing a memory file so a search right after the write can find it.
func (ix *Index) Invalidate() {
	if ix == nil {
		return
	}
	ix.mu.Lock()
	ix.dirty = true
	ix.mu.Unlock()
}

// ReindexReport describes one reindex invocation.
type ReindexReport struct {
	StartedAt  string `json:"started_at"`
	DurationMS int64  `json:"duration_ms"`
	Walked     int    `json:"walked"`    // .md files seen on disk
	Touched    int    `json:"touched"`   // files read because stat changed
	Added      int    `json:"added"`     // new rows
	Updated    int    `json:"updated"`   // rows whose text changed
	Unchanged  int    `json:"unchanged"` // stat unchanged, or same hash
	Deleted    int    `json:"deleted"`   // rows whose file is gone
	Remaining  int    `json:"remaining"` // changed files left for next time
	Partial    bool   `json:"partial"`
}

type fileStat struct {
	rel   string
	abs   string
	kind  string
	size  int64
	mtime int64
}

type rowStat struct {
	size  int64
	mtime int64
	hash  string
}

// Reindex brings the index in line with the markdown trees. Files whose
// mtime and size match their row are skipped. Rows whose file is gone are
// deleted. budget > 0 stops reading files once it is spent; the report is
// then Partial and the rest is picked up by the next call. budget <= 0 has
// no limit.
func (ix *Index) Reindex(budget time.Duration) (ReindexReport, error) {
	ix.reindexMu.Lock()
	defer ix.reindexMu.Unlock()
	return ix.reindexLocked(budget)
}

// ReindexFull runs Reindex without a budget.
func (ix *Index) ReindexFull() (ReindexReport, error) {
	return ix.Reindex(0)
}

// MaybeReindex runs a budgeted incremental reindex when the index is stale.
// When another reindex is already running it returns at once and the search
// uses the index as it stands. reindexed reports whether this call ran one.
func (ix *Index) MaybeReindex() (reindexed bool, rep ReindexReport, err error) {
	if !ix.stale() {
		return false, rep, nil
	}
	if !ix.reindexMu.TryLock() {
		return false, rep, nil
	}
	defer ix.reindexMu.Unlock()
	if !ix.stale() {
		return false, rep, nil
	}
	budget := ix.Budget
	if budget <= 0 {
		budget = DefaultBudget
	}
	rep, err = ix.reindexLocked(budget)
	return true, rep, err
}

func (ix *Index) stale() bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	maxAge := ix.MaxAge
	if maxAge <= 0 {
		maxAge = DefaultMaxAge
	}
	return ix.dirty || !ix.lastComplete || ix.lastReindex.IsZero() || time.Since(ix.lastReindex) > maxAge
}

func (ix *Index) reindexLocked(budget time.Duration) (ReindexReport, error) {
	started := time.Now()
	rep := ReindexReport{StartedAt: started.UTC().Format(time.RFC3339Nano)}
	// Clear dirty before walking: a write that lands during the walk sets
	// it again and is picked up next time.
	ix.mu.Lock()
	ix.dirty = false
	ix.mu.Unlock()

	files, err := walkMemory(ix.Root)
	if err != nil {
		return rep, err
	}
	rep.Walked = len(files)

	existing := map[string]rowStat{}
	rows, err := ix.sql.Query(`SELECT path, size_bytes, mtime_unix, content_hash FROM mem_doc`)
	if err != nil {
		return rep, err
	}
	for rows.Next() {
		var p string
		var rs rowStat
		if err := rows.Scan(&p, &rs.size, &rs.mtime, &rs.hash); err != nil {
			rows.Close()
			return rep, err
		}
		existing[p] = rs
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return rep, err
	}
	rows.Close()

	tx, err := ix.sql.Begin()
	if err != nil {
		return rep, err
	}
	defer tx.Rollback()

	onDisk := make(map[string]bool, len(files))
	var changed []fileStat
	for _, f := range files {
		onDisk[f.rel] = true
		rs, ok := existing[f.rel]
		if ok && rs.size == f.size && rs.mtime == f.mtime {
			rep.Unchanged++
			continue
		}
		changed = append(changed, f)
	}
	// The mem_doc_ad trigger removes each deleted row from mem_fts in the
	// same transaction.
	for p := range existing {
		if onDisk[p] {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM mem_doc WHERE path = ?`, p); err != nil {
			return rep, err
		}
		rep.Deleted++
	}

	now := db.UTCNow()
	for i, f := range changed {
		if budget > 0 && time.Since(started) > budget {
			rep.Remaining = len(changed) - i
			rep.Partial = true
			break
		}
		data, err := os.ReadFile(f.abs)
		if err != nil {
			// Vanished or unreadable between walk and read: leave the row
			// as it is; the next walk settles it.
			continue
		}
		rep.Touched++
		sum := sha256.Sum256(data)
		hash := hex.EncodeToString(sum[:])
		prev, had := existing[f.rel]
		if had && prev.hash == hash {
			if _, err := tx.Exec(`UPDATE mem_doc SET size_bytes = ?, mtime_unix = ?, indexed_at = ? WHERE path = ?`,
				f.size, f.mtime, now, f.rel); err != nil {
				return rep, err
			}
			rep.Unchanged++
			continue
		}
		doc := ParseDoc(f.rel, string(data))
		// The upsert fires mem_doc_ai or mem_doc_au, which replace this
		// document's mem_fts row inside this transaction.
		if _, err := tx.Exec(`
			INSERT INTO mem_doc (path, kind, title, headings, body, size_bytes, mtime_unix, content_hash, indexed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(path) DO UPDATE SET
				kind = excluded.kind, title = excluded.title, headings = excluded.headings,
				body = excluded.body, size_bytes = excluded.size_bytes, mtime_unix = excluded.mtime_unix,
				content_hash = excluded.content_hash, indexed_at = excluded.indexed_at`,
			f.rel, f.kind, doc.Title, doc.Headings, doc.Body, f.size, f.mtime, hash, now); err != nil {
			return rep, err
		}
		if had {
			rep.Updated++
		} else {
			rep.Added++
		}
	}
	finished := time.Now()
	if !rep.Partial {
		if _, err := tx.Exec(`UPDATE mem_index_meta SET last_reindexed_at = ?, updated_at = ? WHERE id = 1`,
			finished.UTC().Format(time.RFC3339Nano), now); err != nil {
			return rep, err
		}
	}
	if err := tx.Commit(); err != nil {
		return rep, err
	}
	rep.DurationMS = finished.Sub(started).Milliseconds()
	ix.mu.Lock()
	if !rep.Partial {
		ix.lastReindex = finished
	}
	ix.lastComplete = !rep.Partial
	if rep.Partial {
		ix.dirty = true
	}
	ix.mu.Unlock()
	if rep.Partial {
		log.Printf("reindex_partial walked=%d touched=%d remaining=%d duration_ms=%d", rep.Walked, rep.Touched, rep.Remaining, rep.DurationMS)
	}
	return rep, nil
}

func walkMemory(root string) ([]fileStat, error) {
	var out []fileStat
	for _, kind := range Kinds {
		base := filepath.Join(root, kind)
		err := filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) && p == base {
					return filepath.SkipDir
				}
				// Unreadable subtree: skip it, keep the rest.
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), ".md") {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return nil
			}
			out = append(out, fileStat{
				rel:   filepath.ToSlash(rel),
				abs:   p,
				kind:  kind,
				size:  fi.Size(),
				mtime: fi.ModTime().Unix(),
			})
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

// Doc is one markdown file as indexed.
type Doc struct {
	Title    string
	Headings string // every ATX heading in file order, joined with " > "
	Body     string // the whole file, front matter included (tags filter reads it)
}

var atxHeading = regexp.MustCompile(`^#{1,6}[ \t]+(.+?)[ \t#]*$`)

// ParseDoc extracts the title and heading breadcrumb from a markdown file.
// Headings are ATX (`# `…`###### `) lines outside fenced code blocks, so a
// shell comment inside ``` is not a heading. The title is front matter
// title/topic, else the first H1, else the first heading, else the file name.
func ParseDoc(rel, text string) Doc {
	doc := Doc{Body: text}
	body := text
	fmTitle := ""
	if strings.HasPrefix(text, "---") {
		if j := strings.Index(text[3:], "\n---"); j >= 0 {
			fm := text[3 : 3+j]
			body = text[3+j+4:]
			for _, line := range strings.Split(fm, "\n") {
				k, v, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				k = strings.ToLower(strings.TrimSpace(k))
				if (k == "title" || k == "topic") && fmTitle == "" {
					fmTitle = strings.Trim(strings.TrimSpace(v), `"'`)
				}
			}
		}
	}
	var heads []string
	firstH1 := ""
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		m := atxHeading.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		h := strings.TrimSpace(m[1])
		if h == "" {
			continue
		}
		heads = append(heads, h)
		if firstH1 == "" && strings.HasPrefix(line, "# ") {
			firstH1 = h
		}
	}
	doc.Headings = strings.Join(heads, " > ")
	switch {
	case fmTitle != "":
		doc.Title = fmTitle
	case firstH1 != "":
		doc.Title = firstH1
	case len(heads) > 0:
		doc.Title = heads[0]
	default:
		doc.Title = strings.TrimSuffix(filepath.Base(rel), ".md")
	}
	return doc
}

// Stats is the GET /api/memory-index body.
type Stats struct {
	Docs            int            `json:"docs"`
	ByKind          map[string]int `json:"by_kind"`
	Bytes           int64          `json:"bytes"`            // sum of indexed file sizes
	IndexFileBytes  int64          `json:"index_file_bytes"` // memory-index.db plus -wal
	LastReindexedAt string         `json:"last_reindexed_at"`
	AgeSec          int64          `json:"age_sec"` // -1 when never reindexed
	SchemaVersion   int            `json:"schema_version"`
	Path            string         `json:"path"`
}

// Stats reports what the index holds.
func (ix *Index) Stats() (Stats, error) {
	st := Stats{ByKind: map[string]int{}, AgeSec: -1, SchemaVersion: db.MemIndexSchemaVersion, Path: db.MemIndexPath(ix.Root)}
	for _, k := range Kinds {
		st.ByKind[k] = 0
	}
	rows, err := ix.sql.Query(`SELECT kind, COUNT(*), COALESCE(SUM(size_bytes), 0) FROM mem_doc GROUP BY kind`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		var b int64
		if err := rows.Scan(&k, &n, &b); err != nil {
			return st, err
		}
		st.ByKind[k] = n
		st.Docs += n
		st.Bytes += b
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	for _, suffix := range []string{"", "-wal"} {
		if fi, err := os.Stat(st.Path + suffix); err == nil {
			st.IndexFileBytes += fi.Size()
		}
	}
	if last := ix.LastReindexed(); !last.IsZero() {
		st.LastReindexedAt = last.UTC().Format(time.RFC3339)
		st.AgeSec = int64(time.Since(last).Seconds())
	}
	return st, nil
}

// LastReindexed is when the last complete reindex finished (zero if never).
func (ix *Index) LastReindexed() time.Time {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.lastReindex
}

func (ix *Index) countDocs(kind string) (inScope, total int, err error) {
	if err = ix.sql.QueryRow(`SELECT COUNT(*) FROM mem_doc`).Scan(&total); err != nil {
		return 0, 0, err
	}
	if kind == "" {
		return total, total, nil
	}
	err = ix.sql.QueryRow(`SELECT COUNT(*) FROM mem_doc WHERE kind = ?`, kind).Scan(&inScope)
	return inScope, total, err
}

// String is for logs.
func (r ReindexReport) String() string {
	return fmt.Sprintf("walked=%d touched=%d added=%d updated=%d unchanged=%d deleted=%d remaining=%d partial=%v duration_ms=%d",
		r.Walked, r.Touched, r.Added, r.Updated, r.Unchanged, r.Deleted, r.Remaining, r.Partial, r.DurationMS)
}
