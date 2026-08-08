# Core review — engine, rtmpserver, srtserver, routing, relay

Reviewed 2026-08-08 by four reviewers: **Fable** orchestrating **codex** and
**agy** for breadth, plus two **Opus** passes for depth (one on `internal/engine`,
one on the transport and routing layers). Every finding below was confirmed
against the code on disk; the routing ones were confirmed by *executing* the
compiler. Anything the reviewers could not confirm was discarded.

Branch: `refactor/core-review-2026-08-08`, off `ac03f84`.

## The shape

Every real defect this session — twelve before this review, plus these — has been
the same thing: **a protection that silently does nothing.** A guard reading the
wrong flag. A helper nothing calls. A counter run on text that could not contain
what it searched for. A readiness field hardcoded true beneath a comment
describing the opposite. None of them failed loudly; several had comments
asserting the behaviour they did not have.

Rank by that, not by severity label. A thing that is quietly absent outranks a
thing that is loudly wrong.

## Already fixed (in `ac03f84`, do not redo)

| Finding | Where |
|---|---|
| Backup SRT passphrase enforced the **primary's** secret | `manager.go:402` |
| `sourceGen++` gated on `measured`, so a mode switch before the first probe bumped nothing | `engine.go:1072` |

## Tier 1 — audible, in the differentiator — **ALL FIXED**

Fixed on this branch. Before touching anything, the package was reverted to
`ac03f84` and a throwaway probe measured each defect, so the numbers below are
what the pre-fix compiler actually emitted rather than what reading it suggested:

```
F1 matrix: norm="off" limiter=false
   [0:a:0]pan=stereo|c0=2*c0+2*c2+2*c4|c1=1*c1[a_t0];...
F1 simple: norm="off"
   [0:a:0]pan=stereo|c0=2*c0|c1=2*c1[a_t0];...
F2 narrowed: warnings=[track 1 has 2 channel(s); channel 3 is ignored ...]
   [0:a:0]pan=stereo|c0=0.4143*c0|c1=0.4143*c1[a_t0];...      (-7.7 dB)
F3 2.1 as 3 channels: L=[0.5858 0 0.4142] R=[0 0.5858 0.4142]  (ch 2 is LFE)
F4 9ch: perChannel L=0.2000 R=0.2500 -> -1.94 dB imbalance
```

| # | Where | Defect | Consequence | Fix |
|---|---|---|---|---|
| 1 | `routing/filtergraph.go:421` | `resolveNorm` assumes only *summing* creates clipping, so a single-track profile is left at `Normalization: off`. But `PanFilter` sums unbounded cells per output channel and `Validate` caps only the **per-cell** gain, never the row sum | A one-track matrix compiles to `pan=stereo\|c0=2*c0+2*c2+2*c4` — up to 6× full scale. Hard clipping, with NormAuto's protection silently absent | `resolveNorm` also takes `peakGain(cells)`. Widened, never narrowed: nothing that has a limiter today loses one |
| 2 | `routing/filtergraph.go:330` | A saved matrix whose track has since lost channels drops the missing cells and never rescales the survivors; the warning names only the dropped channels | A 5.1 matrix against a now-stereo ingest yields `c0=0.4143*c0` — the destination goes **7.7 dB quiet**, a valid graph rather than an error, with nothing saying the level moved | `levelWarning` states the drop in dB. Deliberately *not* auto-rescaled: silently rewriting the operator's coefficients is the same category of sin as silently changing the level |
| 3 | `routing/downmix.go:12` | `DownmixMatrix` keys only on channel *count* and never reads `Track.Layout`, which `ffmpeg/probe.go:112` does populate | A 2.1 track (FL FR LFE) compiles to `c0=0.5858*c0+0.4142*c2` — LFE folded into both legs, directly contradicting the file header's claim that LFE is excluded | Coefficients assigned by channel *name* from libavutil's layout table; count table kept as the fallback. Reproduces the old table exactly for every layout that was already right |
| 4 | `routing/profile.go:30` | `MaxChannels = 8` is enforced only against matrix cells; simple mode passes the probed count straight into `DownmixMatrix`'s unbounded default branch | A >8-channel track routes through a guard that does nothing, and an **odd** wide count normalises the two rows by different sums — 9ch gives a permanent **1.94 dB stereo imbalance** | `normalizeRows` divides both legs by the same figure. The `MaxChannels` inconsistency itself is *not* fixed — see below |

