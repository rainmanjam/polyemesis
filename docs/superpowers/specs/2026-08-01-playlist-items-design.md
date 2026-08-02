# A playlist is a list of normalised items

**What:** a playlist becomes an ordered list of uploads, and each upload gains a
normalised derivative built to one fixed profile.

**Why:** the concat demuxer — the only way to sequence files gaplessly in one
process — requires every item to share codec, timebase, resolution and channel
layout, or it errors or produces garbage. Normalising on import is what makes
that guarantee hold by construction rather than by asking the operator to
produce matching files by hand, which is the wall this feature exists to remove.

This is sub-project **B1** of roadmap item 5. B2 builds the concat sequencing
and item-boundary resume on top of it.

## What already exists

Sub-project A (#64) gave the playlist its own relay hub, so a file on air no
longer makes the primary read live and disable failover. It ships
`PlaylistSettings{Enabled bool, FilePath string}` — one file, looped.

A documented limitation it could not fix:

> The file's codec parameters reach destinations unchanged, because
> `playlistFeedArgs` is `-c copy` and the selector feed is another copy hop.
> Point it at a 1080p HEVC file and every platform connection breaks on the
> switch.

B1 closes that. A normalised item matches by construction, so the hazard stops
being a documented precondition and becomes a property of the data.

## Scope

**In:**

- `PlaylistSettings.Items []PlaylistItem`, ordered, replacing `FilePath`
- a `KindPlaylistItem` job that produces one normalised derivative per upload
- the fixed profile that job targets
- resolving an item to a playable path, and what happens when it is not ready

**Out, deliberately:**

- **Concat sequencing.** B1 still plays the first item only. Sequencing is B2.
- **Item-boundary resume.** It needs sequencing to have boundaries to resume at.
- **A playlists table or CRUD routes.** One playlist, on the settings block.
  Several named playlists is a later decision and needs a reason beyond "we
  might".
- **A UI control.** It lands with B2, when there is a list worth editing rather
  than a single reference. Both keys stay in the UI-nameability skip list until
  then.

## The item model

```go
// PlaylistItem is one entry in the playlist, in play order.
type PlaylistItem struct {
	// UploadID names a stored upload. NOT a path, and that distinction is a
	// security boundary rather than a convenience -- see below.
	UploadID string `json:"uploadId"`
}
```

`PlaylistSettings` becomes:

```go
type PlaylistSettings struct {
	Enabled bool           `json:"enabled"`
	Items   []PlaylistItem `json:"items"`
}
```

### Why an upload reference and not a path

The concat demuxer refuses absolute paths in its list unless given `-safe 0`.
`internal/clipper/args.go` already passes `-safe 0`, and its comment says
exactly why that is defensible there:

> They are paths this process chose, not paths a request supplied, so there is
> nothing here for the check to protect.

**That argument does not transfer to an operator-supplied path, and it is the
whole reason items reference uploads.** `internal/uploads` throws the client's
filename away — `SafeName` keeps only a sanitised stem and extension and always
appends a crypto/rand suffix — so a stored upload's path is chosen by this
process, exactly as a clip's is. Referencing uploads keeps `-safe 0` honest.

A free-text path field would disable the demuxer's own protection on
operator-controlled input, which is the one thing that check exists to stop.

## Normalisation

A new job kind, modelled on `media.KindProxy`:

```text
upload  ->  KindPlaylistItem  ->  normalised derivative (fixed profile)
```

`internal/jobs` already supplies the queue, the governor and the concurrency
limit; `media.KindProxy` supplies the shape, down to `ProxyLimit = 1` and the
deliberate decision not to consult the hardware probe. A normalisation that
raced the live encoders for a GPU would trade a stream for a file.

**The profile is fixed, not derived.** The alternative — matching whatever the
live encoder currently produces — was rejected: the target would move every time
an encoder setting changed, every item would need re-normalising, and a playlist
could go stale against its own programme without anything saying so.

Fixed means deterministic, cacheable per upload, and reusable across playlists.
It also means the switch from playlist to encoder is a codec change the
destinations must tolerate — which is what the slate already does, and what
`acceptance-failover` already measures.

The profile, as constants rather than settings — the same choice
`media.ProxyEncoder` makes, so it is revisable in one place without becoming a
knob nobody knows how to set:

| | |
|---|---|
| container | MPEG-TS, matching what the tier already publishes |
| video | `libx264`, `yuv420p`, 1920×1080, 30 fps, closed GOP |
| audio | AAC, stereo, 48 kHz |

1080p30 rather than the source's geometry because every item must agree and
something has to decide; 1080p is the ceiling most destinations accept and the
one a mismatched item is most likely to already be. **These numbers are a
starting point, not a measurement** — see the risks below.

**One derivative per upload, not per playlist entry.** The same upload used
twice is normalised once.

## When an item is not ready

Normalisation is asynchronous, so an item can be referenced before its
derivative exists. Three states, and the rule for each:

| State | The playlist is |
|---|---|
| every item normalised | available |
| any item still queued or running | **not** available |
| any item failed | **not** available, and the failure is visible |

**Not-available means the selector does not offer the playlist**, which the
existing ranking already handles: an unavailable candidate loses to the slate,
and the slate exists so an operator never sees nothing.

This is the same rule sub-project A established for a tier that is running but
delivering nothing — a candidate is offered only when it would actually deliver.
Offering a playlist whose first item is still transcoding would put a source on
air that cannot play.

## Failure behaviour

**A failed normalisation must be visible.** A playlist that silently never
becomes available is the worst outcome — the operator sees filler that never
starts and has nothing to read. The job system already reports failures; this
must surface through it rather than only in a log line.

**A missing upload is a settings error.** An item naming an upload that no
longer exists is caught at validation, the same way a confinement failure is.

**Disk cost is real and must be bounded.** Derivatives are additional copies of
operator-supplied media. The uploads store already reports free bytes; a
normalisation that would exhaust the disk must fail cleanly rather than filling
it — the same concern `internal/recording` and `internal/uploads` already carry.

## Testing

| Case | Why it matters |
|---|---|
| A normalised derivative matches the fixed profile | The whole guarantee. Probe the output; do not trust the argv |
| Two playlists sharing an upload normalise it once | The cache is the point of keying on the upload |
| A playlist with an unnormalised item is NOT offered | Prevents putting a source on air that cannot play |
| A failed normalisation leaves the playlist unavailable and says so | The silent-never-starts failure |
| An item naming a missing upload fails validation | Same class as A's confinement check |
| `-safe 0` is only ever given server-chosen paths | The security boundary, asserted rather than assumed |

The last one deserves a real test rather than a comment: it is the assumption
that makes the demuxer flag safe, and assumptions that live only in comments are
how this repo has been bitten before.

## What could go wrong

**The fixed profile is a guess until it is measured.** Geometry and bitrate that
suit a talking-head clip may not suit a 4K trailer. The profile is a starting
point that should be revisited once real files have been through it — and it is
in settings-adjacent code precisely so it can be.

**Normalisation competes for CPU with live encoding.** `ProxyLimit = 1` and the
existing governor are the mitigation, and the decision not to consult the
hardware probe is deliberate for the same reason. It is still true that a box
running at its limit will feel a transcode.

**The item list has no UI until B2.** Both settings keys stay in the
UI-nameability skip list, and the reason recorded there must say B2 rather than
naming a task that does not exist — the same false promise this repo already
corrected once.

**Migrating `FilePath` to `Items`.** A deployment that set `FilePath` under
sub-project A has a value that must not silently vanish. Either migrate it into
a single-item list or reject it loudly at load; do not drop it.
