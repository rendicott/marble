package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MaxComputers caps active (non-revoked) peers, for pairing and grant claims alike.
const MaxComputers = 8

// Grant lifetimes (ADR-0036).
const (
	DefaultGrantTTL = 24 * time.Hour
	MaxGrantTTL     = 7 * 24 * time.Hour
	// grantTombstone keeps claimed/expired/revoked grants listed this long so a
	// provisioning that never called home stays visible.
	grantTombstone = 7 * 24 * time.Hour
)

// GrantSecretPrefix marks enrollment secrets so they are recognizable in logs and files.
const GrantSecretPrefix = "mgrant_"

// GrantRow is a single-use, pre-authorized enrollment for a machine that is about to
// appear (ADR-0036). Only the secret's hash is stored.
type GrantRow struct {
	ID          string   `json:"grant_id"`
	DeviceName  string   `json:"device_name"` // hint, not a constraint
	OS          string   `json:"os"`
	Note        string   `json:"note,omitempty"` // free text, e.g. delivery channel
	AllowCIDRs  []string `json:"allow_cidrs,omitempty"`
	State       string   `json:"state"` // pending | claimed | revoked (expired is derived)
	CreatedAt   int64    `json:"created_at"`
	ExpiresAt   int64    `json:"expires_at"`
	ClaimedAt   int64    `json:"claimed_at,omitempty"`
	ClaimedIP   string   `json:"claimed_ip,omitempty"`
	ClaimedName string   `json:"claimed_device_name,omitempty"`
	PeerVersion string   `json:"peer_version,omitempty"`
	ComputerID  string   `json:"computer_id,omitempty"`
}

// EffectiveState reports "expired" for a pending grant past its TTL.
func (g GrantRow) EffectiveState(now time.Time) string {
	if g.State == "pending" && now.Unix() >= g.ExpiresAt {
		return "expired"
	}
	return g.State
}

var (
	ErrGrantInvalid  = errors.New("invalid grant")
	ErrGrantExpired  = errors.New("grant expired")
	ErrGrantUsed     = errors.New("grant already claimed")
	ErrGrantRevoked  = errors.New("grant revoked")
	ErrGrantIP       = errors.New("grant not valid from this address")
	ErrComputerLimit = fmt.Errorf("max %d computers", MaxComputers)
)

func (d *DB) migrateV11toV12() error {
	if d.SQL == nil {
		return fmt.Errorf("no database")
	}
	tx, err := d.SQL.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS computer_grants (
		id TEXT PRIMARY KEY,
		secret_hash TEXT NOT NULL UNIQUE,
		device_name TEXT NOT NULL DEFAULT '',
		os TEXT NOT NULL DEFAULT '',
		note TEXT NOT NULL DEFAULT '',
		allow_cidrs TEXT NOT NULL DEFAULT '[]',
		state TEXT NOT NULL DEFAULT 'pending',
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		claimed_at INTEGER NOT NULL DEFAULT 0,
		claimed_ip TEXT NOT NULL DEFAULT '',
		claimed_name TEXT NOT NULL DEFAULT '',
		peer_version TEXT NOT NULL DEFAULT '',
		computer_id TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		return fmt.Errorf("migrate v12: %w", err)
	}
	if _, err := tx.Exec(`UPDATE schema_meta SET schema_version = 12, updated_at = ? WHERE id = 1`, UTCNow()); err != nil {
		return err
	}
	return tx.Commit()
}

// NormalizeCIDRs validates an allowlist; bare IPs become /32 or /128.
func NormalizeCIDRs(in []string) ([]string, error) {
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "/") {
			ip := net.ParseIP(s)
			if ip == nil {
				return nil, fmt.Errorf("bad address %q", s)
			}
			if ip.To4() != nil {
				s += "/32"
			} else {
				s += "/128"
			}
		}
		if _, _, err := net.ParseCIDR(s); err != nil {
			return nil, fmt.Errorf("bad cidr %q", s)
		}
		out = append(out, s)
	}
	return out, nil
}

func ipAllowed(cidrs []string, ip string) bool {
	if len(cidrs) == 0 {
		return true
	}
	addr := net.ParseIP(ip)
	if addr == nil {
		return false
	}
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil && n.Contains(addr) {
			return true
		}
	}
	return false
}

