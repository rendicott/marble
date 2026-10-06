package agentproc

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Liveness signal names (ADR-0035). Order is the order stuck_reason lists them.
const (
	SigSessionState = "session_state"
	SigProcIO       = "proc_io"
	SigProcCPU      = "proc_cpu"
	SigProcTree     = "proc_tree"
	SigOutput       = "output"
	SigCWD          = "cwd"
)

var signalOrder = []string{SigSessionState, SigProcIO, SigProcCPU, SigProcTree, SigOutput, SigCWD}

const (
	stateTailBytes   = 16 << 10
	stateMaxDirFiles = 64
	stateReglobEvery = 30 * time.Second // re-glob even when resolved (new files appear)
	stateRetryEvery  = 5 * time.Second  // re-glob while unresolved / after a file vanished
	procMinSampleGap = time.Second
	procClkTck       = 100 // USER_HZ on Linux
)

// procRoot is /proc; tests may point it elsewhere.
var procRoot = "/proc"

// SignalStatus is one entry of progress.signals.
type SignalStatus struct {
	Available bool   `json:"available"`
	AgeSec    int    `json:"age_sec"`
	Detail    string `json:"detail,omitempty"`

	lastAdvance time.Time
}

// liveTracker accumulates generic liveness signals for one background task.
// All fields are guarded by mu; I/O happens under mu but never under Manager.mu.
type liveTracker struct {
	mu sync.Mutex

	pid       int
	format    string
	sessionID string
	startedAt time.Time

	aliveWindow time.Duration
	minIO       float64 // bytes/sec
	minCPU      float64 // percent
	outBytes    func() int64

	// session_state
	globs      []string // expanded; empty → unavailable (stateWhy says why)
	stateWhy   string
	statePaths []string
	globbedAt  time.Time
	tailKey    string
	tailTS     time.Time
	phase      string
	stateAdv   time.Time
	stateFiles int
	stateBytes int64

	// process tree + output, sampled
	sampledAt time.Time
	sampled   bool // first sample is never throttled
	procOK    bool
	procWhy   string
	ioTotal   uint64
	cpuTicks  uint64
	kids      map[int]string
	outTotal  int64
	ioRate    float64
	cpuPct    float64
	ioAdv     time.Time
	cpuAdv    time.Time
	treeAdv   time.Time
	outAdv    time.Time
}

func newLiveTracker(format, sessionID string, startedAt time.Time, dcfg DriverConfig, cfg Config, env []string, outBytes func() int64) *liveTracker {
	lc := cfg.Liveness.withDefaults()
	lt := &liveTracker{
		format:      format,
		sessionID:   sessionID,
		startedAt:   startedAt,
		aliveWindow: cfg.AliveWindow(),
		minIO:       float64(lc.MinIOBytesPerSec),
		minCPU:      lc.MinCPUPercent,
		outBytes:    outBytes,
		sampledAt:   startedAt,
		procWhy:     "not sampled yet",
	}
	switch {
	case !dcfg.SessionPinned():
		lt.stateWhy = fmt.Sprintf("no session_id_flag for format=%s", format)
	case len(dcfg.StateGlobs) == 0:
		lt.stateWhy = fmt.Sprintf("no state_globs for format=%s", format)
	default:
		lt.globs = expandStateGlobs(dcfg.StateGlobs, sessionID, envLookup(env))
		if len(lt.globs) == 0 {
			lt.stateWhy = "every state_glob names an unset variable"
		}
	}
	return lt
}

func (lt *liveTracker) setPID(pid int) {
	lt.mu.Lock()
	lt.pid = pid
	lt.mu.Unlock()
}

// tick is the background sampler's entry point.
func (lt *liveTracker) tick(now time.Time) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	lt.sampleProc(now)
}

