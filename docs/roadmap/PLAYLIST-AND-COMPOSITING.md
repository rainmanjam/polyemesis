# Playlist broadcast, and multi-input compositing

Two features that look adjacent and are not. They share one prerequisite and
nothing else.

**Playlist** — go live from a file, on a schedule, with no encoder attached.
Asked for five separate times on the competitor's tracker under different names.

**Compositing** — picture-in-picture, side-by-side, 2×2 grid. The answer to
restream.io's Studio.

---

## Playlist: the "mostly wiring" claim is half right

The prior research concluded polyemesis "has most of the parts already, so this
may be wiring rather than building". Checked against the code, that is **half
right, and the half that is right is bigger than expected**.

### What ships today, with zero new code

- `IngestPull` is a first-class ingest mode, validated for both primary and
  backup ([internal/db/settings.go:25](../../internal/db/settings.go)).
- **`file` is already in the scheme allowlist**
  ([internal/ffmpeg/build.go:86](../../internal/ffmpeg/build.go)).
- `pullFile` already emits the exact playlist primitive (build.go:230):

  ```go
  case pullFile:
      // -stream_loop -1 makes the file look like a feed that never ends, and
      // -re paces it at wall-clock speed. Without -re FFmpeg reads at disk
      // speed and buries the relay in an hour of stream in seconds.
      return []string{"-stream_loop", "-1", "-re"}
  ```

- Path confinement to `DataDir` is done, and wired.
- The UI already offers it: *"Pull — dial a camera, feed or file"*.
- An acceptance suite exists: `scripts/acceptance-pull.sh`.

**So this works today, unmodified:** create a pull source with `file://loop.mp4`,
add a schedule with `action=start` at 20:00 targeting its destinations. At 20:00
the destinations enable, reconcile runs, and you are live from a file with no
encoder attached.

That is a genuine answer to at least three of the five tracker requests — "stream
from MP4", "virtual input: looping video", and "scheduling".

**The wart:** the file loops continuously from server start, so at 20:00 you join
mid-file, not at frame 0. The scheduler cannot fix this. `scheduler.Actuator` is:

```go
type Actuator interface {
    SetDestinationEnabled(id int64, enabled bool) error
    ListDestinationIDs() ([]int64, error)
    Reconcile() error
    SetPlaylistEnabled(enabled bool) error
}
```

`Action` is `start|stop|playlist.start|playlist.stop`. `SetPlaylistEnabled`
arrived with sub-project C and flips one install-wide settings field; every other
method is destination-level. **No schedule can touch a source, restart an ingest,
or select a playlist item** — which is what this section is about, and remains
true.

### What does not exist at all

Verified by repo-wide grep: no sequencing of any kind, no playlist table (the
schema has 20 tables, none of them), **no file-upload path anywhere** (the one
media route is a GET; `internal/media` derives only from recordings), and no way
to *not* loop — `-stream_loop -1` is hardcoded.

**Verdict:** the *demo* is one day. The *feature* — several files, sequenced,
gapless, uploadable, yielding to a live encoder — is building, not wiring. The
prior research conflated the two.

### Sequencing: take the concat demuxer

| Approach | Verdict |
|---|---|
| Supervisor respawns FFmpeg per item | **Reject.** Every seam becomes a process restart: relay silence, a PTS reset, a destination-visible discontinuity. The codebase already learned this — source feeds are `AutoRestart: false` with a sweep-computed `-output_ts_offset`. |
| `-stream_loop -1` on one file | Ships today. Single item. Keep as the degenerate case. |
| **concat demuxer** | **Take it.** One process, one continuous timeline, gapless by construction. Prior art in-repo: `internal/clipper/args.go:120` already builds `-f concat -safe 0 -i list` with a sidecar-file mechanism. |

Concat's hard requirement is that items share codec, timebase, resolution and
channel layout, or it errors or produces garbage. That forces a
**normalize-on-import job** — for which `internal/jobs` and the `media.KindProxy`
worker pattern already supply the queue and governor.

### The sharp edge: a live encoder arriving mid-playlist

This is not wiring, and getting it wrong is a broadcast-safety regression.

The selector derives liveness purely from hub byte counters
([internal/engine/engine.go:2586](../../internal/engine/engine.go)):

```go
primaryRx := e.hub.RxBytes()
```

A playlist publishing into the primary hub makes primary **permanently live**, so
`chooseSource` can never fail over to backup or slate. **A playlist would
silently disable the entire failover feature.**

The correct shape is a **fourth `sourceKind`** with its own hub and a precedence
rule, matching the file's existing "a slate is a holding pattern, never a
destination" logic:

