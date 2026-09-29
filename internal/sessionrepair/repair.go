// Package sessionrepair fixes session Markdown damaged by the pre-v0.4.11 loader:
//
//   - "## … · thinking" headings were not recognized on load, so each thinking
//     message was glued onto the message before it and its id comment
//     overwrote that message's id (the next save persisted the damage).
//   - After a reload the id counter restarted at the (too small) message count,
//     so new messages reused existing ids.
//   - Attachment chips were never persisted, so reloaded chips lost their
//     attachment id and could not be opened.
//
// The current decoder already splits thinking back out; this package restores
// ids and chips and rewrites the files (dry run unless apply is set).
package sessionrepair

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rendicott/marble/internal/memory"

	_ "modernc.org/sqlite"
)

// Stats counts repairs for one session file.
type Stats struct {
	File         string
	StolenIDs    int // thinking ids moved back off the preceding message
	Renumbered   int // duplicate / missing ids given fresh numbers
	ChipsRestore int // attachment chips rebuilt from session_attachments
}

func (s Stats) changed() bool { return s.StolenIDs+s.Renumbered+s.ChipsRestore > 0 }

type attRow struct {
	id, name, mime, kind, messageID string
	size                            int64
	created                         time.Time
}

// Run scans memoryDir/session/*.md, reports what it would fix, and rewrites
// damaged files when apply is true (after backing up the session directory).
func Run(memoryDir string, apply bool, w io.Writer) error {
	sessDir := filepath.Join(memoryDir, "session")
	if apply {
		if pid := lockHolder(memoryDir); pid > 0 {
			return fmt.Errorf("harness pid %d holds %s — stop it first (a running harness would overwrite repaired files on its next save)", pid, filepath.Join(memoryDir, "marble.lock"))
		}
	}
	atts, err := loadAttachments(filepath.Join(memoryDir, "marble.db"))
	if err != nil {
		fmt.Fprintf(w, "warning: attachment chips not restorable (%v)\n", err)
	}
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		return err
	}
	var damaged []Stats
	var total Stats
	type pending struct {
		path string
		body string
	}
	var writes []pending
	scanned := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(sessDir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		doc, err := memory.DecodeSession(string(raw))
		if err != nil {
			fmt.Fprintf(w, "skip %s: %v\n", e.Name(), err)
			continue
		}
		st := RepairDoc(doc, atts[doc.ID])
		st.File = e.Name()
		if !st.changed() {
			continue
		}
		damaged = append(damaged, st)
		total.StolenIDs += st.StolenIDs
		total.Renumbered += st.Renumbered
		total.ChipsRestore += st.ChipsRestore
		writes = append(writes, pending{path, memory.EncodeSession(doc)})
	}

	sort.Slice(damaged, func(i, j int) bool { return damaged[i].File < damaged[j].File })
	fmt.Fprintf(w, "%-18s %8s %10s %6s\n", "session", "thinking", "renumbered", "chips")
	for _, st := range damaged {
		fmt.Fprintf(w, "%-18s %8d %10d %6d\n", st.File, st.StolenIDs, st.Renumbered, st.ChipsRestore)
	}
	fmt.Fprintf(w, "\n%d of %d session files need repair: %d thinking ids restored, %d ids renumbered, %d attachment chips restored\n",
		len(damaged), scanned, total.StolenIDs, total.Renumbered, total.ChipsRestore)
	if !apply {
		fmt.Fprintln(w, "dry run — nothing written (re-run with -apply while the harness is stopped)")
		return nil
	}
	if len(writes) == 0 {
		return nil
	}
	backup := filepath.Join(memoryDir, "backups", "session-pre-repair-"+time.Now().UTC().Format("20060102T150405Z"))
	if err := copyDir(sessDir, backup); err != nil {
		return fmt.Errorf("backup failed, nothing written: %w", err)
	}
	fmt.Fprintf(w, "backup: %s\n", backup)
	for _, p := range writes {
		tmp := p.path + ".repair.tmp"
		if err := os.WriteFile(tmp, []byte(p.body), 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, p.path); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "rewrote %d files\n", len(writes))
	return nil
}

