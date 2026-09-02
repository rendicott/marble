# ADR-orb-0003: Spike compute/data — DynamoDB + Lambda vs RDS + ECS

| Field | Value |
|-------|--------|
| **Status** | **Accepted** — option **A** (2026-09-01) |
| **Date** | 2026-09-01 |
| **Deciders** | Project owner |
| **Related** | Orb design §9, Q34, Q54, Q56; infra already applied: VPC + S3 |
| **Decision** | **DynamoDB + Lambda + API Gateway + S3 + SES for spike and unpaid v1.** Do **not** buy RDS, ElastiCache, NAT, ALB, or always-on Fargate until idle cost is no longer the constraint. Keep the existing VPC+S3; do not attach Lambda to the VPC. |

## Why this question now

ADR-0001 §9 assumed **ALB + ECS Fargate + RDS Postgres 16 + Redis**, with JetStream on ECS when fanout hurts. Q34 allowed a cheaper spike (Postgres LISTEN + Redis). Q54: first 90 days under a few hundred USD.

Those always-on pieces dominate **idle** cost. The product is empty most of the time (you + Marble + one friend). Paying for a database that is on 24/7 is the wrong shape.

S3 bodies bucket is already live (`orb-bodies-019051664753`). That stays in every option.

## Idle cost (us-east-2, order of magnitude, 2026)

| Stack | Idle / month | Notes |
|-------|----------------|-------|
| **Current (VPC + S3 only)** | ~$0–1 | VPC/IGW free; S3 empty |
| **Lambda + HTTP API + WebSocket API + DynamoDB on-demand + S3 + SES** | **~$0–3** | $0 when no requests/connections. Dynamo on-demand has no table minimum. APIGW $0 with zero traffic. SES per email. |
| **Always-on Fargate (0.25 vCPU / 0.5 GB) + ALB** | ~$25–40 | ALB ~$16–22 + LCU; Fargate ~$9–15; no free tier |
| **+ RDS `db.t4g.micro` single-AZ** | **+$12–18** | Storage + backup extra. Multi-AZ doubles compute. |
| **+ ElastiCache `t4g.micro`** | **+$12** | |
| **+ NAT Gateway** | **+$32 + data** | Only if private-subnet Lambda/ECS need egress |
| **ADR §9 as written (Fargate + ALB + RDS Multi-AZ + Redis + NAT)** | **~$90–160 idle** | Before any Orb traffic. Blows Q54 if it sits for 90 days. |

Lambda free tier (1M requests + 400k GB-seconds / month) covers the entire spike with room. DynamoDB on-demand: you pay RRU/WRU only.

**Idle-near-zero is real** if we stay out of VPC-attached Lambda (that forces NAT) and skip ALB/RDS.

## Does Dynamo + Lambda fit the product?

Orb’s hot path is not a relational warehouse. It is:

1. Auth / magic link (token with TTL)
2. Topics + hashed API keys
3. Publish → persist envelope + body pointer → 202
4. Fan-out **≤2 KB envelope** to live subscribers
5. `GET` body from S3
6. Caps, invites, delivery counters

| Need | Postgres + ECS | Dynamo + Lambda |
|------|----------------|-----------------|
| Envelope 2 KB | row | item (400 KB limit; fine) |
| Body ≤1 MB | bytea or S3 | **S3** (already). Dynamo cannot hold 1 MB well anyway. |
| Magic-link / session TTL | `expires_at` + cron | **TTL attribute** (native) |
| Key lookup `orb_pk_…` | unique index | PK `KEY#hash` |
| Topic feed (time order) | `WHERE topic ORDER BY ts` | PK `TOPIC#id`, SK `MSG#ts#id` |
| Subscriber set | table | PK `TOPIC#id`, SK `SUB#device` |
| Invite token | unique index | PK `INV#hash`, TTL |
| Free caps (Q42) | counters | atomic `ADD` on a month key |
| Delivery stats (Q13) | `COUNT(*)` | counter attributes / sparse GSI |
| `X-Orb-Path` (Q50) | check in tx | `TransactWrite` 25 items |
| Durable bot cursor | offset table | SK `CURSOR#bot` |
| Local SQL / joins | easy | denormalize; more thought |
| **Live subscribe (WS/SSE)** | process holds sockets | **API Gateway WebSocket** holds sockets; Lambda on `$connect` / `$disconnect` / publish fan-out |
| JetStream replay | later ECS | Dynamo query by SK range = replay; Streams for push |