- Playlist ranks *below* primary and backup, *above* slate.
- A live encoder pre-empts the playlist immediately — not subject to the
  stability window, because sitting on filler while the show is back on air is
  the worse failure.
- Returning to the playlist after the encoder drops does **not** resume at an
  item boundary. See below — this line used to say the opposite, and the
  opposite was wrong.

This touches an 80-line pure function backed by an 860-line `failover_test.go`
where every branch is a broadcast decision. **Highest blast radius in either
feature.**

#### Item-boundary resume: considered, and rejected

The paragraph above originally read *"returning to the playlist after the
encoder drops resumes at an **item boundary**, not mid-item, or the seam is a
visible jump."* Sub-project B2 designed against that and rejected it. It is
written down here, rather than quietly dropped, because it is a plausible idea
that reads like an obvious requirement and will otherwise be proposed again.

**What the playlist actually does.** One FFmpeg holds the whole playlist open,
`-stream_loop -1` over a concat list, publishing continuously into a hub of its
own whether or not it is the source on air. The selector does not start or stop
it when the encoder drops; it changes which hub the destinations' feed reads.
So the playlist never *resumes* — it never stopped. Coming back to it lands
wherever it happens to be, exactly as tuning back to a channel does.

**Why that is right, and not a shortcut.** Three reasons, in the order they
decided it.

1. *It is what the products in this space do.* OBS's Media Source restarts a
   file when it is shown again unless "restart when activated" is switched off,
   and its scene-switch behaviour is a per-source toggle rather than a
   guarantee. vMix's playlist keeps running behind the mix and returns to the
   position it is at. AWS Elemental MediaLive's input switching does not
   reposition a source on a switch back at all. Nobody in this market makes
   boundary-alignment the unconditional rule, and an operator arriving from any
   of them would find one surprising.
2. *Holding a boundary means holding the live encoder off air.* The seam that
   matters is not the one on the file, it is the one on the show. Aligning to an
   item boundary can only be done by delaying the switch until the boundary
   arrives — up to a whole item, and a playlist of ten-minute programmes makes
   that ten minutes of filler with the encoder back. This tier's own precedence
   rule already says the opposite: "a live encoder pre-empts the playlist
   immediately — not subject to the stability window, because sitting on filler
   while the show is back on air is the worse failure." Boundary resume
   contradicts a rule the same section states two lines above it.
3. *A boundary is not a clean seam anyway.* Measured, not assumed: the concat
   demuxer under `-c copy` does not produce a clean loop boundary either. At the
   wrap the last item's final frame holds for about 2.5 seconds and the first
   item plays about 2 seconds short — reproducible with nothing but ffmpeg and
   the derivatives off disk (see `scripts/acceptance-failover.sh` step 9, which
   records the measurement). Cutting *to* an item boundary would land on that,
   so the visible jump the original line wanted to avoid is present at the very
   place it wanted to move the cut to.

**What this costs.** An operator who edits an on-air playlist gets a respawn:
the tier's signature changes, the process restarts, and playback begins at item
one. That is a real cost and it is not hidden — it is why the concat list is
signature-named and why the table below still lists on-air editing as a risk.

**What would change the answer.** The operator toggle OBS and vMix both ship.
That is out of scope here rather than rejected: it needs both behaviours, a
settings key, a control, and an acceptance case of its own. If it is built, it
is a toggle with this as the default — not a reinstatement of the original line.

### Scheduler

Two increments. `action=start` on destinations works and joins mid-file.

**The first increment shipped** as sub-project C: `playlist.start` /
`playlist.stop` flip `failover.playlist.enabled` and ask for a reconcile. The
skip-if-missed rule and the `MarkScheduleRun` monotonic guard generalised
unchanged, as predicted here — that machinery really was reusable as-is.

**No `source_id` column was added, and none should be.** This line originally
prescribed one. `db.Settings` is install-wide: `GetSettings` takes no source id,
`effectiveSettings` overlays only `settings.Ingest = src.Ingest`, and `db.Source`
carries no failover fields — so there is no per-source playlist for a `source_id`
to select, and a column would have been a schema change selecting between
identical things. Moving the playlist block onto `db.Source` is what would make
it mean something, and that is deferred until somebody runs two programmes and
wants different filler on each. See
`docs/superpowers/specs/2026-08-03-scheduled-playlist-design.md`.

**Still to do:** `source.select`. The selector pin is in-memory only
(`e.sel.pinned`), so there is no stored intent to flip — scheduling it needs
somewhere to persist the pin first, or it breaks the invariant below.

