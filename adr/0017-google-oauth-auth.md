# ADR-0017: Google OAuth Login, Multi-User Identity & TLS

| Field | Value |
|-------|--------|
| **Status** | Accepted / Implemented |
| **Date** | 2026-07-20 |
| **Deciders** | Project owner |
| **Tags** | auth, oauth, google, multi-user, security, sessions, ui, tls, https |
| **Extends** | ADR-0001, ADR-0007, ADR-0009, ADR-0010, ADR-0016 |
| **Answers** | `adr/0017-answers.json` (`2026-07-20T03:51:19.494Z`) |

## Context

Marble is open by default (local operator trust). Operators want optional **Sign in with Google**, an **email allowlist** (everyone full admin), **identity on chat history** (not sent to the model), public **`/mpub`**, optional **in-process TLS**, and compatibility with **reverse-proxy TLS** (Caddy, ALB, Tailscale Serve).

## Goals

1. Optional Google OAuth (authorization code + PKCE + state).  
2. **open** vs **google** mode from complete CLI config; partial OAuth → fatal.  
3. Allowlist = flag ∪ file; exact emails; all full admin.  
4. Shared chat sessions; single busy lock.  
5. Identity on user messages in UI/MD/events — **never** to the model.  
6. mpub: **public** pages stay open; **private** pages admin-only when google mode (see follow-on); health public (account **count** only).  
7. Optional TLS via cert/key **files**; reverse proxy without local certs allowed (warn).  
8. Attribute Stop / cron Run-now to actor in **log + session events** only.

## Non-goals (v1)

- Other OAuth providers; RBAC roles  
- Domain wildcards; durable login across restart  
- Automatic Let's Encrypt / ACME  
- mTLS; HTTP→HTTPS :80 redirect listener  
- `--tls-key-env` (defer)  

## Decision

### 1. Auth modes

| Mode | When | Behavior |
|------|------|----------|
| **open** | OAuth not fully configured | No login (today) |
| **google** | client-id + secret-env (non-empty) + redirect + non-empty allowlist | Login required for protected routes |
| **invalid** | Partial OAuth flags | **Fatal** startup |

No separate `--auth-required` flag.

### 2. OAuth launch flags

```text
--oauth-client-id=….apps.googleusercontent.com
--oauth-client-secret-env=GOOGLE_OAUTH_CLIENT_SECRET
--oauth-redirect-url=https://public-host:8080/auth/callback
--oauth-allow-emails=a@x.com,b@y.com
--oauth-allow-file=/path/to/allowlist.txt
```

- Secret only via **env var name** (never argv).  
- Allowlist: union of emails + file; lowercase exact match; `#` comments in file.  
- Redirect URL fully explicit (document Tailscale HTTPS).  

### 3. OAuth flow & cookie

- Google OIDC: code + **PKCE** + `state`; scopes `openid email profile`; require `email_verified`.  
- Cookie `marble_session`: HttpOnly, SameSite=Lax, Max-Age **7 days** absolute, in-memory server map.  
- `Secure` cookie unless redirect is `http://localhost` or `http://127.0.0.1`.  
- Re-login after harness restart.  
- Routes: `/auth/login`, `/auth/callback`, `/auth/logout`, `/auth/me`.  
- CSRF: mutating SPA API calls require header **`X-Marble-Requested-With: fetch`**.  
- SSE: same-site cookie; UI handles **401** → re-login.

### 4. Public vs protected

| Path | google mode |
|------|-------------|
| `/mpub`, `/mpub/*` | **Path public at middleware**; handler gates **private** docs (admin cookie required). Public docs + index (public items only) remain open |
| `/auth/*` | **Public** |
| `GET /api/health` | **Public** — `auth_mode`, `auth_accounts` (count only) |
| SPA + other `/api/*` | **Protected** |

### 4b. mpub visibility (follow-on to ADR-0009)

| Mode | Behavior |
|------|----------|
| **public** | Anyone can `GET /mpub/{slug}` (and appears on public index) |
| **private** | Allowlisted signed-in admins only; anonymous → login redirect (browser) or 401 JSON |
| **Default on `mpub_publish`** | **private** unless user explicitly asks for public |
| **Legacy meta** (no `visibility` field) | Treated as **public** (preserve old links) |
| **Promote / demote** | `mpub_set_visibility` or `mpub_publish` with `visibility=` |
| **open auth mode** | No login; all viewers treated as admin (private still readable locally) |

