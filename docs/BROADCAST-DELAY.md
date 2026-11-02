# Broadcast delay

A destination can hold its output back by up to 15 minutes. Viewers on that
platform see the programme that much later than it happens. That gives you
time to cut something before it airs there. Every other destination is
unaffected.

Set it in the destination editor, under the advanced settings:
**Broadcast delay (seconds)**, from `0` (off) to `900`. Over the API it is
`delaySeconds` on the destination.

## What happens when a delayed destination starts

1. The destination starts **buffering**. Its card shows
   "Filling the delay: 12 of 30 s" with a progress bar, and the Stream Deck
   key shows "Buffering delay".
2. About two seconds before the first delayed second is due, its FFmpeg
   starts. Until then there is no FFmpeg for that destination, by design.
   This is not an outage: no alert fires and no `destination.down` is sent.
3. The delayed feed reaches the platform. The card shows "Delayed 30 s", and
   the Stream Deck key adds a "Delay 30s" line.

Video is still copied. The delay moves the same MPEG-TS the relay already
carries, so the delayed copy costs no encode.

**YouTube go-live waits for the delayed data.** polyemesis moves a YouTube
broadcast to live only when YouTube says the stream is active. On a delayed
destination that happens after the delay.

**A rejected stream key is found after the delay.** The FFmpeg starts late,
so a bad key on a 15-minute delay is reported 15 minutes after you go live.
It is then classified and held exactly as without a delay. Test a new key
with the delay at 0 first.

## Stopping

| You do | The delayed tail |
| --- | --- |
| **Stop** on one destination, or turn it off in the editor | **Airs.** The destination keeps publishing until everything buffered has played out, then stops. |
| **Stop now** on the card, or `POST /destinations/{id}/stop?now=true` | **Cut.** Nothing still buffered airs. |
| **Cut now** on a destination that is already airing its tail | **Cut.** |
| **Stop all** | **Cut**, on every destination. Stop all is the panic button. |
| **Delete** the destination | **Cut.** |
| Change any setting that restarts the destination, including the delay itself | **Cut**, and the new run fills its buffer from empty. The editor warns before you save. |
| Server shutdown | **Cut.** |

While a stopped destination airs its tail, its card says
**"Airing delayed buffer: 42 s left"** in red, with a **Cut now** button.
The Stream Deck key says "Airing delay tail". The destination counts as on
the air until the tail ends. The `destination.down` webhook, and the YouTube
broadcast end it triggers, wait until then.

A destination that was deleted while airing its tail still appears on the
dashboard until the tail ends, so you can see it and cut it.

If you start a destination again while its tail is still airing, the tail is
cut first, so only one copy ever publishes to the platform. This holds even
if the same destination now belongs to another programme.

## Dump

**Dump** drops the newest part of the buffer, so something that just happened
never reaches that platform, and the destination stays on the air. Viewers skip
forward a few seconds. The delay is shorter by what was dumped.

Press **Dump** in the delay panel on the destination's card. Choose
**Everything since the last dump point** or a number of seconds, and confirm.
The dialog shows the delay now and about what it will be after. The card then
says, for example, "Dumped 11.4 s — delay now 18.6 s." On a Stream Deck, hold the **Dump** key.
**Dump all** on the deck, or `POST /destinations/delay/dump-all`, dumps every
delayed destination of the programme at once.

- **Everything since the last dump point** is the default: it drops everything that
  arrived since the last dump, or since the destination started, down to the
  safety margin.
- **A number of seconds** drops at least that much of the newest programme.
- **The safety margin.** A dump never leaves less than 2 s plus one GOP of
  delay. If you ask for more than that allows, it dumps as much as it can and
  says so ("clamped").
- **It cuts at keyframes.** The cut is at the start of a keyframe in the buffer,
  and the stream resumes at the next keyframe that arrives. So a dump takes a
  little more than you asked for, up to one GOP (usually 2 s) more, and takes up
  to one GOP to finish. The platform receives every picture whole, with no
  corrupt frames, and its timestamps run on without a jump or a gap. The audio
  carries on the same way.
- **Captions** follow the shorter delay. Captions still queued for this
  destination are dropped, including those for the words that were dumped.
- **Not while filling or draining.** A destination that is still filling its
  delay, or that was stopped and is airing its tail, cannot be dumped. Use
  **Cut now** for a tail.
- **Every dump raises the `delay.dumped` alert** and the `destination.delay`
  event with reason `dumped`.

Dump needs H.264 or HEVC video with IDR keyframes. Every mainstream encoder sends
those by default. An encoder set to an open GOP (x264 `open-gop`, x265 by
default) sends keyframes a dump cannot cut at safely, and dump then says
there is nothing it can cut at.

### The delay stays shorter until the destination next starts