**Left open, deliberately.** `MaxChannels = 8` still bounds matrix cell indices
while simple mode routes a 9-channel track happily, so a width you can route is
a width you cannot address cell-by-cell. That is a validation bound the UI's
channel grid is built against; raising it is a UI change, not a bug fix, and the
audible half of finding 4 is closed without it.

**Verification.** `levels_test.go` — 14 tests including two that go through real
FFmpeg: all 27 named layouts compile to graphs FFmpeg accepts against a real
track of that layout, and a 2.1 source whose FL/FR are digital silence and whose
LFE carries a 60 Hz tone now measures **−91.0 dB** at the output (silence)
where it previously carried the tone at −7.7 dB.

## Tier 2 — silently absent protections — **ALL FIXED**

All nine fixed across `52efbff`, `5eac880` and `662202c`. Finding 8 was confirmed
the same way Tier 1 was: the lock removed again, and `-race` naming both
`c.last[pid]` and `subscriber.sendErrors`.

| # | Where | Defect | Consequence |
|---|---|---|---|
| 5 | `rtmpserver.go:419` + `manager.go:486` | The readiness grace's comment says an unknown or disabled source is answered at once "so this cannot be used to hold connections open". It can: `manager.go` registers an RTMP target for **every** source regardless of ingest mode | Any valid token for an SRT-mode source is found + enabled + not-ready, so every connect takes the full **6 s hold, unbounded in parallel**. Introduced by the grace fix earlier today |
| 6 | `engine.go:1852` + `silence.go:174` | `detachFeedForSilence` releases `selMu`, then `reconcileSilence` swaps the silence hub holding only `e.mu`. A 500 ms selector sweep in that window starts a feed on a hub about to close, and `feedAt` was zeroed so the backoff will not stop it | `ensureFeed` then sees the signature already matching and leaves it alone **permanently**: selector hub carries zero bytes, every destination reads "running", nothing publishes, no error |
| 7 | `engine.go:923` | `onStorage` is the only production caller of `reconcileRecorder` not holding `reconcileMu`, and `recorderSig` is read unlocked | Free-space recovery racing a reconcile spawns two recorders on the same segment pattern — an orphan FFmpeg and a relay port `Stop` cannot reach |
| 8 | `relay.go:59,333,360` | The comment "cc is touched only by run, so it needs no lock" is false — `Deliver` runs on the srtserver read-loop goroutine, and an SRT takeover can have two sessions inside it at once | Unsynchronised writes to `continuity.last[pid]` and `subscriber.sendErrors`: a genuine data race that corrupts the TSLost figure the whole "UDP on loopback is defensible because it is measured" argument rests on |
| 9 | `engine.go:1613`, `engine.go:5558`, `silence.go:156` | Three more readers of `probed` that mean `measured` — the same confusion fixed in `wantSilence` and `reconcileOutputs` today | Meters torn down ~9 s into any outage; captions crash-loop on an unprobed engine; `SourceKnown` labels a real measured layout as placeholder-derived, inverting the disagreement it was written to remove |
| 10 | `manager.go:158`, `manager.go:213` | `Manager.Sync` and `reconcileSharedIngest` run outside `m.mu` with no equivalent of `reconcileMu`, and `Reconcile` is called concurrently from five HTTP handlers | Two concurrent reconciles start two engines for one source — the second overwrites the first, leaking a running engine with hub, ingest child and relay ports nothing can stop |
| 11 | `rtmpserver.go:729,772` | `isSetup` treats every `DataAMF0` as setup and files them all under one `meta` slot | Any mid-stream cue point replaces the cached `onMetaData`, so late joiners — including the engine's own FFmpeg — get a cue point replayed as their metadata |
| 12 | `rtmpserver.go:836` | `HasSubscriber` counts map entries; a subscriber whose FFmpeg died while nothing was publishing stays parked in `serveSubscriber`'s select forever | `Ready` reads true for a stream with only a dead reader — the green-encoder-no-output failure, narrowed but not closed |
| 13 | `srtserver.go:369` | `handlePublish` re-resolves the token but never re-checks `Enabled` the way `handleConnect` did | A source disabled in the connect-to-publish window is still admitted and delivers into the hub |

## Tier 3 — performance — **4 of 6 fixed, 2 declined with reasons**

