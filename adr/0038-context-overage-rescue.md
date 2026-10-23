# ADR-0038: Context Overage — Clamp, Retry, and Model-Free Rescue

| Field | Value |
|-------|-------|
| **Status** | **Accepted** (2026-10-23) |
| **Date** | 2026-10-23 |
| **Author** | — |
| **Deciders** | Project owner |
| **Tags** | session, context, budgets, recovery, ui, cli, no-model |
| **Extends** | ADR-0002 (session memory), ADR-0013 (system prompt & soul), ADR-0018 (context budget), ADR-0022 (computer-heavy turns) |
| **Evidence** | Session `0wft8zjerv` on a 1,048,576-token window became permanently unusable. Every send returned `model HTTP 400 … you requested 1048656 tokens (655440 in the messages, 393216 in the completion)`. `/compact` made it worse. There was no model configured with a larger window, and no way out from inside the session. |

## Context

Two independent faults met.

**The output budget was never clamped.** `chatOnce` (`internal/model/client.go`) sent
`MaxTokens: c.MaxTokens` — the catalog row's `max_output` — on every call, whatever the
prompt already weighed. With `max_output` at 393,216, every request reserved 37.5% of a
1M window for output before a single prompt token was counted. `BudgetTokens`
(`internal/config/config.go`) subtracted that reservation when computing the advisory
budget, but nothing enforced it on the wire.

**The estimate was blind.** `internal/token/estimate.go` is `chars/4`, and images are a flat
1,500 tokens each. On that transcript the provider counted 655,440 message tokens where the
harness saw ~42,000 — a fifteenfold gap the estimator cannot see through, because the
weight is image and tool payload. Earlier in the same session the gap was 8.7%
(598,592 estimated ceiling, 655,440 counted). `trimHistory` therefore believed it had
trimmed enough. The provider disagreed.

The escape hatches were welded shut. `/compact` is not a harness command; it goes to the
model as a user message, which *adds* tokens. `compactSession` (`internal/session/loop.go`)
is itself a model call carrying the same reservation, so the way out of a context hole
required the context that was exhausted. Auto-compact existed but starts each turn with a
zero streak and needs three consecutive high-usage rounds
(`ContextAutoCompactRounds`), and a first-call 400 ends the turn at round one. Finally
`forceEndAssistant` told the operator "send another message (e.g. 'continue') to resume" —
advice that could not work, and the exact sentence the operator followed.

## Decision

Two layers, in this order of authority.

### 1. Preflight clamp and a single typed retry

`ClampMaxTokens` (`internal/session/clamp.go`) sets `max_tokens = limit − estimate − margin`
before the POST, so output can no longer crowd out the prompt. The margin is **learned, not
fixed**. A bare `limit/64` was measured and does not hold:

```text
limit 1,048,576  estimate 598,592  margin 16,384  →  max_tokens 433,600
655,440 provider messages + 433,600     = 1,089,040  … still 40,464 over
```

`AdaptiveMargin` scales the margin by the observed skew between provider-reported
`usage.prompt_tokens` and the harness estimate for that base_url+model, defaulting to 1.10
until samples exist and clamped to a fraction of the window. `TestAdaptiveMarginIncidentSkew`
asserts both that the adaptive margin fits **and** that the fixed-margin fixture still
overflows, so the demonstration cannot quietly rot.

When a context 400 arrives anyway, `internal/model/context_limit.go` parses it into a typed
`ContextLimitError` carrying the provider's own message count, and `contextRetry` performs
**one** retry with `max_tokens = limit − messages − margin`. The provider's count beats the
estimate because it is the only number the provider agrees with. The retry is not a loop:
a second 400 escalates. `noteProviderLimit` records a provider window larger than the
catalog without rewriting the catalog (`TestLargerProviderLimitIsRecordedNotCataloged`).

### 2. `overage-rescue` — deterministic, model-free

When the prompt itself does not fit, `PlanRescue` (`internal/session/overage.go`) decides
what to hide from the model projection. It is a pure function: no `Runner`, no HTTP client,
no clock, no randomness; the timestamp is injected by the caller. Actions are `rescued`,
`noop`, and `unrecoverable`, where `unrecoverable` leaves the document byte-identical.

Cut order: images before text; oldest rounds before newer ones; tool clusters only as whole
units (an assistant `tool_calls` block and its results are never split); a monster message is
tail-stubbed rather than dropped; the newest round is never omitted wholesale. The kept tail
is passed through `sanitizeToolCallHistory` so strict providers never see an orphan tool
result. When the fixed overhead alone (system prompt, soul, prompt pack, tool schemas)
exceeds the window, the answer is `unrecoverable` and the operator is told to switch models.

**Nothing is deleted.** Flags ride in the transcript markdown as
`<!-- model_omit: overage-rescue -->` and `<!-- model_stub_b64: … -->`. This is the whole
trick: `LoadFromDoc` rebuilds history from the UI transcript, so a history-only trim —
what `trimHistory` and `compactSession` do — evaporates on the next reload. Flags survive,
because they are in the file. Omitted rows stay visible in the UI, dimmed, and the newest
`harness` row is appended to the leading system message (capped at 1,500 chars) so the model
knows what was hidden.

Surfaces:

- `POST /api/sessions/{id}/overage-rescue` — busy session answers 409; archives before
  mutating; never starts a turn; `{"dry_run":true}` writes nothing.
- `marble-harness overage-rescue [-memory D] [-session ID] [-apply] [ID]` — dry run by
  default, refuses to scan every session, and refuses `-apply` while a live harness holds
  `marble.lock`. Works with the harness stopped; writes no event row (it has no live DB
  session and must not race the harness's event log).
- UI: **Rescue context** appears in the composer only in phase `context_overage`; the
  status pill reads `context full`; the interrupt bubble says "Context overage" and no
  longer promises that another message will resume. Only `kind="continue"` stops carry the
  old sentence.

Archives go to `<memory>/backups/overage/<id>/<UTC timestamp>.md`, written before any
mutation. A second run on a rescued session reports `noop` and writes no second archive.

## Consequences

- A stuck session is recoverable without a model call, without a larger context model, and
  without losing a byte of transcript. Archived pre-images make the repair reversible by
  hand.
- The adaptive ratio is in-process and per-model; it is not persisted, so the first clamp
  after a restart begins at 1.10. Rescue's no-observation scale is a separate 1.25 default.
- The retry silently reduces the output budget. It fires no advisory (only the preflight
  clamp does), so a long answer may be cut short with no visible explanation. Accepted for
  now; a one-line advisory would be an improvement.
- The CLI cannot see MCP tool schemas or the Wonderstand prompt pack, because those exist
  only inside a running harness. Its overhead estimate is therefore conservative-low, and
  it may plan a projection that the live harness then finds slightly tight. The clamp still
  protects the call.
- Rescue is one-way: it never clears flags. Re-including omitted text on a larger model is
  an explicit future restore, not an automatic behaviour — automatic re-inclusion is how a
  session gets stuck again on the way back.
- The estimator itself is unchanged. `chars/4` remains wrong about images; the clamp margin
  and the typed 400 are what absorb it. Fixing vision token accounting is separate work.
