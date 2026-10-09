# ADR-0036: Peer Auto-Enrollment via Single-Use Grants

| Field | Value |
|-------|--------|
| **Status** | **Accepted** (implemented in harness v0.4.18, peer v0.2.3) |
| **Date** | 2026-10-09 |
| **Author** | — |
| **Deciders** | Project owner |
| **Tags** | peer-protocol, computers, provisioning, automation |
| **Extends** | ADR-0020 (computers + pairing), ADR-0021 (desktop peer) |
| **Evidence** | Field request "Peer Auto-Enrollment via Grants": a Windows Server 2022 EC2 instance for Orb client tests installed and ran the peer fine, but pairing could not complete unattended. |

## Context

Pairing (ADR-0020) is a mutual handshake: the operator mints an H-code, carries it to the
machine, the peer joins and shows a P-code, the operator types it back and confirms, the peer
polls for its token. Two operator round-trips and a secret typed across a boundary. Fine for a
laptop once; a blocker for "terraform apply brings up a VM, it appears as a controllable
computer, tear it down after".

The trust relationship is not symmetric. The harness drives the peer (screenshots, clicks,
exec); the peer has no tool surface into the harness. The only real question is "should this
harness accept this machine?", which belongs on the harness side and can be answered in advance.

## Decision

The operator (or automation) mints a **grant**: a single-use, short-lived, pre-authorized
expectation that a machine is about to appear. The secret is delivered out-of-band by any
channel; the machine redeems it with one request and receives an ordinary device token.

- `POST /api/computers/grants` (operator) → `grant_id`, `grant_secret` (shown once), `expires_at`.
  Only `sha256(secret)` is stored (table `computer_grants`, schema v12).
- `POST /api/computers/enroll` (public; the secret is the credential) → `computer_id`,
  `device_id`, `device_token`. One request, one response.
- `GET /api/computers/grants`, `DELETE /api/computers/grants/{id}`; Settings → Computers lists
  grants beside computers, so an expected machine that never called home is visible.
- Peer: `marble-peer enroll` reads the grant from `--grant`, `MARBLE_GRANT`, or
  `~/.marble-peer/grant`; `marble-peer run` enrolls from the env/file on start.

Locked decisions from the request:

1. **`device_name` is a hint.** The machine's self-reported name wins (EC2 picks
   `EC2AMAZ-…`); a cosmetic mismatch must not fail provisioning. Collisions get `-2`, `-3`….
2. **No reusable grants.** Always single-use; fleets mint N grants, which also gives
   per-machine attribution.
3. **Name: grant** — the harness grants permission; trust flows from it.

Resolved open questions:

| Question | Resolution |
|----------|------------|
| Claim rate limiting | Per source address: 10 failed claims in 10 minutes → 429. Secrets carry 192 random bits, so this limits noise and abuse rather than guessing. |
| IP allowlist per grant | Optional `allow_cidrs` (IPs or CIDRs); claims from elsewhere get 403. |
| Expired grants: tombstone or vanish | Tombstone: claimed, expired and revoked grants stay listed (with claim audit) for 7 days, then are deleted. |
| Idempotent enroll | Peer-side: if the peer already holds a token for that harness, `enroll` exits 0 without spending the grant (`--force` overrides). The harness never re-issues a token for a claimed grant. |

Other rules:

- **Default off**: `/api/computers/enroll` returns 404 unless a pending, unexpired grant exists.
- **TTL** 24h default, 60s–7d.
- **Scope**: a claim can only create (or re-key) one computer row. It cannot read sessions,
  memory or secrets, or touch other peers.
- **Recovery**: a `device_id` already registered (even revoked) keeps its computer id and gets
  a fresh token, so a peer that lost credentials re-enrolls without a console visit.
- **Limit**: the existing cap of 8 active computers applies; a claim over the cap is rolled
  back and the grant stays claimable.
- **Audit**: creation (name, os, note, allowlist, expiry) and every claim or rejection (source
  address, reported name, peer version, resulting computer) are logged; claim fields are kept
  on the grant row.
- The claim is a conditional `UPDATE … WHERE state='pending'` inside the same transaction as
  the computer insert, so two racing claims cannot both win.

## Consequences

- Unattended provisioning works: deliver `MARBLE_HARNESS` + `MARBLE_GRANT` (or drop the grant
  file) and start the peer.
- The H-code / P-code flow is unchanged and remains the interactive path.
- Schema v12 is additive. An older harness opening a v12 database treats it as newer than
  itself (read-only), as with every schema bump.
- `enroll` returns the device token over whatever transport the harness URL uses; the peer's
  existing rule applies (cleartext only for loopback / tailnet or with `--allow-http`).

## Not done

- A model tool for minting grants from a session (automation currently calls the HTTP API).
- A reference Terraform / user-data module for the Orb test rig.
