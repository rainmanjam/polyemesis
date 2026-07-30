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

**It occupies the primary ingest.** While a file is playing, the primary hub has
bytes flowing, so failover sees the programme as live and will not switch to a
backup or a slate. If you need failover *and* filler, that interaction is part of
the full playlist design and is not solved here.

## If you only want filler

For "something on screen when the encoder drops", you want the **slate**, not
this — *Settings → Failover*. A slate is built at the probed geometry of the
departed ingest so a copying destination does not choke on the change, and it
yields the moment the real feed returns. Filler and programme are different jobs.

---

## See also

- [CONFIGURATION.md](CONFIGURATION.md) — the ingest settings block
- [roadmap/PLAYLIST-AND-COMPOSITING.md](roadmap/PLAYLIST-AND-COMPOSITING.md) —
  what the full feature adds and what it costs
- [TESTING.md](TESTING.md) — running the suite yourself
