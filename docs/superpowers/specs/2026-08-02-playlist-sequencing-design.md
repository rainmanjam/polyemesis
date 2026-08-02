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
nothing, and A's invariant requires it to be delivering to be selectable at all.
When the encoder drops, the selector cuts to a playlist that is already on the
air's doorstep: one cut, no gap, joining whatever item is mid-play.

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

**The premise does not survive scrutiny.** "The seam is a visible jump" assumes
the viewer has a reference for where the clip should start. They do not — they
were watching the live show, and the cut to filler changes content completely
either way. A mid-item join is imperceptible in material nobody has seen. The
one case where it is perceptible — a second drop shortly after a return, where a
clip appears to skip forward — has as its alternative making the viewer rewatch
a minute they just saw. Neither is a failure.

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
-f concat -safe 0 -i <dataDir>/playlist-media/playlist.txt
```

with `-stream_loop -1` looping the whole list rather than one file, and the
existing `-c copy` and `+genpts` unchanged.

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

**A list is bounded.** `MaxPlaylistItems` (1000) already exists in
`internal/db/settings.go` and its comment says it exists to bound what B2 turns
this list into. That bound now does its job.

## Testing

| Case | Why it matters |
|---|---|
| Several mismatched sources normalise, concatenate and play as one timeline | The sub-project's entire premise. Probe the join, do not trust the argv |
| A destination rides every item boundary without restarting | The seam is the risk; this is the number that proves normalisation works |
| Every path in a generated list came from Resolve or DerivativePath | The `-safe 0` boundary, tested rather than asserted |
| Reordering items respawns the tier; an unrelated settings save does not | `playlistSig` must hash contents, not membership |
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

## What could go wrong

**`-stream_loop -1` on a concat input is unverified.** It is documented to loop
an input and the concat demuxer is one input, but the interaction — particularly
what happens to timestamps at the wrap — must be MEASURED against real FFmpeg
before it is relied on. This project's standard is a probe, not a plausible
argument.

**Timestamp continuity across `-c copy` seams is the sharp edge.** `+genpts`
gives the relay a monotonic base at a loop boundary today with one file. Whether
it does the same across item boundaries and at the list wrap is exactly what the
destination-restart measurement is for.

**Normalisation still yields to a live ingest.** `playlist.normalise` inherits
`ModeDeferred` with `YieldToStream`, so items added mid-broadcast do not become
playable until it ends. Left as the safe default in B1 and unchanged here; a
UI that shows "transcoding" indefinitely during a broadcast makes the cost
visible for the first time, which may be the thing that settles the question.

**`DurationMS` remains unpopulated.** B2 does not need durations, so nothing
here fixes it — the disk guard keeps estimating from source size and reporting
`bounded=false`. Stated so this design is not read as having addressed it.
