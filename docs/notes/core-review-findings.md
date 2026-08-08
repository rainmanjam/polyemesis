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

## Tier 1 — audible, in the differentiator

| # | Where | Defect | Consequence |
|---|---|---|---|
| 1 | `routing/filtergraph.go:421` | `resolveNorm` assumes only *summing* creates clipping, so a single-track profile is left at `Normalization: off`. But `PanFilter` sums unbounded cells per output channel and `Validate` caps only the **per-cell** gain, never the row sum | A one-track matrix compiles to `pan=stereo\|c0=2*c0+2*c2+2*c4` — up to 6× full scale. Hard clipping, with NormAuto's protection silently absent. **Executed and confirmed** |
| 2 | `routing/filtergraph.go:330` | A saved matrix whose track has since lost channels drops the missing cells and never rescales the survivors; the warning names only the dropped channels | A 5.1 matrix against a now-stereo ingest yields `c0=0.4143*c0` — the destination goes **7.7 dB quiet**, a valid graph rather than an error, with nothing saying the level moved. **Executed and confirmed** |
| 3 | `routing/downmix.go:12` | `DownmixMatrix` keys only on channel *count* and never reads `Track.Layout`, which `ffmpeg/probe.go:112` does populate | A 2.1 track (FL FR LFE) compiles to `c0=0.5858*c0+0.4142*c2` — LFE folded into both legs, directly contradicting the file header's claim that LFE is excluded. **Executed and confirmed** |
| 4 | `routing/profile.go:30` | `MaxChannels = 8` is enforced only against matrix cells; simple mode passes the probed count straight into `DownmixMatrix`'s unbounded default branch | A >8-channel track routes through a guard that does nothing, and an **odd** wide count normalises the two rows by different sums — 9ch gives a permanent **1.94 dB stereo imbalance**. **Executed and confirmed** |

## Tier 2 — silently absent protections

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

## Tier 3 — performance, all quantified

| Where | Cost |
|---|---|
| `relay.go:347` | `fanout` allocates a fresh `[]*subscriber` **per datagram** — ~1,900 allocations/sec per hub at 20 Mbit/s, × hub count, on the hottest path in the process |
| `rtmpserver.go:696` | `pump` takes the server-wide `s.mu` per RTMP message and sends to every subscriber under it — one busy multitrack publisher degrades admission latency for all sources |
| `rtmpserver.go:827` | `awaitReady` polls `lookup` at 10 Hz, and production `lookup` does a full `ListSources()` + map rebuild — ~60 DB reads per held publisher per grace window |
| `engine.go:6361` | `onState` → `publishStatus` → `Status()` costs 3 DB queries plus a `routing.Compile` per non-running destination; a reconcile starting N destinations rebuilds the whole snapshot N times |
| `engine.go:6217` | `Status` calls the linear `destByID` twice per row with the same argument |
| `destinations.go:767` | `reconcileBackup` renders a full argv + SHA-256 above an early return that never reads it |

## Tier 4 — structure, behaviour-preserving

The strongest is the second: findings 9 and at least three already fixed are all
*"this reader picked the wrong one of these five fields."* Today that discipline
is fifty lines of comment.

1. **`sourceState` type** — move `source/probed/measured/measuredMode/sourceGen/videoInfo` behind `layoutForProcessBuilding() (Source, bool)` and `layoutForDisplay()`, plus `commitProbe/invalidateForMode/clearProbed`. Makes the wrong read **unspellable**.
2. **`selector.go`** — extract the selector/failover tier from `engine.go` (~2400–3700). Finding 6 is invisible only because both tiers live in one file; a boundary forces "who holds `selMu` across the silence swap" into a signature.
3. **`status.go`** — `Status`/`Renditions`/`SourceInfo`/`destByID` take `e.mu` five separate times and the DB three, so the snapshot is not internally consistent.
4. **`setup.go`** (rtmpserver) — the setup cache is a self-contained value type; extracting it lets the E-RTMP multitrack slotting be tested without a Server or a handshake.
5. **`ports.go`** (relay) — `PortAllocator` shares no state or concept with `Hub`.

## Verified clean

Worth recording so the next pass does not re-tread it: `routing` end-to-end
correctness (downmix coefficients, policy override asymmetry, Compile/duck
ordering, validation bounds); the DMCA guarantee that an excluded track used as a
duck *trigger* never reaches `[aout]` — executed and confirmed; `continuity.advance`
on all four cases; srtserver admission throughout (per-`PublisherKey` slots,
takeover re-checked under lock, passphrase both directions); `destinations.go`
spec-hash discipline; lock order `previewMu → selMu → mu` consistent at every site.
