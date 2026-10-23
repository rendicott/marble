package session

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rendicott/marble/internal/memory"
	"github.com/rendicott/marble/internal/model"
	"github.com/rendicott/marble/internal/token"
)

const (
	overageRescueTag  = "overage-rescue"
	overageTailPrefix = "…[overage-rescue"
	harnessMarkerMax  = 1500
)

// RescuePlan is the deterministic result of PlanRescue.
// At is left zero; the caller sets it (and optionally Archive) before ApplyPlan.
// PlanRescue does not read the clock.
type RescuePlan struct {
	Action          string  `json:"action"` // rescued | noop | unrecoverable
	Limit           int     `json:"context_limit"`
	Margin          int     `json:"margin"`
	MinOut          int     `json:"-"`
	Overhead        int     `json:"-"`
	Scale           float64 `json:"scale"`
	Target          int     `json:"target"`
	RoomForHistory  int     `json:"-"`
	BeforeEst       int     `json:"before_est"`
	AfterEst        int     `json:"after_est"`
	OmittedMessages int     `json:"omitted_messages"`
	StubbedMessages int     `json:"stubbed_messages"`
	OmittedImages   int     `json:"omitted_images"`
	Error           string  `json:"error,omitempty"`
	At              time.Time
	Archive         string

	edits []rescueEdit
}

type rescueEdit struct {
	Index int
	Omit  string
	Stub  string
}

// projectsToHistory reports whether a transcript role is copied into model
// history. Harness rows stay UI-only; the newest one is appended to the
// leading system message by LoadFromDoc.
func projectsToHistory(role string) bool {
	switch role {
	case "user", "assistant", "tool":
		return true
	default:
		return false
	}
}

// newestHarnessMarker is the newest harness row, whitespace-trimmed and
// capped at 1500 characters, ready to append to the leading system message.
// Empty when there is no harness row.
func newestHarnessMarker(msgs []memory.TranscriptMessage) string {
	var content string
	for _, m := range msgs {
		if m.Role != "harness" {
			continue
		}
		content = m.Content
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	if len(content) <= harnessMarkerMax {
		return content
	}
	content = content[:harnessMarkerMax]
	for len(content) > 0 && !utf8.ValidString(content) {
		content = content[:len(content)-1]
	}
	return content
}

// PlanRescue decides which transcript flags make the model projection fit
// limit-margin-minOut after overhead. It does not mutate msgs.
// scale is the provider/estimate ratio (1 when unknown). A message costs
// ceil(estimate * scale). overhead is the system prompt, soul, prompt pack,
// and tool schemas — not part of msgs.
func PlanRescue(msgs []memory.TranscriptMessage, overhead, limit, margin, minOut int, scale float64) (RescuePlan, error) {
	if limit <= 0 {
		return RescuePlan{}, fmt.Errorf("context limit must be positive")
	}
	if overhead < 0 || margin < 0 || minOut < 0 {
		return RescuePlan{}, fmt.Errorf("overhead, margin, and minOut must be >= 0")
	}
	if scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
		return RescuePlan{}, fmt.Errorf("scale must be a positive finite number")
	}
	target := limit - margin - minOut
	room := target - overhead
	before := projectionScaled(msgs, scale)
	plan := RescuePlan{
		Limit:          limit,
		Margin:         margin,
		MinOut:         minOut,
		Overhead:       overhead,
		Scale:          scale,
		Target:         target,
		RoomForHistory: room,
		BeforeEst:      before,
		AfterEst:       before,
	}
	if room <= 0 {
		plan.Action = "unrecoverable"
		plan.Error = fmt.Sprintf("fixed overhead %d exceeds target %d (limit %d, margin %d, min completion %d)", overhead, target, limit, margin, minOut)
		return plan, nil
	}
	if before <= room {
		plan.Action = "noop"
		return plan, nil
	}

	work := make([]memory.TranscriptMessage, len(msgs))
	copy(work, msgs)
	// Leave room for the harness marker ApplyPlan will append onto the system
	// message. The reported RoomForHistory stays the design value.
	fitRoom := room
	if reserve := rescueMarkerReserve(); room > reserve+32 {
		fitRoom = room - reserve
	}

	plan.OmittedImages = dropProjectionImages(work, fitRoom, scale)
	dropOldestRounds(work, fitRoom, scale)
	stubOrOmitToolClusters(work, fitRoom, scale)
	if !projectionFits(work, fitRoom, scale) {
		tailStubUntilFit(work, fitRoom, scale)
	}
	if !projectionFits(work, fitRoom, scale) {
		plan.Action = "unrecoverable"
		plan.OmittedImages = 0
		plan.Error = fmt.Sprintf("projection still does not fit room %d after stubs (scaled %d)", room, projectionScaled(work, scale))
		return plan, nil
	}

	for i := range work {
		if work[i].ModelOmit == msgs[i].ModelOmit && work[i].ModelStub == msgs[i].ModelStub {
			continue
		}
		plan.edits = append(plan.edits, rescueEdit{
			Index: i,
			Omit:  work[i].ModelOmit,
			Stub:  work[i].ModelStub,
		})
		wasOut := msgs[i].ModelOmit != "" && msgs[i].ModelStub == ""
		nowOut := work[i].ModelOmit != "" && work[i].ModelStub == ""
		if nowOut && !wasOut {
			plan.OmittedMessages++
		} else if work[i].ModelStub != "" && work[i].ModelStub != msgs[i].ModelStub {
			plan.StubbedMessages++
		}
	}
	if len(plan.edits) == 0 {
		plan.Action = "noop"
		plan.OmittedImages = 0
		return plan, nil
	}
	plan.Action = "rescued"
	plan.AfterEst = projectionScaled(work, scale)
	return plan, nil
}

