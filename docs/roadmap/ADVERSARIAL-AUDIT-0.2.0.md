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

## Found during remediation

Two defects that the audit missed and that fixing it turned up. Recorded here
because "what the audit did not see" is the part of an audit worth keeping.

### E1. Facebook's per-instance Graph base redirects 1 call in ~40

`internal/oauth/facebook.go` carries two base-URL mechanisms covering different
surfaces. `fbGraphBase` (package var) is what `fbGet` and `fbPost` use, and
those two carry all 12 Graph call sites. `f.graphBase` (unexported field) feeds
`f.graphEndpoint()`, which has **exactly one caller** — the credential-check
token endpoint.

So `&Facebook{graphBase: srv.URL}` — the pattern `credcheck_providers_test.go`
uses, and the one that *looks* like the provider's test seam — leaves
`IngestFor`, `RescheduleBroadcast` and `PushMetadata` pointed at the real
graph.facebook.com. Meanwhile `facebook_test.go` redirects by mutating the
package var globally under `t.Cleanup`, which makes those tests order-dependent
and non-parallelizable.

This is the actual root of [D5](#d5-five-function-pointer-seams-on-server-up-from-zero-with-one-root-cause):
the seam that exists is not merely unexported, it is *misleading*, and the five
closures in `internal/api` were added around a mechanism that would not have
worked anyway.

### E2. A source card picks whichever uplink it finds first

Fixing [A2](#a2-a-standby-srt-encoder-and-the-primary-evict-each-other) made two
live links per source reachable, and exposed that `internal/api/sources.go` took
the first `SRTLinks()` entry matching the source id. `SRTLinks` is built by
ranging a map, so a source with a primary and a standby could report the
primary's bitrate on one refresh and the standby's on the next with nothing
having changed.

Fixed by naming the decision: `linkForCard` prefers the primary and falls back
to the standby, because an operator whose primary has dropped is reading that
card precisely because the standby is carrying the show.

### E3. A COPPA declaration could be made but never taken back

Auditing the rest of the destination dialog's payload for
[B1](#b1-facebook-backup-ingest-cannot-be-turned-off)'s shape found one more.
`compliance.madeForKids` sent `undefined` for "Leave as it is on YouTube", which
`JSON.stringify` omits, which the decode-over-existing PUT preserves. So an
operator could declare a destination made-for-kids and had no way to withdraw
it.

`db.Compliance.MadeForKids` is a `*bool` for exactly this reason — three states,
not two. The UI was collapsing them to two. Fixed by sending explicit `null`.

Everything else in that payload was checked and travels correctly: `privacy` and
`facebookPrivacy` send `""`, `crosspost` sends `[]`, `accountId` and
`renditionId` send `null`, and the transport, resilience and audio numbers send
`0`.

### E4. The Facebook drift guards make their own copy untranslatable

`internal/db/facebook_ui_drift_test.go` asserts the English substrings
`"Crosspost to Pages"`, `"upload bandwidth"` and `"reconnects the stream"`
against `DestinationDialog.tsx`'s **source text**. Each appears exactly once, in
the rendered JSX.

So moving that copy into `en.json` — which is what fixing the untranslated-strings
item requires — turns both guards red, and the only way to keep them green would
be to leave the phrases behind in a comment. That is gaming the guard rather than
satisfying it, so the Facebook block stayed English while the rest of the new UI
copy was translated into all fifteen locales.

The guards and the change want the same thing. The guards need to assert against
`en.json` instead of against the component, which is the same fix
[C3](#c3-four-ui-drift-guards-survive-their-block-being-switched-off) needs for a
different reason — a guard that reads rendered source text is measuring the wrong
artifact twice over.

### E5. YouTube and Twitch bypassed their own base variables entirely

Grounding [E1](#e1-facebooks-per-instance-graph-base-redirects-1-call-in-40)
turned up the same defect in two more providers, and in a worse form than
Facebook's. `ytAPIBase` and `twitchHelixBase` both carry the comment *"a var so
tests can point the whole provider at a stub"* — and `YouTube.Account`,
`YouTube.Ingest`, `Twitch.Account` and `Twitch.Ingest` hard-code the full
`https://www.googleapis.com/youtube/v3/...` and `https://api.twitch.tv/helix/...`
URLs inline, bypassing the var. `Twitch.AuthURL` hard-codes `id.twitch.tv`, and
Twitch's `tokenURL` field redirected the token endpoint only.

So the mechanism documented as the way to stub these providers did not stub the
account and ingest paths — the two that matter most for going live.

Fixed together with E1 rather than separately: leaving these in place would have
made the new `WithBaseURL` a fresh trap of exactly the kind being removed.

The guard is `TestAStubbedProviderReachesNoRealHost`, which enumerates **38
entry points across four providers** and installs a transport that *refuses*
non-stub hosts — so an escape is a named test failure rather than a silent real
request to the platform. It enumerates rather than samples precisely because
sampling is how E1 survived. A second guard reads the source and self-fails if
it ever matches zero lines.

### E6. A stream key reached the MQTT broker as a RETAINED message

This one corrects the audit's own security verdict, and it is the most serious
thing the exercise found.

Fixing [B6](#b6-ffmpeg-argv-and-raw-stderr-are-returned-unredacted-and-now-carry-the-backup-key)
turned up a third instance of the same bytes. `supervisor.runOnce` joins the last
three stderr lines classified `error`/`fatal` into the process's exit error,
which becomes `Status.LastError`. `Error opening output rtmps://host/app/<key>:
Connection refused` is exactly that shape.

That field is serialised by `GET /processes`, copied onto the dashboard snapshot,
**and copied into `SourceState.IngestError`, which is published to MQTT
retained.**

A retained topic outlives the process that produced it and is readable by every
subscriber on the broker **behind no session at all**. So unlike B6 — which the
audit correctly graded as crossing no privilege boundary — this one does. The
"no unauthenticated exposure" line in [Verified clean](#verified-clean) was true
of the route table and false of the product.

Worse, `internal/mqtt/state_test.go` had explicitly *exempted* `ingestError` from
its credential-name ban, on the reasoning that *"an error is FFmpeg's, and
neither is a URL"*. That reasoning is false, and the exemption is what let the
field through the guard that existed to catch exactly this.

Fixed by masking in `Status()`, which closes all three consumers at once, and by
correcting the test comment to say the field is safe *because the value is
masked at source* rather than because it cannot carry a URL.

**And that fix was itself incomplete**, which an adversarial review of the
remediation caught. Masking in `Logs()` covered the reader that prompted it and
missed the other two copies `appendLog` fans out: `LogSink` is a `FileSink` in
production, so an unmasked line became a **permanent** one in `process.log` —
the artifact people attach to bug reports, which the database beside it never
is — and `OnLog` feeds the console's live log panel over the WebSocket. Now
masked at construction, the single point every copy is made from. Guards were
added for the sink and the callback specifically, because the original guard
asserted on `Logs()` and could not see either.

The lesson is the audit's own, one level up: **the observable a guard watches
decides whether it can fail at all** — and a guard on the reader cannot see the
writers.

### E7. Two smaller items from the same thread

**`internal/api/expert.go` is a fourth egress of the same bytes** — it returns
the raw argv and its own locally-quoted command string to the client without
redaction. Unlike the other three this is arguably the point of the endpoint:
expert mode exists so an operator can see and edit the real command. It needs a
deliberate decision recorded rather than being left to look like an oversight.

**`.dockerignore` has no `.claude` rule.** While the stale worktree existed,
278 MB of duplicate checkout was being sent into every Docker build context.
Moot once the worktree went, and it would happen again with the next one.

### E8. The SPA fallback defeats status-only route tests BOTH ways

[C2](#c2-the-deliveries-route-guard-passes-with-the-route-deleted) said a route
test asserting only `200` is testing the SPA fallback. That was half of it.

`web.Handler` answers an unrouted `/api/v1/...` path two different ways:

- with `internal/web/dist/index.html` present (after `make ui`) — **200 and the
  SPA bundle**, which is what the audit measured;
- **without it, which is how CI runs `go test`** — `http.Error(…, 404)`.

So a test asserting only `404` *also* passes with its route deleted, and that is
the configuration the test suite actually runs in. Measured: with all three
`/schedules/{id}` registrations commented out and `index.html` moved aside, the
old 404 assertion stayed green.

Fixed with a `mustJSONError` helper that additionally requires a JSON body with
an `error` field — neither fallback can produce one — applied across schedules,
alert rules, renditions, destinations, expert overrides, the stem download and
the media delete, each mutation observed to fail.

One nuance worth keeping: removing a single **method** from a path that keeps
its others yields chi's 405, which a status assertion *does* catch. The hole
opens only when a whole pattern goes.

### E9. Confinement tests that pass by refusing everything

Same sweep, different shape. `TestClipDownloadRefusesAnythingOutsideTheClipDirectory`
and its delete twin assert only that a canary did **not** come back — which is
also true when the route does not exist. Measured: with
`r.Get("/clips/{name}/download")` commented out, both stayed green.

The stem half of the same file already had a positive counterpart, with a
comment explaining why it was needed. The clip half never got one. A refusal
test needs a partner proving the thing it refuses is otherwise reachable, or it
is indistinguishable from a missing feature.

## F. Found by reviewing the remediation

The fixes were themselves reviewed adversarially, by a reader told to attack the
claims rather than re-find the bugs. It found four defects in our own work. They
are recorded because "the fix was wrong" is the failure mode this whole exercise
is about, and three of the four are the *same shape* as the findings they fixed.

### F1. The A8 fix left the publication-to-`Start` window open

[A8](#a8-a-reconcile-racing-shutdown-creates-an-orphan) was closed by checking
`e.stopped` under `e.mu` before publishing. That is not enough, because `Start()`
happens *after* publication and after the unlock:

1. a reconcile publishes into `e.dests` — `stopped` is false, the guard passes —
   and unlocks;
2. `Stop` sets `stopped`, copies the map and tears the destination down. **A
   `Process.Stop` on a process that was never started is a silent no-op**, so
   nothing stops — but the hub subscription is removed and the relay port goes
   back to the allocator;
3. the reconcile resumes and calls `Start()`.

The child now runs untracked after shutdown, on a port that will be reissued
underneath it. Reachable through `Manager.Sync` removing a source while another
`Manager.Reconcile` still holds that engine pointer.

**Why the new tests missed it:** every one of them starts from an
already-stopped engine. They exercise the guard and never the window — the same
lesson as [E6](#e6-a-stream-key-reached-the-mqtt-broker-as-a-retained-message),
which is that a guard sees only the observable it watches.

Fixed at the class rather than the instance: a terminal "retired" latch on
`supervisor.Process`, so a `Stop` arriving before `Start` makes every later
`Start` a no-op for every caller, not just this one.

### F2. The marker ceiling evicted the show nearest to going out

`Announce` sorted markers by occurrence and kept the tail, so the entry the
ceiling dropped was the **soonest**. Dropping a marker whose `live_video` still
exists is not lost bookkeeping: the next sweep finds nothing for that schedule,
creates a second event page, and evicts another marker to fit it. People are
already subscribed to the first. It thrashes — one orphaned event page per sweep
— and it starts with the broadcast about to go out.

Choosing the furthest-out victim instead would only pick a quieter one; any
eviction of a live marker has that shape. The ceiling now applies to **intents**
only — markers with no broadcast id, where losing one costs a retry, which is
what happens without a marker anyway.

Note the original code's comment *admitted* this cost ("costs a duplicate event
page for the show nearest in time — worth stating rather than pretending the
ceiling is free"). Stating a cost honestly is not the same as it being
acceptable, and a comment is not a justification.

### F3. A create racing go-live strands an intent forever

The sweep records an empty intent, the destination is enabled during the Graph
call, the create succeeds, and the completion correctly refuses to change the
live key — but leaves the intent behind. Every later sweep then finds
`held && BroadcastID == ""` and returns without retrying, and the marker is never
aged out because ageing happens only in `Announce`, which that branch never
reaches.

**Resolved as two cases, because they are not the same problem.**

*Race lost, id in hand.* When the completion write is declined, the broadcast id
is known for the only moment it ever will be. The intent is unwound and the id
goes into the warning so an operator can remove the orphan — this codebase has no
delete for a `live_video`. It is deliberately **not** written into the marker: a
marker naming that broadcast claims a show whose key the encoder does not publish
to — an empty event page beside a live stream, recorded as success — and the next
occurrence would *move* that broadcast rather than create a usable one, so the
wrong key would follow the show for ever.

*Process died between the two writes, no id.* Nothing can recover one. Finding it
would mean asking Graph which scheduled broadcasts the target holds and matching
on start time, which adopts a stranger's broadcast as readily as ours. So the
intent ages out after `staleBroadcastAfter` consecutive sweeps. **Harm accepted:
if the lost create did reach Facebook, the retry makes a second event page.** The
alternative is the show having none, which is certain rather than possible — and
a duplicate is visible on the operator's own Page, while a stranded marker is
visible nowhere. [F2](#f2-the-marker-ceiling-evicted-the-show-nearest-to-going-out)
makes the same trade in the same words.

Ageing is **counted, not wall-clocked**, and the reason generalises past this
feature: a timestamp would have to live in the marker, and a new field is absent
on every row an upgrade finds — which reads as infinitely old and would age every
existing intent at once, on the first sweep after the upgrade.

### F4. Overlapping sweeps can both create the same broadcast

`preannounceOnce` has no mutex and no conditional claim. Production starts one
loop, so this is latent rather than live — but `PreannounceLoop` is exported and
the state machine is not safe against overlap on its own.

**Resolved as a conditional claim rather than a mutex.** `UpdateAnnouncement`
already re-reads the row inside its own transaction, so declining a row that
already holds a marker for this schedule makes the intent write a compare-and-set.
The loser creates nothing and is not counted as a failure.

A mutex was the obvious answer and the wrong one: it guards one process where a
daemon can be started twice, and it would sit *outside* the transaction it is
supposed to be protecting.

Worth recording from the guard, because it is the same shape as everything else
here: the first version gated the blocking create with `sync.Once`, and `Do` makes
every later caller **wait** for the first call to return — so both creates stalled
until the HTTP client's timeout, both failed, and the test went red for a reason
that had nothing to do with the defect. A red run proves no more than a green one
if it is red for the wrong reason. A plain flag under a mutex lets the second
caller through, which is what turns a hang into a real failure.

### F5. The "every spec field reaches argv" guard proved the opposite

`dest_spec_derived_test.go` mutates `ffmpeg.DestSpec` fields and checks only
`destArgvSig` — it never checks the field reaches `DestinationArgs`. And
`DestSpec.CopyVideo` is *already* ignored by `DestinationArgs` while still moving
the signature, so toggling it restarts a live destination for a change that does
not alter the command line.

That is [B5](#b5-reformatting-an-expert-argument-drops-a-live-connection)'s exact
defect, reintroduced through the hash that unified
[D1](#d1-two-hand-maintained-restart-hashes-over-one-shared-argv-builder). The
guard written to protect the unification is the one that hid it.

**Asserting the rule instead of the premise found four instances, not one** —
ten field/kind pairs. Besides `CopyVideo`: Opus on RTMP, `Transport.NoDurationFilesize`
on SRT, file and audio destinations, and `VideoDelayMS` on audio. The last three
are reachable by an operator today, and each one drops a live connection to
deliver a change that alters no command line.

The fix is structural rather than a list of exceptions: `destArgvSig` is now
`hashStrings(ffmpeg.DestinationArgs(s))` with `RelayURL` cleared, so **the
signature IS the command line** and all four fall out at once. A future field
that does not reach the argv cannot move it.

One more can't-fail guard surfaced inside the fix: the reflective field-bumper
set every field to a generic `"-changed"`, which for the two enumeration fields
— `Audio.Codec` and `Kind` — the argv builder did not recognise, so those pairs
compared a spec against itself and passed vacuously. The bumper now gives them
values the builder knows, and the test counts how many pairs moved the argv at
all, so a silent return to zero fails.

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
