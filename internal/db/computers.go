package db

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// ComputerRow is a registered desktop peer (ADR-0020 schema v5).
type ComputerRow struct {
	ID           string
	DisplayName  string
	DeviceID     string
	TokenHash    string
	OS           string
	CapsJSON     string
	EndpointHint string
	PolicyJSON   string
	Enabled      bool
	RevokedAt    sql.NullInt64
	LastSeenAt   sql.NullInt64
	CreatedAt    int64
	UpdatedAt    int64
}

func (d *DB) migrateV4toV5() error {
	if !d.Writable() {
		return fmt.Errorf("db not writable")
	}
	tx, err := d.SQL.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS computers (
			id TEXT PRIMARY KEY,
			display_name TEXT NOT NULL,
			device_id TEXT NOT NULL UNIQUE,
			token_hash TEXT NOT NULL,
			os TEXT NOT NULL DEFAULT '',
			caps_json TEXT NOT NULL DEFAULT '{}',
			endpoint_hint TEXT NOT NULL DEFAULT '',
			policy_json TEXT NOT NULL DEFAULT '{}',
			enabled INTEGER NOT NULL DEFAULT 1,
			revoked_at INTEGER,
			last_seen_at INTEGER,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_computers_device ON computers(device_id)`,
		`CREATE TABLE IF NOT EXISTS computer_pairings (
			id TEXT PRIMARY KEY,
			h_code TEXT NOT NULL,
			p_code TEXT NOT NULL DEFAULT '',
			device_id TEXT NOT NULL DEFAULT '',
			device_token_plain TEXT NOT NULL DEFAULT '',
			os TEXT NOT NULL DEFAULT '',
			caps_json TEXT NOT NULL DEFAULT '{}',
			status TEXT NOT NULL DEFAULT 'pending',
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		)`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	// session.computer_id
	var n int
	_ = tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='computer_id'`).Scan(&n)
	if n == 0 {
		if _, err := tx.Exec(`ALTER TABLE sessions ADD COLUMN computer_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(`UPDATE schema_meta SET schema_version = 5, updated_at = ? WHERE id = 1`, now); err != nil {
		return err
	}
	return tx.Commit()
}

// HashDeviceToken stores only a hash of the peer token.
func HashDeviceToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// RandomCode returns an uppercase alphanumeric code of n chars.
func RandomCode(n int) (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i := range b {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out), nil
}

// RandomToken returns a hex token.
func RandomToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// InsertComputer inserts a sealed computer row.
func (d *DB) InsertComputer(c ComputerRow) error {
	if !d.Writable() {
		return fmt.Errorf("db not writable")
	}
	en := 0
	if c.Enabled {
		en = 1
	}
	_, err := d.SQL.Exec(`
		INSERT INTO computers (id, display_name, device_id, token_hash, os, caps_json, endpoint_hint, policy_json,
			enabled, revoked_at, last_seen_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.DisplayName, c.DeviceID, c.TokenHash, c.OS, c.CapsJSON, c.EndpointHint, c.PolicyJSON,
		en, nullI64(c.RevokedAt), nullI64(c.LastSeenAt), c.CreatedAt, c.UpdatedAt)
	return err
}

func nullI64(n sql.NullInt64) interface{} {
	if n.Valid {
		return n.Int64
	}
	return nil
}

// ListComputers returns non-revoked computers.
func (d *DB) ListComputers() ([]ComputerRow, error) {
	if !d.Writable() {
		return nil, nil
	}
	rows, err := d.SQL.Query(`
		SELECT id, display_name, device_id, token_hash, os, caps_json, endpoint_hint, policy_json,
			enabled, revoked_at, last_seen_at, created_at, updated_at
		FROM computers WHERE revoked_at IS NULL ORDER BY display_name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ComputerRow
	for rows.Next() {
		var c ComputerRow
		var en int
		if err := rows.Scan(&c.ID, &c.DisplayName, &c.DeviceID, &c.TokenHash, &c.OS, &c.CapsJSON,
			&c.EndpointHint, &c.PolicyJSON, &en, &c.RevokedAt, &c.LastSeenAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Enabled = en != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetComputer by id.
func (d *DB) GetComputer(id string) (*ComputerRow, error) {
	if !d.Writable() {
		return nil, nil
	}
	var c ComputerRow
	var en int
	err := d.SQL.QueryRow(`
		SELECT id, display_name, device_id, token_hash, os, caps_json, endpoint_hint, policy_json,
			enabled, revoked_at, last_seen_at, created_at, updated_at
		FROM computers WHERE id = ?`, id).Scan(
		&c.ID, &c.DisplayName, &c.DeviceID, &c.TokenHash, &c.OS, &c.CapsJSON,
		&c.EndpointHint, &c.PolicyJSON, &en, &c.RevokedAt, &c.LastSeenAt, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.Enabled = en != 0
	return &c, nil
}

// GetComputerByDevice looks up by device_id.
func (d *DB) GetComputerByDevice(deviceID string) (*ComputerRow, error) {
	if !d.Writable() {
		return nil, nil
	}
	var c ComputerRow
	var en int
	err := d.SQL.QueryRow(`
		SELECT id, display_name, device_id, token_hash, os, caps_json, endpoint_hint, policy_json,
			enabled, revoked_at, last_seen_at, created_at, updated_at
		FROM computers WHERE device_id = ? AND revoked_at IS NULL`, deviceID).Scan(
		&c.ID, &c.DisplayName, &c.DeviceID, &c.TokenHash, &c.OS, &c.CapsJSON,
		&c.EndpointHint, &c.PolicyJSON, &en, &c.RevokedAt, &c.LastSeenAt, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.Enabled = en != 0
	return &c, nil
}

// TouchComputer updates last_seen and caps.
func (d *DB) TouchComputer(id, capsJSON, endpoint string) error {
	if !d.Writable() {
		return nil
	}
	now := time.Now().Unix()
	_, err := d.SQL.Exec(`UPDATE computers SET last_seen_at=?, caps_json=COALESCE(NULLIF(?,''), caps_json),
		endpoint_hint=COALESCE(NULLIF(?,''), endpoint_hint), updated_at=? WHERE id=?`,
		now, capsJSON, endpoint, now, id)
	return err
}

// RevokeComputer marks revoked.
func (d *DB) RevokeComputer(id string) error {
	if !d.Writable() {
		return fmt.Errorf("db not writable")
	}
	now := time.Now().Unix()
	_, err := d.SQL.Exec(`UPDATE computers SET revoked_at=?, enabled=0, updated_at=? WHERE id=?`, now, now, id)
	return err
}

// CountComputers active (non-revoked).
func (d *DB) CountComputers() (int, error) {
	if !d.Writable() {
		return 0, nil
	}
	var n int
	err := d.SQL.QueryRow(`SELECT COUNT(*) FROM computers WHERE revoked_at IS NULL`).Scan(&n)
	return n, err
}

// SetSessionComputerID sets sessions.computer_id.
func (d *DB) SetSessionComputerID(sessionID, computerID string) error {
	if !d.Writable() {
		return fmt.Errorf("db not writable")
	}
	_, err := d.SQL.Exec(`UPDATE sessions SET computer_id=?, updated_at=? WHERE id=?`,
		computerID, time.Now().UTC().Format(time.RFC3339), sessionID)
	return err
}

// GetSessionComputerID returns bound computer or "".
func (d *DB) GetSessionComputerID(sessionID string) (string, error) {
	if !d.Writable() {
		return "", nil
	}
	var id string
	err := d.SQL.QueryRow(`SELECT COALESCE(computer_id,'') FROM sessions WHERE id=?`, sessionID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

// PairingRow ephemeral pairing session.
type PairingRow struct {
	ID              string
	HCode           string
	PCode           string
	DeviceID        string
	DeviceTokenPlain string // only while pending seal; cleared after
	OS              string
	CapsJSON        string
	Status          string // pending | joined | sealed | expired
	CreatedAt       int64
	ExpiresAt       int64
}

// CreatePairing inserts a new pairing with H-code.
func (d *DB) CreatePairing(id, hCode string, ttl time.Duration) error {
	if !d.Writable() {
		return fmt.Errorf("db not writable")
	}
	now := time.Now().Unix()
	exp := now + int64(ttl.Seconds())
	_, err := d.SQL.Exec(`INSERT INTO computer_pairings (id, h_code, p_code, device_id, device_token_plain, os, caps_json, status, created_at, expires_at)
		VALUES (?, ?, '', '', '', '', '{}', 'pending', ?, ?)`, id, strings.ToUpper(hCode), now, exp)
	return err
}

// GetPairing by id.
func (d *DB) GetPairing(id string) (*PairingRow, error) {
	if !d.Writable() {
		return nil, nil
	}
	var p PairingRow
	err := d.SQL.QueryRow(`SELECT id, h_code, p_code, device_id, device_token_plain, os, caps_json, status, created_at, expires_at
		FROM computer_pairings WHERE id=?`, id).Scan(
		&p.ID, &p.HCode, &p.PCode, &p.DeviceID, &p.DeviceTokenPlain, &p.OS, &p.CapsJSON, &p.Status, &p.CreatedAt, &p.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetPairingByHCode finds active pairing by H code.
func (d *DB) GetPairingByHCode(hCode string) (*PairingRow, error) {
	if !d.Writable() {
		return nil, nil
	}
	now := time.Now().Unix()
	var p PairingRow
	err := d.SQL.QueryRow(`SELECT id, h_code, p_code, device_id, device_token_plain, os, caps_json, status, created_at, expires_at
		FROM computer_pairings WHERE h_code=? AND expires_at > ? AND status IN ('pending','joined')
		ORDER BY created_at DESC LIMIT 1`, strings.ToUpper(hCode), now).Scan(
		&p.ID, &p.HCode, &p.PCode, &p.DeviceID, &p.DeviceTokenPlain, &p.OS, &p.CapsJSON, &p.Status, &p.CreatedAt, &p.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// UpdatePairingJoin records peer join + P code.
func (d *DB) UpdatePairingJoin(id, pCode, deviceID, tokenPlain, os, capsJSON string) error {
	if !d.Writable() {
		return fmt.Errorf("db not writable")
	}
	_, err := d.SQL.Exec(`UPDATE computer_pairings SET p_code=?, device_id=?, device_token_plain=?, os=?, caps_json=?, status='joined'
		WHERE id=? AND status='pending'`, strings.ToUpper(pCode), deviceID, tokenPlain, os, capsJSON, id)
	return err
}

// SealPairing marks sealed and clears plain token from pairing table.
func (d *DB) SealPairing(id string) error {
	if !d.Writable() {
		return fmt.Errorf("db not writable")
	}
	_, err := d.SQL.Exec(`UPDATE computer_pairings SET status='sealed', device_token_plain='' WHERE id=?`, id)
	return err
}

// CleanupExpiredPairings removes old rows.
func (d *DB) CleanupExpiredPairings() {
	if !d.Writable() {
		return
	}
	now := time.Now().Unix()
	_, _ = d.SQL.Exec(`DELETE FROM computer_pairings WHERE expires_at < ? OR (status='sealed' AND created_at < ?)`,
		now, now-86400)
}
