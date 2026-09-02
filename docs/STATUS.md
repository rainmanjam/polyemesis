# Status — 2026-09-01, main `99262db2`, latest release v0.8.0

What works, what is known broken, and how each claim was established. Produced
by a twelve-reviewer audit graded against **self-hosted v1.0**: one operator
running polyemesis on their own machine. Every defect listed here was verified
by a second reader against the cited line before it was written down; the
reviewers' own reports are summarised, not copied.

Companion: [PATH-TO-PRODUCTION.md](PATH-TO-PRODUCTION.md) orders the fixes.

## In one paragraph

The streaming path is sound and unusually well-guarded: the engine publishes
every child under one lock behind a stopped-guard and unwinds ports and hub
subscriptions on the losing branch; `go test -race` is clean across
engine/relay/supervisor; the API enforces authorisation at the router and
derives its route population from registrations; credentials are sealed at
rest with `secure_delete` and re-asserted permissions; the test suite carries
nine guards with red fixtures that prove they can fail. **What is not yet
production-grade is the operator's safety net around that path** — the
installer, the backup, the login throttle behind a proxy, the shutdown budget,
and the merge gates — and in each case the device exists and is not wired to
the thing it protects.

## Works, with evidence

| Area | Claim | Evidence |
|---|---|---|
| Engine lifecycle | Five child-start paths publish under `e.mu` behind `e.stopped` and release port + hub subscription on failure | each path read; `go test -race ./internal/engine ./internal/relay ./internal/supervisor` clean |
| Engine | `reconcileMu` covers all three production reconcile entry points; relay fan-out serialised across both writers | read |
| Process control | Process groups + `KillMode=mixed` in the shipped unit | `deploy/polyemesis.service`, `scripts/install.sh:1523` |
| API authz | Enforced at the router, not per handler; route population derived from registrations; read-scope denial and credential redaction pinned by reflective classification tests | 9 guards + `TestLedgerPreflight` (23 subtests) run, pass |
| API | Webhook/alert SSRF closed at dial time against one shared range list (`internal/netguard`) | read; imported by `hooks`, `alerts` |
| Auth | JWT alg-pinned; epoch revocation fails closed; bcrypt default cost; first-run bootstrap race-safe (`INSERT … WHERE NOT EXISTS`); `-reset-admin` never reopens setup | read; `users.go:192 ErrUserExists` |
| Secrets at rest | `secure_delete` in DSN (`db.go:168`); WAL checkpoint on the no-work boot with regression test; `wal_checkpoint(TRUNCATE)` read back; sidecars chmod'd on every open; read path fails closed | read |
| Migrations | Forward-only with three devices above rung 0: `refuseNewerSchema` before any write, an AST check on `Open`, and `previous_release_schema_test.go` opening the real shipped previous schema | tests run |
| Scans | `govulncheck` 0 · `npm audit` 0 · gitleaks 0 tracked hits | run 2026-09-01 |
| Path safety | Containment at all eight media/recording/upload/overlay resolvers; no shell invocation anywhere; drawtext escaping measured | read |
| Tests | 45 Go packages green, median coverage ~90%, security packages 76–100%; vitest 94 files / 747 tests green; skip ratchet at zero headroom (95 = 95 = 95) | run |
| Guards | Vacuity controls fired 4/4: a planted `t.Skip`, a lowered CI ceiling, a reworded grace log line, and a blanked `npm test` each turned the matching guard red | mutation + restore |
| CI | 13 required `acceptance:` contexts match ci.yml's matrix exactly; gitleaks plants a secret in an allowlisted file to prove the allowlist is not over-broad and refuses a 0-commit range; release gates refused 3 of the last 5 tag pushes before publishing anything | `gh api` + read |
| Installer | 16-step acceptance suite generates the script under test; 4-distro preflight matrix; `SHA256SUMS` enforced (missing sums also refuses); FFmpeg detection with live filter probe; `secret.key` backup refusal in both modes | read |
| Site | Builds (39 pages); 0 dangling internal links; all 47 repo deep-links resolve; Pages publishes on `main` for `web/**`, `docs/**`, `pages.yml` | build + sweep |
| UI | All 15 locales at 1425 keys; zero `window.confirm`; 63/63 icon buttons carry an accessible name; `ConfirmDestructive` shared by 13 sites with a typed-subject contact device; `useHeldStop` is a real delayed-commit undo | tests + read |
| Production | v0.8.0 deployed to the OVH host 2026-09-01 06:34 UTC; clean shutdown and start; 0 WARN/ERROR in the first three minutes | journal |

## Known broken — Critical

Each entry: what the operator does, what happens, whether it is silent, what
exists today, the device that would stop it and the rung it reaches.

