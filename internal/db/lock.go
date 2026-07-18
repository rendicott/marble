package db

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func (d *DB) lockPath() string {
	return filepath.Join(d.Root, "marble.lock")
}

func (d *DB) acquireLock() error {
	path := d.lockPath()
	// If lock exists, check PID
	if b, err := os.ReadFile(path); err == nil {
		pidStr := strings.TrimSpace(string(b))
		if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 {
			if processAlive(pid) {
				return fmt.Errorf("memory directory locked by another harness (pid %d): %s", pid, path)
			}
		}
		_ = os.Remove(path)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return fmt.Errorf("memory directory locked (flock): %w", err)
	}
	if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		return err
	}
	_ = f.Sync()
	d.lockFile = f
	return nil
}

func (d *DB) releaseLock() {
	if d.lockFile == nil {
		return
	}
	_ = syscall.Flock(int(d.lockFile.Fd()), syscall.LOCK_UN)
	name := d.lockFile.Name()
	_ = d.lockFile.Close()
	d.lockFile = nil
	_ = os.Remove(name)
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks existence on Unix
	err = p.Signal(syscall.Signal(0))
	return err == nil
}
