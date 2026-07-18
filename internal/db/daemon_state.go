package db

// DaemonSnapshot is written each sweep.
type DaemonSnapshot struct {
	LastSweepAt         string
	NextSweepAt         string
	LastSweepDurationMs int
	SessionsSeen        int
	SessionsDirty       int
	SessionsFlushed     int
	SessionsFailed      int
	BlobsPurged         int
	SessionsPruned      int
	LastError           string
	LastDailyCompactAt  string
}

// UpdateDaemonState upserts singleton daemon_state.
func (d *DB) UpdateDaemonState(s DaemonSnapshot) error {
	if !d.Writable() {
		return nil
	}
	now := UTCNow()
	_, err := d.SQL.Exec(`
		UPDATE daemon_state SET
			last_sweep_at=?,
			next_sweep_at=?,
			last_sweep_duration_ms=?,
			sessions_seen=?,
			sessions_dirty=?,
			sessions_flushed=?,
			sessions_failed=?,
			blobs_purged=?,
			sessions_pruned=?,
			last_error=?,
			last_daily_compact_at=COALESCE(?, last_daily_compact_at),
			updated_at=?
		WHERE id=1
	`,
		nullStr(s.LastSweepAt),
		nullStr(s.NextSweepAt),
		s.LastSweepDurationMs,
		s.SessionsSeen,
		s.SessionsDirty,
		s.SessionsFlushed,
		s.SessionsFailed,
		s.BlobsPurged,
		s.SessionsPruned,
		nullStr(s.LastError),
		nullStr(s.LastDailyCompactAt),
		now,
	)
	return err
}

// ReadDaemonState for health.
func (d *DB) ReadDaemonState() (map[string]interface{}, error) {
	if !d.Writable() {
		return nil, nil
	}
	var last, next, errStr, daily, updated *string
	var dur, seen, dirty, flushed, failed, blobs, pruned *int
	err := d.SQL.QueryRow(`
		SELECT last_sweep_at, next_sweep_at, last_sweep_duration_ms,
			sessions_seen, sessions_dirty, sessions_flushed, sessions_failed,
			blobs_purged, sessions_pruned, last_error, last_daily_compact_at, updated_at
		FROM daemon_state WHERE id=1
	`).Scan(&last, &next, &dur, &seen, &dirty, &flushed, &failed, &blobs, &pruned, &errStr, &daily, &updated)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"last_sweep_at":          last,
		"next_sweep_at":          next,
		"last_sweep_duration_ms": dur,
		"sessions_seen":          seen,
		"sessions_dirty":         dirty,
		"sessions_flushed":       flushed,
		"sessions_failed":        failed,
		"blobs_purged":           blobs,
		"sessions_pruned":        pruned,
		"last_error":             errStr,
		"last_daily_compact_at":  daily,
		"daemon_updated_at":      updated,
	}, nil
}