**C1 (#642) — `install.sh --tls acme` uninstalls the server it just installed.**
`verify()` (`scripts/install.sh:2011`) probes `https://127.0.0.1:PORT` with
`-k`; `initACME` (`internal/tlsx/tlsx.go:204`) sets only `GetCertificate`, so a
handshake with no SNI has no certificate to offer and aborts — `-k` cannot
help. `verify` returns 1 at `:2266`, *before* `INSTALL_COMPLETE=true` at
`:2268`, so the EXIT trap (`:273`) disables the unit, removes the binary,
`/etc/polyemesis`, `/opt/polyemesis` and the service account of a working
server, then prints that nothing was left running. Silent: it looks like a
failed install. Guard today: none — CI runs `--check` only and the TLS
acceptance suite has no acme install. Device: `verify` must speak SNI for the
configured hostname, or acme mode must carry a self-signed fallback in
`conf.Certificates`; and the trap must never remove an install whose service
reached `active`. Warning rung (an acceptance case) plus Control rung (the
trap condition).

**C2 (#643) — The only rollback from a forward-only migration is a backup that may
not open.** Both generated `update.sh` scripts (`scripts/install.sh:1651`
binary, `:1892` docker) copy a *live*, actively-written WAL database and stop
the service afterwards. The guard checks that `secret.key` and `polyemesis.db`
exist, never that the copy opens; no `integrity_check` or `VACUUM INTO` exists
in the tree. It then prints "backup verified". Silent until the day the backup
is needed. Device: stop first, or `VACUUM INTO` a consistent copy while live;
open the copy and run `PRAGMA integrity_check` before printing "verified".
Control rung — the script cannot proceed past an unopenable backup.

**C3 (#647) — Login throttling is bypassable with the shipped proxy config.**
`deploy/nginx.conf.example:75` sets `$proxy_add_x_forwarded_for`, which
*appends*; `auth.ClientIP` (`internal/auth/throttle.go:186`) takes the
*leftmost* hop. `SECURITY.md:232` and `docs/INSTALL.md:597` both instruct
`trustProxyHeaders: true`. An attacker rotating `X-Forwarded-For` gets a fresh
throttle key per request: unlimited guessing at the single admin password, and
attacker-chosen IPs in the audit log. Silent. Device: take the *rightmost*
untrusted hop (the one the proxy appended), and ship `X-Forwarded-For
$remote_addr` (overwrite) in the example. Control rung.

**C4 (#644) — A `--config` path that does not exist boots a different, empty
install.** `config.Load` returns defaults on `IsNotExist`
(`internal/config/config.go:143`); `main.go:109` cannot tell an explicit
`--config` from the default name. A typo creates `./data`, mints a **new
`secret.key`**, opens an empty database, binds `:8080` plaintext and reopens
unauthenticated `POST /setup`. Silent: the server starts. Device: refuse to
start when a path was given explicitly and is absent. Control rung.

**C5 (#645) — Shutdown has no single deadline.** `TimeoutStopSec=45`
(`deploy/polyemesis.service:68`) bounds a sequence of independently chosen
budgets: 20s HTTP (`cmd/polyemesis/main.go:439`), 30s per engine
(`internal/engine/engine.go:881`, covering only three of its children),
serial per-tier teardown, and a captioner wait that takes no context. A
wedged child pushes the sum past 45s; systemd SIGKILLs the cgroup and
`grace.go` names the result: a truncated Matroska file that is exactly the
right size on disk. Silent. Device: one shutdown context derived from
`TimeoutStopSec` minus margin, threaded to every stop. Control rung.

**C6 (#646) — The console resolves its programme once per page load.**
`LiveDataProvider.tsx:225` fetches `/sources` with `[]` deps and nothing
re-resolves `programme` or `sourceCount`. Creating a second source during
setup, or deleting the current one, desynchronises Meters, Monitoring and Clips
against a healthy server until reload. Silent. Distinct from #638. Device:
re-resolve on source create/delete events (the socket already carries them).
Warning rung.

**C7 (#648) — The `container:` acceptance legs report but gate nothing.** Branch
protection lists 24 required contexts and 0 of the three `container:` legs.
`container: acceptance-browser` — the only pre-merge Playwright coverage of
the shipped image — failed 10 of 137 runs and never blocked a merge. Device:
add the three contexts to the ruleset. Control rung.

**C8 (#649) — Branch protection does not apply to the person who merges.**
`enforce_admins: false`, `required_approving_review_count: 0`. On a
single-maintainer repository whose maintainer is the admin, all 24 required
contexts are advisory. Every other gate in this document is downstream of this
one. Device: `enforce_admins: true`. Control rung.

## Known broken — Major (summary; full detail in the reviewers' reports)

- API: password change bumps `users.token_epoch` but `api_tokens` are looked up
  by hash only (`tokens.go:182`) — a leaked admin token survives incident
  response. `POST /setup` is unauthenticated and unthrottled: first boot is a
  race for the install (bootstrap is race-*safe*; the window is still public).
- DB: `secret.key` is minted silently over a restored data directory
  (`secrets.go:88–96`); `-reset-admin` runs migrations against a possibly-live DB.
- Security: no `config.yaml` → plaintext HTTP on `0.0.0.0:8080`; pull URLs
  bypass `netguard` (`ValidatePullURL` checks scheme only); self-update trusts
  an unsigned `SHA256SUMS`; `ResolveModel` accepts any existing absolute path
  (admin-only — graded Minor for self-hosted).
- Ops: docker `update.sh` restarts the container with no on-air guard while
  `uninstall.sh` has one; neither `update.sh` keeps a rollback artefact;
  in-app upgrade is unreachable under `ProtectSystem=strict`;
  `docs/UPGRADING.md:83` still says 0.7.0 is unreleased; `/health` is a
  constant that three mechanisms treat as proof; no docker log rotation.
- CI: SonarCloud and Socket gate nothing; `web/`'s lockfile is never audited or
  updated; `changelog-gate` couples a release to a same-day UTC window; no
  release runbook; the `-race` step has ~1.4 min headroom against its own
  20-minute deadline with nothing pinning the margin.
- Tests: `make check` runs plain `go test ./...` (`Makefile:84`) — no `-race`,
  no `POLYEMESIS_LEDGER=strict` — while its parity guard matches `\bgo test\b`
  and certifies parity it does not have.
- UI: `useHeldStop.ts:39` arms its timer for elapsed rather than remaining
  time; `ConfirmDestructive` ships English literals (`:77–78`) that
  `i18n.test.ts` cannot see; two pages render a positive empty state for a
  failed read; `npm run lint` cannot fail CI on the rule that catches the
  stale-closure class behind #606/#612.
- UX: playout "Generate a new link" rotates the token on one unconfirmed
  click (`PlayoutPage.tsx:498`); a held stop is discarded on unmount; chat
  delete is a hover icon on a scrolling list; "Purge history" deletes export
  files before reporting; expert Apply/Clear restarts a live destination
  without saying so.
- Site: the calculator says platform ceilings are "the same figures the server
  validates against" — `internal/db/platforms.go:349` says **ADVISORY,
  ALWAYS** and no Go code reads `KbpsMax`; its "YouTube is capped at 3
  destinations" warning is stale (`oauth_handlers.go:603`) and fires with no
  YouTube selected; `/download` says Go 1.26.5+ against `go.mod`'s 1.27.0.
- History: #376 and #380 cite runbooks deleted by `c99fc00e` the same day;
  the only #631 detector lives on an unmerged branch; no hardware encode and
  no real minted-key publish has ever been observed (#375/#376/#380).

**C9 (#651) — The docs-drift guards never run on the PRs that drift the
docs.** The guards are Go tests (`internal/oauth/platforms_doc_drift_test.go`
reads `docs/`); ci.yml's documentation gate skips every Go step when a PR
touches only documentation (`ci.yml:201–207`). Silent: the docs-only leg
reports green as "did no work". Device: run the `*_doc_drift_test.go`
packages on the docs-only path — they read markdown and cost seconds.
Control rung.

## Documentation drift (verified)

Fourteen cross-document contradictions were confirmed; the ones an operator
acts on:

- `docs/UPGRADING.md:83` — 0.7.0 "not yet released" (v0.7.0 and v0.8.0 are
  tagged), prefacing a mandatory credential-scrub `VACUUM` with "nothing below
  applies to you".
- `docs/UPGRADING.md:266` — "only the **first** playlist item plays;
  sequencing is a later change" — `selector.go` emits a concat entry per item,
  `internal/ffmpeg/concat.go` renders it, and `acceptance-playlist-phase0` is a
  required CI leg. The stale sentence sits inside a *breaking* migration note.
- `docs/MODULES.md:177–178` — `Dockerfile.vaapi` base `ubuntu:24.04` (it is
  `ubuntu:26.04`, `Dockerfile.vaapi:89`; `HARDWARE.md` has it right) and build
  stage `golang:1.26-alpine` (all three Dockerfiles: `golang:1.27-alpine`) —
  contradicting the same file's "Go 1.27.0 floor" ten lines above.
- `docs/INSTALL.md` tells the operator to fix the `:443` warning in a way the
  code does not honour (docs MAJ-2, detail in the audit report).
- `docs/TESTING.md` §10's acceptance-suite inventory disagrees with ci.yml's
  matrix in three places.
- `docs/DEPENDENCIES.md` states an `@types/node` invariant the lockfile does
  not hold.
- The CUDA image pins FFmpeg 6.1.1, below the 7.1 four documents say Enhanced
  RTMP needs; nothing connects the two facts.
- `docs/SITE-DEPLOY.md:3–5` omits `docs/**` from the Pages path filter.

Six undocumented, tested behaviours and five unverified leads are listed in
the reviewer's report. Of thirteen contradictions the knowledge graph
proposed, six were false on reading — the graph was useful for reach, not
judgement.

## Housekeeping observed during the audit

Not defects: the gitnexus index reports a storage-version mismatch (43 vs
build 40); serena has no active project and no `gopls`; `agy` is
permission-denied in headless mode. Reviewers fell back to `codex exec` for
independent reads, which agreed on every top finding.
