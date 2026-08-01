# The playlist gets its own hub

**What:** a playlist plays into its own relay hub and its own supervised feed,
instead of occupying the primary ingest.

**Why:** today, going live from a file means the primary hub has bytes flowing,
so failover reads the programme as live and will not switch to a backup or a
slate. **A playing file silently disables the entire failover feature.** You
cannot have filler and a safety net at the same time. That is a broadcast-safety
regression rather than a missing feature, and it is the reason this is the first
sub-project rather than a later one.

This is sub-project **A** of roadmap item 5. It is independently shippable: it is
today's scheduled-broadcast feature minus the failover regression.

## What already exists, and what this adds

Roadmap item 4 shipped the *decision*. `chooseSource` ranks an ordered candidate
list, and a playlist is already the fourth candidate:

- ranked **below both ingests and above the slate** — a scheduled broadcast is a
  fallback for "nobody is streaming", not a pre-emption of somebody who is
- a live encoder **pre-empts it immediately**, not subject to the stability
  window. Verified across all 64 reachable cases: with the playlist on air and a
  live primary, the primary wins whether or not it is stable
- an operator can pin it, which the `pinned` path supports for free

What does not exist is the ability to *run* it. `playoutRunning` is hardcoded
`false`, so all 740 playlist rows in the decision table are unreachable, and
three functions in the feed machinery deliberately fail loudly rather than build
a feed for a kind they do not understand.

This sub-project makes the decision reachable. It changes no ranking.

## Scope

**In:**

- the rename `sourcePlayout` → `sourcePlaylist`
- a playlist settings block, its own hub, and its own supervised feed
- `playlistRunning` sampled from that hub
- `SwitchSource` accepting a `"playlist"` pin
- the three feed functions gaining their `sourcePlaylist` case

**Out, deliberately:**

- **Sequencing.** One file, looped, exactly as the pull source does today.
  Several files gapless is the concat demuxer and a normalise-on-import job,
  which is sub-project B.
- **Resume at an item boundary.** It needs items, and items do not exist until
  B. Putting a boundary rule here would be writing against a concept the code
  cannot yet express.
- **Scheduling a playlist.** Sub-project C.
- **Widening the wire format** beyond the settings block below.

## The rename, first

`sourcePlayout` is stored as `"playout"`, while every operator-facing reason
string says "playlist" and `internal/playout` is an entirely different feature —
the viewer-facing HLS origin that packages the relay for viewers.

Three names for two things is a collision the next reader pays for. It is free to
fix now, because nothing sets `playoutRunning` and `SwitchSource` rejects
`"playout"`, so **no user has ever persisted that value**. After this
sub-project ships it is a stored setting and a UI string, and changing it is a
migration.

`sourcePlayout` → `sourcePlaylist`, wire value `"playout"` → `"playlist"`. The
five reason strings already say "playlist" and do not change.

## Settings

Modelled on `SlateSettings`, which is the closest existing thing — an operator
supplies a path, the server confines it, and a tier plays it when the selector
says so.

```go
type PlaylistSettings struct {
	Enabled bool `json:"enabled"`
	// FilePath is relative to the data directory and confined there exactly as
	// SlateSettings.ImagePath and a file:// pull source are. An operator-supplied
	// path is the shape SECURITY.md's path confinement section exists to defend.
	FilePath string `json:"filePath"`
}
```

Deliberately smaller than `SlateSettings`. The slate carries encoder, preset,
colour and bitrate because it *synthesises* a picture. A playlist plays a file
that already has its own encoding, so it needs none of them. Adding knobs a
later sub-project might want is how a settings block becomes unreadable.

## The tier

The playlist mirrors the slate's shape, because the slate is the existing proof
that a synthesised source can be a first-class selector candidate.

```text
settings.Playlist{Enabled, FilePath}
        |
  playlistHub()          own relay.Hub, alongside sourceHub() and backupHub()
        |
  supervised FFmpeg      -stream_loop -1 -re -i <confined path>
        |
  playlistRunning        that hub is delivering
```