// ApplyPlan returns a new document with plan's flags and, on rescued, one
// harness marker. noop and unrecoverable leave the transcript bytes unchanged.
// The caller archives the live file before writing the result.
func ApplyPlan(doc *memory.SessionDoc, plan RescuePlan) (*memory.SessionDoc, error) {
	if doc == nil {
		return nil, fmt.Errorf("nil session doc")
	}
	out := *doc
	out.Messages = append([]memory.TranscriptMessage(nil), doc.Messages...)
	if plan.Action != "rescued" {
		return &out, nil
	}
	for _, e := range plan.edits {
		if e.Index < 0 || e.Index >= len(out.Messages) {
			return nil, fmt.Errorf("rescue edit %d outside %d messages", e.Index, len(out.Messages))
		}
		out.Messages[e.Index].ModelOmit = e.Omit
		out.Messages[e.Index].ModelStub = e.Stub
	}
	at := plan.At
	if at.IsZero() {
		at = time.Unix(0, 0).UTC()
	}
	at = at.UTC()
	ts := at.Format("20060102T150405Z")
	archive := plan.Archive
	if archive == "" {
		archive = fmt.Sprintf("backups/overage/%s/%s.md", doc.ID, ts)
	}
	marker := formatRescueMarker(ts, plan, archive)
	out.Messages = append(out.Messages, memory.TranscriptMessage{
		ID:        "m-rescue-" + ts,
		Role:      "harness",
		Content:   marker,
		CreatedAt: at,
		ModelOmit: overageRescueTag,
	})
	out.UpdatedAt = at
	out.MessageCount = len(out.Messages)
	return &out, nil
}

func formatRescueMarker(ts string, plan RescuePlan, archive string) string {
	return fmt.Sprintf(
		"Context rescue %s: hid %d messages and %d images from the model\n(scaled estimate %d → %d, target %d, scale %s).\nFull text is still in this transcript. Pre-image: %s",
		ts,
		plan.OmittedMessages,
		plan.OmittedImages,
		plan.BeforeEst,
		plan.AfterEst,
		plan.RoomForHistory,
		strconv.FormatFloat(plan.Scale, 'f', -1, 64),
		archive,
	)
}

func rescueMarkerReserve() int {
	sample := formatRescueMarker(
		"20060102T150405Z",
		RescuePlan{
			OmittedMessages: 9999,
			OmittedImages:   9999,
			BeforeEst:       2_000_000,
			AfterEst:        2_000_000,
			RoomForHistory:  2_000_000,
			Scale:           1.25,
		},
		"backups/overage/0123456789/20060102T150405Z.md",
	)
	return token.Estimate(sample) + 8
}

func pureOmit(m memory.TranscriptMessage) bool {
	return m.ModelOmit != "" && m.ModelStub == ""
}

func projectedMessage(m memory.TranscriptMessage) (model.Message, bool) {
	if !projectsToHistory(m.Role) || pureOmit(m) {
		return model.Message{}, false
	}
	mm := model.Message{
		Role:       m.Role,
		Name:       m.ToolName,
		ToolCallID: m.ToolCallID,
	}
	if m.ModelStub != "" {
		mm.Content = model.ContentFromText(m.ModelStub)
		return mm, true
	}
	mm.Content = historyContentFromTranscript(m)
	return mm, true
}

func historyContentFromTranscript(m memory.TranscriptMessage) model.Content {
	um := Message{
		Role:        m.Role,
		Content:     m.Content,
		ToolName:    m.ToolName,
		ToolCallID:  m.ToolCallID,
		Attachments: uiAttachmentsFromTranscript(m.Attachments),
	}
	return historyContentFromUIMessage(um)
}

