package session

import (
	"log"
	"sync"
	"time"

	"github.com/rendicott/marble/internal/db"
)

// Daemon periodically flushes dirty sessions and runs maintenance.
type Daemon struct {
	reg      *Registry
	interval time.Duration

	stop chan struct{}
	wg   sync.WaitGroup

	mu        sync.Mutex
	lastRun   time.Time
	lastError error
	lastFlush int
	lastPrune int
	lastBlobs int
}

// NewDaemon creates a background persist loop.
func NewDaemon(reg *Registry, interval time.Duration) *Daemon {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &Daemon{
		reg:      reg,
		interval: interval,
		stop:     make(chan struct{}),
	}
}

// Start begins the loop in a goroutine.
func (d *Daemon) Start() {
	d.wg.Add(1)
	go d.loop()
}

// Stop signals the loop and waits for a final flush.
func (d *Daemon) Stop() {
	close(d.stop)
	d.wg.Wait()
	n, err := d.reg.PersistDirty()
	if err != nil {
		log.Printf("daemon final flush: %v", err)
	} else if n > 0 {
		log.Printf("daemon final flush: %d session(s)", n)
	}
	if err := d.reg.CompactToday(); err != nil {
		log.Printf("daemon final daily compact: %v", err)
	}
	pruned, blobs, err := d.reg.RunMaintenance()
	if err != nil {
		log.Printf("daemon final maintenance: %v", err)
	} else if pruned > 0 || blobs > 0 {
		log.Printf("daemon final maintenance: pruned=%d blobs=%d", pruned, blobs)
	}
}

func (d *Daemon) loop() {
	defer d.wg.Done()
	t := time.NewTicker(d.interval)
	defer t.Stop()
	d.tick()
	for {
		select {
		case <-d.stop:
			return
		case <-t.C:
			d.tick()
		}
	}
}

func (d *Daemon) tick() {
	start := time.Now()
	seen := d.reg.LiveCount()
	dirty := d.reg.DirtyCount()
	n, err := d.reg.PersistDirty()
	failed := 0
	if err != nil {
		failed = 1
	}
	pruned, blobs, merr := d.reg.RunMaintenance()
	if merr != nil && err == nil {
		err = merr
	}
	dailyErr := d.reg.CompactToday()
	if dailyErr != nil && err == nil {
		err = dailyErr
	}

	d.mu.Lock()
	d.lastRun = time.Now()
	d.lastFlush = n
	d.lastPrune = pruned
	d.lastBlobs = blobs
	d.lastError = err
	d.mu.Unlock()

	if err != nil {
		log.Printf("daemon persist: %v", err)
	} else if n > 0 {
		log.Printf("daemon persist: flushed %d session(s)", n)
	}
	if pruned > 0 || blobs > 0 {
		log.Printf("daemon maintenance: pruned_sessions=%d blobs=%d", pruned, blobs)
	}

	// daemon_state (normal only)
	if d.reg.sqldb != nil && d.reg.sqldb.Writable() {
		next := time.Now().UTC().Add(d.interval).Format(time.RFC3339)
		last := start.UTC().Format(time.RFC3339)
		errStr := ""
		if err != nil {
			errStr = err.Error()
		}
		_ = d.reg.sqldb.UpdateDaemonState(db.DaemonSnapshot{
			LastSweepAt:         last,
			NextSweepAt:         next,
			LastSweepDurationMs: int(time.Since(start).Milliseconds()),
			SessionsSeen:        seen,
			SessionsDirty:       dirty,
			SessionsFlushed:     n,
			SessionsFailed:      failed,
			BlobsPurged:         blobs,
			SessionsPruned:      pruned,
			LastError:           errStr,
		})
	}
}

// Health for /api/health.
func (d *Daemon) Health() map[string]interface{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	errStr := ""
	if d.lastError != nil {
		errStr = d.lastError.Error()
	}
	out := map[string]interface{}{
		"persist_interval_sec": int(d.interval.Seconds()),
		"last_daemon_run":      nullTime(d.lastRun),
		"last_daemon_flush":    d.lastFlush,
		"last_daemon_prune":    d.lastPrune,
		"last_daemon_blobs":    d.lastBlobs,
		"last_daemon_err":      errStr,
		"dirty_sessions":       d.reg.DirtyCount(),
	}
	if d.reg.sqldb != nil {
		for k, v := range d.reg.sqldb.Health() {
			out[k] = v
		}
		if st, err := d.reg.sqldb.ReadDaemonState(); err == nil {
			for k, v := range st {
				out[k] = v
			}
		}
	}
	return out
}

func nullTime(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}