// observe samples everything and returns signal statuses (cwd excluded; the caller owns it).
func (lt *liveTracker) observe(now time.Time) (map[string]SignalStatus, string) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	lt.sampleProc(now)
	lt.sampleState(now)

	out := map[string]SignalStatus{}
	if len(lt.globs) == 0 {
		out[SigSessionState] = SignalStatus{Detail: lt.stateWhy}
	} else if len(lt.statePaths) == 0 {
		out[SigSessionState] = SignalStatus{Detail: "no state matched for session " + lt.sessionID}
	} else {
		d := fmt.Sprintf("session %s, %d files, %d bytes", lt.sessionID, lt.stateFiles, lt.stateBytes)
		if !lt.tailTS.IsZero() {
			d += ", last entry " + lt.tailTS.UTC().Format(time.RFC3339)
		}
		if lt.phase != "" {
			d += ", phase=" + lt.phase
		}
		out[SigSessionState] = SignalStatus{Available: true, Detail: d, lastAdvance: lt.stateAdv}
	}

	if lt.procOK {
		out[SigProcIO] = SignalStatus{Available: true, lastAdvance: lt.ioAdv,
			Detail: fmt.Sprintf("%.0f B/s (min %.0f)", lt.ioRate, lt.minIO)}
		out[SigProcCPU] = SignalStatus{Available: true, lastAdvance: lt.cpuAdv,
			Detail: fmt.Sprintf("%.1f%% (min %.1f%%)", lt.cpuPct, lt.minCPU)}
		out[SigProcTree] = SignalStatus{Available: true, lastAdvance: lt.treeAdv, Detail: kidsDetail(lt.kids)}
	} else {
		for _, k := range []string{SigProcIO, SigProcCPU, SigProcTree} {
			out[k] = SignalStatus{Detail: lt.procWhy}
		}
	}
	if lt.outBytes != nil {
		out[SigOutput] = SignalStatus{Available: true, lastAdvance: lt.outAdv,
			Detail: fmt.Sprintf("%d bytes", lt.outTotal)}
	}
	return out, lt.phase
}

// sampleProc updates process-tree and output signals. Caller holds lt.mu.
func (lt *liveTracker) sampleProc(now time.Time) {
	if lt.sampled && now.Sub(lt.sampledAt) < procMinSampleGap {
		return
	}
	dt := now.Sub(lt.sampledAt).Seconds()
	if dt < 0.001 {
		dt = 0.001
	}
	lt.sampledAt = now

	if lt.outBytes != nil {
		if n := lt.outBytes(); n > lt.outTotal {
			lt.outTotal = n
			lt.outAdv = now
		}
	}
	if lt.pid <= 0 {
		return
	}
	lt.sampled = true
	tree, err := readProcTree(lt.pid)
	if err != nil {
		lt.procOK = false
		lt.procWhy = err.Error()
		return
	}
	lt.procOK = true

	var dIO, dCPU uint64
	if tree.io > lt.ioTotal {
		dIO = tree.io - lt.ioTotal
	}
	if tree.cpu > lt.cpuTicks {
		dCPU = tree.cpu - lt.cpuTicks
	}
	lt.ioTotal, lt.cpuTicks = tree.io, tree.cpu
	lt.ioRate = float64(dIO) / dt
	lt.cpuPct = float64(dCPU) / procClkTck / dt * 100
	if lt.ioRate >= lt.minIO {
		lt.ioAdv = now
	}
	if lt.cpuPct >= lt.minCPU {
		lt.cpuAdv = now
	}
	if !samePIDs(lt.kids, tree.kids) {
		if lt.kids != nil || len(tree.kids) > 0 {
			lt.treeAdv = now
		}
	}
	lt.kids = tree.kids
}