func projectionScaled(msgs []memory.TranscriptMessage, scale float64) int {
	var rest []model.Message
	for _, m := range msgs {
		if mm, ok := projectedMessage(m); ok {
			rest = append(rest, mm)
		}
	}
	rest = sanitizeToolCallHistory(rest)
	total := 0
	for _, m := range rest {
		total += scaledTokens(estimateAll([]model.Message{m}), scale)
	}
	return total
}

func projectionFits(msgs []memory.TranscriptMessage, room int, scale float64) bool {
	return projectionScaled(msgs, scale) <= room
}

func scaledTokens(n int, scale float64) int {
	if n <= 0 {
		return 0
	}
	v := math.Ceil(float64(n) * scale)
	if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return int(v)
}

func messageImageIDs(m memory.TranscriptMessage) []string {
	if !projectsToHistory(m.Role) || pureOmit(m) || m.ModelStub != "" {
		return nil
	}
	switch m.Role {
	case "user":
		var ids []string
		for _, a := range m.Attachments {
			if a.Kind == "image" && a.ID != "" {
				ids = append(ids, a.ID)
			}
		}
		return ids
	case "tool":
		return extractAttachmentIDs(m.Content)
	default:
		return nil
	}
}

// dropProjectionImages replaces image parts with a text placeholder, oldest
// messages first. The newest user message's images go last. A stub is text,
// so every image on a message is dropped together — that is what survives reload.
func dropProjectionImages(msgs []memory.TranscriptMessage, room int, scale float64) int {
	if projectionFits(msgs, room, scale) {
		return 0
	}
	newestUser := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && !pureOmit(msgs[i]) {
			newestUser = i
			break
		}
	}
	omitted := 0
	dropAt := func(i int) {
		ids := messageImageIDs(msgs[i])
		if len(ids) == 0 {
			return
		}
		msgs[i].ModelStub = imageOmittedStub(msgs[i], ids)
		omitted += len(ids)
	}
	for i := range msgs {
		if projectionFits(msgs, room, scale) {
			return omitted
		}
		if i == newestUser {
			continue
		}
		dropAt(i)
	}
	if newestUser >= 0 && !projectionFits(msgs, room, scale) {
		dropAt(newestUser)
	}
	return omitted
}

func imageOmittedStub(m memory.TranscriptMessage, ids []string) string {
	var b strings.Builder
	text := strings.TrimRight(m.Content, "\n")
	b.WriteString(text)
	for _, id := range ids {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "[image omitted %s]", id)
	}
	return b.String()
}

type msgRound struct {
	idxs []int
}

func splitRounds(msgs []memory.TranscriptMessage) []msgRound {
	var rounds []msgRound
	cur := -1
	for i, m := range msgs {
		if pureOmit(m) {
			continue
		}
		if m.Role == "user" || cur < 0 {
			rounds = append(rounds, msgRound{})
			cur = len(rounds) - 1
		}
		rounds[cur].idxs = append(rounds[cur].idxs, i)
	}
	return rounds
}

func roundHasProjection(msgs []memory.TranscriptMessage, rd msgRound) bool {
	for _, i := range rd.idxs {
		if _, ok := projectedMessage(msgs[i]); ok {
			return true
		}
	}
	return false
}

func dropOldestRounds(msgs []memory.TranscriptMessage, room int, scale float64) {
	for !projectionFits(msgs, room, scale) {
		rounds := splitRounds(msgs)
		var active []msgRound
		for _, rd := range rounds {
			if roundHasProjection(msgs, rd) {
				active = append(active, rd)
			}
		}
		if len(active) <= 1 {
			return
		}
		for _, i := range active[0].idxs {
			msgs[i].ModelOmit = overageRescueTag
			msgs[i].ModelStub = ""
		}
	}
}

type toolCluster struct {
	tools []int
}

func toolClusters(msgs []memory.TranscriptMessage, rd msgRound) []toolCluster {
	var out []toolCluster
	var tools []int
	flush := func() {
		if len(tools) > 0 {
			out = append(out, toolCluster{tools: append([]int(nil), tools...)})
		}
		tools = nil
	}
	for _, i := range rd.idxs {
		m := msgs[i]
		if pureOmit(m) {
			continue
		}
		switch m.Role {
		case "assistant", "user":
			flush()
		case "tool":
			tools = append(tools, i)
		}
	}
	flush()
	return out
}

func newestProjectedRound(msgs []memory.TranscriptMessage) (msgRound, bool) {
	rounds := splitRounds(msgs)
	for i := len(rounds) - 1; i >= 0; i-- {
		if roundHasProjection(msgs, rounds[i]) {
			return rounds[i], true
		}
	}
	return msgRound{}, false
}