`-stream_loop -1 -re` is not new: it is exactly what `pullFile` already emits,
and the comment there gives the reason `-re` is not optional — without it FFmpeg
reads at disk speed and buries the relay in an hour of stream in seconds.

The three feed functions gain a `sourcePlaylist` case:

| function | today | after |
|---|---|---|
| `feedUpstreamSig` | fails loudly | the playlist's own signature |
| `startFeed` | fails loudly | starts the playlist feed |
| `downstreamFeedInput` | fails loudly | the playlist hub |

Those errors were written for this moment and name all three functions, so the
work is enumerated rather than discovered.

## Data flow

`sampleSources` reads the playlist hub's byte counter exactly as it reads the
primary's, and sets `playlistRunning` on the `sourceChoice`. Nothing else about
the decision changes.

That field is deliberately a plain boolean sampled by the caller, not a lookup
into playout state from inside the decision. `chooseSource` must stay pure and
cheap, because the golden table's claim to being exhaustive depends on its
inputs being enumerable.

## Failure behaviour

**A playlist that cannot start must degrade to the slate, never to nothing.**
The existing ranking gives this free: the playlist is only available when its
hub is delivering, so a feed that fails to start leaves the playlist unavailable
and the slate wins on the next sweep.

**A missing or unreadable file is a settings error, not a crash.** Path
confinement rejects it at validation, the same way the slate's image path is
rejected.

**The playlist never pre-empts a live encoder**, and this sub-project must not
make it possible to. The ranking is already correct and is frozen by the golden
table; any change to it here is a regression, not a feature.

## Testing

**The golden table regenerates twice, and both diffs are review artifacts.**
Once for the rename, once for `playlistRunning` becoming reachable. The
additive-invariance test must still show **zero** of the original 1024 decisions
moving — the rename changes a stored string, not a decision, and making a row
reachable does not change what it decides.

**The acceptance case that is the entire point:**

> filler playing, encoder drops, failover still works

That is impossible today — the playing file makes the primary read live, so the
switch never happens. It belongs in `acceptance-failover.sh`, which already
proves a destination rides a real switch without restarting.

| Case | Why it matters |
|---|---|
| Playlist plays into its own hub, primary hub stays empty | The whole point: failover can still see the primary is down |
| Encoder returns mid-playlist → primary pre-empts immediately | The broadcast-safety rule, now reachable rather than theoretical |
| Playlist feed fails to start → slate wins, not black | Degrades to filler, never to nothing |
| A `"playlist"` pin is honoured | The pin path exists but has never been reachable |
| Path outside the data directory is rejected | Same guarantee as the slate's image and `file://` pulls |

## What could go wrong

**The rename was checked rather than assumed, and it is free.** `sourceKind`
appears in no database type. The only persisted-looking use is
`Pinned sourceKind` on the status struct (`engine.go:4168`), which is an API
*response* field; `sel.pinned` is set only in memory by `SwitchSource` and never
written, so a pin does not even survive a restart. Combined with `SwitchSource`
rejecting `"playout"` today, no user can have stored that value anywhere. The
rename is a code-and-testdata change, not a migration.

It does change one API-visible string — `pinned` in the status response — but
nothing can currently produce `"playout"` there, so no client can be relying on
it.

**Two hubs where there was one.** The playlist hub is another thing that must be
closed on teardown and must not leak. The existing tiers are the model, and the
sweeper that bounds the segment directory is the precedent for what happens when
one is forgotten.

**`acceptance-failover` is the suite most likely to become flaky**, because this
adds a source to a suite whose whole job is switching between sources. It has
been measured at 13/13 with zero destination restarts; that is the number to
hold, and a regression in it matters more than a slow test.

**This makes 740 decision rows reachable for the first time.** They have been
frozen and reviewed, but frozen is not the same as exercised. The acceptance
cases above are what turn them from a table into behaviour.
