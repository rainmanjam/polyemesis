# Path to production — self-hosted v1.0

Ordered from the 2026-09-01 audit ([STATUS.md](STATUS.md)). "Production" here
means one operator running polyemesis on a machine they own, upgrading it
without losing data, and being told when something is wrong. Each item names
the issue that tracks it, the mistake-proofing rung the fix reaches, and the
evidence that will show it is done — because "fixed" without a check that can
fail is how three of the items below came to exist.

## Stage 0 — make the gates real (one afternoon)

Nothing else on this list is enforced until this is. Every required check on
`main` is advisory for the person who merges.

| # | Item | Rung | Done when |
|---|---|---|---|
| #649 | `enforce_admins: true` on the `main` ruleset | Control | `gh api …/branches/main/protection` shows `enforce_admins.enabled: true` |
| #648 | Add the three `container:` legs to required contexts | Control | protection lists 27 contexts; a red `acceptance-browser` blocks a PR |
| — | Make SonarCloud a required context, or remove it; same for Socket | Control | a failing quality gate turns `mergeStateStatus` to `BLOCKED`, not `UNSTABLE` |
| #651 | Run the `*_doc_drift_test.go` packages on the docs-only CI path | Control | a docs-only PR that breaks `platforms_doc_drift_test` goes red |

## Stage 1 — the operator cannot be harmed by the tools built to protect them

These are the four ways a careful operator, following the docs, loses the
install or the data. They block v1.0 outright.

| # | Item | Rung | Done when |
|---|---|---|---|
| #642 | `install.sh --tls acme` must not uninstall a working server: `verify` speaks SNI or acme carries a selfsigned fallback; the EXIT trap never removes an `active` service | Control + acceptance case | `acceptance-tls.sh` has an acme install case that passes; `install.sh --tls acme` on a fresh VM leaves a running unit |
| #643 | `update.sh` takes a consistent backup (`VACUUM INTO` or stop-first), opens it, runs `integrity_check`, and keeps the previous binary | Control | the script refuses to proceed on an unopenable copy — proven by corrupting one in the installer suite |
| #644 | An explicit `--config` path that is absent refuses to start | Control | `polyemesis --config /nope` exits non-zero naming the path; the default name still defaults |
| #645 | One shutdown context derived from `TimeoutStopSec`, threaded to every stop | Control | a wedged child is killed by polyemesis inside 45s and the recording is finalised; test asserts the sum of budgets < unit timeout |

## Stage 2 — security defaults an operator would expect

| # | Item | Rung | Done when |
|---|---|---|---|
| #647 | Throttle keys on the rightmost untrusted `X-Forwarded-For` hop; nginx example overwrites the header | Control | test: two requests with different spoofed leftmost hops share a throttle key |
| — | Password change revokes API tokens, or says at that moment that it does not (`api_tokens` are hash-only, `tokens.go:182`) | Warning | UI names surviving tokens on the password-change screen |
| — | No `config.yaml` → refuse plaintext on `0.0.0.0`, or bind loopback until TLS is configured (`config.go:129`, mode `""` → off) | Control | fresh start without config does not listen on all interfaces in the clear |
| — | Pull URLs go through `netguard` like webhooks do (`ValidatePullURL` checks scheme only) | Control | a pull of `http://169.254.169.254/` is refused |
| — | `POST /setup` throttled and, when the box has any listener bound before first login, documented as a first-boot window | Warning | rate-limited; INSTALL.md says to complete setup before exposing the port |

## Stage 3 — the console tells the truth

| # | Item | Rung | Done when |
|---|---|---|---|
| #646 | Re-resolve programme and `sourceCount` on source create/delete | Warning | provider test: second source created → `sourceCount` becomes 2 without reload |
| #638 | A programme switcher, so multi-source installs are not stuck on `source[0]` | Control | switcher writes through `rememberProgramme`; meters follow it |
| — | `useHeldStop` arms for *remaining* time (`useHeldStop.ts:39`); a held stop that unmounts either commits or says it was dropped | Control | test with real timers, not fake ones that mask it |
| — | `ConfirmDestructive` literals (`:77–78`) go through `t()` so `i18n.test.ts` can see them | Warning | 15 catalogues carry the keys; the guard fails if a literal returns |
| — | Failed reads render "could not read", never a positive empty state (`ClipsPage.tsx:271`, `RecordingsPage.tsx:101`) | Warning | `readState` guard extended to those two pages |
| — | Playout "Generate a new link" and "Purge history" go through `ConfirmDestructive` naming viewers / files | Warning | both call sites use the shared dialog |