// sampleState refreshes the session_state signal. Caller holds lt.mu.
func (lt *liveTracker) sampleState(now time.Time) {
	if len(lt.globs) == 0 {
		return
	}
	stats, missing := statAll(lt.statePaths)
	regl := now.Sub(lt.globbedAt) >= stateReglobEvery
	if (len(lt.statePaths) == 0 || missing) && now.Sub(lt.globbedAt) >= stateRetryEvery {
		regl = true
	}
	if regl {
		lt.statePaths = resolveStatePaths(lt.globs)
		lt.globbedAt = now
		stats, _ = statAll(lt.statePaths)
	}
	if len(stats) == 0 {
		return
	}
	var newest time.Time
	var total int64
	var jsonl os.FileInfo
	var jsonlPath string
	for p, fi := range stats {
		total += fi.Size()
		if fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
		if strings.HasSuffix(p, ".jsonl") && (jsonl == nil || fi.ModTime().After(jsonl.ModTime())) {
			jsonl, jsonlPath = fi, p
		}
	}
	lt.stateFiles, lt.stateBytes = len(stats), total
	if jsonl != nil {
		key := fmt.Sprintf("%s|%d|%d", jsonlPath, jsonl.Size(), jsonl.ModTime().UnixNano())
		if key != lt.tailKey {
			lt.tailKey = key
			ts, phase := tailJSONL(jsonlPath, jsonl.Size())
			if !ts.IsZero() {
				lt.tailTS = ts
			}
			if phase != "" {
				lt.phase = phase
			}
		}
	}
	adv := newest
	if lt.tailTS.After(adv) {
		adv = lt.tailTS
	}
	if adv.After(now) {
		adv = now // clock skew between the child's timestamps and ours
	}
	if adv.After(lt.stateAdv) {
		lt.stateAdv = adv
	}
}

// expandStateGlobs substitutes {session_id}, ~/ and $VARS. A pattern naming an
// unset (empty) variable is dropped so "$GROK_HOME/..." falls through to "~/.grok/...".
func expandStateGlobs(patterns []string, sessionID string, getenv func(string) string) []string {
	home := getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" || sessionID == "" {
			continue
		}
		p = strings.ReplaceAll(p, "{session_id}", sessionID)
		if p == "~" || strings.HasPrefix(p, "~/") {
			if home == "" {
				continue
			}
			p = home + p[1:]
		}
		unset := false
		p = os.Expand(p, func(k string) string {
			v := getenv(k)
			if v == "" {
				unset = true
			}
			return v
		})
		if unset || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, filepath.Clean(p))
	}
	return out
}

// envLookup resolves vars from the child's env slice (later entries win), falling back to ours.
func envLookup(env []string) func(string) string {
	m := map[string]string{}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	return func(k string) string {
		if v, ok := m[k]; ok {
			return v
		}
		return os.Getenv(k)
	}
}

// resolveStatePaths globs each pattern; a matched dir contributes its direct regular files.
func resolveStatePaths(globs []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, g := range globs {
		matches, err := filepath.Glob(g)
		if err != nil {
			continue
		}
		for _, m := range matches {
			fi, err := os.Stat(m)
			if err != nil {
				continue
			}
			if !fi.IsDir() {
				add(m)
				continue
			}
			ents, err := os.ReadDir(m)
			if err != nil {
				continue
			}
			n := 0
			for _, e := range ents {
				if !e.Type().IsRegular() || strings.HasSuffix(e.Name(), ".lock") {
					continue
				}
				add(filepath.Join(m, e.Name()))
				if n++; n >= stateMaxDirFiles {
					break
				}
			}
		}
	}
	return out
}

func statAll(paths []string) (map[string]os.FileInfo, bool) {
	out := make(map[string]os.FileInfo, len(paths))
	missing := false
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			missing = true
			continue
		}
		out[p] = fi
	}
	return out, missing
}

