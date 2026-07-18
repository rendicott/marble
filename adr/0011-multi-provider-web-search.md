# ADR-0011: Multi-Provider Web Search (Tavily + Brave, …)

| Field | Value |
|-------|--------|
| **Status** | **Deferred** |
| **Date** | 2026-07-18 |
| **Deciders** | Project owner |
| **Tags** | mcp, search, skills, system-prompt, research |
| **Extends** | ADR-0006 (MCP client), ADR-0005 (skills / tool suite), ADR-0001 (system prompt) |
| **Depends on** | Operators may already run one search MCP (e.g. Tavily) via `$MEMORY/mcp.json` |

## Context

Many agent stacks layer **more than one web search provider** (commonly **Tavily** and **Brave Search**) because indexes, latency, cost, and tool shapes differ:

| Provider (typical) | Strengths |
|--------------------|-----------|
| **Tavily** | Research-oriented tools (search, extract, crawl, map, research) |
| **Brave** | Fast factual / news-oriented lookup |

Marble already supports **multiple MCP servers** in `mcp.json`. Each tool appears as `mcp_<server>_<tool>` with a description. What is missing is a **routing policy**: when both are connected, the model sees a flat tool list and may pick randomly, double-call, or never fall back.

Question: how should the harness teach **effective use of both** — system prompt, skills, native wrapper tools, or later product features?

## Status: Deferred

**No implementation in the current MVP.** This ADR records the preferred approach so it is not re-debated from scratch. Operators may still enable multiple search MCPs manually today; behavior will be model-dependent without the policy layers below.

**Revisit when:** multi-search is a recurring pain, or Settings/MCP work makes a good place for an operator system addendum.

## Goals (when un-deferred)

1. Operators can enable **two or more** search MCPs without the model flailing.  
2. Clear **routing heuristics** (which provider for which job).  
3. **Fallback** once if the first provider is empty/errors.  
4. Prefer **config + skills** over hard-coding provider brands into Go when possible.  
5. Stay token-cheap on the always-on system prompt.

## Non-goals (deferred scope)

- Native paid search API client in Go (use MCP)  
- Parallel fan-out of every query to all providers by default  
- Ranking/fusion engine across providers  
- Multi-model routing (separate concern; MVP remains one model)  

## Decision (accepted design, deferred build)

### Layered approach (not system-prompt XOR skills)

| Layer | Role | When loaded |
|-------|------|-------------|
| **MCP `mcp.json`** | Capability — both (or N) servers enabled; secrets via env | Process start / MCP reload |
| **System prompt (short)** | Always-on **routing policy** + “load web-research skill for deep work” | Every session |
| **Skill** (`$MEMORY/skills/web-research/` or similar) | Full playbook: query craft, Tavily extract/crawl, Brave news, merge, cite, memory/mpub | `skill_load` when needed |
| **Optional later** | Operator system addendum (Settings/DB); MCP catalog inject; native `web_search` façade | Future ADR/PR |

**Do not** rely on tool descriptions alone when two tools both mean “search.”

### Recommended routing policy (encode in prompt + skill)

| Need | Prefer | Notes |
|------|--------|--------|
| Quick fact / news | **Brave** (or light search) | Latency / freshness |
| Research + page content | **Tavily** search → extract | Use extract/crawl/map/research when present |
| Site map / multi-page | **Tavily** map/crawl | |
| First result weak/empty | **Switch provider once** | Avoid parallel by default |
| Local repo / sysadmin | **No web** | Files + shell first |

### System prompt sketch (short, always-on)

```text
Web research:
- Prefer mcp_tavily_* for multi-hop research, extract/crawl/map, deep research.
- Prefer mcp_brave_* for fast factual lookup, recent news, simple queries.
- If the first provider is thin/empty/errors, retry once with the other.
- Cite URLs; do not invent sources.
- Do not run both in parallel for the same query unless the first is weak.
- For non-trivial research, skill_search/skill_load the web-research skill first.
```

Exact tool names depend on MCP packages; policy should refer to **server prefixes** (`mcp_tavily_`, `mcp_brave_`) rather than brittle full names where possible.

### Skill sketch

Path idea: `$MEMORY/skills/web-research/SKILL.md` (and optional references).

Contents: when to search vs local tools; query formulation; Tavily multi-step patterns; Brave for alternate index/news; merge/dedupe; cite; optional `memory_write` / `mpub_publish`; rate-limit and empty-result handling.

### Optional later enhancements (still deferred)

1. **Settings “operator system addendum”** — edit routing text without rebuild.  
2. **Auto-inject MCP catalog** (enabled servers + one-line roles) into system message at session create.  
3. **Native `web_search` tool** — policy wrapper that picks primary/fallback (hides MCP names).  
4. **Skill auto-hint** when ≥2 search-like MCP servers are connected.

### Explicitly not chosen for deferred v1

- Hard-coding Tavily/Brave-only logic in the agent loop.  
- Requiring a skill load for every one-line “search X” (system prompt handles light routing).  
- Replacing MCP with first-party HTTP clients in this ADR.

## Consequences

### Positive (when implemented)

- Multi-provider setup becomes intentional instead of accidental tool soup.  
- Skills stay editable ops content; prompt stays small.  
- Aligns with ADR-0006 (MCP) and ADR-0005 (skills).

### Trade-offs

- Model must still follow instructions (no hard enforcement without a wrapper tool).  
- Provider package renames require prompt/skill updates.  
- Extra skills tokens only when loaded.

### Risks

| Risk | Mitigation |
|------|------------|
| Model ignores policy | Wrapper tool later; shorter clearer prompt |
| Duplicate cost (double search) | Prompt: no parallel by default |
| Skill never loaded | System prompt requires skill for deep research |

## Acceptance criteria (when un-deferred)

- [ ] Decision to implement (status → Accepted)  
- [ ] System prompt (or addendum) routing text  
- [ ] Web-research skill checked in or documented under `$MEMORY/skills`  
- [ ] Example `mcp.json` with two search servers (no secrets)  
- [ ] Optional: catalog inject or Settings addendum  
- [ ] Manual eval: simple query → Brave-ish path; research → Tavily multi-step; fallback works  

## References

- ADR-0006 MCP client / `$MEMORY/mcp.json`  
- ADR-0005 `skill_search` / `skill_load`  
- Current `defaultSystemPrompt` MCP one-liner  
- Operator discussion: layer Tavily + Brave via prompt + skill; defer product wrappers  