## Stage 4 — upgrade and rollback are a procedure, not folklore

| # | Item | Rung | Done when |
|---|---|---|---|
| — | `update.sh` (docker) gets the same on-air guard as `uninstall.sh` | Control | it refuses while a destination is publishing, with `--force` |
| — | Both `update.sh` variants keep a rollback artefact (`.previous` binary / image digest) and print the rollback command | Control | `internal/upgrade` finds what the script left |
| — | `docs/UPGRADING.md:83` no longer says 0.7.0 is unreleased; the VACUUM credential scrub is stated as required; `:266` no longer says only the first playlist item plays | Warning | docs-drift check covers UPGRADING.md |
| — | `docs/MODULES.md:177–178` image table matches the Dockerfiles (`ubuntu:26.04`, `golang:1.27-alpine`); `TESTING.md` §10 matches ci.yml's matrix; `SITE-DEPLOY.md` names the `docs/**` filter | Warning | a test asserts MODULES.md's table against `FROM` lines |
| — | `/api/v1/health` reports something that can be false (engine reachable, DB opens, disk floor) | Warning | installer `verify` and the debug bundle read a real answer |
| — | A release runbook: merge → wait for push-to-main ci → tag → publish → deploy → verify; the same-day-UTC changelog gate documented | 0 → Warning | `docs/RELEASING.md` exists and the release workflow links to it |
| — | `secret.key` minted over an existing database logs a WARN naming the count of destinations that will fail to open | Warning | boot log line + test |

## Stage 5 — the checks that certify the above can fail

| # | Item | Rung | Done when |
|---|---|---|---|
| — | `make check` runs what CI runs (`-race`, `POLYEMESIS_LEDGER=strict`, `coverage-instrument-guard`); parity guard matches the real invocation | Warning | mutating the Makefile turns `check_parity_test` red |
| — | `web/` in dependabot and in `npm audit`; `oxlint` warnings that matter promoted to errors (`exhaustive-deps`) | Warning | `npm run lint` fails on a stale-closure warning |
| — | `HWEncoders` read under `t.mu` in `clips.go:664`; FFmpeg floor fails closed on an unparseable version | Control | `-race` on the clip path; a `ffmpeg -version` of garbage refuses to start |
| — | Calculator copy matches `platforms.go` (advisory, not enforced); YouTube 3-destination warning removed; `/download` says Go 1.27+ | Warning | site check-build asserts figures against `platforms.go` |

## Stage 6 — the backlog that is still open

- #440 Windows heap abort — reopened, cause unexplained.
- #398 respawn cause — original reading refuted; three parked branches hold the measurements (`fix/398-relay-probe-window`, `measure/398-rtmp`, `fix/398-seam-pin`).
- #627 HEVC → RTMP — the most shippable of the three.
- #628 / #631 grace-period kills and children outliving shutdown — both become tractable once #645 gives shutdown one budget.
- #375 / #376 / #380 — **no hardware encode and no real minted-key publish has ever been observed.** #376 and #380 cite runbooks deleted by `c99fc00e`; either restore them or rewrite the issues.

## What "v1.0" would then mean

An operator can install with any documented TLS mode and keep the server;
upgrade and get back to the previous release with their data; be throttled
when guessed at, behind the proxy the docs told them to use; shut the service
down and keep every recording; and see the console describe the install they
actually have. Every one of those sentences has a check above that fails when
it stops being true.

## Branch dispositions (from the same audit)

| Branch | Disposition | Why |
|---|---|---|
| `fix/release-blockers`, `fix/440-unsafe-pointer`, `fix/460-relay-consumer-probe` | delete | tree diff against `main` on their own files is 0 — content landed via other PRs |
| `feat/442-device-flow-ui`, `feat/442-twitch-device-flow` | delete | superseded by closed #442 |
| `fix/398-header-only-detection` | salvage | 42 lines still differ from `main` |
| `fix/398-relay-probe-window`, `measure/398-rtmp`, `fix/398-seam-pin` | record into #398, then delete | explicit PARKED bodies with measurements |
| `test/reconcile-teardown-suite` | merge when #628 lands | the only #631 detector |
| `chore/wiki-publisher` | decide | 153 lines, never had a PR |
| dependabot #622 #623 #624 | merge | `MERGEABLE`, only `BEHIND` |
| `.claude/worktrees/agent-a10ff…` (706-line diff) | discard | draft of #387 PR-5, superseded by merged #408 |