func stubOrOmitToolClusters(msgs []memory.TranscriptMessage, room int, scale float64) {
	rd, ok := newestProjectedRound(msgs)
	if !ok {
		return
	}
	clusters := toolClusters(msgs, rd)
	for _, c := range clusters {
		if projectionFits(msgs, room, scale) {
			return
		}
		for _, ti := range c.tools {
			applyToolOmitStub(&msgs[ti])
		}
	}
	if projectionFits(msgs, room, scale) {
		return
	}
	// A cluster is omitted only when dropping all of its tool rows (together)
	// is what makes the rest fit. Otherwise the overflow is a later message
	// and pass 4 stubs that message.
	for _, c := range clusters {
		if projectionFits(msgs, room, scale) {
			return
		}
		if len(c.tools) == 0 {
			continue
		}
		if costWithout(msgs, c.tools, scale) <= room {
			for _, ti := range c.tools {
				msgs[ti].ModelOmit = overageRescueTag
				msgs[ti].ModelStub = ""
			}
		}
	}
}

func applyToolOmitStub(m *memory.TranscriptMessage) {
	if strings.HasPrefix(m.ModelStub, overageTailPrefix) {
		return
	}
	name := m.ToolName
	if name == "" {
		name = "tool"
	}
	line := fmt.Sprintf("[overage-rescue omitted tool result name=%s call_id=%s chars=%d]", name, m.ToolCallID, len(m.Content))
	if m.ModelStub != "" && token.Estimate(m.ModelStub) <= token.Estimate(line) {
		return
	}
	m.ModelStub = line
}

func costWithout(msgs []memory.TranscriptMessage, idxs []int, scale float64) int {
	type saved struct {
		omit, stub string
	}
	prev := make([]saved, len(idxs))
	for i, ix := range idxs {
		prev[i] = saved{msgs[ix].ModelOmit, msgs[ix].ModelStub}
		msgs[ix].ModelOmit = overageRescueTag
		msgs[ix].ModelStub = ""
	}
	n := projectionScaled(msgs, scale)
	for i, ix := range idxs {
		msgs[ix].ModelOmit = prev[i].omit
		msgs[ix].ModelStub = prev[i].stub
	}
	return n
}

func tailStubUntilFit(msgs []memory.TranscriptMessage, room int, scale float64) {
	exhausted := map[int]bool{}
	for !projectionFits(msgs, room, scale) {
		idx := largestProjected(msgs, exhausted)
		if idx < 0 {
			return
		}
		if !fitTail(msgs, idx, room, scale) {
			exhausted[idx] = true
		}
	}
}

func largestProjected(msgs []memory.TranscriptMessage, skip map[int]bool) int {
	best, bestN := -1, -1
	for i, m := range msgs {
		if skip[i] {
			continue
		}
		mm, ok := projectedMessage(m)
		if !ok {
			continue
		}
		n := estimateAll([]model.Message{mm})
		if n > bestN {
			bestN = n
			best = i
		}
	}
	return best
}

// fitTail sets model_stub to the longest suffix whose scaled cost lets the
// projection fit. A stub that already starts with the rescue prefix is
// shortened in place. Returns false when the stub did not change.
func fitTail(msgs []memory.TranscriptMessage, idx int, room int, scale float64) bool {
	m := msgs[idx]
	src := tailSource(m)
	prevStub := m.ModelStub
	id := m.ID
	if id == "" {
		id = "message"
	}
	lo, hi := 0, len(src)
	best := -1
	for lo <= hi {
		mid := (lo + hi) / 2
		msgs[idx].ModelStub = formatTail(src, mid, id)
		if projectionFits(msgs, room, scale) {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if best >= 0 {
		msgs[idx].ModelStub = formatTail(src, best, id)
	} else {
		msgs[idx].ModelStub = formatTail(src, 0, id)
	}
	return msgs[idx].ModelStub != prevStub
}

func tailSource(m memory.TranscriptMessage) string {
	if strings.HasPrefix(m.ModelStub, overageTailPrefix) {
		return tailBody(m.ModelStub)
	}
	if strings.HasPrefix(m.ModelStub, "[overage-rescue omitted tool result") {
		return m.ModelStub
	}
	return m.Content
}

func tailBody(stub string) string {
	if i := strings.IndexByte(stub, '\n'); i >= 0 && strings.HasPrefix(stub, overageTailPrefix) {
		return stub[i+1:]
	}
	return stub
}

func formatTail(src string, n int, id string) string {
	if n < 0 {
		n = 0
	}
	if n > len(src) {
		n = len(src)
	}
	suffix := ""
	if n > 0 {
		suffix = src[len(src)-n:]
	}
	return fmt.Sprintf("…[overage-rescue kept the last %d chars of %s]\n%s", n, id, suffix)
}
