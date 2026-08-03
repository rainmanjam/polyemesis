# Broadcasting from a file, on a schedule

You can go live from a recorded file, at a time you choose, with no encoder
attached and nothing running in OBS. This needs no features that are not already
here — it is `file://` pull ingest plus a schedule.

Proven end to end by
[`scripts/acceptance-playlist-phase0.sh`](../scripts/acceptance-playlist-phase0.sh),
which runs in CI.

---

## What you get

- A file that plays on a loop as the ingest, so the "source" is always live.
- Destinations that switch themselves on at a wall-clock time.
- Everything downstream unchanged: per-destination audio routing, renditions,
  recording, meters and reconnect all work exactly as they do with a real
  encoder, because by the time they see it the bytes are just bytes.

## Setting it up

**1. Get the file onto the server.** Either upload it from the browser —
**Library → Media → drop a file** — or place it in the data directory yourself
if you have a shell on the box. Pull sources are confined to that directory,
exactly as file destinations are:

```
<dataDir>/uploads/show-a1b2c3d4.ts     uploaded from the browser
<dataDir>/recordings/show.ts           placed by hand
```

An upload is stored under a name the **server** chooses, not the one you
supplied: the client's filename is a hint and is discarded, because it is the
one place a caller controls both the bytes and a path. The Library shows the
stored name and the exact pull URL to paste, so you never have to guess it.

Uploads live in their own directory rather than `recordings/` so that a
retention policy written about footage the server captured cannot delete a file
you deliberately put there. Every file in the Library carries an **origin** tag
— *uploaded*, *recorded* or *clip* — which is how you tell the two apart at a
glance.

MPEG-TS is the least surprising container here. An MP4 works, but its `moov`
atom means FFmpeg wants the whole file before it starts, which shows up as a
slow first frame.

**2. Point the ingest at it.** *Settings → Ingest → Mode: **Pull***, with the
pull URL from the Library:

```
file://uploads/show-a1b2c3d4.ts
```

The path is relative to the data directory. An absolute path, a `..`, or a
Windows drive letter is refused.

polyemesis adds `-stream_loop -1` so the file looks like a feed that never ends,
and `-re` so it plays at wall-clock speed. Without `-re` FFmpeg reads at disk
speed and buries the relay in an hour of stream in seconds.

**3. Add your destinations and leave them disabled.**

**4. Schedule the start.** *Automation → Schedules → New*, action **Start**, and
pick the destinations. `once` fires at a single instant; `daily` and `weekly`
take a wall-clock time and an IANA zone.

At the appointed time the schedule flips the same `enabled` bit you would have
clicked and asks for a reconcile — so a scheduled start is indistinguishable
from a manual one. There is exactly one way a destination comes up.

## What was measured

The acceptance suite proves, by measurement rather than assertion:

| | Result |
|---|---|
| The ingest goes live with **no encoder connected** | bytes arriving at the relay |
| The destination is **off** before its window, and does not fire early | checked 3 s in |
| The schedule **turns it on** at the window | — |
| The output carries the file's audio | 1200 Hz tone at −24.1 dBFS through a bandpass |
| The file **loops** | 19.4 s of output from a 6 s clip |
| The loop seam costs nothing downstream | **0 MPEG-TS continuity breaks** across ~3 loop points |

That last row is the interesting one. `-stream_loop` rewinds the file, and the
relay has counted continuity-counter breaks all along — so whether a rewind is
*visible* downstream was measurable for free. It is not.

## What this does not do

**It joins mid-file, not at frame 0.** The loop starts when the ingest starts,
which is when you save the setting — not when the schedule fires. A show
scheduled for 20:00 begins wherever the loop happens to be at 20:00.

Nothing in the current design can fix that. `scheduler.Actuator` has exactly
three methods and all of them are destination-level:

```go
type Actuator interface {
    SetDestinationEnabled(id int64, enabled bool) error
    ListDestinationIDs() ([]int64, error)
    Reconcile() error
}
```

A schedule cannot touch a source, restart an ingest, or seek. Starting at frame
0 needs the full playlist work — see
[the roadmap](roadmap/PLAYLIST-AND-COMPOSITING.md).

**One file, not a playlist.** There is no sequencing and no gapless transition
between items. Several files in order needs the concat demuxer and a
normalise-on-import step — the upload path is now in place, so that work no
longer has to build one first.

**It occupies the primary ingest.** This route *is* the primary — the file is
what the ingest pulls — so while it plays the primary hub has bytes on it and
failover reads the programme as live. Nothing here can change that, and nothing
should: you asked for the file to be the source.

What has changed is that this is no longer the only way to put a file on air.
Failover now has a **playlist** of its own, and that one does not touch the
ingest — see below.

## Filler while failover is watching: the failover playlist