// RepairDoc fixes ids and chips in place. rows are the session's attachment rows.
func RepairDoc(doc *memory.SessionDoc, rows []attRow) Stats {
	var st Stats
	msgs := doc.Messages
	head := idHead(doc)
	maxSeq := 0
	for _, m := range msgs {
		if n := seqOf(m.ID); n > maxSeq {
			maxSeq = n
		}
	}
	fresh := func(role string) string {
		maxSeq++
		return prefixFor(role) + "-" + head + "-" + strconv.Itoa(maxSeq)
	}

	// 1) Stolen thinking ids. A run of id-less thinking messages after message P
	// means P carries the last thinking's id; ids were sequential, so P was N-r
	// and the run N-r+1..N.
	for i := 0; i < len(msgs); i++ {
		if msgs[i].Role != "thinking" || msgs[i].ID != "" {
			continue
		}
		j := i
		for j < len(msgs) && msgs[j].Role == "thinking" && msgs[j].ID == "" {
			j++
		}
		run := j - i
		p := i - 1
		if p >= 0 && strings.HasPrefix(msgs[p].ID, "th-") && msgs[p].Role != "thinking" {
			n := seqOf(msgs[p].ID)
			stolen := msgs[p].ID
			msgs[j-1].ID = stolen
			st.StolenIDs++
			for k := i; k < j-1; k++ {
				msgs[k].ID = claim(msgs, "th-"+head+"-"+strconv.Itoa(n-run+(k-i)+1), k, p, fresh, "thinking", &st)
			}
			msgs[p].ID = claim(msgs, prefixFor(msgs[p].Role)+"-"+head+"-"+strconv.Itoa(n-run), p, p, fresh, msgs[p].Role, &st)
		} else {
			for k := i; k < j; k++ {
				msgs[k].ID = fresh("thinking")
				st.Renumbered++
			}
		}
		i = j - 1
	}

	// 2) Duplicate ids: the first occurrence keeps it. (Empty ids are left
	// alone — sessions from before message ids existed are not damaged.)
	seen := map[string]bool{}
	for i := range msgs {
		if msgs[i].ID == "" {
			continue
		}
		if seen[msgs[i].ID] {
			msgs[i].ID = fresh(msgs[i].Role)
			st.Renumbered++
		}
		seen[msgs[i].ID] = true
	}

	// 3) Attachment chips.
	used := map[string]bool{}
	for _, m := range msgs {
		for _, a := range m.Attachments {
			used[a.ID] = true
		}
	}
	byID := map[string]attRow{}
	for _, r := range rows {
		byID[r.id] = r
	}
	for i := range msgs {
		m := &msgs[i]
		if len(m.Attachments) > 0 {
			continue
		}
		switch m.Role {
		case "attachment":
			var pick *attRow
			// The screenshot tool result right after the chip names its id.
			if i+1 < len(msgs) && msgs[i+1].Role == "tool" {
				if id := firstAttachmentID(msgs[i+1].Content); id != "" {
					if r, ok := byID[id]; ok && !used[id] {
						pick = &r
					}
				}
			}
			if pick == nil {
				pick = nearestRow(rows, used, strings.TrimSpace(m.Content), m.CreatedAt)
			}
			if pick != nil {
				used[pick.id] = true
				m.Attachments = []memory.TranscriptAttachment{chip(*pick)}
				st.ChipsRestore++
			}
		case "user":
			for _, r := range rows {
				if r.messageID == m.ID && !used[r.id] {
					used[r.id] = true
					m.Attachments = append(m.Attachments, chip(r))
					st.ChipsRestore++
				}
			}
		}
	}
	doc.Messages = msgs
	return st
}

// claim returns want for message idx unless a different message already holds
// it. A holder after p was created after the damaging reload (ids restarted
// low), so it gives the id up and is renumbered; otherwise idx gets a fresh id.
func claim(msgs []memory.TranscriptMessage, want string, idx, p int, fresh func(string) string, role string, st *Stats) string {
	for k := range msgs {
		if k == idx || msgs[k].ID != want {
			continue
		}
		if k > p {
			msgs[k].ID = fresh(msgs[k].Role)
			st.Renumbered++
			return want
		}
		st.Renumbered++
		return fresh(role)
	}
	return want
}

func chip(r attRow) memory.TranscriptAttachment {
	return memory.TranscriptAttachment{ID: r.id, Name: r.name, MIME: r.mime, Kind: r.kind, Size: r.size}
}

// nearestRow picks the unused row with this name created closest to at (±10 min).
func nearestRow(rows []attRow, used map[string]bool, name string, at time.Time) *attRow {
	var best *attRow
	var bestD time.Duration
	for i := range rows {
		r := rows[i]
		if used[r.id] || r.name != name {
			continue
		}
		d := r.created.Sub(at)
		if d < 0 {
			d = -d
		}
		if d > 10*time.Minute {
			continue
		}
		if best == nil || d < bestD {
			best, bestD = &rows[i], d
		}
	}
	return best
}

func firstAttachmentID(s string) string {
	const key = `"attachment_id":"`
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	rest := s[i+len(key):]
	if j := strings.IndexByte(rest, '"'); j > 0 {
		return rest[:j]
	}
	return ""
}

// idHead is the middle part of message ids ("m-0werf619-3" → "0werf619").
func idHead(doc *memory.SessionDoc) string {
	for _, m := range doc.Messages {
		if parts := strings.Split(m.ID, "-"); len(parts) == 3 && parts[1] != "" {
			return parts[1]
		}
	}
	if len(doc.ID) > 8 {
		return doc.ID[:8]
	}
	return doc.ID
}

func seqOf(id string) int {
	i := strings.LastIndexByte(id, '-')
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(id[i+1:])
	return n
}

func prefixFor(role string) string {
	switch role {
	case "thinking":
		return "th"
	case "tool":
		return "t"
	case "attachment":
		return "a"
	case "harness":
		return "h"
	}
	return "m"
}

func loadAttachments(dbPath string) (map[string][]attRow, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, err
	}
	d, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	rows, err := d.Query(`SELECT id, session_id, name, mime, kind, byte_size, created_at, COALESCE(message_id,'') FROM session_attachments`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]attRow{}
	for rows.Next() {
		var r attRow
		var sid, created string
		if err := rows.Scan(&r.id, &sid, &r.name, &r.mime, &r.kind, &r.size, &created, &r.messageID); err != nil {
			return nil, err
		}
		r.created, _ = time.Parse(time.RFC3339, created)
		out[sid] = append(out[sid], r)
	}
	return out, rows.Err()
}

// lockHolder returns the pid in marble.lock if that process is alive.
func lockHolder(memoryDir string) int {
	b, err := os.ReadFile(filepath.Join(memoryDir, "marble.lock"))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0
	}
	if syscall.Kill(pid, 0) == nil {
		return pid
	}
	return 0
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}