### 5. Multi-user product

- **Shared** session list for all allowlisted admins.  
- One active turn per session.  
- User messages store `user_email`, `user_name`, `user_sub`.  
- **Model history:** plain content only — **no** identity prefix.  
- UI: identity chips on user bubbles; header shows email + logout.  
- Settings (authenticated): show **full allowlist emails** + client id + mode.  
- **Stop / cron Run-now:** record acting user in **server log + session events** only (not chat, not model).

### 6. TLS

```text
--tls-cert-file=/path/to/fullchain.pem
--tls-key-file=/path/to/privkey.pem
```

| Case | Behavior |
|------|----------|
| Both cert + key set | HTTPS on `--addr` |
| Only one of cert/key | **Fatal** |
| Neither | HTTP (today) |
| google mode + `https://` redirect + no TLS files | **Warn** only — reverse proxy / ALB / Caddy / Tailscale Serve OK |
| Auto Let's Encrypt | **Deferred** — document file certs + reverse proxy |
| :80 redirect / mTLS / key-via-env | **Deferred** |

## Decisions locked (Q1–Q28b)

| ID | Decision |
|----|----------|
| **Q1** | open when OAuth absent |
| **Q2** | google only when fully configured |
| **Q3** | partial OAuth → fatal |
| **Q4** | allow-emails ∪ allow-file |
| **Q5** | shared sessions |
| **Q6** | single busy lock |
| **Q7** | identity fields on user messages |
| **Q8** | health public |
| **Q9** | PKCE + state |
| **Q10** | 7d cookie |
| **Q11** | in-memory sessions |
| **Q12** | CSRF custom header |
| **Q13** | **no** identity to model |
| **Q14** | Settings: emails; health: count |
| **Q15** | exact emails only |
| **Q16** | no --auth-required |
| **Q17** | **A+B** log + session events for Stop / cron run-now |
| **Q18** | SSE cookie; 401 re-login |
| **Q19** | explicit redirect URL |
| **Q20** | Secure=false only localhost/127.0.0.1 |
| **Q21** | optional in-process TLS |
| **Q22** | `--tls-cert-file` + `--tls-key-file` |
| **Q23** | key file only in v1 |
| **Q24** | defer auto LE |
| **Q25** | partial TLS → fatal |
| **Q26** | no :80 redirect |
| **Q27** | defer mTLS |
| **Q28** | reverse-proxy TLS without local certs is valid |
| **Q28b** | **warn** if https redirect and no in-process TLS |

## Implementation order

1. Config: OAuth + TLS flags; mode detection; allowlist load; TLS file validation.  
2. `http.Server` ListenAndServeTLS when cert/key set; startup warnings for Q28b.  
3. `internal/auth`: Google OAuth, PKCE, cookie sessions, middleware, CSRF header.  
4. Wire protect SPA + API; public mpub/health/auth.  
5. Message identity + MD; UI chips; no model prefix.  
6. Attribute Stop / cron run-now in log + events.  
7. Settings allowlist list; health account count.  
8. Login/logout UI; SSE 401.  
9. Tests + README (Google Console, allowlist, cert files, reverse proxy).  

## Acceptance criteria

- [x] All open questions answered  
- [x] ADR locked + review HTML updated  
- [x] open/google modes; fail closed on partial OAuth/TLS  
- [x] mpub visibility (public open / private admin) + health public count only  
- [x] Identity on messages; never to model  
- [x] Optional file TLS; warn-only without certs for proxy  
- [x] A+B attribution for Stop / cron run-now  
- [x] Tests  

## Changelog

| Date | Change |
|------|--------|
| 2026-07-20 | Proposed |
| 2026-07-20 | Round 1 lock Q1–Q16, Q18–Q20 |
| 2026-07-20 | Round 2: Q17 A+B; TLS Q21–Q28b locked; reverse-proxy warn; accepted |

## References

- Google OAuth 2.0 / OIDC web server apps  
- ADR-0009 mpub; ADR-0016 env secrets  
- OWASP session management / CSRF  