If what you want is *programming that covers an outage* rather than *a file as
the source*, use the failover playlist instead. It plays a file into a hub of its
own, so the primary's hub stays empty and the primary is still watched the whole
time it plays. It ranks below both ingests and above the slate: an outage lands
on your programme rather than on a standby card, and a real encoder pre-empts it
the moment one arrives. You can pin it if you want it to win anyway.

| | this page's route (pull ingest) | the failover playlist |
|---|---|---|
| What the file is | the source | what runs when no encoder is delivering |
| Failover while it plays | reads the programme as live | fully live; the primary is watched throughout |
| It starts | when you save the setting | when nothing else can deliver, or when you pin it |
| Where | *Settings → Ingest → Pull* | `failover.playlist` in the settings API |

There is no form control for it yet — set `failover.playlist.enabled` and
`failover.playlist.items` through the settings API. `items` is a list, each
entry an `{"upload": "<name>"}` naming a file already sent through the
uploads page — a bare stored filename, never a path, confined to the uploads
directory exactly as `internal/uploads.Store.Resolve` enforces everywhere
else it is used. An item naming an upload that does not exist is refused with
a 400 when you save. Today the list may hold several entries but only the
first plays; sequencing arrives with the work in
[the roadmap](roadmap/PLAYLIST-AND-COMPOSITING.md), which also brings the
control itself.

### Saving is not the same as being ready

**A playlist does not go on air the moment you save it.** Saving queues one
`playlist.normalise` job per distinct upload, which transcodes it to the single
fixed profile every item has to share — 1080p30 H.264 in MPEG-TS, stereo AAC at
48 kHz — and the tier refuses to start until *every* item's job has finished.
Until then nothing is lost: the playlist is simply unavailable, the slate keeps
the stream, and the server logs `playlist not started; not every item has been
normalised yet`. When the last job lands the playlist becomes available on its
own; you do not have to save again.

Two things follow from where that work runs.

- **It yields to your live stream.** Normalisation is ordinary background work
  under the job governor, and the default policy is that heavy work does not
  compete with an ingest that is on air. An item you add mid-broadcast will
  normally start transcoding when the broadcast ends. Add filler *before* you go
  live, not during.
- **Watch it on the Jobs page.** The job is listed as *Playlist normalise*, with
  the reason it is not running if it is being held back. A failure lands there
  too — an audio-only upload, for instance, is refused permanently and says so,
  rather than being retried forever.

The derivative is written to `<dataDir>/playlist-media/` and is keyed on the
upload, so the same file used twice in a list is transcoded once. Deleting an
upload that a playlist names will take that playlist off air; remove the item as
well.

### The file must match your encoder's codec, and nothing checks

**This is the one way to get the failover playlist badly wrong, and normalising
does not yet solve it.** The normalised derivative described above is required
before a playlist may start, but it is not what plays: the tier still hands
FFmpeg your **original** upload. The derivative becomes the input when
sequencing arrives, which is the point of it — a list of files can only be
spliced if they share a profile. Until then it is a readiness token, and the
paragraphs below still apply in full to the file you uploaded.

The playlist is
copied, not re-encoded — `-c copy` from the file, and a copy hop into the
selector — so the file's codec, resolution, frame rate and pixel format reach
your destinations exactly as they were encoded. A destination that is also
copying hands them straight to the platform. If the file does not match what your
encoder sends, the switch onto it is a **mid-stream codec change**, and platforms
answer that by dropping the connection — which is the one thing the whole
failover tier exists to prevent. Point it at a 1080p HEVC file while your encoder
sends 720p H.264 and every platform connection breaks the moment the file goes
on air.

Match the file to your encoder: same codec, same resolution, same frame rate,
same pixel format. Re-encode it once, ahead of time, if it does not already
match.

Nothing validates this. Checking would mean probing your file when you save the
setting and comparing it against an ingest that may not be connected yet, which
is its own piece of work and is not built. Until it is, the constraint is yours
to hold. `scripts/acceptance-failover.sh` builds its filler clip to match its
publisher for exactly this reason, and says so in a comment.

The **slate** has no such constraint — it is synthesised at the *probed* geometry
of the departed ingest, which is precisely why it can never cause this.

## If you only want filler

For "something on screen when the encoder drops" and nothing more, you want the
**slate** — *Settings → Failover*. A slate is built at the probed geometry of the
departed ingest so a copying destination does not choke on the change, it needs
no file and no matching, and it yields the moment the real feed returns. Reach
for the failover playlist above when the filler should be *your* programming and
you are willing to match its codec. Filler and programme are different jobs.

---

## See also

- [CONFIGURATION.md](CONFIGURATION.md) — the ingest settings block
- [roadmap/PLAYLIST-AND-COMPOSITING.md](roadmap/PLAYLIST-AND-COMPOSITING.md) —
  what the full feature adds and what it costs
- [TESTING.md](TESTING.md) — running the suite yourself