Keep the package's stated invariant: *"The runner deliberately cannot start a
process: it writes the same intent a human would and asks for a reconcile."* A
playlist go-live must be a stored-intent flip plus reconcile, never a direct
spawn.

---

## Compositing: multi-source landed, but as isolation

The "N sources exist" premise is **true** — and proven end to end by
`scripts/acceptance-multisource.sh` with per-source tone and bandpass cross-talk
checks. But `internal/engine/manager.go:19` is explicit about what that means:

> Manager runs one Engine per source. The alternative was to make a single Engine
> internally multi-source, which would have meant reworking the hub, the
> reconciler, the destination and rendition maps, the silence tier and the
> failover selector all at once — every one of which is already correct for
> exactly one programme.

Each Engine owns its own hub, destinations, renditions, selector and recorder.
Nothing in the codebase reads two hubs.

**So "a filter graph, not an architecture" is half right.** The filter graph
genuinely is easy, and prior art exists — `rendition.go:473` already builds
`[bg][fg]overlay=…` with chroma-grid alignment via `evenExpr()`. What is missing
is an **owner**: no object in the process is allowed to span two engines. That is
an architecture question, and it is the bulk of the work.

### Graphs

PiP, mirroring the existing overlay construction:

```
[0:v]scale=1920:1080,setsar=1,setpts=PTS-STARTPTS[bg];
[1:v]scale=480:270,setsar=1,setpts=PTS-STARTPTS[pip];
[bg][pip]overlay=x=W-w-32:y=H-h-32:eof_action=pass:shortest=0[vout]
```

2×2 grid:

```
[0:v]scale=960:540,setsar=1,fps=30,setpts=PTS-STARTPTS[a]; … [d];
[a][b][c][d]xstack=inputs=4:layout=0_0|w0_0|0_h0|w0_h0:fill=black[vout]
```

`setpts=PTS-STARTPTS` per branch is load-bearing: each input is an independent
MPEG-TS with an unrelated PTS origin, unlike the selector case where one
`-output_ts_offset` suffices.

### Where the process lives

Best fit by a wide margin: **give the composite a `db.Source` row with a new
ingest mode.** It then inherits an entire `Engine` for free — destinations,
renditions, routing, recording, playout, meters — and the only thing that changes
is what publishes into `e.hub`. `reconcileIngest` already branches on mode.

But `Manager` must gain two things it does not have:

- **Dependency ordering.** `Manager.Sync()` iterates in `ListSources()` order with
  no notion of "this one reads that one". Needs a topological pass.
- **Cycle rejection.** A composites B, B composites A must be refused at
  validation time, not discovered as a hang.

**Per-input failover comes free** if each composite input points at the
contributing source's *selector* hub rather than its raw ingest: a dead input is
then already covered by that source's own slate, which solves the
`xstack`-stalls-on-missing-input problem with no new machinery. Caveat: the
selector hub only exists when failover is enabled on that source.

### Audio is where the product lives

`RenditionArgs` carries the canary:

> The load-bearing line is `-map 0:a -c:a copy`: every audio track the ingest
> carries arrives at the destinations bit-identical… If this ever becomes an
> audio encode or a mixdown, the product's differentiator is gone.

A composite must **concatenate** track lists, never mix: source A's tracks land
at output 0..n, source B's at n+1..m, all `-c:a copy`. Two consequences:

- `routing.MaxTracks = 6` caps the merged count. Two 6-track sources overflow.
  Either raise it — touching the downmix matrix, validation and the UI matrix —
  or bound composites to fewer tracks per input.
- Track annotations are per-source. The composite's engine needs a **merged
  annotation set with provenance** ("track 7 = source B's music"), or
  per-destination routing across a composite is unusable.

### CPU

Compositing forces a video re-encode. Today the only video encodes are
renditions; destinations and playout variants are all `-c:v copy`. A composite
adds one full encode *before* the ladder, so the accounting goes from "one encode
per distinct rendition" to "one composite + N renditions".

Mitigate by making the composite the top ladder rung so downstream renditions
only rescale. There is a `GPUBusy()` signal but **no admission control** that
would refuse an unaffordable composite; that gap becomes user-visible here first.

---

## Test plan — measurements

**Playlist gaplessness — four independent measurements**, each of which a
respawn-per-item implementation fails:

1. **No silence at the seam.** Items at 300 Hz / 900 Hz / 2000 Hz. Record the
   destination, slide a 100 ms window across it emitting a broadband-RMS series.
   Gapless ⇒ **RMS never drops below −50 dBFS anywhere**, while the 300 Hz band
   falls and the 900 Hz band rises within one window.
