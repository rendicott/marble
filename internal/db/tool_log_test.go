package db

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMigrateV15toV16(t *testing.T) {
	root := t.TempDir()
	d, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL.Exec(`DROP TABLE tool_call_log`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL.Exec(`UPDATE schema_meta SET schema_version = 15 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	d.Close()

	d2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if d2.Mode != ModeNormal {
		t.Fatalf("mode %s: %s", d2.Mode, d2.Reason)
	}
	var ver int
	if err := d2.SQL.QueryRow(`SELECT schema_version FROM schema_meta WHERE id = 1`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != CurrentSchemaVersion {
		t.Fatalf("schema %d want %d", ver, CurrentSchemaVersion)
	}
	var n int
	if err := d2.SQL.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='tool_call_log'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("table: %d %v", n, err)
	}
	for _, idx := range []string{"idx_tool_call_log_session_seq", "idx_tool_call_log_tool_ts"} {
		var c int
		if err := d2.SQL.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&c); err != nil || c != 1 {
			t.Fatalf("index %s: %d %v", idx, c, err)
		}
	}
}

func TestListToolCallLogsNewestFirst(t *testing.T) {
	d := openWatchDB(t)
	insertLog(t, d, ToolCallLog{SessionID: "s1", TS: "2026-11-01T00:00:01Z", ToolName: "memory_search", ArgsJSON: `{"q":"old"}`, OK: 1, LatencyMS: 1, ResultChars: 3})
	insertLog(t, d, ToolCallLog{SessionID: "s1", TS: "2026-11-01T00:00:03Z", ToolName: "file_read", ArgsJSON: `{"path":"a"}`, OK: 1, LatencyMS: 2, ResultChars: 4})
	insertLog(t, d, ToolCallLog{SessionID: "s2", TS: "2026-11-01T00:00:02Z", ToolName: "memory_search", ArgsJSON: `{"q":"mid"}`, OK: 1, LatencyMS: 3, ResultChars: 5})

	rows, err := d.ListToolCallLogs("", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("len %d", len(rows))
	}
	if rows[0].TS != "2026-11-01T00:00:03Z" || rows[1].TS != "2026-11-01T00:00:02Z" || rows[2].TS != "2026-11-01T00:00:01Z" {
		t.Fatalf("order %+v %+v %+v", rows[0].TS, rows[1].TS, rows[2].TS)
	}
	if rows[0].Seq != 2 || rows[2].Seq != 1 {
		t.Fatalf("seq per session: first row seq %d third %d", rows[0].Seq, rows[2].Seq)
	}

	filtered, err := d.ListToolCallLogs("s1", "memory_search", "2026-11-01T00:00:01Z", 1001)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].ArgsJSON != `{"q":"old"}` {
		t.Fatalf("filter %+v", filtered)
	}
	capped, err := d.ListToolCallLogs("", "", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(capped) != 2 {
		t.Fatalf("limit %d", len(capped))
	}
}

func TestToolCallLogStats(t *testing.T) {
	d := openWatchDB(t)
	// s1: same args twice (one empty), then an error, then a different tool with the first args.
	insertLog(t, d, logRow("s1", "2026-11-01T00:00:01Z", "memory_search", `{"q":"a"}`, 1, "", 10, 100, ip(2), summaryJSON(false, 2, "alpha", "beta")))
	insertLog(t, d, logRow("s1", "2026-11-01T00:00:02Z", "memory_search", `{"q":"a"}`, 1, "", 30, 10, ip(0), summaryJSON(true, 0)))
	insertLog(t, d, logRow("s1", "2026-11-01T00:00:03Z", "memory_search", `{"q":"b"}`, 0, "boom", 20, 5, nil, ""))
	insertLog(t, d, logRow("s1", "2026-11-01T00:00:04Z", "file_read", `{"q":"a"}`, 1, "", 40, 50, ip(1), summaryJSON(false, 1, "alpha")))
	// s2 repeats the same args. A different session does not continue s1's run.
	insertLog(t, d, logRow("s2", "2026-11-01T00:00:05Z", "memory_search", `{"q":"a"}`, 1, "", 50, 8, ip(3), summaryJSON(false, 3, "gamma")))
	insertLog(t, d, logRow("s2", "2026-11-01T00:00:06Z", "memory_search", `{"q":"a"}`, 1, "", 60, 12, ip(3), summaryJSON(false, 3, "gamma")))
	// Eleven distinct paths, all count 1, so the histogram of docs keeps the first ten names.
	var many []string
	for i := 1; i <= 11; i++ {
		many = append(many, pathN(i))
	}
	insertLog(t, d, logRow("s3", "2026-11-01T00:00:07Z", "memory_search", `{"q":"z"}`, 1, "", 70, 20, ip(11), summaryJSON(false, 11, many...)))

	st, err := d.ToolCallLogStats("", "")
	if err != nil {
		t.Fatal(err)
	}
	if st.CallCount != 7 || st.ErrorCount != 1 || st.TimeoutCount != 0 {
		t.Fatalf("counts %+v", st)
	}
	if st.EmptyResultRate != 1.0/7.0 {
		t.Fatalf("empty rate %v", st.EmptyResultRate)
	}
	if st.ConsecutiveSameArgs != 2 {
		t.Fatalf("repeats %d", st.ConsecutiveSameArgs)
	}
	if st.LatencyMSMean != 40 || st.LatencyMSMedian != 40 {
		t.Fatalf("latency mean %v median %v", st.LatencyMSMean, st.LatencyMSMedian)
	}
	if st.ResultCharsMedian != 12 {
		t.Fatalf("chars median %v", st.ResultCharsMedian)
	}
	wantHits := []HitCountBucket{{0, 1}, {1, 1}, {2, 1}, {3, 2}, {11, 1}}
	if len(st.HitCountHistogram) != len(wantHits) {
		t.Fatalf("histogram %+v", st.HitCountHistogram)
	}
	for i, b := range wantHits {
		if st.HitCountHistogram[i] != b {
			t.Fatalf("histogram %+v", st.HitCountHistogram)
		}
	}
	if len(st.TopDocs) != 10 {
		t.Fatalf("top docs %d %+v", len(st.TopDocs), st.TopDocs)
	}
	// alpha and gamma appear twice; the eleven pXX paths appear once and lose p11.
	if st.TopDocs[0].Path != "alpha" || st.TopDocs[0].Count != 2 || st.TopDocs[1].Path != "gamma" || st.TopDocs[1].Count != 2 {
		t.Fatalf("top %+v", st.TopDocs[:2])
	}
	if st.TopDocs[len(st.TopDocs)-1].Path == "p11" {
		t.Fatalf("p11 should be cut: %+v", st.TopDocs)
	}

	mem, err := d.ToolCallLogStats("memory_search", "")
	if err != nil {
		t.Fatal(err)
	}
	if mem.CallCount != 6 || mem.ErrorCount != 1 || mem.ConsecutiveSameArgs != 2 {
		t.Fatalf("filtered %+v", mem)
	}
	if mem.EmptyResultRate != 1.0/6.0 {
		t.Fatalf("filtered empty %v", mem.EmptyResultRate)
	}
	// file_read is outside the filter, so it does not break or extend the memory_search run.
	since, err := d.ToolCallLogStats("", "2026-11-01T00:00:05Z")
	if err != nil {
		t.Fatal(err)
	}
	if since.CallCount != 3 || since.ConsecutiveSameArgs != 1 || since.ErrorCount != 0 {
		t.Fatalf("since %+v", since)
	}
}

func TestToolLogStatsTimeoutCount(t *testing.T) {
	d := openWatchDB(t)
	insertLog(t, d, logRow("s1", "2026-11-01T00:00:01Z", "shell_execute", `{"command":"sleep 5"}`, 1, "", 1000, 40, ip(0), `{"hits":0,"empty":true,"data":{"timed_out":true,"killed_timeout":true}}`))
	insertLog(t, d, logRow("s1", "2026-11-01T00:00:02Z", "shell_execute", `{"command":"sleep 5"}`, 1, "", 1000, 40, ip(0), `{"hits":0,"empty":true,"data":{"killed_timeout":true,"timed_out":true}}`))
	insertLog(t, d, logRow("s1", "2026-11-01T00:00:03Z", "shell_execute", `{"command":"true"}`, 1, "", 5, 20, ip(0), `{"hits":0,"empty":true,"data":{"timed_out":false}}`))
	insertLog(t, d, logRow("s1", "2026-11-01T00:00:04Z", "shell_execute", `{"command":"y"}`, 1, "", 2, 8, ip(0), `{"hits":0,"empty":false,"data":{"killed_timeout":true,"timed_out":false}}`))
	insertLog(t, d, logRow("s1", "2026-11-01T00:00:05Z", "shell_execute", `{"command":"x"}`, 0, "nope", 1, 8, nil, ""))

	st, err := d.ToolCallLogStats("shell_execute", "")
	if err != nil {
		t.Fatal(err)
	}
	if st.CallCount != 5 || st.ErrorCount != 1 || st.TimeoutCount != 2 {
		t.Fatalf("counts %+v", st)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	errAt := strings.Index(body, `"error_count"`)
	toAt := strings.Index(body, `"timeout_count"`)
	emptyAt := strings.Index(body, `"empty_result_rate"`)
	if errAt < 0 || toAt < errAt || emptyAt < toAt {
		t.Fatalf("field order %s", body)
	}
	if !strings.Contains(body, `"call_count":5`) || !strings.Contains(body, `"error_count":1`) || !strings.Contains(body, `"timeout_count":2`) {
		t.Fatalf("json %s", body)
	}
}

func insertLog(t *testing.T, d *DB, row ToolCallLog) {
	t.Helper()
	if err := d.InsertToolCallLog(row); err != nil {
		t.Fatal(err)
	}
}

func logRow(sessionID, ts, tool, args string, ok int, errText string, lat int64, chars int, hits *int, summary string) ToolCallLog {
	return ToolCallLog{
		SessionID: sessionID, TS: ts, ToolName: tool, ArgsJSON: args,
		OK: ok, Error: errText, LatencyMS: lat, ResultChars: chars,
		HitCount: hits, ResultSummaryJSON: summary,
	}
}

func ip(n int) *int { return &n }

func pathN(i int) string {
	if i < 10 {
		return "p0" + string(rune('0'+i))
	}
	return "p" + string(rune('0'+i/10)) + string(rune('0'+i%10))
}

func summaryJSON(empty bool, hits int, paths ...string) string {
	docs := make([]map[string]string, 0, len(paths))
	for _, p := range paths {
		docs = append(docs, map[string]string{"path": p})
	}
	b, err := json.Marshal(map[string]any{
		"hits":  hits,
		"empty": empty,
		"data":  map[string]any{"docs": docs},
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestToolLogStatsDistinctQueryRate(t *testing.T) {
	d, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// The baseline pattern: the same search asked six times in a row.
	for i := 0; i < 6; i++ {
		if err := d.InsertToolCallLog(ToolCallLog{
			SessionID: "s1", TS: "2026-11-28T09:39:3" + string(rune('0'+i)) + "Z", ToolName: "memory_search",
			ArgsJSON: `{"query":"windows signing certificate SmartScreen EV"}`, OK: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	st, err := d.ToolCallLogStats("memory_search", "")
	if err != nil {
		t.Fatal(err)
	}
	if st.ConsecutiveSameArgs != 5 {
		t.Fatalf("consecutive_same_args %d want 5", st.ConsecutiveSameArgs)
	}
	if st.DistinctQueryRate < 0.166 || st.DistinctQueryRate > 0.167 {
		t.Fatalf("distinct_query_rate %v want 1/6", st.DistinctQueryRate)
	}
	b, _ := json.Marshal(st)
	if !strings.Contains(string(b), `"distinct_query_rate":`) {
		t.Fatalf("json %s", b)
	}
	empty, err := d.ToolCallLogStats("nothing", "")
	if err != nil || empty.DistinctQueryRate != 0 {
		t.Fatalf("empty %+v %v", empty, err)
	}
}
