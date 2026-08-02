# A playlist plays all of its items, in order

**What:** the playlist tier feeds FFmpeg a concat list of normalised
derivatives instead of one original file, so every item plays, in order, on one
continuous timeline — and the operator gets a control to build that list.

**Why:** B1 made every item match by construction. Nothing reads those
derivatives yet. `internal/engine/engine.go` carries a comment headed `BUG.`
saying so: the readiness gate insists a normalised copy exists and the next line
hands FFmpeg the operator's original. B2 is where that inverts and the
sub-project pays off.

This is sub-project **B2** of roadmap item 5. **C** (source-scoped scheduling)
builds on it.

## What already exists

**Sub-project A (#64)** gave the playlist its own relay hub, so a file on air no
longer makes the primary ingest read live and silently disable failover. It
established the invariant this design is bound by:

> Availability means BYTES ON THE HUB. The selector offers a candidate only when
> it is actually delivering.

**Sub-project B1 (#68)** replaced `FilePath` with an ordered `Items []PlaylistItem`,
added the `playlistmedia.KindNormalise` job that produces one derivative per
upload to a fixed profile, wired that job end to end, and gated the tier on
every item having a derivative. It deliberately still plays the first item only,
and deliberately still feeds the ORIGINAL.

## Scope

**In:**

- concat sequencing across all items, replacing `-stream_loop -1` on one file
- the derivative becoming the input
- loop semantics across the whole list
- the operator UI, and the per-item readiness the UI needs
- a real test of the `-safe 0` boundary, which until now had nothing to guard

**Out, deliberately:**

- **Item-boundary resume.** Considered at length and rejected — see below.
- **Scheduling.** Sub-project C.
- **Several named playlists.** Still one playlist on the settings block. A
  playlists table needs a reason beyond "we might".
- **Per-item trim points, transitions, shuffle.** vMix has all three. None has
  been asked for, and each is a settings key with a UI and a failure mode.

## The return decision, and why it is not what the roadmap said

`docs/roadmap/PLAYLIST-AND-COMPOSITING.md` says:

> Returning to the playlist after the encoder drops resumes at an **item
> boundary**, not mid-item, or the seam is a visible jump.

**This design rejects that.** The playlist tier runs continuously — it is a
`-c copy` remux of an already-normalised file at `-re`, which costs almost
nothing. When the encoder drops, the selector cuts to a playlist that is already
on the air's doorstep: one cut, no gap, joining whatever item is mid-play.

**A does not force this, and an earlier draft of this spec wrongly said it did.**
A's invariant is that a candidate must be DELIVERING to be SELECTED — not that
it must exist perpetually. Starting the tier on demand and holding the slate
until its hub carries bytes would satisfy A perfectly well. The continuous tier
is a preference, argued below on cost; it is not a constraint, and stating it as
one made the argument look stronger than it is.

Four reasons, recorded so this is not relitigated from memory:

**Nobody ships the alternatives.** [OBS][obs] defaults to carry-on, with
"Restart playback when source becomes active" as an opt-in toggle; true
resume-where-it-left-off is a [standing feature request][obs-req] that has never
landed. [vMix][vmix] makes the start point an explicit operator action ("Begin
from selected item"), not an automatic behaviour. [AWS Elemental MediaLive][aws]
states the governing principle outright — *"a running channel must always be
encoding content"* — substituting replacement content on the output side and
never stopping the pipeline. Waiting for a natural boundary is shipped by
nobody, because unbounded slate is worse than any jump cut.

**The premise is weaker than it looks, though not empty.** "The seam is a
visible jump" assumes the viewer has a reference for where the clip should
start. Usually they do not — they were watching the live show, and the cut to
filler changes content completely either way.

But an independent review pushed back on this, correctly, and the counter-case is
recorded rather than buried: **filler with an opening beat does not tolerate a
mid-item join.** A countdown joined at 0:40 is actively wrong. A sponsor read
that starts mid-sentence, a music track that starts mid-chorus, an instructional
clip missing its setup — all of these are material where the start carries
meaning. And under a flapping encoder a viewer CAN acquire a reference: live →
item at 0:20 → live → the same item at 1:10 is a visible skip.

The decision stands, because the alternative's cost is a slate blip on every
return and that is worse for the failure this feature serves. But the honest
statement is that this is a trade between two visible artefacts in a minority of
cases, not the absence of one. **The escape hatch is an operator toggle**, which
is what OBS and vMix both ship — and B2 is the sub-project that makes it
reachable, because it brings the UI. If real playlists turn out to be countdowns
and sponsor reads rather than ambient loops, the toggle is the answer and the
duration table is its mechanism.

**It introduces a worse artefact than it removes.** Respawning to reach an item
boundary means the tier stops delivering, so the slate covers the gap: the
operator sees live → slate → filler, two cuts instead of one. A flapping encoder
— the exact condition this feature exists for, and what
`scripts/acceptance-failover.sh` already exercises — produces repeated slate
blips. It trades an imperceptible seam for a visible one.

**It costs most of this sub-project's complexity.** A duration table, an
`out_time_ms` → item-index mapping, list rotation and respawn-on-return exist
solely to serve it.

**Consequence, accepted deliberately:** without that machinery a respawn can only
start at item 1, so **editing the playlist restarts it from the top.** An edit is
operator-initiated and they know they just changed it. If this later proves
wrong, the duration table is the mechanism and it can be added without redesign
— nothing here forecloses it.

[obs]: https://obsproject.com/forum/threads/restart-playback-when-source-becomes-active.98459/
[obs-req]: https://obsproject.com/forum/threads/request-for-video-media-source-pause-and-unpause-when-showing-hiding.154270/
[vmix]: https://www.vmix.com/help23/Playlist.html
[aws]: https://docs.aws.amazon.com/medialive/latest/ug/feature-input-loss.html

## Sequencing

`playlistFeedArgs` stops naming one file and names a list:

```text
-f concat -safe 0 -i <dataDir>/playlist-media/playlist-<sig>.txt
```

with `-stream_loop -1` looping the whole list rather than one file, and `-c copy`
unchanged.

### The list file is named after the signature, not fixed

A fixed `playlist.txt` is a time-of-check/time-of-use bug, not merely a
partial-read risk. `internal/engine/manager.go` runs ONE ENGINE PER SOURCE and
they share a data directory, so: engine A installs list A and spawns FFmpeg;
engine B installs list B at the same path; FFmpeg A opens the path and plays B's
list while engine A has recorded signature A. Atomic replacement does not help —
each write is atomic and the wrong one still wins. `selMu` is per-engine and
establishes no ownership of a shared file.

**So the filename carries the signature**, the exact path is passed to FFmpeg,
and the file is retained until that process stops. Two engines cannot collide
because two different lists cannot hash the same. Stale lists are swept when a
tier stops. Windows replacement semantics stop mattering, because nothing is
ever replaced.

**Always concat, even for one item.** The roadmap suggested keeping
`-stream_loop -1` on a single file as a degenerate case. Rejected: two argv
shapes mean two sets of seam behaviour, two things to test, and a rarely-taken
branch that is wrong in a way nobody notices. A one-entry list costs nothing.

**The list names derivatives, never originals.** That is the whole point of B1,
and it is what makes `-c copy` safe across a seam: every item shares codec,
timebase, resolution and channel layout by construction.

**The list is written atomically before spawn**, and `playlistSig` hashes its
CONTENTS, so an edit that changes the order — not just the membership — still
respawns. Hashing the item names alone would miss a reorder.

### One concat-list writer, not two

`internal/clipper` already renders a concat list, with deliberate single-quote
escaping: the demuxer has no backslash escape inside quotes, so a path
containing one must close, escape and reopen.

**That helper moves to `internal/ffmpeg` and both callers use it.** Copying it
into `internal/playlistmedia` would recreate precisely the drift B1 spent a fix
round eliminating, when four independent `TrimSpace` sites had to be collapsed
into `db.PlaylistUploadName`. One implementation, two callers.

## The timestamp contract

An earlier draft said `-c copy` and `+genpts` carried over "unchanged", implying
`+genpts` handles continuity. **It does not, and that had to be corrected.**
`-fflags +genpts` generates timestamps that are MISSING; a normalised MPEG-TS
derivative already has valid PTS and DTS. It neither rebases valid timestamps nor
repairs a bad concat offset. Two different mechanisms actually carry continuity
here, and the design has to name them:

- **Across an item boundary**, the concat demuxer offsets each file by the
  PREVIOUS FILE'S DURATION. Continuity therefore depends on that duration being
  accurate. FFmpeg's own documentation warns that inaccurate durations produce
  artifacts and that streams of unequal length within a file produce gaps.
- **Across the list wrap**, `-stream_loop`'s own timestamp-offset logic carries
  it, not the demuxer.

**Audio/video skew per item is a live hazard, not a theoretical one.**
`internal/playlistmedia` passes `-shortest` ONLY on the synthesised-silence path.
An ordinary input with a longer audio than video track (or the reverse) produces
a derivative whose streams end at different points, and concat's per-file offset
then applies to a file that does not have one clean end. Across a ten-item list
that skew accumulates.

**The contract this design commits to:** every derivative's audio and video must
end together, so that one duration describes the file. That is a change to the
NORMALISER — `-shortest` on every path, not just the silent one — and it belongs
here rather than in B1 because B1 never concatenated anything, so nothing could
observe the skew.

**Verification is a probe of packets, not of playback.** Monotonic DTS *and* PTS
across every item seam and across at least TWO complete list wraps, with
FFmpeg's own non-monotonic-DTS warnings treated as failures rather than noise. A
test that only checks the stream plays is a test that passes while drifting.

## `-safe 0` stops being theoretical

The concat demuxer refuses absolute paths unless given `-safe 0`, which disables
its own protection. B1's verification asserted this boundary and the assertion
**passed vacuously** — there was no concat list anywhere in the engine, so there
was nothing for the check to protect. That was recorded at the time rather than
banked.

Now there is a list, and every path in it must be traceable to
`uploads.Store.Resolve` or `playlistmedia.DerivativePath` — both of which reduce
a name to its base and join it to a directory this process chose. Operator text
must never reach the list. **This gets a real test that fails when the boundary
is bypassed**, not a grep for a string.

## When an item is not ready

Unchanged from B1: the playlist is offered only when every item has a
derivative. What changes is that the operator can finally SEE why.

B1's re-review left this open:

> A stale playlist item is now silent in the API. The only surface saying "this
> item's upload is gone" is a log line plus the playlist not starting.

B2 adds per-item readiness to the API so the UI can render it. Three states, and
they already exist in the system — the queue knows whether a normalisation is
queued, running or failed, and the filesystem knows whether a derivative
landed.

**On its own read endpoint, NOT on the settings blob.** This is deliberate and
it is the same trap `handlePutMQTTPassword` documents: the settings document
travels outward on every read and the settings page PUTs back what it GOT, so a
derived, read-only field riding in that payload is something the UI will send
back as though it were configuration. B1's N1 lockout came from the settings
round-trip carrying state the operator could not edit; putting live job status
into the same payload invites the same class of bug. Readiness is observed
state, not settings, and it gets its own GET:

| State | Shown as |
|---|---|
| derivative present | ready |
| job queued or running | transcoding |
| job failed, or the upload is gone | needs attention, with the reason |

**A failed normalisation must name the item.** "The playlist is not on air" with
no indication of which of eleven items is the problem is the silent-never-starts
failure B1's spec called the worst outcome, moved one layer up.

## The UI

A playlist editor in the failover section of `SettingsPage`, beside the slate
control it most resembles: choose from existing uploads, reorder, remove, and
see each item's readiness.

**This honours a promise made twice.** Both playlist settings keys sit in the
UI-nameability skip list in `internal/db/settings_drift_test.go`, and the reason
recorded there names sub-project B2. That reason string was ALREADY CORRECTED
ONCE after an earlier version falsely promised the control would arrive in B1.
Shipping B2 without a UI would make the guard carry a lie for the third time.

**Both entries come out of the skip list.** That is the acceptance criterion for
this part: not "a UI exists" but "the drift guard passes without an exemption".

It also closes B1's carried N1 caveat. The settings-lockout fix — validation
demanding existence only for items the operator is INTRODUCING — was needed
partly because a stale item could not be cleared from any screen. Once it can,
the fix stays (validation still must not punish inherited state) but its
justification narrows.

## Failure behaviour

**A seam is where a destination drops.** Every item boundary is a point where a
platform sees new packets, and the entire normalisation design exists so those
packets are indistinguishable from the previous item's. If a destination
restarts at a boundary, normalisation has failed at its one job — so the
acceptance measure is destination restarts across boundaries, not "the playlist
played".

**An empty list is not a playlist.** Already handled: B1 refuses `Enabled` with
no items at validation ("playlist is enabled but has no items"). Recorded here
because B2 must not regress it — an empty concat file is a runtime failure
FFmpeg would report, and validation catching it first is the difference between
a settings error and a crash-looping tier.

### Deleting media out from under a playing list

B1 carried this as a known gap; B2 makes it load-bearing, because the concat
demuxer RE-OPENS each file as it advances and again on every wrap. A derivative
that vanishes mid-list is not a one-off error — it is a respawn loop.

**The broadcast is already safe, and it is A's invariant that makes it so.** A
tier whose input has gone delivers no bytes, `playlistRunning` goes false, and
the slate wins on the byte counter. Nothing reaches air that cannot play. What
is missing is not safety but EXPLANATION: the tier respawn-loops indefinitely
and nothing tells the operator why the playlist stopped.

Three decisions follow:

1. **`DELETE /api/v1/media/{name}` refuses an upload a playlist item names**, and
   says which item. This is the in-use guard B1 deferred. It is defensible now
   in a way it was not then: B1's N1 lockout came from punishing an operator for
   state they had no control to edit, and B2 gives them the control.
2. **The delete path reconciles.** Today it does not, so even a permitted
   deletion leaves the engine's view stale.
3. **A derivative that vanishes anyway** — swept, or removed by hand — is
   reported through the readiness endpoint as needing attention, naming the item.
   The slate has already covered the air by then.

**A no-op reconcile cannot see any of this, and that is a real gap.**
`reconcilePlaylist` returns early when `cur.sig == want`, BEFORE readiness is
evaluated. Settings unchanged plus a derivative deleted underneath equals a
reconcile that does nothing. The signature hashes the desired list, not the state
of the files on disk, and it should not start stat-ing every derivative under
`selMu` to fix that. The answer is event-driven: deletion reconciles (2 above),
and job completion already does — B1 wired `jobs.WithOnChange`.

**A list is bounded.** `MaxPlaylistItems` (1000) already exists in
`internal/db/settings.go` and its comment says it exists to bound what B2 turns
this list into. That bound now does its job.

## Testing

| Case | Why it matters |
|---|---|
| Several mismatched sources normalise, concatenate and play as one timeline | The sub-project's entire premise. Probe the join, do not trust the argv |
| DTS and PTS are monotonic across every seam AND two full list wraps | The timestamp contract. FFmpeg's non-monotonic warnings are failures here, not noise |
| Every derivative's audio and video end together | One duration per file is what concat's offset arithmetic depends on |
| A destination rides every item boundary without restarting | The seam is the risk; this is the number that proves normalisation works |
| A destination riding a MISMATCHED live↔playlist cut, measured not assumed | The failure path B2 does not fix. The existing suite hides it by building filler that matches |
| Every path in a generated list came from Resolve or DerivativePath | The `-safe 0` boundary, tested rather than asserted |
| Two engines building lists concurrently each play their own | The list filename carries the signature precisely so this cannot collide |
| Reordering items respawns the tier; an unrelated settings save does not | `playlistSig` must hash contents, not membership |
| Deleting an upload a playlist names is refused, and names the item | The in-use guard, and the reason a stale item cannot strand a broadcast |
| A derivative removed out-of-band drops the playlist to the slate | A's byte counter is the backstop; this proves it still is once concat re-opens files |
| A one-item playlist takes the same code path as a ten-item one | No degenerate branch that only breaks in production |
| An empty enabled playlist is refused at validation | Not left for FFmpeg to discover |
| The drift guard passes with both skip-list entries REMOVED | The UI promise, mechanically enforced |

Every new guard must be shown to fail against a named one-line mutation. This
branch's predecessor produced six tests that could not fail, three of them found
only by running the mutation rather than reading the test.

**The golden tables must not move.** B2 changes what feeds the playlist tier,
not how the selector ranks it. Both tables under `internal/engine/testdata/`
stay byte-identical, and a moved row means something was changed that this
sub-project has no business changing.

## Adjustments to work already landed

Each of these is a statement in the tree that this design makes false. A comment
asserting something the code does not do is the failure mode this repo keeps
having to correct.

- **`engine.go`'s `BUG.` comment** — delete it. The derivative is the input now.
- **The readiness docstring and the deleted-upload test.** Their stated reason is
  "the argv names the upload, so FFmpeg would respawn-loop on a file that is
  gone". Once the argv names the DERIVATIVE that is no longer true. Keep
  requiring the upload to exist — an item naming a deleted upload is a
  configuration error the operator must see — but rewrite the reason, or it
  becomes another comment that asserts the opposite of the code.
- **`docs/roadmap/PLAYLIST-AND-COMPOSITING.md`** — record that item-boundary
  resume was considered and rejected, with the reasoning, so it is not
  reintroduced from the old text.
- **`internal/api/playlist_normalise.go`** — the N1 comment cites "the settings
  UI has no playlist control yet" as part of why the lockout was so bad. Narrow
  it once the control exists.
- **`docs/SCHEDULED-BROADCAST.md`** — it describes a single-file playlist.
- **`internal/playlistmedia`** — `-shortest` moves from the silent-audio path to
  every path, per the timestamp contract. B1 had no way to observe the skew.
- **`internal/api/media.go`** — the delete endpoint gains an in-use guard and a
  reconcile.

## Commit shape

Engine, API and UI land as separate reviewable commits even though they are one
sub-project. The whole-branch review is where cross-layer interactions surface —
B1's worst two findings were both invisible to task-scoped review — but a
reviewer should still be able to read the concat change without a React diff in
the way.

## What B2 does NOT fix: the ingest mismatch

**Normalisation makes items match EACH OTHER. Nothing makes them match the
operator's encoder.** `playlistFeedArgs` already says so in capitals: the file's
codec parameters reach destinations unchanged, and a destination that is copying
passes them straight to the platform, so switching to material that differs is a
mid-stream codec change — which platforms answer by dropping the connection.

Items are normalised to a FIXED 1080p30 H.264/AAC profile. An operator
publishing 720p60 gets a codec change at every live↔playlist cut, in both
directions. **That is the feature's actual failure path, and B2 does not close
it.**

Worse, the existing measurement hides it. `scripts/acceptance-failover.sh`
reports 0 destination restarts — and builds its filler clip to match the
publisher DELIBERATELY, saying so in a comment. The mismatch is excluded by
construction, so the green number says nothing about the case an operator will
actually hit. This is the same shape as B1's Critical finding, where a hand-copied
derivative made a dead code path look green.

**B2's obligation is therefore to MEASURE it, not to fix it:** an acceptance case
with a deliberately mismatched publisher, recording what destinations actually do
at the live↔playlist cut. That turns an unknown into a number. Fixing it means
either constraining the ingest or re-encoding at the selector — the latter
reverses a decision made throughout the engine and adds a permanent transcode to
every broadcast, which is its own roadmap item and not this one.

## What could go wrong

**`-stream_loop -1` over a concat input is likely fine, and still needs
measuring.** `-stream_loop` is a generic input option and the concat demuxer
supports seeking to start, so this is not an unknown incompatibility — an earlier
draft overstated it. What must be measured is the WRAP: at least two complete
passes through the list, checking timestamps across the seam where the last item
meets the first.

**Timestamp continuity across `-c copy` seams is the sharp edge**, for the
reasons in the timestamp contract above. It is not covered by `+genpts` and must
not be assumed from a stream that merely plays.

**Normalisation still yields to a live ingest.** `playlist.normalise` inherits
`ModeDeferred` with `YieldToStream`, so items added mid-broadcast do not become
playable until it ends. Left as the safe default in B1 and unchanged here; a
UI that shows "transcoding" indefinitely during a broadcast makes the cost
visible for the first time, which may be the thing that settles the question.

**`DurationMS` remains unpopulated.** B2 does not need durations, so nothing
here fixes it — the disk guard keeps estimating from source size and reporting
`bounded=false`. Stated so this design is not read as having addressed it.