2. **Zero TS discontinuities — instrumentation already in the repo.**
   `internal/relay/relay.go:383` already counts MPEG-TS continuity-counter breaks
   and surfaces them via `Stats()`. A gapless transition must add **exactly 0**.
   No new instrumentation needed.
3. **Frame accounting.** `ffprobe -count_frames` vs `Σ(item duration) × fps` —
   within 1 frame per seam. A respawn loses 10–30 and this pins the number.
4. **PTS monotonicity.** Zero backwards steps; max inter-packet delta ≤ 2× the
   frame interval.

**Playlist yielding.** Run 10 s, connect a real SRT publisher carrying a 4th tone.
Measure: the new tone appears and the playlist tone falls to the leakage floor
within the grace window; `Failover()` reports `source=primary`; and the
destination's `Restarts` count is **unchanged** — the whole point is that the
destination process never restarts.

**Composite placement — pixel probes, not eyeballs:**

1. **Layout.** Feed the four sources red/blue/green/white; probe the centre of
   each expected quadrant with `crop=1:1:X:Y,signalstats` → `YAVG/UAVG/VAVG`,
   ±8. This proves *placement*, not merely that something composited.
2. **PiP size.** Probe the inset centre **and a pixel just outside its border**.
   Without the second probe, a full-screen B passes.
3. **Audio on the right tracks with zero cross-talk.** Bandpass each output
   track: 0..n carry only A's tones, n+1..m only B's. **This is the test that
   catches an accidental mixdown** — the differentiator regressing.
4. **Cost, reported not asserted.** Capture `speed=` from `-progress` (already
   parsed) plus CPU/RSS, and publish a table of composite fps vs input count.

---

## Risks and effort

**Playlist**

| Risk | Severity |
|---|---|
| A playlist on the primary hub makes primary permanently live and **silently disables failover** | **High** — must be designed around from day one |
| `sourcePlaylist` touches `chooseSource`, 80 lines backed by 860 lines of tests, every branch a broadcast decision | **High** |
| Concat needs codec/timebase-identical items; without normalize-on-import it errors or produces garbage | Medium |
| **No upload path exists** — this is the first place polyemesis accepts arbitrary user files | Medium |
| Editing an on-air playlist means a respawn unless the design commits to "applies at next item boundary" | Medium |

**1 day** to document and acceptance-test the already-shipping subset.
**17–22 days** for the full feature.

**Compositing**

| Risk | Severity |
|---|---|
| Merged tracks vs `MaxTracks = 6`; any implementation that mixes instead of concatenating **kills the differentiator** | **High** |
| Forces a re-encode; first feature that can make the box the bottleneck, and there is no admission control | **High** |
| Violates the deliberate per-source isolation; needs cross-engine ordering and cycle rejection | Medium |
| Independent PTS origins across inputs; drift over hours | Medium |

**21–26 days.**

## Dependency order

Neither blocks the other. But **both want the same refactor**: generalising the
selector from a fixed three-value enum plus a hardcoded `chooseSource` ladder
into an **ordered list of candidate sources**. Playlist needs it to yield to a
live encoder; compositing wants it for per-input failover.

Doing it once, before either, is materially cheaper than doing it twice — and
much safer than doing it twice in the codebase's most safety-critical pure
function.

**Recommended order:**

1. **Playlist Phase 0 — one day.** Document and acceptance-test that
   looping-file pull plus an existing schedule already delivers scheduled
   pre-recorded broadcast. This answers three of five tracker requests
   immediately, at essentially zero cost and zero risk. **The single
   highest-leverage day in either feature.**
2. **Selector generalisation** — the shared prerequisite, done once. ✅ **Shipped.**
   `chooseSource` ranks an ordered candidate list, and a playlist is already the
   fourth candidate, ranked below both ingests and above the slate. What remains
   for phase 1 is wiring: nothing sets `playoutRunning` yet, and `SwitchSource`
   still refuses a `"playout"` pin, so the decision exists and cannot yet be
   reached.
3. **Playlist phases 1–4** — bigger demand, lower technical risk, does not
   disturb the multi-source isolation boundary.
4. **Compositing** — larger, riskier, and the one that puts the audio
   differentiator in play.

Compositing does become more tractable after multi-source — but not for the
reason the earlier research gave. Multi-source helped by providing N hubs, not by
making the architecture question go away.

---

## See also

- [ROADMAP](README.md)
- [../ARCHITECTURE.md](../ARCHITECTURE.md) — the per-source isolation boundary
- [OVERLAYS.md](OVERLAYS.md) — shares the `overlay=`/`evenExpr` helpers
