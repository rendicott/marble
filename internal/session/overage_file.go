package session

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rendicott/marble/internal/memory"
	"github.com/rendicott/marble/internal/model"
	"github.com/rendicott/marble/internal/tools"

	_ "modernc.org/sqlite"
)

// FileRescueRequest is one offline overage-rescue of a session markdown file.
// The CLI has no live session, so this path never writes a session_events row.
type FileRescueRequest struct {
	MemoryDir string
	ID        string
	Apply     bool
	// Explicit is set when the operator passed -context-limit, -max-output, and
	// -context-reserve together. The triple is used only when the catalog has
	// no enabled chat row for the session model. ContextReserve 0 is legal.
	Explicit       bool
	ContextLimit   int
	MaxOutput      int
	ContextReserve int
	At             time.Time
}

// RescueFile decodes session/<id>.md, runs PlanRescue with the same margin,
// minimum completion, overhead, and scale as Registry.OverageRescue, and on
// -apply archives the file before writing. Dry run does not take marble.lock
// and does not write. There is no event row: nothing here owns the live DB
// sequence, and a running harness is the process that appends events.
func RescueFile(req FileRescueRequest) (RescueResult, error) {
	id := strings.TrimSpace(req.ID)
	if err := checkSessionID(id); err != nil {
		return RescueResult{}, err
	}
	root, err := filepath.Abs(req.MemoryDir)
	if err != nil {
		return RescueResult{}, err
	}
	if req.Apply {
		if pid := fileLockHolder(root); pid > 0 {
			return RescueResult{}, fmt.Errorf("harness pid %d holds %s — stop it first (a running harness would overwrite the file on its next save)", pid, filepath.Join(root, "marble.lock"))
		}
	}
	path := filepath.Join(root, "session", id+".md")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return RescueResult{}, fmt.Errorf("session not found")
		}
		return RescueResult{}, err
	}
	doc, err := memory.DecodeSession(string(raw))
	if err != nil {
		return RescueResult{}, fmt.Errorf("decode session: %w", err)
	}
	if doc.ID != "" && doc.ID != id {
		return RescueResult{}, fmt.Errorf("session id %q does not match file id %q", id, doc.ID)
	}
	if doc.ID == "" {
		doc.ID = id
	}
	if doc.Status == "closed" {
		return RescueResult{}, fmt.Errorf("session is closed")
	}

	limit, capTools, err := resolveRescueLimit(root, doc, req)
	if err != nil {
		return RescueResult{}, err
	}
	margin := rescueMargin(limit)
	minOut := retryMinOut
	soul := readSoulFile(root)
	var specs []model.ToolSpec
	if capTools {
		// Built-in schemas only. MCP tools are process state and are not on disk.
		specs = (&tools.Registry{}).Specs()
	}
	overhead := rescueOverheadParts(doc.Kind, soul, false, specs, doc.Messages)
	obs, ok := transcriptContextObservation(doc.Messages, overhead)
	scale := rescueScale(obs, ok)
	plan, err := PlanRescue(doc.Messages, overhead, limit, margin, minOut, scale)
	if err != nil {
		return RescueResult{}, err
	}
	res := rescueResultFromPlan(plan)
	res.DryRun = !req.Apply
	if !req.Apply || plan.Action != "rescued" {
		return res, nil
	}

	at := req.At
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	rel := "backups/overage/" + id + "/" + at.Format("20060102T150405Z") + ".md"
	abs, err := rescueArchiveAbs(root, id, rel)
	if err != nil {
		return RescueResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return RescueResult{}, fmt.Errorf("archive: %w", err)
	}
	// Copy the pre-image bytes. A failed copy leaves the live file alone.
	if err := os.WriteFile(abs, raw, 0o644); err != nil {
		return RescueResult{}, fmt.Errorf("archive: %w", err)
	}
	plan.At = at
	plan.Archive = rel
	newDoc, err := ApplyPlan(doc, plan)
	if err != nil {
		return RescueResult{}, fmt.Errorf("persist failed after archive %s: %w", rel, err)
	}
	store, err := memory.New(root)
	if err != nil {
		return RescueResult{}, fmt.Errorf("persist failed after archive %s: %w", rel, err)
	}
	if err := store.WriteSession(newDoc); err != nil {
		return RescueResult{}, fmt.Errorf("persist failed after archive %s: %w", rel, err)
	}
	res.Archive = rel
	res.AfterEst = plan.AfterEst
	return res, nil
}

