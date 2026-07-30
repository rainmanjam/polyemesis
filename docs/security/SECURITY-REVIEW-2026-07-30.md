# Security Review: polyemesis, full application

**Date:** 2026-07-30 · **Commit:** `90e0a83` (`feat/overlays-v05`, PR #23 pre-merge)
**Type:** Static review. No active testing was performed against any running instance.
**Scope:** Go backend (`cmd/`, `internal/`), React UI (`ui/src`), dependency tree, git history.

> **Status: all findings remediated, 2026-07-30.** Every item below was fixed before
> merge rather than scheduled. Each fix carries a test that fails when the fix is removed —
> verified by mutation, not assumed. See [Remediation, as shipped](#remediation-as-shipped)
> for what changed and what each guard catches.
>
> Findings are kept in their original form above that section. A review rewritten to
> describe the fixed state loses the thing worth keeping: what was actually wrong, and how
> it got that way.

---

## Executive summary

**Risk posture at review: Low. Post-remediation: Low, with the Medium closed.** No Critical
or High findings at any point.

Six findings: one Medium, three Low, two Informational. The Medium was a latent defense that
was built and never switched on — `KickConfig.Verify` existed, was nil-checked, and was never
assigned, so webhook signature verification was skipped at runtime.

The codebase is unusually well-defended for its size, and most of what an audit normally
finds is already handled deliberately and with a comment explaining why:

| Control | State |
|---|---|
| Password storage | `bcrypt.DefaultCost` (10) — `internal/db/users.go:54` |
| JWT algorithm pinning | `*jwt.SigningMethodHMAC` asserted — blocks `alg:none` and HMAC/RSA confusion |
| CSRF | Double-submit cookie, `requireCSRF` on the whole authenticated group |
| Security headers | CSP, `X-Frame-Options: DENY`, `nosniff`, `Referrer-Policy: no-referrer`, opt-in HSTS |
| Secrets at rest | NaCl `secretbox` (XSalsa20-Poly1305) — `internal/secrets/secrets.go` |
| API tokens | 256-bit CSPRNG, only SHA-256 stored — `internal/db/tokens.go` |
| Token comparison | `subtle.ConstantTimeCompare` in playout and SRT paths |
| Login throttle | Per-IP, checked *before* bcrypt runs — `internal/api/handlers.go:79` |
| Proxy header trust | `X-Forwarded-*` honoured only under `TrustProxyHeaders` |
| Randomness | `crypto/rand` for every security-relevant value |
| XSS | Zero `dangerouslySetInnerHTML` in `ui/src` — React escaping throughout |
| First-run setup | `CreateUser` refuses to run twice; cannot take over an existing install |

---

## Automated scan results

| Tool | Result |
|---|---|
| `govulncheck ./...` | **0** reachable vulnerabilities; 1 in a required-but-uncalled module |
| `osv-scanner` | 1 — `GO-2026-5932`, `x/crypto/openpgp` unmaintained (not imported; see SEC-6) |
| `npm audit` (239 deps) | **0** at every severity |
| `trivy fs` (vuln+secret+misconfig, HIGH/CRITICAL) | **0** |
| `gitleaks` (320 commits, 12.75 MB) | 6 — all test fixtures (see SEC-5) |
| `semgrep` (254 files, `r/go` + `r/javascript.react`) | 58 raw → 4 warranted findings after validation |

### A note on the semgrep run

The first semgrep invocation (`p/security-audit`, `p/golang`, `p/react`, `p/typescript`)
reported `TOTAL 0` and **would have been recorded as a clean pass**. It was not clean —
it was empty. One registry config failed to parse, semgrep aborted rule loading, and
`paths.scanned` was `0`. Zero files were examined.

A zero-finding SAST result must be corroborated by a non-zero scanned-file count before it
means anything. The rerun with `r/go` + `r/javascript.react` scanned 254 files and returned
58 hits. **If CI runs semgrep, it must assert `paths.scanned > 0` or it can pass forever
while testing nothing.**

---

## Threat model (STRIDE)

| Threat | Asset | Vector | Mitigation | Residual |
|---|---|---|---|---|
| **S**poofing | Chat events | Forged Kick webhook POST | 128-bit secret path segment only; signature unverified | **SEC-1** |
| **S**poofing | Operator session | Stolen JWT cookie | HttpOnly, Secure under TLS, 7-day TTL | **SEC-2** — not revocable |
| **T**ampering | Stream keys, OAuth tokens | DB read | `secretbox` sealed at rest | None |
| **T**ampering | Request in transit | MITM | TLS; HSTS opt-in | None |
| **R**epudiation | Moderation actions | Ban/delete with no audit trail | Structured logs only | Low — no tamper-evident log |
| **I**nfo disclosure | Stream keys in alerts | Webhook/notify payloads | `internal/alerts/redact.go`, tested | None |
| **I**nfo disclosure | Build fingerprint | `/api/v1/version` | Deliberately authenticated | None |
| **D**oS | Login endpoint | Credential stuffing | Per-IP throttle before bcrypt | None |
| **D**oS | Kick webhook | Unbounded body | `io.LimitReader(kickBodyLimit)` | None |
| **E**oP | Admin functions | Direct API call | Single-role model; `requireAuth` + `requireCSRF` on the whole group | None |

---

## Findings

### Medium

#### [SEC-1] Kick webhook signature verification is implemented but never wired — CVSS 3.7 (`AV:N/AC:H/PR:N/UI:N/S:U/C:N/I:L/A:N`)

`internal/chat/kick.go:75` declares the hook, `:343` guards it, `internal/api/chat_wiring.go:172`
never sets it:

```go
// kick.go:75
Verify func(r *http.Request, body []byte) error

// kick.go:343 — nil at runtime, so the whole block is skipped
if k.cfg.Verify != nil {
    if err := k.cfg.Verify(r, body); err != nil { ... }
}
```

`chat.NewKick(chat.KickConfig{...})` sets `AccountRef`, `BroadcasterUserID`, `Channel`,
`Token`, `PublicURL`, `CallbackSecret` — and no `Verify`. Grepping the entire repo for
`Verify:` returns only MQTT's `TLSSkipVerify`. It is nil on every code path.

The only control on `POST /api/v1/chat/kick/{secret}` is the 128-bit `crypto/rand` path
segment. That is a genuine and well-built control — but it is a bearer secret in a **URL**,
and URLs leak in ways signatures do not: reverse-proxy access logs, any non-TLS hop,
`Referer` on an adjacent page, and Kick's own storage of the callback.

**Business impact.** An attacker who learns the URL can inject arbitrary chat events into
the operator's unified pane. Not code execution — React escapes the text and the DB write is
parameterised. The impact is *deception*: forged messages can carry a moderator badge, an
arbitrary display name, and an `author_id` that the new moderator user card will happily
render a history for. The operator's moderation decisions are made from that pane.

**Remediation.** Kick signs webhooks; verify the signature and make the hook mandatory rather
than optional, so a future adapter cannot silently ship unverified again:

```go
// chat_wiring.go — wire it
return chat.NewKick(chat.KickConfig{
    ...
    Verify: chat.KickSignatureVerifier(kickPublicKey),
})

// kick.go — fail closed instead of open
if k.cfg.Verify == nil {
    http.Error(w, "webhook verification is not configured", http.StatusServiceUnavailable)
    return
}
```

Flipping the nil case from *skip* to *refuse* is the durable half: it converts a silent
omission into a startup-visible failure.

---

### Low

#### [SEC-2] Sessions cannot be revoked before their 7-day expiry — CVSS 3.1 (`AV:N/AC:H/PR:L/UI:N/S:U/C:L/I:N/A:N`)

`internal/auth/auth.go:32` sets `sessionTTL = 7 * 24 * time.Hour`. Sessions are stateless
JWTs, so:

- `POST /auth/logout` clears the cookie but the token stays valid for up to 7 days.
- `handleChangePassword` (`internal/api/handlers.go:170`) **re-issues** rather than
  invalidating — the comment says so explicitly: *"a password change should refresh the
  session, not end it."* Other live sessions keep working.

A password change is the standard response to *"I think someone has my session"*, and here it
does not achieve that.

The 7-day TTL itself is a defensible call for a single-operator tool (documented: *"long
enough that a streamer is not logged out mid-broadcast"*). The gap is revocation, not
duration.

**Remediation (cheap version):** add a `token_epoch` integer to `users`, embed it as a claim,
bump it on password change, and reject tokens whose epoch is stale. ~15 lines, keeps sessions
stateless, and makes password-change-as-panic-button actually work.

#### [SEC-3] Webhook secret compared with `!=` rather than in constant time — CVSS 2.6 (`AV:N/AC:H/PR:N/UI:N/S:U/C:N/I:L/A:N`)

`internal/api/chat_wiring.go:251`:

```go
if want == "" || chi.URLParam(r, "secret") != want {
```

Go's string comparison short-circuits on the first differing byte. Network jitter makes this
effectively unexploitable across the internet, which is why this is Low and not higher.

It is worth fixing anyway for a reason that is not the timing channel: this repo already uses
`subtle.ConstantTimeCompare` for the playout token (`internal/api/playout.go:346`) and the SRT
token (`internal/srtserver/srtserver.go:416`). One bearer secret compared a different way is
an inconsistency a reader has to stop and re-derive. Make it three for three.

#### [SEC-4] First-run setup has a TOCTOU window — CVSS 3.7 (`AV:N/AC:H/PR:N/UI:N/S:U/C:N/I:L/A:N`)

`internal/db/users.go:47-60` does `HasUser()` and then `INSERT` without a transaction or a
guard on the table. Two concurrent `POST /api/v1/setup` requests can both pass the check.

`username TEXT NOT NULL UNIQUE` (`schema.sql:5`) saves the identical-username race, but two
requests with *different* usernames both succeed and both get a session.

Only reachable on an install that has never completed setup, and only within a millisecond
window, so the practical risk is close to zero. The fix is nearly free — wrap it in a
transaction, or make the guard structural:

```sql
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_singleton ON users((1));
```

which makes "there is at most one user" an invariant the database enforces rather than one
the application checks.

---

### Informational

#### [SEC-5] gitleaks reports 6 secrets — all are test fixtures

| File | What it is |
|---|---|
| `internal/alerts/redact_test.go:10` | `live_284729384_pQ8fZmT3xR9wLkYvB2nHsA` — a fake stream key, the input to the redaction tests |
| `internal/oauth/facebook_test.go:111,197` | Fake Facebook ingest URLs (`.../rtmp/1234567890`) |

No action needed on the values. **Action needed on CI:** if gitleaks runs there, add a
`.gitleaksignore` or a fixture allowlist. Six permanent known-failures is how a scanner stops
being read.

#### [SEC-6] `GO-2026-5932` — `x/crypto/openpgp` unmaintained — not exploitable here

Flagged by osv-scanner. `govulncheck` confirms it is not reachable: *"1 vulnerability in
modules you require, but your code doesn't appear to call these."* `git grep openpgp` finds
only an indirect `go.mod` entry (`github.com/benburkert/openpgp`). No import exists. Track it;
do not gate the merge on it.

---

## Validated as NOT vulnerable

Recording these so the next review does not re-derive them:

| Flagged | Why it is fine |
|---|---|
| `internal/chat/kick.go:330` — `io.WriteString(w, r.URL.Query().Get("probe"))` | Reflected input, but `Content-Type: text/plain; charset=utf-8` is set on the line above **and** the global middleware sends `X-Content-Type-Options: nosniff`. No sniffing path to HTML. |
| `cmd/polyemesis/main.go:414` — semgrep `open-redirect` | `redirectHost` prefers the configured hostname; the certificate is only valid for that name regardless. Same-scheme upgrade, not an attacker-controlled destination. |
| `internal/api/oauth_handlers.go:45` — `X-Forwarded-Host` into the OAuth `redirect_uri` | Gated on `TrustProxyHeaders`, and the platform rejects a `redirect_uri` that does not match the registered one. |
| `internal/auth/throttle.go:153` — XFF-keyed rate limiting | Correctly gated on `trustProxy`; falls back to `RemoteAddr`. The comment states the exact bypass this prevents. |
| `internal/playout/handler.go:141` — `Access-Control-Allow-Origin: *` | Public media only, and no `Access-Control-Allow-Credentials`, so no credentialed cross-origin read is possible. |
| `internal/mqtt/client.go:173` — `InsecureSkipVerify` | Operator opt-in for a self-signed broker, surfaced in the UI as exactly that. |
| `internal/transcribe/download.go:151` — SHA-1 | Integrity check against the model vendor's published checksum. SHA-1 is what upstream publishes; not a security decision this repo gets to make. |
| `internal/chat/hub.go:939` — `math/rand` | Purge-interval jitter. Not security-relevant. |
| 21 × `os.MkdirAll(..., 0o755)` | Server-owned data directories. `internal/fsperm` handles the cases that need tighter modes. |
| 18 × `exec.CommandContext` | All FFmpeg/ffprobe invocations with argv slices, never a shell string. No injection path. |
| `internal/db/*.go` — concatenated SQL | Every concatenation is of `const` column lists; all values travel as `?`. Already hardened deliberately after a SonarCloud `go:S2077` report. |

---

## Remediation, as shipped

All six items were fixed on 2026-07-30, before merge. Full Go suite green under `-race`
(30 packages), `gofmt` clean, `tsc -b` clean, `oxlint` clean, UI build clean, gitleaks clean.

| # | Shipped | Guard that fails if it regresses |
|---|---|---|
| SEC-1 | `internal/chat/kick_verify.go` — RSA-PKCS1v15/SHA-256 over `{id}.{ts}.{body}`, key fetched and cached from Kick's `/public/v1/public-key`. Wired at `chat_wiring.go`. Handler now **refuses** on a nil `Verify` instead of skipping | `TestKickHandlerRefusesEveryDeliveryWhenNoVerifierIsConfigured`, `TestKickHandlerRejectsAnUnsignedDelivery`, 9-case rejection table, **`TestKickAdapterIsBuiltWithSignatureVerification`** |
| SEC-2 | `users.token_epoch`, embedded as a JWT claim and checked on every request. `SetPassword` bumps it in the same `UPDATE`. `auth.New` now **requires** an `EpochFunc` and fails closed without one | `internal/auth/epoch_test.go` (6 tests), `TestSetPasswordBumpsTheTokenEpoch`, `TestMigrateUserTokenEpochUpgradesAnExistingInstall` |
| SEC-3 | `secretEqual` in `internal/api/security.go`, wrapping `subtle.ConstantTimeCompare`. Three of three bearer secrets now compare the same way | `TestSecretEqual` (8 cases) |
| SEC-4 | `CreateUser`'s INSERT carries `WHERE NOT EXISTS (SELECT 1 FROM users)` — one statement, atomic in SQLite. New `ErrUserExists` | `TestConcurrentFirstRunSetupCreatesExactlyOneAdmin` (8 goroutines, distinct usernames, `-race`) |
| SEC-5 | `.gitleaks.toml` with `condition = "AND"` on paths **and** values | Verified by planting a new secret in an allowlisted file: still caught |
| CI | New `.github/workflows/security.yml`: govulncheck, npm audit, gitleaks, semgrep — with the scanned-file assertion | Replayed against the original 0-file scan JSON: correctly fails |
| SEC-6 | Unchanged. `GO-2026-5932` is unreachable; monitor | `govulncheck` in the new workflow |

### Two things worth recording

**The wiring guard is the one that mattered.** Mutation testing each fix showed that deleting
`Verify:` from the production construction site — *the original bug, exactly* — broke **no
test at all**, even with the full signature suite in `internal/chat` passing. Those tests
build their own adapter with their own config, so they can never observe what the real server
constructs. `TestKickAdapterIsBuiltWithSignatureVerification` goes through `chatAdapter` and
asserts an unsigned POST gets `401`; it fails with a named message if the wiring is removed.
A guard on a component proves nothing about the call site that forgot to use it.

**`auth.New` and the Kick handler now both fail closed on a missing dependency**, and that
shape is deliberate rather than defensive habit. The entire SEC-1 finding was one `!= nil`
guard doing what it was written to do. An optional security control is an absent one.

### Correcting a premise this repo had recorded

`KickConfig.Verify` carried the comment *"Kick does not document a signature scheme this code
can verify"* and *"the hook for Kick's signature header once its scheme is documented. Nothing
here invents one."* That was an honest position — better than shipping a check that only
looked like one — and it was **out of date**. The scheme is fully documented at
`docs.kick.com/events/webhook-security`, including a Go reference implementation.

This is the second time in this codebase that a recorded "not possible" turned out to be
stale: the Kick stream key was documented as unavailable until it was found riding on a
response already being fetched. Both cost a real capability for months.

The pattern is worth naming: **a "we looked and there was nothing" comment justifying a
disabled security control deserves re-checking on a schedule**, because it ages silently and
nothing fails when it goes stale.

---

## Coverage limits

Stated plainly so nobody reads more assurance into this than it earned:

- **Static only.** No running instance was probed, no authorization matrix was exercised
  against live endpoints, no fuzzing.
- **The Kick verifier has never seen a real Kick delivery.** It is tested against
  locally-generated RSA signatures built to the documented format, by a signer written
  independently of the verifier so the two cannot agree on a shared mistake. That catches a
  wrong implementation; it cannot catch a wrong *specification*. The first genuine webhook
  after a Kick account is connected is the real test — if chat stops arriving, this is the
  first thing to check, and `internal/chat/kick_verify.go` documents the exact byte layout to
  compare against.
- **The chat moderation UI has never been exercised end to end** — it needs a connected
  platform account, and the acceptance suite has no way to supply one. The Go and API layers
  are unit- and mutation-tested; the browser click-through path is not.
- **No container image scan.** `trivy fs` covered the source tree; `trivy image` against a
  built `Dockerfile`/`Dockerfile.cuda`/`Dockerfile.vaapi` artifact was not run and would be
  worth adding to release CI.
- **Third-party platform APIs are trusted** to behave as documented.