| Where | Cost | Outcome |
|---|---|---|
| `relay.go:347` | `fanout` allocates a fresh `[]*subscriber` **per datagram** — ~1,900 allocations/sec per hub at 20 Mbit/s | **Fixed**, and the claim was **overstated**. Measured: Go's escape analysis kept that slice on the stack at 1, 4, 8 and 12 subscribers, and allocated only at 16 (128 B). The per-datagram *lock* was real at every count; both are now gone via a copy-on-write `atomic.Pointer` list |
| `rtmpserver.go:827` | `awaitReady` polls `lookup` at 10 Hz, and production `lookup` does a full `ListSources()` + map rebuild — ~60 DB reads per held publisher per grace window | **Fixed.** Woken by the subscriber-attach event instead, with a 1 s backstop tick for the parts of `Ready` this package cannot observe. ~60 lookups → ~1–6, and the publisher is admitted the instant its ingest child arrives rather than up to a poll late |
| `engine.go:6217` | `Status` calls the linear `destByID` twice per row with the same argument | **Fixed.** Indexed once into a map |
| `destinations.go:767` | `reconcileBackup` renders a full argv + SHA-256 above an early return that never reads it | **Fixed.** Computed only on the branch that reads it — the discarded case was every destination without redundancy, on every reconcile |
| `rtmpserver.go:696` | `pump` takes the server-wide `s.mu` per RTMP message | **Declined.** Real in shape, small in magnitude: an uncontended mutex is tens of nanoseconds and the lock is held for a map iterate plus non-blocking channel sends, against a few hundred messages a second. Fixing it properly means moving `subs`/`setup`/`slots`/`dropped` behind a per-stream lock across nine sites in the ingest admission path — a lock-order change on the hot path, for a win too small to justify it in the same pass as four correctness fixes to the same file. Worth doing on its own, with its own review |
| `engine.go:6361` | `onState` → `publishStatus` → `Status()` costs 3 DB queries plus a `routing.Compile` per non-running destination; a reconcile starting N destinations rebuilds the whole snapshot N times | **Declined for now.** The `destByID` fix takes a bite out of it, but the real fix is coalescing status pushes, which changes when the UI sees updates. That is a behaviour change wearing a performance costume and belongs in its own change with its own way of being judged |

## Tier 4 — structure, behaviour-preserving — **ALL DONE**

All five landed with no behaviour change: the full suite and `go test -race ./...`
are green before and after. `engine.go` went from **6,530 to 3,881 lines**.

1. **`sourceState` type** — `source/probed/measured/measuredMode/sourceGen/videoInfo` are now one type with `layoutForProcessBuilding() (Source, bool)`, `layoutForDisplay()`, `arrivingNow()`, `commitProbe`, `invalidate` and `clearProbed`. Embedded anonymously, so all ~100 existing reads still compile and only composite literals needed touching. **Every raw mutation site in production is gone.**
2. **`selector.go`** — 2,346 lines: the selector, backup listener, playlist tier and operator failover controls.
3. **`status.go`** — 320 lines: `Status`/`Renditions`/`SourceInfo`/`destByID`/`Processes`. The file comment states the un-fixed part plainly — the snapshot is still assembled from several instants, and this is where that gets fixed when something needs it to be atomic.
4. **`setup.go`** (rtmpserver) — 180 lines, and it is now a pure value type: nothing in it touches a socket, a lock or a `Server`.
5. **`ports.go`** (relay) — `PortAllocator`, which shared no state or concept with `Hub`.

**A correction to my own framing.** #1 was written as "makes the wrong read
**unspellable**". Go cannot do that: anything in the package can still write
`e.probed`. What the type actually delivers is one place that owns the invariant,
two questions with names that cannot be mistaken for each other, and the correct
read being the shorter one to write. The file says so rather than claiming the
guarantee.

The refactor did buy one real guarantee, though. `TestDestinationsAreNotPlanned…`
used to assert the measured/placeholder pairing by **counting text occurrences**
in `engine.go` — a proxy that had already broken once when a second legitimate
invalidation site appeared. It is now structural: `invalidate()` is the only
thing in the package that does either half, it does both, and the test fails if
any other file assigns either on its own.

## Verified clean

Worth recording so the next pass does not re-tread it: `routing` end-to-end
correctness (downmix coefficients, policy override asymmetry, Compile/duck
ordering, validation bounds); the DMCA guarantee that an excluded track used as a
duck *trigger* never reaches `[aout]` — executed and confirmed; `continuity.advance`
on all four cases; srtserver admission throughout (per-`PublisherKey` slots,
takeover re-checked under lock, passphrase both directions); `destinations.go`
spec-hash discipline; lock order `previewMu → selMu → mu` consistent at every site.