// tailJSONL reads at most stateTailBytes from the end of path and scans backwards for
// the newest timestamp (ts|timestamp|time) and phase. Unparseable lines are skipped.
func tailJSONL(path string, size int64) (time.Time, string) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, ""
	}
	defer f.Close()
	off := size - stateTailBytes
	if off < 0 {
		off = 0
	}
	buf, err := io.ReadAll(io.NewSectionReader(f, off, size-off))
	if err != nil {
		return time.Time{}, ""
	}
	lines := strings.Split(string(buf), "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // first line is almost certainly partial
	}
	var ts time.Time
	phase := ""
	for i := len(lines) - 1; i >= 0 && (ts.IsZero() || phase == ""); i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || line[0] != '{' {
			continue
		}
		var m map[string]interface{}
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		if ts.IsZero() {
			ts = entryTime(m)
		}
		if phase == "" {
			if s, ok := m["phase"].(string); ok {
				phase = s
			}
		}
	}
	return ts, phase
}

func entryTime(m map[string]interface{}) time.Time {
	for _, k := range []string{"ts", "timestamp", "time"} {
		switch v := m[k].(type) {
		case string:
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				return t
			}
		case float64:
			if v > 1e12 {
				return time.UnixMilli(int64(v))
			}
			if v > 1e9 {
				return time.Unix(int64(v), 0)
			}
		}
	}
	return time.Time{}
}

type procTree struct {
	io   uint64         // rchar+wchar of live members
	cpu  uint64         // utime+stime+cutime+cstime of live members (ticks)
	kids map[int]string // descendants (excluding root) → comm
}

type procStat struct {
	comm string
	ppid int
	cpu  uint64
}

// readProcTree walks the descendants of root via the ppid chain. Tools often run in
// their own process group, so pgid is not enough (ADR-0035 evidence).
func readProcTree(root int) (procTree, error) {
	if _, err := readProcStat(root); err != nil {
		return procTree{}, fmt.Errorf("no %s entry for pid %d", procRoot, root)
	}
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return procTree{}, fmt.Errorf("%s unreadable", procRoot)
	}
	stats := map[int]procStat{}
	children := map[int][]int{}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		st, err := readProcStat(pid)
		if err != nil {
			continue
		}
		stats[pid] = st
		children[st.ppid] = append(children[st.ppid], pid)
	}
	tree := procTree{kids: map[int]string{}}
	queue := []int{root}
	visited := map[int]bool{}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if visited[pid] {
			continue
		}
		visited[pid] = true
		st, ok := stats[pid]
		if !ok {
			continue
		}
		tree.cpu += st.cpu
		tree.io += readProcIO(pid)
		if pid != root {
			tree.kids[pid] = st.comm
		}
		queue = append(queue, children[pid]...)
	}
	return tree, nil
}

func readProcStat(pid int) (procStat, error) {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return procStat{}, err
	}
	s := string(b)
	lp, rp := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if lp < 0 || rp < lp || rp+2 > len(s) {
		return procStat{}, fmt.Errorf("bad stat")
	}
	f := strings.Fields(s[rp+2:])
	// f[0]=state f[1]=ppid … f[11]=utime f[12]=stime f[13]=cutime f[14]=cstime
	if len(f) < 15 {
		return procStat{}, fmt.Errorf("short stat")
	}
	ppid, _ := strconv.Atoi(f[1])
	var cpu uint64
	for _, i := range []int{11, 12, 13, 14} {
		n, _ := strconv.ParseUint(f[i], 10, 64)
		cpu += n
	}
	return procStat{comm: s[lp+1 : rp], ppid: ppid, cpu: cpu}, nil
}

func readProcIO(pid int) uint64 {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "io"))
	if err != nil {
		return 0
	}
	var total uint64
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok || (k != "rchar" && k != "wchar") {
			continue
		}
		n, _ := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		total += n
	}
	return total
}

func samePIDs(a, b map[int]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func kidsDetail(kids map[int]string) string {
	if len(kids) == 0 {
		return "0 descendants"
	}
	names := make([]string, 0, len(kids))
	for _, c := range kids {
		names = append(names, c)
	}
	sort.Strings(names)
	if len(names) > 4 {
		names = append(names[:4], "…")
	}
	return fmt.Sprintf("%d descendants: %s", len(kids), strings.Join(names, ", "))
}