func resolveRescueLimit(root string, doc *memory.SessionDoc, req FileRescueRequest) (limit int, capTools bool, err error) {
	if req.Explicit {
		if req.ContextLimit <= 0 || req.MaxOutput <= 0 || req.ContextReserve < 0 {
			return 0, false, fmt.Errorf("context-limit, max-output, and context-reserve must be set together (positive limit and max-output; reserve >= 0)")
		}
	}
	modelID := strings.TrimSpace(doc.ModelID)
	if modelID == "" {
		modelID = lookupSessionModelID(root, doc.ID)
	}
	if modelID != "" {
		row, found, qerr := lookupCatalogRow(root, modelID)
		if qerr != nil && !req.Explicit {
			return 0, false, qerr
		}
		if qerr == nil && found {
			return row.limit, row.capTools, nil
		}
	}
	if !req.Explicit {
		if modelID != "" {
			return 0, false, fmt.Errorf("model_id %q is not an enabled chat model in the catalog; pass -context-limit, -max-output, and -context-reserve", modelID)
		}
		return 0, false, fmt.Errorf("session has no catalog model_id; pass -context-limit, -max-output, and -context-reserve")
	}
	// Process default is tools on. Reserve is not added: Registry.rescueParams
	// sizes margin with rescueMargin and min completion with retryMinOut.
	return req.ContextLimit, true, nil
}

type rescueCatalog struct {
	limit    int
	capTools bool
}

func lookupCatalogRow(root, modelID string) (rescueCatalog, bool, error) {
	path := filepath.Join(root, "marble.db")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return rescueCatalog{}, false, nil
		}
		return rescueCatalog{}, false, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return rescueCatalog{}, false, fmt.Errorf("catalog: %w", err)
	}
	defer db.Close()
	var limit, cap, enabled int
	var kind string
	err = db.QueryRow(`SELECT context_limit, cap_tools, enabled, COALESCE(kind,'chat') FROM model_catalog WHERE id = ?`, modelID).Scan(&limit, &cap, &enabled, &kind)
	if err == sql.ErrNoRows {
		return rescueCatalog{}, false, nil
	}
	if err != nil {
		return rescueCatalog{}, false, fmt.Errorf("catalog: %w", err)
	}
	if enabled == 0 || strings.EqualFold(kind, "image") || limit <= 0 {
		return rescueCatalog{}, false, nil
	}
	return rescueCatalog{limit: limit, capTools: cap != 0}, true, nil
}

func lookupSessionModelID(root, id string) string {
	path := filepath.Join(root, "marble.db")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return ""
	}
	defer db.Close()
	var modelID string
	err = db.QueryRow(`SELECT model_id FROM sessions WHERE id = ?`, id).Scan(&modelID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(modelID)
}

func readSoulFile(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "soul.md"))
	if err != nil {
		return ""
	}
	return string(b)
}

var reHarnessEst = regexp.MustCompile(`\(est (\d+), window \d+`)

// transcriptContextObservation reads a provider message count from the newest
// assistant or harness row. The in-process Est is not on disk; a harness
// "(est N, window …)" in that row wins, otherwise Est is the current
// projection plus overhead.
func transcriptContextObservation(msgs []memory.TranscriptMessage, overhead int) (ContextLimitObservation, bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		role := msgs[i].Role
		if role != "assistant" && role != "harness" {
			continue
		}
		cle := model.ParseContextLimit(400, msgs[i].Content)
		if cle == nil || cle.Messages <= 0 {
			continue
		}
		est := 0
		if m := reHarnessEst.FindStringSubmatch(msgs[i].Content); len(m) == 2 {
			est, _ = strconv.Atoi(m[1])
		}
		if est <= 0 {
			est = projectionScaled(msgs, 1) + overhead
		}
		if est <= 0 {
			continue
		}
		return ContextLimitObservation{
			Limit:      cle.Limit,
			Messages:   cle.Messages,
			Completion: cle.Completion,
			Est:        est,
		}, true
	}
	return ContextLimitObservation{}, false
}

func checkSessionID(id string) error {
	if id == "" || id != filepath.Base(id) || strings.Contains(id, "..") || strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("session id required (-session ID or a single positional id); refusing to scan every session")
	}
	return nil
}

// fileLockHolder is copied from sessionrepair.lockHolder.
func fileLockHolder(memoryDir string) int {
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

func rescueArchiveAbs(root, id, rel string) (string, error) {
	if err := checkSessionID(id); err != nil {
		return "", fmt.Errorf("archive: bad session id")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("archive: %w", err)
	}
	abs, err := filepath.Abs(filepath.Join(absRoot, filepath.FromSlash(rel)))
	if err != nil {
		return "", fmt.Errorf("archive: %w", err)
	}
	if abs != absRoot && !strings.HasPrefix(abs, absRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive: path escapes memory root")
	}
	return abs, nil
}