After a dump the destination keeps its shorter delay. The full delay comes back
the next time the destination starts and fills its buffer from empty, for
example at the start of the next show. The card shows both figures meanwhile:
"Delayed 18 of 30 s". polyemesis does not restore the delay by itself, and
restarting the destination to restore it is not a good idea: a restart takes
the platform off the air for the whole delay while the buffer fills again.

Why it cannot simply grow back. The video is copied, never re-encoded, so
there is no way to stretch the stream back to its old length. The only way to
win the seconds back on the air is to send the platform a hold, a slate or a
frozen picture, for that long. We tested both ways to do that without an
encoder per destination:

- **A pre-encoded slate.** It reaches the platform as a mid-stream change of the
  video's parameters. The platform set up its decoder from the encoder's own
  parameters when the stream started. In our test the programme was H.264 Main
  from a hardware encoder and the slate was H.264 High, and the RTMP stream
  header still said Main. FFmpeg decodes that without complaint. Whether each
  platform's ingest and players do cannot be checked without live streams, and
  a slate that matches every encoder exactly would need an encoder for each
  destination.
- **Repeating the last keyframe** as a freeze matches the codec by construction.
  It means rewriting H.264 picture headers, sending a variable frame rate that
  platforms flag, and making matching silent audio for copied-audio
  destinations.

Neither can be made safe for every platform without encoding. So the delay
stays shorter until the next start.

## When the source changes

- **Failover** between primary, backup and slate passes through the buffer.
  Viewers see the switch after the delay.
- **A rendition restart, or a rebuilt silence or failover tier,** keeps the
  buffer. Only the buffer's input moves to the new feed.
- **The encoder (OBS) disconnecting** stops nothing. The buffer plays out what
  it holds.

## Memory and disk

A 15-minute buffer of a 20 Mbit/s stream is 2.25 GB, so the buffer is
bounded:

- **RAM:** up to 64 MiB per delayed destination, about a minute at 8 Mbit/s,
  plus two 1 MiB working segments. A short delay at a normal bitrate never
  touches the disk.
- **Disk:** anything over the RAM budget spills to
  `<dataDir>/delay/<destination>-<n>/` in 1 MiB segments. Each segment is
  deleted as soon as it has been read back. The disk budget is the delay at
  50 Mbit/s: 5.6 GB for 15 minutes, 375 MB for one minute.
- **Disk guard:** a spill must leave the recording guard's free-space floor
  (`recording.minFreeGb`) on the data volume, or 2 GB when that is not set.

**When the buffer runs out of room, it drops new data and keeps the delay.**
It never sends old data early to make room, because a delay that silently got
shorter is worse than a gap. The platform sees a freeze where data was
dropped. The card shows a warning, `destination.delay` is sent with reason
`dropping` (at most once a minute), and the dropped bytes are counted in
`polyemesis_destination_delay_dropped_bytes_total`.

Spill files are the programme before it has aired. They are readable only by
the polyemesis user. They are deleted when the destination stops, and any
left behind by a crash are deleted when polyemesis next starts.

## Where else the delay shows

- **Status:** `delaySeconds` and a `delay` object on each destination in
  `GET /api/v1/status`. The `delay` object holds the phase, fill, buffered
  seconds, drain time left, RAM and disk bytes, and dropped bytes. It also holds
  `effectiveSeconds` (the delay after any dump), `dumpedSeconds` and
  `dumpable`.
- **Events:** `destination.delay` on `GET /api/v1/events`. See
  [EVENTS.md](EVENTS.md). `destination.level` carries `delaySeconds`: a level
  change reaches the process at once and viewers hear it after the delay.
- **Metrics:** `polyemesis_destination_delay_seconds`, `_delay_effective_seconds`, `_delay_buffer_fill`,
  `_delay_buffer_bytes{tier}`, `_delay_dropped_bytes_total` and
  `_delay_draining`. See [MONITORING.md](MONITORING.md).
- **MCP:** `get_status`, `list_destinations` and `get_destination` show the
  delay. `stop_destination` takes `now: true`. `dump_destination_delay` and
  `dump_all_delays` dump, with confirmation. See [MCP.md](MCP.md).
- **Stream Deck:** the **Stop destination** key has a **Cut the delayed tail**
  option, and a **Dump** key dumps one destination or every delayed one. Both
  need the 1.5 s hold. See the [Stream Deck README](../integrations/streamdeck/README.md).
- **API:** `POST /destinations/{id}/delay/dump` and
  `POST /destinations/delay/dump-all`. See [API.md](API.md).

## Live captions

Live captions follow the destination's `delaySeconds` to stay in step with the
picture. A YouTube destination that posts captions by HTTP POST is the
exception: YouTube applies its 30 s or 60 s delay, and polyemesis sends it
undelayed. See [CAPTIONS.md](CAPTIONS.md).
