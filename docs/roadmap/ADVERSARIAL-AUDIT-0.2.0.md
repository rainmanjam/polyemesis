# Adversarial audit of 0.2.0

**Run 2026-08-05, against the v0.2.0 tag.** Five passes over the 27.9k lines of
real change between `v0.1.0` and `v0.2.0` — 9.4k non-test Go, 13.3k test Go,
1.9k UI, 3.2k CI and scripts, across 214 files.

Nothing here is filed. This document is the ranked list; deciding what becomes
an issue is a separate step.

## Method

Five passes, deliberately overlapping, so that a finding only one of them sees
is a finding one of them probably got wrong:

| Pass | Surface |
| --- | --- |
| 1 | `internal/engine`, `internal/api`, `internal/oauth`, `internal/db`, `internal/srtserver` — tests excluded |
| 2 | `ui/src`, `.github/workflows`, `scripts/` |
| 3 | every `_test.go` change — guards that cannot fail |
| 4 | secrets, authorization, injection, workflow trust |
| 5 | design and maintainability, plus an independent read |

**Every finding below was re-verified against the tree by a second reader
instructed to refute it**, and the verification quoted the decisive code. One
claim did not survive and is recorded under [Refuted](#refuted) rather than
deleted — the refutation is worth as much as the finding. One finding
([C2](#c2-the-deliveries-route-guard-passes-with-the-route-deleted)) was
settled by running its named mutation against a committed tree, which is this
project's own standard.

The bar was "everything, including design and maintainability", so the list
runs from a leaked FFmpeg process down to a misplaced comment.

---

## The short version

Nothing here endangers a stream that is already running normally. Everything
that hurts is in the **edges of the new backup-ingest and pre-announce work**:
shutdown, concurrent reconcile, a schedule that collides with another, and an
operator saving a dialog at the wrong moment.

Five findings are one theme. **The backup output was added to `internal/engine`
as a second process hanging off `destination`, and the paths that manage the
primary's whole lifecycle were not all taught about it.** Shutdown does not stop
it (A1), `/processes` does not list it (B2), policy changes never reach it (B3),
`Status` reads its fields without a lock (B4), and its restart hash is a second
hand-maintained copy of the primary's (D1). Fixing them as one change is likely
cheaper than fixing them as five.

The **security pass found no unauthenticated route, no traversal, no workflow
injection and no OAuth redirect** — the categories that would have blocked the
release are clean, and several are defended better than typical.

The **test pass is the uncomfortable one.** Six guards written specifically to
protect this release's features cannot fail, and two of them fail in exactly the
way this repo has already documented twice: a `strings.Contains` substring that
survives the thing under test being switched off.

---

## A. Would lose or corrupt a broadcast

### A1. Shutdown and source deletion leak the backup FFmpeg process, forever

`internal/engine/engine.go` — `Stop` clears `e.dests` and then stops only
`d.proc`. `stopBackup` is reachable from exactly two places, `teardownDest` and
`reconcileBackup`, and neither is on the shutdown path.

The consequence is worse than a leak. `startBackup` builds the process with
`supervisor.Spec{AutoRestart: true}`, so the orphan does not exit — it
reconnects to the backup RTMP endpoint forever. Deleting a **source** takes the
same path (`Manager.Sync` does `delete(m.engines, id)` then `eng.Stop()`) while
the daemon keeps running, so an operator who deletes a source is still
publishing to Facebook from a process nothing can see or stop.

`d.backupPort` is never released, and `e.alloc` is the manager-wide allocator
shared across every engine — each deleted source with a backup permanently
burns one of 500 ports.

### A2. A standby SRT encoder and the primary evict each other

`internal/srtserver/srtserver.go`. `Target.Key()` correctly defines publisher
identity as `(SourceID, Backup)` — and **has no callers anywhere**. Every gate
uses the bare source id: `s.live[target.SourceID]` at the admission check, the
takeover check, the store, the delete, and `Publishing`.

The `.backup` target is reachable in production: `Manager.lookupToken` emits a
second `Target{Sink: bh, Backup: true}` addressed by `<token>.backup` whenever
`eng.BackupHub() != nil`. So with both encoders up:

- whichever connects first takes the single slot; the other is refused with
  `REJ_RESOURCE` and the message "source already publishing";
- if the primary goes quiet for `StaleAfter` (3 seconds), the standby *evicts*
  it and takes the slot — and the recovering primary is then the one refused.

Two targets with deliberately distinct sinks (`eng.Hub()` vs `eng.BackupHub()`)
that exist in order to run concurrently mutually exclude each other. This is the
failover feature not working in the one situation it was built for.
`Publishing(sourceID)` also cannot say which of the two it is reporting.

### A3. Two concurrent reconciles can silently starve a live destination

`Reconcile` takes `e.mu` only for the settings swap and releases it before
`reconcileOutputs`. There is no reconcile lock, no queue, no single owning
goroutine — and the callers are concurrent: five HTTP handlers, the scheduler's
actuator, and `observeLoop`.

`startDestinations` checks `e.dests[id] == nil`, unlocks, and only publishes
into `e.dests` at the end of `startDest` — after `Allocate()` and
`hub.Subscribe()`. Two reconciles can both see nil. `relay.Hub.SubscribeAddr` is
a bare map assignment on a deterministic name, so the second **replaces** the
first: the first FFmpeg keeps running against a port the hub no longer sends to,
and its `*destination` is overwritten in `e.dests`, so nothing will ever stop it
or release its port. Identical for `dest:<id>:backup`.

This is the map-assignment hazard the codebase already documents on
`destSubName`. The comment is right; the caller is not serialized.

### A4. An ordinary Save in the destination dialog reverts a pre-announced stream key

[`DestinationDialog.tsx:594`](../../ui/src/components/DestinationDialog.tsx)
loads `streamKey` when the dialog opens, and `:719` sends
`streamKey: streamKey.trim()` in **every** save payload, whether or not the
operator touched it.

[`preannounce.go:192`](../../internal/api/preannounce.go) writes a *new* primary
key whenever it creates a broadcast, and the file's own comment at `:189` calls
this THE INVARIANT: *"The key the pre-created broadcast returned has to be the
one the encoder publishes to, or the event page people were notified about stays
empty beside a live stream."*

Sequence: operator opens the dialog (holding K1) → the five-minute sweep writes
K2 → operator renames the destination and saves → the PUT carries K1 → the
Facebook event page advertises K2 and the encoder publishes to K1. Nothing warns.

### A5. Pre-announce mutates a restart-hash field and never reconciles

`preannounce.go` calls `saveAnnouncement` → `store.UpdateDestination` and never
calls `Reconcile`. Confirmed: 17 `Reconcile()` call sites in `internal/api`,
none in `preannounce.go` — while `handleUpdateDestination` right next door does
call it.

`StreamKey` feeds `Target()`, which is the **first element of `destSpec`**, the
restart hash. `preannounceOnce` has no `d.Enabled` check, so an already-live
destination named by a start schedule inside the seven-day horizon is in scope
(a weekly show whose destination was left enabled). The running FFmpeg keeps the
old key until some *unrelated* reconcile notices the spec changed — and then
cycles the live process at a moment the operator cannot connect to anything they
did.

### A6. The pre-announce sweep silently reverts operator edits

`dests` is read once at the top of the sweep, before any network I/O.
`announceOne` then makes the Graph call and writes that pre-call snapshot back
through `UpdateDestination`, which is an unconditional full-row
`UPDATE ... WHERE id=?` with no version or `updated_at` guard anywhere in the
repo.

Any operator action landing inside the window is silently reverted:
`SetDestinationEnabled`, `handleUpdateDestination`, `handleRefreshKey`. The
window is not one 30-second call — for destinations late in the sweep it is the
whole sweep.

The same function also creates the Facebook broadcast **before** persisting the
marker, and `saveAnnouncement` only logs on failure. A failed write leaves a
real public `live_video` with no local record, and the next sweep creates a
second one. The file's header claim — *"Every failure path returns WITHOUT
touching the destination"* — does not cover this path.

### A7. Two start schedules reschedule the same broadcast back and forth forever

`FacebookSettings` stores **one** `BroadcastID`/`ScheduledFor` pair per
destination, but the sweep iterates every enabled start schedule × every
destination it targets — and `scheduleTargets` returns `true` for any schedule
with an empty `DestinationIDs`, which the code itself documents as "the
commonest shape".

Two "start the show" schedules therefore both match every Facebook destination.
Within a single sweep: A creates the broadcast for t₁; B sees
`AnnouncedFor(t₂) == false` and `BroadcastID != ""`, so it *reschedules that
same broadcast* to t₂. Next sweep, A moves it back. One Graph write every five
minutes, forever; subscribers get a time-change notification each way; one of
the two shows has no event page at all.

No dedup, no per-occurrence keying, and no test covers two schedules on one
destination.

### A8. A reconcile racing shutdown creates an orphan

`startDest` publishes into `e.dests` and calls `proc.Start()` without checking
`e.stopped`; `startBackup` never reads `e.stopped` at all.

What makes this a defect rather than an oversight is that **it is the only start
path in the file that omits the guard**. `startRendition`, `startFeed`,
`reconcileBackupIngest`, `reconcilePlaylist`, `reconcileLoudness`,
`startLoudness`, `reconcileClips`, `reconcileCaptions`, `startPreviewLocked`,
`reconcileSelector` and `reconcileRecorder` all have it, and `startRendition`
carries a comment explaining exactly why.

### A9. File destinations on a rendition leak a subscription onto a reused port

`startDest` subscribes to the `hub` argument, but on a `ResolveForWrite` failure
it unsubscribes from `e.hub`. `upstreamHub` returns `e.hub` only for the
passthrough-off-source case; a rendition returns `r.hub` and a running selector
returns the selector hub.

So for a file destination on a rendition or selector, the subscriber stays in
that hub forever *while the port goes back to the allocator*. Worse than a leak:
the port is reissued, and the stale entry blasts transport-stream datagrams into
whatever now owns that socket.

---

## B. Would mislead an operator

### B1. Facebook backup ingest cannot be turned off

[`DestinationDialog.tsx:1324`](../../ui/src/components/DestinationDialog.tsx)
sets `backupIngest: e.target.checked || undefined`. Unchecking produces
`undefined`, which `JSON.stringify` **omits**, and
[`handleUpdateDestination`](../../internal/api/handlers.go) decodes over the
existing row — so the stored `true` survives. The dialog reports success; the
server keeps provisioning and publishing the backup feed, at double the upload
bandwidth the toggle's own help text warns about.

The sting: the drift guard written to protect this exact toggle asserts
`strings.Contains(src, "backupIngest: e.target.checked")` — which is still a
substring of the broken line. **The guard passes while the toggle is one-way.**

### B2. A backup process's logs and command line are unreachable

`Processes()` appends only `d.proc`. `d.backup` is never appended — and the
`e.backup.proc` that *is* appended is the source-side backup-ingest tier, a
different thing entirely.

Both API consumers go through `Processes()`, so
`GET /processes/dest:1:backup/logs` returns `404 no such process`. The card
shows the backup's state (`dest.backupProcess`), so an operator can see that
redundancy is broken and has no way to find out why — the one moment those logs
exist for.

The specific irony: `destArgs`' own justification for existing is that a drifted
backup argv *"would be invisible until somebody compared two argv strings on the
monitoring page."* The backup's argv is never on that page, so that comparison
cannot be performed.

### B3. Resilience changes never reach the backup

`applyDestPolicy` is primary-only — every reference is `d.proc`. And
`backupSpecOf` deliberately excludes `Resilience`, so `reconcileBackup`
short-circuits on an unchanged spec.

An operator raising `giveUpAfter` sees `noteReload(..., reloadLive, "reconnect
policy retuned...")` report it as applied. The primary is retuned and revived;
the backup keeps the `MaxRestarts` baked in at `supervisor.New` time and, **if
it is already in `StateFailed`, stays failed permanently** — no path revives it.

The fix is *not* to add `Resilience` to `backupSpecOf`: that would restart a
live redundant feed to deliver a value that can be set on a running process,
which is the mistake `destSpec`'s own comment records having already made once.

### B4. `Status` races the backup fields

`startDestinations` publishes `&next` into `e.dests`, drops the lock, and *then*
calls `reconcileBackup`, which writes `d.backup`, `d.backupPort`, `d.backupSub`,
`d.backupSpec` and `d.backupErr`. `Status` copies destination pointers under
`e.mu` and reads those fields after unlocking.

The code states the violated invariant itself, in a comment on the copy-on-
publish idiom directly above: *"Replaced wholesale rather than mutated in place:
Status hands out these pointers and then reads their fields after dropping the
lock, which is only safe while a published destination never changes again."*

**`go test -race` will not catch this today** — no test drives `Status` and
`Reconcile` concurrently. A test that loops `Status()` against a `Reconcile()`
on a destination with `BackupIngest` set would fire it immediately.

### B5. Reformatting an expert argument drops a live connection

`destSpec` hashes `ExtraInputArgs`/`ExtraOutputArgs` as the **raw
operator-typed strings**, while `destArgs` passes them through `expertArgv` →
`ffmpeg.SplitArgs`. Changing whitespace — or re-saving a value that
`expertArgv` discards as unparseable anyway — changes the hash and restarts the
destination for a byte-identical command line.

Hash the parsed argv, not the text.

### B6. FFmpeg argv and raw stderr are returned unredacted, and now carry the backup key

`handleListProcesses` returns `p.CommandString()` and `handleProcessLogs`
returns `p.Logs()` verbatim; `supervisor` shell-quotes but does not redact. With
backup ingest on, the argv contains `rtmps://<host>/rtmp/<BackupStreamKey>`, and
FFmpeg's stderr prints the full publish URL on a connect error.

**No privilege boundary is crossed** — both routes are behind
`requireAuth`+`requireCSRF`, and the same caller can already read `streamKey`
and `backupStreamKey` in cleartext from `GET /destinations`. It is listed here
because the project redacts this exact byte stream *everywhere else on egress*:
`hooks.payload` scrubs `e.Error` with the comment "Error in particular is FFmpeg
stderr, which prints the full publish URL", and `alerts.Redact`,
`RedactWebhookURL` and `mqtt.redactURL` all do the same. This is the one egress
path the policy is not applied to, so the key reaches devtools, screenshots and
support-ticket pastes that the policy exists to keep it out of.
`alerts.RedactURL` would mask it correctly as-is.

### B7. An ineligible Facebook account logs a warning every five minutes, forever

`preannounce.go:184`. The comment at `:180` acknowledges it — *"it will repeat
every sweep"* — and says suppressing it *"needs somewhere to remember it, which
is not built"*. But `s.rescheduleFails` **is** that somewhere, already keyed by
destination ID, twenty lines further down. 288 identical warnings per
destination per day for a condition that cannot resolve without operator action.

---

## C. Guards and gates that cannot fail

This section is the one that should sting. Every item is a guard written *for
this release's features* that would stay green through the failure it was
written to catch.

### C1. The stream-key redaction guard redacts its own fixture

[`internal/hooks/payload_test.go:39`](../../internal/hooks/payload_test.go)
builds its envelope with `Reason: ev.redacted().Reason, Error: ev.redacted().Error`
— it calls the redactor itself, then asserts the redactor redacted.

**Mutation it misses:** in `internal/hooks/dispatch.go`, change
`d.intake <- ev.redacted()` to `d.intake <- ev`. Every real webhook payload then
carries FFmpeg stderr and stream keys to the receiver, and this test stays green
because it never goes through `dispatch`.

This guards the single most sensitive egress path in the product.

### C2. The deliveries-route guard passes with the route deleted

[`internal/api/hooks_test.go:150`](../../internal/api/hooks_test.go) asserts only
`http.StatusOK`. `api.go:649` registers `r.NotFound(h.ServeHTTP)` — the embedded
SPA — on the root mux, so an unknown `/api/v1/...` path returns the SPA bundle
with 200.

**Verified by running the mutation** against a committed tree: commenting out
`r.Get("/hooks/{id}/deliveries", s.handleHookDeliveries)` at
[`api.go:505`](../../internal/api/api.go) left `TestDeliveriesRouteExists`
passing. `ok github.com/rainmanjam/polyemesis/internal/api 0.567s`.

This generalizes: **any route test in this codebase that asserts only a status
code is testing the SPA fallback.** Worth a sweep beyond this release.

### C3. Four UI drift guards survive their block being switched off

`facebook_ui_drift_test.go` and `compliance_drift_test.go` match substrings that
remain present when the surrounding conditional is disabled:

- wrapping the Facebook settings block in `{false && platform === "facebook" && (`
  leaves the crosspost label, the donate input id, the backup toggle's
  `e.target.checked` and the payload field all in the source;
- `&& false` on the audience block leaves every `<SelectItem>` and the
  `!facebookTargetsAPage` substring in place;
- `for (const w of warnings) false && toast.warning(...)` leaves the matched text;
- `{dest.facebookBroadcastId && false && (` leaves the `href` substring.

The mutations are artificial as written — nobody types `&& false`. The class is
not: any refactor that moves a control behind a new condition, or that deletes
the surrounding block while leaving a reference elsewhere in the file, produces
the same green.

`TestTheCardShowsTheBackupFeedsState` already learned this lesson and fixed it
by matching the **whole condition** `{dest.backupProcess && (` — with a comment
recording the measurement. That technique was not carried across to its
neighbours. Doing so is a small, mechanical change.

### C4. The concurrent compliance-push test never creates overlap

[`internal/api/metadata_test.go:717`](../../internal/api/metadata_test.go) closes
its `start` channel before the push begins, so both stubs run immediately and
sequential workers pass. Removing `go` from the fan-out in `metadata.go` leaves
it green.

The test's own comment is honest that it *"closes a coverage gap rather than a
defect"* — its purpose is to give `-race` two `PushCompliance` calls in flight.
It does not achieve that, so the coverage it claims does not exist.

### C5. The flake-rate workflow passes after measuring nothing

[`.github/workflows/flake-rate.yml:115`](../../.github/workflows/flake-rate.yml)
validates the dispatch input with `case "$RUNS" in ''|*[!0-9]*)` — which accepts
`"0"`. `seq 1 0` emits nothing, the loop body never executes, and the job
publishes *"0 failed / 0 runs"* and exits green.

This is the instrument currently being used to settle issue #94. A zero-run
report reads as a clean suite.

### C6. The credential re-check guard passes with the stored client ID ignored

[`internal/api/credcheck_test.go:128`](../../internal/api/credcheck_test.go).
Replacing `creds.ClientID` with a hard-coded valid-format YouTube id in
`oauth_handlers.go` still produces the expected `unverified/format` verdict,
because YouTube's check is format-only and the seeded secret is never consulted.

### C7. Two smaller ones

- **The SRT dual-family probe is opt-in.** `wildcard_probe_test.go` skips unless
  `POLYEMESIS_SRT_PROBE=1`, so the real-socket proof of the #28 fix does not run
  in CI. The unit-level `TestAWildcardBindsBothFamilies` does run.
- **A 100 ms negative wait.** `hooks/dispatch_test.go:212` proves a
  non-subscribed event was *not* delivered by sleeping. A delayed delivery after
  the sleep leaves it green.

### C8. `PreannounceLoop` never runs a sweep at start

Nothing calls `preannounceOnce` before the first five-minute tick, so a restart
two minutes before a scheduled show misses the window. Low harm on its own — the
horizon is seven days — but it means the loop's post-restart behaviour differs
from its steady-state behaviour and is untested.

---

## D. Would cost the maintainer later

### D1. Two hand-maintained restart hashes over one shared argv builder

`destArgs` was extracted with an explicit rationale — *"Shared by the primary and
the backup so the two cannot drift"* — but the **decision of when to respawn**
was not. `destSpec` and `backupSpecOf` are two independent hand-written lists
over the same `ffmpeg.DestSpec` inputs. `backupSpecOf`'s comment says
"deliberately not derived from it", which is right about the *verdict* and wrong
about the *inputs*.

Adding one field to `ffmpeg.DestSpec` now needs edits in three places, and
missing the third is silent and asymmetric: the primary picks up the setting, the
backup keeps the old command line, and the two feeds Facebook receives differ
with nothing reporting it.

**The drift has already started.** `compiled.OutLabel` is a `destArgs` input and
appears in *neither* hash. It currently always co-varies with `FilterComplex`,
so it is latent rather than live — which is exactly how this class stays hidden.

Derive both hashes from the built `ffmpeg.DestSpec`, substituting
`Target`/`RelayURL` per role. Keep the two verdicts separate; unify the inputs.

### D2. `destSpec`'s membership is otherwise correct — audited

All 16 inputs were checked against `ffmpeg.DestSpec`. Every one maps to a field
that changes the argv. The `Resilience` removal is correct. `Facebook.BackupIngest`
being absent is correct, and is what `reconcileBackup` exists to compensate for.
**No field is wrongly in, and no command-line field is missing** — apart from the
raw-vs-parsed expert args in [B5](#b5-reformatting-an-expert-argument-drops-a-live-connection).
Recording this because it is the highest-risk structure in the engine and it
holds up.

### D3. `engine.go` is 6,759 lines and the seams are already contiguous

Up from 5,052 at v0.1.0. 189 functions: 41 exported `*Engine` methods, 85
unexported, 63 free. It is 85% of the package's non-test lines.

This is not cohesive-but-large — the ordering already sorts into near-contiguous
blocks: destinations + backup `1679–2355`, renditions `2356–2807`, selector /
failover / feed `3027–4460`, playlist `4462–4964`, loudness `5370–5563`, clips
`5565–5818`, captions `5820–6082`.

The concrete cost is [D1](#d1-two-hand-maintained-restart-hashes-over-one-shared-argv-builder):
`destSpec` and `backupSpecOf` sit 264 lines apart in a 6,759-line file, which is
why nobody noticed they are one concept. Split the destination seam first — it is
the block being actively edited and where the drift risk lives.

### D4. Facebook bypasses the platform abstraction the repo documents

`oauth.TargetedProvider`'s comment is explicit: *"never type-assert Provider at a
call site."* `api.go:247` does `fb, ok := p.(*oauth.Facebook)` — a **concrete
type** assertion — to reach `RescheduleBroadcast`, which is on no interface.

Leakage into non-test files: `preannounce.go` (20 Facebook references — a file
that is Facebook-only by construction), `oauth_handlers.go` (12),
`chat_wiring.go` (13), `automation.go` (6), `handlers.go` (6), `metadata.go` (5),
and `engine.go`'s `wantsBackup` reading `row.Facebook.BackupIngest`.

That last one contradicts its own justification. `BackupURL`/`BackupStreamKey`
were placed on `Destination` rather than `FacebookSettings` "because the ENGINE
consumes them, and **the engine should not have to know which platform a
destination is**" — and then the engine's gate on those fields reads a
Facebook-namespaced struct.

A second platform reuses none of it, even though the sweep, the
reschedule-failure counter and `AnnouncedFor(occurrence)` are all genuinely
platform-neutral. Two small changes fix it: a `ScheduledBroadcaster` interface,
and promoting a `BackupIngestWanted` bool onto `Destination` beside the URL
fields it gates.

### D5. Five function-pointer seams on `Server`, up from zero, with one root cause

`git grep 'Fn func' v0.1.0 -- internal/api/*.go` returns nothing. There are now
five, and the field comments narrate the trajectory themselves: *"the third of
these"*, *"the last of them"*, *"the fifth of these"*.

Four of the five take the pusher as a parameter, so the test replaces a closure
that receives an interface value it could have stubbed directly. **Only the
unexported `graphBase` forces any of them to exist** — and `internal/oauth`'s own
tests already do `&Facebook{graphBase: srv.URL}` because they live in the
package. One injection point in `oauth` deletes all five fields. Left alone, the
next platform call needing a test adds a sixth.

### D6. A stale worktree is a second copy of the tree

`.claude/worktrees/oauth-self-registration/` contains a full copy that every
`find` and `grep` hits — `internal/engine/engine.go` exists twice, 6,759 lines
and 5,052 lines. Two reviewers in this audit had to exclude it by hand.

### D7. `metadata_test.go` has become the grab bag

1,566 lines, 37 tests, at least five distinct subjects — route/session guards,
metadata push, compliance conflict resolution against pure functions with no
HTTP at all, broadcast-settings failure reporting, and precheck/merge. Meanwhile
`internal/api` has single-guard files of 28, 47, 54 and 75 lines.

A maintainer adding a compliance-conflict test has three plausible homes and no
rule to choose between them. Split the ~400 lines of pure `complianceBy*` and
conflict-naming tests into `internal/api/compliance_test.go`; they never touch a
request.

### D8. Comment drift — clean inside the diff, two dangling citations outside it

All 171 `Test<Name>` citations in comments were checked against the 2,232
declared test functions. **Every apparent miss inside the 0.2.0 diff is a
line-wrap and resolves.** The `selector_playlist_test.go` citations of deleted
tests are deliberate tombstones.

Two genuine dangling citations exist in `internal/mqtt`, which has no commits in
this range: `client.go:155` cites `TestClientNeverUsesTheQueuePath` and
`state.go:15` cites `TestPayloadsCarryNoSecrets`. Neither exists, and nothing in
`internal/mqtt/*_test.go` covers the `PublishViaQueue` claim at all. Pre-existing
debt — recorded because this repo has been bitten by a comment citing a
nonexistent guard before.

One real drift inside the diff: `engine.go:2082–2085`, the "Muxer and socket
tuning" comment sits above the `Audio:` field, orphaned from the `Transport:`
literal it describes.

### D9. Smaller items

- **`rescheduleFails` is never cleaned on destination delete.** Entries are added
  on failure and removed on success or on giving up; nothing removes one when the
  destination goes away. Trivial in size, recorded as a symptom of the map having
  no owner.
- **No backup endpoint for non-Facebook destinations.** The columns and the
  engine machinery are platform-agnostic; the only writer is Facebook
  provisioning. Most CDNs publish a backup endpoint and an operator cannot enter
  one. A gap, not a defect.
- **Creating a webhook has no in-flight guard.** `HooksCard.save` has no busy
  state; two fast clicks create two hooks and the UI keeps only the second
  signing secret, so the first receiver can never verify signatures. Every other
  mutating dialog — including `DestinationDialog.save` — has a `busy` flag, so
  this is a local omission rather than a pattern.
- **New operator-facing strings are untranslated.** The hooks card and the new
  Facebook and playlist copy are raw English while the switcher offers twelve
  languages.
- **The playlist editor forbids `intro → segment → intro`.** `PlaylistEditor`
  removes an upload from the picker once queued. The code says this is deliberate
  (*"the same file twice is a mistake this editor can prevent"*); the reviewer
  disagreed on the grounds that the backend normalizes repeats and plays each
  occurrence. **A design disagreement, not a bug** — recorded so the decision is
  made on purpose rather than inherited.
- **API tokens are unscoped.** `db.APIToken` has no scope field and every token is
  full admin. Combined with expert args reaching argv, a token is effectively
  host-level read via FFmpeg protocols. Deliberate and admin-only, but worth
  stating plainly somewhere.
- **A partial SRT bind is a warning, not an error.** `srtserver.go` continues when
  one address family fails, so an install can silently end up single-stack.

---

## Refuted

**"The pre-announce token refresh can block indefinitely."** The ordering half is
correct — `s.tokenFor(ctx, ...)` runs on the server-lifetime context six lines
before `cctx` is created. But it cannot hang: `tokenFor` short-circuits unless
the account is expired, and the refresh goes through the package-shared
`&http.Client{Timeout: 20 * time.Second}`. Ceiling 20 seconds, not unbounded.

The serial-cost half of the same finding stands, and is recorded: each
`announceOne` creates its own 30-second timeout inside two nested loops, so the
worst case is 30s per (schedule × destination) pair. Bounded in practice — it is
a dedicated goroutine and `time.Ticker` drops ticks rather than stacking sweeps.

## Verified clean

Each checked against the real route table and files rather than the diff:

- **Authorization on every new route.** Hooks, schedules, jobs, playlist,
  clipper, media, metadata/compliance and `platforms/*` are all inside the
  `requireAuth`+`requireCSRF` group. The seven public routes each have a defence
  — `POST /setup` relies on `CreateUser` refusing twice, and
  `/chat/kick/{secret}` uses `subtle.ConstantTimeCompare` before anything else.
- **Path traversal and shell injection.** No `sh -c` anywhere; all execution is
  `exec.Command(bin, argv...)`, so metacharacters are inert. File destinations
  and uploads both reject `/` *and* `\` on every platform and then prefix-check
  against an absolute base. The diff range also *contains* the fix for a real
  prior wildcard-deletion bug, and the equality-match replacement is correct.
- **The SRT wildcard bind change.** `bindAddrs()` splits only when the host part
  is empty, which already meant all interfaces. `127.0.0.1:9000` and
  `0.0.0.0:9000` bind exactly as before.
- **GitHub Actions.** No `pull_request_target`. Every `github.event.*` and
  `inputs.*` is passed through `env:` and dereferenced as a quoted shell
  variable. Permissions are per-job and least-privilege. Actions are SHA-pinned;
  syft is version-pinned *and* SHA-256 verified.
- **OAuth.** Redirect target is a fixed relative path with the message escaped.
  State is single-use and validated server-side before `code` is touched, plus
  PKCE. `Facebook.graphBase` is unexported with no config path to set it.
- **Metrics labels and engine logging.** No URL, no key; the new backup paths log
  deliberate placeholders (`"<token>.backup"`, `PublicIngestURL("<server>")`).
- **The SQLite migrations.** Additive `ADD COLUMN ... NOT NULL DEFAULT`, keyed by
  column name; scan, insert and update agree on the new columns.
- **Test-suite ratio.** 78k test lines against 84.5k non-test is 0.92:1 — healthy
  in aggregate. The problem is placement ([D7](#d7-metadata_testgo-has-become-the-grab-bag)),
  not volume.

## See also

- [THEME-AND-UI](THEME-AND-UI.md) — the 0.3.0 UI work, which touches
  `DestinationCard` and `DestinationDialog` and should absorb
  [B1](#b1-facebook-backup-ingest-cannot-be-turned-off) and
  [C3](#c3-four-ui-drift-guards-survive-their-block-being-switched-off).
- [UNREACHABLE-KNOBS](UNREACHABLE-KNOBS.md) — the survey whose method
  [C3](#c3-four-ui-drift-guards-survive-their-block-being-switched-off) shows the
  limits of: a guard that a knob is *named* is not a guard that it is *reachable*.