// CreateGrant mints a grant and returns the row plus the plaintext secret (shown once).
func (d *DB) CreateGrant(deviceName, osName, note string, allow []string, ttl time.Duration) (GrantRow, string, error) {
	if !d.Writable() {
		return GrantRow{}, "", fmt.Errorf("db not writable")
	}
	if ttl <= 0 {
		ttl = DefaultGrantTTL
	}
	if ttl > MaxGrantTTL {
		return GrantRow{}, "", fmt.Errorf("ttl exceeds %s", MaxGrantTTL)
	}
	cidrs, err := NormalizeCIDRs(allow)
	if err != nil {
		return GrantRow{}, "", err
	}
	raw, err := RandomToken(24)
	if err != nil {
		return GrantRow{}, "", err
	}
	idPart, err := RandomToken(4)
	if err != nil {
		return GrantRow{}, "", err
	}
	secret := GrantSecretPrefix + raw
	now := time.Now()
	g := GrantRow{
		ID:         "g_" + idPart,
		DeviceName: strings.TrimSpace(deviceName),
		OS:         strings.TrimSpace(osName),
		Note:       strings.TrimSpace(note),
		AllowCIDRs: cidrs,
		State:      "pending",
		CreatedAt:  now.Unix(),
		ExpiresAt:  now.Add(ttl).Unix(),
	}
	cj, _ := json.Marshal(cidrs)
	if cidrs == nil {
		cj = []byte("[]")
	}
	_, err = d.SQL.Exec(`INSERT INTO computer_grants (id, secret_hash, device_name, os, note, allow_cidrs, state, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		g.ID, HashDeviceToken(secret), g.DeviceName, g.OS, g.Note, string(cj), g.CreatedAt, g.ExpiresAt)
	if err != nil {
		return GrantRow{}, "", err
	}
	return g, secret, nil
}

const grantCols = `id, device_name, os, note, allow_cidrs, state, created_at, expires_at,
	claimed_at, claimed_ip, claimed_name, peer_version, computer_id`

type rowScanner interface{ Scan(...interface{}) error }

func scanGrant(r rowScanner) (GrantRow, error) {
	var g GrantRow
	var cj string
	err := r.Scan(&g.ID, &g.DeviceName, &g.OS, &g.Note, &cj, &g.State, &g.CreatedAt, &g.ExpiresAt,
		&g.ClaimedAt, &g.ClaimedIP, &g.ClaimedName, &g.PeerVersion, &g.ComputerID)
	if err == nil {
		_ = json.Unmarshal([]byte(cj), &g.AllowCIDRs)
	}
	return g, err
}

// ListGrants returns grants newest first, dropping tombstones older than a week.
func (d *DB) ListGrants() ([]GrantRow, error) {
	if !d.Writable() {
		return nil, nil
	}
	cut := time.Now().Add(-grantTombstone).Unix()
	_, _ = d.SQL.Exec(`DELETE FROM computer_grants WHERE (state != 'pending' AND MAX(claimed_at, expires_at) < ?)
		OR (state = 'pending' AND expires_at < ?)`, cut, cut)
	rows, err := d.SQL.Query(`SELECT ` + grantCols + ` FROM computer_grants ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GrantRow
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// HasClaimableGrant reports whether any pending, unexpired grant exists. The enroll
// endpoint is dark (404) otherwise.
func (d *DB) HasClaimableGrant() bool {
	if !d.Writable() {
		return false
	}
	var n int
	_ = d.SQL.QueryRow(`SELECT COUNT(*) FROM computer_grants WHERE state='pending' AND expires_at > ?`,
		time.Now().Unix()).Scan(&n)
	return n > 0
}

// RevokeGrant stops a pending grant from being claimed. Peers that already claimed it
// keep their own device tokens.
func (d *DB) RevokeGrant(id string) error {
	if !d.Writable() {
		return fmt.Errorf("db not writable")
	}
	res, err := d.SQL.Exec(`UPDATE computer_grants SET state='revoked' WHERE id=? AND state='pending'`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no pending grant %q", id)
	}
	return nil
}

// ClaimInput is what an enrolling peer reports about itself.
type ClaimInput struct {
	Secret      string
	DeviceName  string
	DeviceID    string // optional; minted when empty
	OS          string
	PeerVersion string
	CapsJSON    string
	RemoteIP    string
}

// ClaimResult is returned once to the peer.
type ClaimResult struct {
	Grant       GrantRow
	ComputerID  string
	DisplayName string
	DeviceID    string
	DeviceToken string
	Reenrolled  bool // an existing computer with this device_id got a fresh token
}

// ClaimGrant redeems a grant: single use, TTL, optional IP allowlist. The machine's
// reported name wins over the grant's hint. A device_id that is already registered
// (recovery after lost credentials) keeps its computer id and gets a new token.
func (d *DB) ClaimGrant(in ClaimInput) (ClaimResult, error) {
	if !d.Writable() {
		return ClaimResult{}, fmt.Errorf("db not writable")
	}
	in.Secret = strings.TrimSpace(in.Secret)
	if !strings.HasPrefix(in.Secret, GrantSecretPrefix) {
		return ClaimResult{}, ErrGrantInvalid
	}
	tx, err := d.SQL.Begin()
	if err != nil {
		return ClaimResult{}, err
	}
	defer tx.Rollback()

	g, err := scanGrant(tx.QueryRow(`SELECT `+grantCols+` FROM computer_grants WHERE secret_hash=?`, HashDeviceToken(in.Secret)))
	if err == sql.ErrNoRows {
		return ClaimResult{}, ErrGrantInvalid
	}
	if err != nil {
		return ClaimResult{}, err
	}
	now := time.Now()
	switch g.EffectiveState(now) {
	case "claimed":
		return ClaimResult{Grant: g}, ErrGrantUsed
	case "revoked":
		return ClaimResult{Grant: g}, ErrGrantRevoked
	case "expired":
		return ClaimResult{Grant: g}, ErrGrantExpired
	}
	if !ipAllowed(g.AllowCIDRs, in.RemoteIP) {
		return ClaimResult{Grant: g}, ErrGrantIP
	}

	name := strings.TrimSpace(in.DeviceName)
	if name == "" {
		name = g.DeviceName
	}
	osName := strings.TrimSpace(in.OS)
	if osName == "" {
		osName = g.OS
	}
	devID := strings.TrimSpace(in.DeviceID)
	if devID == "" {
		devID = uuid.NewString()
	}
	if in.CapsJSON == "" {
		in.CapsJSON = "{}"
	}
	token, err := RandomToken(32)
	if err != nil {
		return ClaimResult{}, err
	}
	res := ClaimResult{DeviceID: devID, DeviceToken: token}
	ts := now.Unix()

	var existingID, existingName string
	err = tx.QueryRow(`SELECT id, display_name FROM computers WHERE device_id=?`, devID).Scan(&existingID, &existingName)
	switch {
	case err == nil:
		if _, err := tx.Exec(`UPDATE computers SET token_hash=?, os=?, caps_json=?, enabled=1, revoked_at=NULL, updated_at=? WHERE id=?`,
			HashDeviceToken(token), osName, in.CapsJSON, ts, existingID); err != nil {
			return ClaimResult{}, err
		}
		res.ComputerID, res.DisplayName, res.Reenrolled = existingID, existingName, true
	case err == sql.ErrNoRows:
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM computers WHERE revoked_at IS NULL`).Scan(&n); err != nil {
			return ClaimResult{}, err
		}
		if n >= MaxComputers {
			return ClaimResult{Grant: g}, ErrComputerLimit
		}
		id, err := uniqueComputerID(tx, computerSlug(name))
		if err != nil {
			return ClaimResult{}, err
		}
		if name == "" {
			name = id
		}
		if _, err := tx.Exec(`INSERT INTO computers (id, display_name, device_id, token_hash, os, caps_json, endpoint_hint, policy_json,
			enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, '{}', 1, ?, ?)`,
			id, name, devID, HashDeviceToken(token), osName, in.CapsJSON, in.RemoteIP, ts, ts); err != nil {
			return ClaimResult{}, err
		}
		res.ComputerID, res.DisplayName = id, name
	default:
		return ClaimResult{}, err
	}

	// Conditional update: two racing claims cannot both win.
	up, err := tx.Exec(`UPDATE computer_grants SET state='claimed', claimed_at=?, claimed_ip=?, claimed_name=?, peer_version=?, computer_id=?
		WHERE id=? AND state='pending'`,
		ts, in.RemoteIP, strings.TrimSpace(in.DeviceName), strings.TrimSpace(in.PeerVersion), res.ComputerID, g.ID)
	if err != nil {
		return ClaimResult{}, err
	}
	if n, _ := up.RowsAffected(); n != 1 {
		return ClaimResult{Grant: g}, ErrGrantUsed
	}
	if err := tx.Commit(); err != nil {
		return ClaimResult{}, err
	}
	g.State, g.ClaimedAt, g.ClaimedIP, g.ComputerID = "claimed", ts, in.RemoteIP, res.ComputerID
	g.ClaimedName, g.PeerVersion = strings.TrimSpace(in.DeviceName), strings.TrimSpace(in.PeerVersion)
	res.Grant = g
	return res, nil
}

// computerSlug lowercases to [a-z0-9-]; "process" is reserved (the local computer).
func computerSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-':
			b.WriteRune(r)
		case r == ' ' || r == '_' || r == '.':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 48 {
		out = strings.Trim(out[:48], "-")
	}
	if out == "" || out == "process" {
		out = "peer"
	}
	return out
}

// uniqueComputerID appends -2, -3, … until the id is free (revoked rows still hold theirs).
func uniqueComputerID(tx *sql.Tx, base string) (string, error) {
	for i := 1; i < 1000; i++ {
		id := base
		if i > 1 {
			id = fmt.Sprintf("%s-%d", base, i)
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM computers WHERE id=?`, id).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return id, nil
		}
	}
	return "", fmt.Errorf("no free computer id for %q", base)
}
