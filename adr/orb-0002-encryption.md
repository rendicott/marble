# ADR-orb-0002: Encryption stubs (v1 plaintext, prepare for later)

| Field | Value |
|-------|--------|
| **Status** | Proposed / exploration (Q45) |
| **Date** | 2026-08-31 |
| **Deciders** | Project owner |
| **Related** | Orb design v0.1.4, ADR-0001 Q45 |
| **Decision for v1** | **No encryption of bodies or envelopes.** Server sees plaintext. **Stub the seams** so TLS-at-rest and optional E2E are not a rewrite. |

## Context

Agent traces, approval asks, and private-topic markdown can be sensitive. v1 is a hosted bus in `us-east-2`; RDS/S3/JetStream operators (us) can read everything. Users will eventually ask “can Orb not see my bodies?”

Q45: no encryption in v1, but prepare. This note is the mini-ADR.

## What “encryption” could mean (do not conflate)

| Layer | What it protects | v1 | Later |
|-------|------------------|----|--------|
| **TLS in transit** | Network observers | **Yes** (HTTPS/WSS only) | mTLS for bots optional |
| **Disk / AWS at rest** | Stolen volume, casual S3 list | RDS/S3 default AES-256 + KMS **on** | CMK per env; no per-tenant CMK yet |
| **App-level at rest** | Our own DB dumps, support staff | **No** — plaintext in Postgres/S3 | Envelope encryption with app KEK |
| **E2E (publisher→subscriber)** | Orb operators | **No** | Optional per private topic |
| **Push postcard** | APNs/FCM/lock screen | Title-only default on private (Q33) | Same; never put body in push |

v1 already has TLS + cloud default at-rest. The gap is **Orb staff and the application** can read bodies. That is the thing to stub.

## Why E2E is hard on this product

1. **Fan-out + anonymous public listen.** Public topics cannot be E2E in any useful sense (world can subscribe). E2E is **private-topic only**.
2. **Server-split envelopes (Q14).** If the server writes `preview` from the body, it must see plaintext. Publisher-supplied envelopes can be ciphertext + opaque preview; default path cannot.
3. **Markdown + SVG (Q7).** Clients must render. E2E means every client decrypts before sanitize/render. SVG sanitizer still runs on plaintext in the client.
4. **Search, delivery stats (Q13), abuse/CSAM (Q46).** Server-side receipts and freeze still work on ciphertext ids. Content moderation and “how many opened” metadata do **not** need body plaintext. Automated CSAM **does** need pixels — E2E private topics would skip server-side scanners (client-side or none).
5. **Key distribution.** Subscribers join via invite. Options: (a) topic secret in the invite URL (ntfy-like, stolen invite = stolen plaintext), (b) per-device keys + owner re-wraps (MLS/Signal-group — heavy), (c) publisher encrypts to a topic public key held by Orb (not E2E). (a) is the only honest v1.5 stub.
6. **Bots and MCP (Q47).** An MCP server on our API that fetches bodies is **Orb-as-client**. E2E bots need the topic secret in the agent’s env, not in Orb.
7. **Retention / GDPR delete.** Ciphertext deletes the same as plaintext. Key destruction is faster “crypto-shredding” if we ever wrap with per-topic DEKs.
8. **JSON flagged bodies (Q8).** Same blob store; content-type does not change crypto.

## v1 stubs (implement these, not crypto)

Schema / API (no user-visible encryption):

- `Topic.crypto_mode`: `none` (only value in v1). Reserved: `topic_secret`, `e2e_mls`.
- `MessageMeta.crypto`: `{ mode: "none", dek_kid: null }`.
- Body object: always raw bytes + `content_type`. Do **not** gzip-then-forget a format that cannot grow a header (`orb-body/1` magic + flags later).
- Envelope: no ciphertext fields in v1; reserve `enc` optional object in the JSON schema (`null`).
- S3: SSE-S3 or SSE-KMS (account key). Bucket policy deny public. No per-object customer key yet.
- Postgres: default RDS encryption. No pgcrypto on message tables.
- Logs: never log body or preview beyond 80 chars already in envelope; hash `message_id` in access logs.
- Invite payload: room for `crypto_hint` unused.

Client: one `BodyCodec` interface (`identity` only). Markdown/SVG sanitize stays on decoded bytes.

## Strawman for v1.5 (`topic_secret`) — not approved

- Private topic: owner sets a topic secret (or we generate). Invite URL carries it (fragment `#s=…` so it never hits our access logs).
- Publisher encrypts body with a DEK; DEK wrapped with topic secret (XChaCha20-Poly1305 or libsodium secretbox). Envelope stays plaintext metadata (title may still leak).
- Server stores ciphertext; cannot render preview unless publisher sent a plaintext preview (Q14 explicit envelope).
- Stolen invite still decrypts — same as today’s grant model. This is **not** protection from a malicious subscriber; it is protection from Orb/AWS operators and backups.

True E2E (protection from stolen invites) needs per-device keys and is phase 2+.

## Decision

- **v1:** plaintext application data; TLS + AWS default at-rest; stubs above.
- **Do not** advertise “encrypted topics.”
- **Privacy policy:** US processing, staff can access private bodies for abuse/ops (Q44).
- **Next:** if we productize `topic_secret`, write a full ADR with nonce, version byte, and invite-fragment UX.

## Consequences

- Abuse/CSAM (Q46) can inspect bodies in v1. If `topic_secret` ships, private topics drop off server-side scanners — disclose that.
- Delivery stats (Q13) stay on envelope/ack events, independent of crypto.
- MCP and web inbox work without client crypto in v1.