**Bodies stay in S3 in both designs.** Dynamo is the metadata/auth/envelope index, not the blob store.

## The real risk: connections, not CRUD

Lambda is a bad **socket server**. It is a fine **request handler**.

- **HTTP** (signup, publish, fetch body): API Gateway HTTP API → Lambda. Cold start ~100–400ms on tiny Go/Node; publish SLO was P95 &lt; 100ms regional — **cold starts may miss that**. For spike (curl + Marble) it is acceptable. Provisioned concurrency ($~12/mo for 1) only if it hurts.
- **WebSocket:** API Gateway WebSocket API stores connections; you write connection IDs into Dynamo (`TOPIC#id` / `CONN#id`). On publish, Lambda **Query**s connection IDs and `PostToConnection`. Idle with **zero** clients = $0. One Mac tray connected 24/7 is connection-minutes ≈ **cents**.
- **SSE:** long-lived HTTP does not fit Lambda’s 15 min cap well. **Spike: WebSocket only** (tray + web). SSE later or via APIGW WS.
- **Fan-out 10k subs (Q37):** 10k `PostToConnection` from one Lambda is painful (time + 29s HTTP API timeout). Mitigation: SQS chunking / Step Functions. **You will not have 10k subs in the spike.** Soft cap can stay; implementation can be naive (loop) until hundreds of connections.

Postgres `LISTEN` / Redis pubsub / NATS are nicer for fan-out **if you already pay for a process**. They are not a reason to pay $80/mo idle.

## What we would *not* do

- **Lambda in the existing VPC** — then you need NAT (~$32/mo) and idle-zero dies. S3, Dynamo, SES, APIGW are public AWS APIs; Lambda uses the AWS backbone without a VPC.
- **RDS Proxy + Aurora Serverless v2** — “serverless SQL” still has ACUs that don’t sit at $0 as cleanly as Dynamo on-demand for this access pattern; more moving parts.
- **Dynamo for 1 MB bodies** — no. S3.
- **Recreating DNS/SES** — no.

Keep **VPC + subnets** as already applied. Harmless (~$0). Useful later if we add Fargate. Do not put the spike API in it.

## Spike architecture (if we accept this ADR)

```
Browser / curl / Marble
    │
    ├─ HTTPS  api.orbnet.app     → API Gateway HTTP API → Lambda (Go)
    └─ WSS    gw.orbnet.app      → API Gateway WebSocket → Lambda
                                      │
                    DynamoDB (on-demand, TTL)
                      accounts, sessions, topics, keys,
                      envelopes, subs, connections, caps
                                      │
                    S3 orb-bodies-019051664753
                                      │
                    SES noreply@ (already)
```

Portal static files: S3 + CloudFront (or even API Gateway + Lambda returning HTML for spike). ACM in `orbnet-dev`; validation CNAMEs still in personal Route53.

**Local dev:** `aws-lambda-go` + DynamoDB local **or** hit the real on-demand tables (cheaper than running Postgres). Same AWS account.

## When to graduate to ECS + Postgres (or JetStream)

Revisit when **any** of these is true:

1. Always-on WS fan-out is cheaper/simpler than APIGW `PostToConnection` (many thousands of concurrent connections).
2. You want SQL for billing/admin and are paying for it anyway.
3. JetStream / NATS is actually needed (Q34) — that’s a **process**, so Fargate becomes justified.
4. Cold starts violate a real SLO you care about (then one small Fargate or provisioned concurrency — still no RDS required).

That is **phase 1+**, not the spike. Q2 hosted-only; Dynamo lock-in is acceptable until self-host (phase 3), which would need a different store anyway.

## Decision

**Accepted A (2026-09-01):** Spike + unpaid v1 = Lambda + APIGW + DynamoDB on-demand + S3 + SES. No RDS/ECS/NAT/ALB. Design §9 updated. Next: Go Lambda + single-table Dynamo.

Rejected B (always-on Postgres) and C (§9 idle stack). Graduate when WS fan-out, SQL, or JetStream actually hurt.
