package db

import (
	"errors"
	"testing"
	"time"
)

func grantDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestGrantClaimLifecycle(t *testing.T) {
	d := grantDB(t)
	if d.HasClaimableGrant() {
		t.Fatal("fresh db must have no claimable grant (enroll stays dark)")
	}
	g, secret, err := d.CreateGrant("orb-win-test", "windows", "cloud-init", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if g.ExpiresAt-g.CreatedAt != int64(DefaultGrantTTL.Seconds()) || g.State != "pending" {
		t.Fatalf("default ttl/state: %+v", g)
	}
	if !d.HasClaimableGrant() {
		t.Fatal("pending grant should open enroll")
	}

	// Self-reported name wins over the hint (EC2 picks its own hostname).
	res, err := d.ClaimGrant(ClaimInput{Secret: secret, DeviceName: "EC2AMAZ-9TG1LM4", OS: "windows",
		PeerVersion: "v0.2.3", CapsJSON: `{"desktop":true}`, RemoteIP: "100.64.0.9"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ComputerID != "ec2amaz-9tg1lm4" || res.DisplayName != "EC2AMAZ-9TG1LM4" || res.DeviceID == "" || res.DeviceToken == "" {
		t.Fatalf("claim result %+v", res)
	}
	c, _ := d.GetComputerByDevice(res.DeviceID)
	if c == nil || c.TokenHash != HashDeviceToken(res.DeviceToken) || c.OS != "windows" {
		t.Fatalf("computer row %+v", c)
	}

	// Single use.
	if _, err := d.ClaimGrant(ClaimInput{Secret: secret, DeviceName: "other"}); !errors.Is(err, ErrGrantUsed) {
		t.Fatalf("second claim: %v", err)
	}
	if d.HasClaimableGrant() {
		t.Fatal("claimed grant must not keep enroll open")
	}
	list, _ := d.ListGrants()
	if len(list) != 1 || list[0].State != "claimed" || list[0].ClaimedIP != "100.64.0.9" ||
		list[0].ComputerID != res.ComputerID || list[0].PeerVersion != "v0.2.3" {
		t.Fatalf("audit fields %+v", list)
	}
}

func TestGrantRejections(t *testing.T) {
	d := grantDB(t)
	if _, err := d.ClaimGrant(ClaimInput{Secret: "mgrant_nope"}); !errors.Is(err, ErrGrantInvalid) {
		t.Fatalf("unknown secret: %v", err)
	}
	if _, err := d.ClaimGrant(ClaimInput{Secret: "H-ABC123"}); !errors.Is(err, ErrGrantInvalid) {
		t.Fatalf("non-grant secret: %v", err)
	}

	g, secret, _ := d.CreateGrant("a", "linux", "", nil, time.Hour)
	if err := d.RevokeGrant(g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimGrant(ClaimInput{Secret: secret}); !errors.Is(err, ErrGrantRevoked) {
		t.Fatalf("revoked: %v", err)
	}

	g, secret, _ = d.CreateGrant("b", "linux", "", nil, time.Hour)
	_, _ = d.SQL.Exec(`UPDATE computer_grants SET expires_at=? WHERE id=?`, time.Now().Unix()-1, g.ID)
	if _, err := d.ClaimGrant(ClaimInput{Secret: secret}); !errors.Is(err, ErrGrantExpired) {
		t.Fatalf("expired: %v", err)
	}
	list, _ := d.ListGrants()
	for _, x := range list {
		if x.ID == g.ID && x.EffectiveState(time.Now()) != "expired" {
			t.Fatalf("expired grant should be a visible tombstone: %+v", x)
		}
	}

	_, secret, err := d.CreateGrant("c", "linux", "", []string{"10.0.0.0/8", "100.64.0.7"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimGrant(ClaimInput{Secret: secret, RemoteIP: "192.168.1.5"}); !errors.Is(err, ErrGrantIP) {
		t.Fatalf("ip allowlist: %v", err)
	}
	if _, err := d.ClaimGrant(ClaimInput{Secret: secret, RemoteIP: "100.64.0.7"}); err != nil {
		t.Fatalf("allowed ip: %v", err)
	}

	if _, _, err := d.CreateGrant("x", "", "", []string{"not-an-ip"}, time.Hour); err == nil {
		t.Fatal("bad cidr should be rejected")
	}
	if _, _, err := d.CreateGrant("x", "", "", nil, MaxGrantTTL+time.Hour); err == nil {
		t.Fatal("ttl over max should be rejected")
	}
}

// Recovery: a machine that lost its token re-enrolls with a fresh grant and keeps its
// computer id (even if it was revoked); name collisions with other machines get a suffix.
func TestGrantReenrollAndNameCollision(t *testing.T) {
	d := grantDB(t)
	_, s1, _ := d.CreateGrant("box", "linux", "", nil, time.Hour)
	first, err := d.ClaimGrant(ClaimInput{Secret: s1, DeviceName: "box", DeviceID: "dev-1"})
	if err != nil {
		t.Fatal(err)
	}
	_ = d.RevokeComputer(first.ComputerID)

	_, s2, _ := d.CreateGrant("box", "linux", "", nil, time.Hour)
	again, err := d.ClaimGrant(ClaimInput{Secret: s2, DeviceName: "box", DeviceID: "dev-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !again.Reenrolled || again.ComputerID != "box" || again.DeviceToken == first.DeviceToken {
		t.Fatalf("re-enroll %+v", again)
	}
	c, _ := d.GetComputerByDevice("dev-1")
	if c == nil || !c.Enabled || c.RevokedAt.Valid || c.TokenHash != HashDeviceToken(again.DeviceToken) {
		t.Fatalf("revived row %+v", c)
	}

	_, s3, _ := d.CreateGrant("", "", "", nil, time.Hour)
	other, err := d.ClaimGrant(ClaimInput{Secret: s3, DeviceName: "Box", DeviceID: "dev-2"})
	if err != nil {
		t.Fatal(err)
	}
	if other.ComputerID != "box-2" {
		t.Fatalf("collision id %q", other.ComputerID)
	}
}

func TestGrantComputerLimit(t *testing.T) {
	d := grantDB(t)
	for i := 0; i < MaxComputers; i++ {
		_, s, _ := d.CreateGrant("", "", "", nil, time.Hour)
		if _, err := d.ClaimGrant(ClaimInput{Secret: s, DeviceName: "m"}); err != nil {
			t.Fatal(err)
		}
	}
	_, s, _ := d.CreateGrant("", "", "", nil, time.Hour)
	if _, err := d.ClaimGrant(ClaimInput{Secret: s, DeviceName: "m"}); !errors.Is(err, ErrComputerLimit) {
		t.Fatalf("limit: %v", err)
	}
	// The failed claim rolled back: the grant is still claimable after a revoke frees a slot.
	_ = d.RevokeComputer("m")
	if _, err := d.ClaimGrant(ClaimInput{Secret: s, DeviceName: "m"}); err != nil {
		t.Fatalf("grant should survive a rolled-back claim: %v", err)
	}
}

func TestComputerSlug(t *testing.T) {
	for in, want := range map[string]string{
		"EC2AMAZ-9TG1LM4": "ec2amaz-9tg1lm4",
		"Rini's Mac.local": "rinis-mac-local",
		"":                 "peer",
		"process":          "peer",
		"__":               "peer",
	} {
		if got := computerSlug(in); got != want {
			t.Errorf("computerSlug(%q)=%q want %q", in, got, want)
		}
	}
}
