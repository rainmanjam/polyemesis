# #674 — a destination that starts with the ingest never characterises its audio

## The defect

A destination's FFmpeg probes the relay once, at startup, bounded by
`analyzeduration=15s` / `probesize=32MB` (`internal/ffmpeg/build.go:736-737`,
applied via `RelayInputArgs()`). If it starts while the relay carries video but
audio has not yet begun to flow — which is what happens when a destination and
the ingest come up together — the audio streams are never characterised.

FFmpeg characterises input streams exactly once. There is no re-probe. So the
process then runs **indefinitely**, reading 0 audio packets, with its
filtergraph never initialised and nothing written to the output, and it does
not exit — so nothing restarts it.

## The evidence (trace run, 2026-09-02)

`dest:4`, the RTMP destination created in acceptance step 4c:

```
01:57:24.120  Splitting the commandline.                        <- process starts
01:58:36.760  Format mpegts probed with size=2048 and score=50  <- 72.6s later, FIRST data
01:58:51.388  Consider increasing 'analyzeduration' (15000000)  <- x3, one per audio stream
01:59:08.224  Input stream #0:2 (audio): 0 packets read (0 bytes)
              [out#0/flv] Nothing was written into output file
```

Per-PID TS packets seen by that process across its probe window:

```
40  pid=100   video
12  pid=0     PAT
 1  pid=103   audio
 1  pid=102   audio
 1  pid=101   audio
```

One PES per audio PID in 15 seconds, against ~43 AAC frames/sec expected. The
audio PIDs are present in the PMT and present on the wire — they are simply far
too sparse during the ingest's own startup for FFmpeg to characterise them.

The 15.0s between the first probe and the three "Consider increasing
analyzeduration" lines is `analyzeduration` expiring exactly on schedule.

## Why it looked impossible for so long

`drive layout` is a fresh, short-lived `ffprobe` on its own relay
subscription. It starts when the stream is already flowing steadily, probes a
dense stream, and correctly reports `ch=2 aac stereo` — at the very moment a
long-lived destination on the same relay reads 0 audio packets. Both are true.
They probed at different times.

Every measurement that said "the bytes are fine" was right. The bytes were
always fine. What was wrong was *when* one particular reader looked at them.

## The natural experiment already in the suite

Step 4c creates the RTMP destination BEFORE the E-RTMP ingest comes up
(`scripts/acceptance-docker.sh`, dest created ~line 485, ingest probed ~line
504). Step 4d reuses that same destination row — it is only deleted at the end
of 4d — after the ingest switch restarted the engine, so it re-probes an
already-flowing stream.

Same row, same code, same compiled graph, opposite outcome. The only variable
is when the destination's FFmpeg probes relative to audio flowing.

## What is NOT the cause

Ruled out by measurement over the preceding investigation:

- Relay fan-out, `fifo_size`, kernel drops (0 drops at the destination socket)
- PID assignment (identical head and tail: 0x100 video, 0x101-0x103 audio)
- The routing graph or track selection (`0:a:1` for "Track 2" is correct)
- The bytes themselves (same bytes work as a file and replayed over UDP)
- The missing `aformat=channel_layouts=` on destination legs. The filter error
  is at EOF, downstream of 0 audio packets, not the cause of them.

## Rungs

- **Control** — do not start a destination until the relay carries audio; or
  make FFmpeg's probe unable to conclude before audio is present.
- **Warning** — exit non-zero when a mapped audio stream reports 0 packets
  after N seconds, so the supervisor restarts and re-probes a flowing stream.
  Self-healing, and cheap.
- **Detection** — the `firstMediaLogger` added for #675 already knows a
  destination never published; it can alarm rather than only log.

The current state is rung 0: silent, permanent, and self-inflicted at startup.

## What was done

**Warning rung — `engine.guardSilentPublish`** (`internal/engine/destinations.go`).
A watchdog per destination. It restarts a destination that is running, being
fed, and publishing nothing. The discriminator is `hub.RxBytes()`: "published
nothing" on its own is also true of every destination on an idle install, and
restarting those would burn `MaxRestarts` and then give up PERMANENTLY on a
system whose only fault was that nobody was streaming — strictly worse than the
bug. Bounded at `maxSilentRestarts` (3) so a permanent fault cannot hide behind
a process that always looks freshly started.

Five tests, each mutation-tested: the restart fires, an idle destination is left
alone, a publishing destination is never restarted, the count is bounded, and a
retired process stands the watchdog down.

**Control-adjacent — `ffmpeg.relayProbeWindow` 15s -> 45s.**
The window was sized for a long GOP, which is the VIDEO worst case; the binding
constraint turned out to be audio sparsity in a feed's first seconds. It is a
ceiling, not a wait — probing ends the moment parameters are known — so this is
free on a healthy stream and only spends where the old value was failing.

`silentPublishBudget` is now DERIVED from `ffmpeg.RelayProbeWindow` rather than
written beside it, with `TestTheSilentPublishBudgetOutlastsTheProbeWindow`
pinning the relationship: a watchdog budget at or below the probe window would
restart precisely the slow starts it exists to rescue. Raising the window from
15s to 45s would have collided with the hardcoded 45s budget.

## The control gate that was REJECTED, and why

Refusing to start a destination while `hub.RxBytes() == 0` was implemented and
reverted. It breaks failover, and the suite says so in as many words:

	probed=false only means nothing is arriving right now; e.source still holds
	the real layout, and a destination added during a failover has to be able to
	start and carry the slate
	-- TestAnIdleButAlreadyMeasuredLayoutStillPlansDestinations

A destination MUST be able to start against a relay that is not receiving, so it
can carry the slate. Blocking that trades #674 for a worse outage. The tests are
correct and were left alone.

So the true control rung — a destination that cannot start before its input is
characterised — is not reachable without changing that deliberate failover
behaviour. What is reachable is a probe window that a feed's startup cannot
exhaust, plus a bounded self-healing restart underneath it.

---

# GROUND TRUTH (2026-09-03) — everything above this line was wrong

Captured `relay.capture` (exactly the bytes `fanout()` forwards to every
subscriber) scoped to the 4c window, copied to the host, and counted the raw TS
PIDs. No demuxer, no probing, no inference.

	PID 0x100 (video):  33,612 packets
	PID 0x103 (audio):     350
	PID 0x101 (audio):     326
	PID 0x102 (audio):     320

Split into ten equal slices of the window:

	slice |  video | a:101 | a:102 | a:103
	  0-7 | ~3,460 |     0 |     0 |     0
	    8 |  3,193 |    88 |    86 |    94
	    9 |  2,735 |   238 |   234 |   256

**For the entire E-RTMP portion of 4c the relay carries ZERO audio.** Not
sparse - none. Audio appears only in the last 20%, which is where the suite
switches the ingest back to SRT.

## What this retires

- **Probe-window / analyzeduration.** Nothing to probe. Widening it was wrong
  and regressed 4d.
- **Interleave buffering in the ingest muxer.** Real mechanism, wrong bug: it
  cannot explain 80% of a window with no audio at all.
- **`aformat=channel_layouts=` on the destination legs.** There is no audio to
  pin a layout onto.
- **"Audio is sparse at a feed's head."** It is binary: a destination reads
  ~1,050 audio packets or exactly 0.
- **The per-PID counts that started this** (1 PES per audio PID in the probe
  window vs 37 cumulative). Real numbers, but they measured the dump's `tail`
  window, not the stream.

## What it leaves

E-RTMP audio WORKS in 4b (correct per-track dBFS, minutes earlier, same ingest)
and is entirely absent in 4c. Between them: the RTMP sink container starts and a
new destination is created, which triggers an engine reconcile.

So the question is no longer "why is audio malformed" but **"what stops audio
reaching the relay part-way through an E-RTMP session"**.

Leading candidate: the ingest re-subscribes to the RTMP server mid-broadcast and
does not receive the per-track AAC sequence headers it needs. `rtmpserver`
already has a setup cache for exactly this (`internal/rtmpserver/setup.go`:
"what a subscriber joining mid-stream needs replayed"), and it does key
multitrack per track (`audio-mt-%d-%s`), so if this is the mechanism the fault
is in WHEN it is reset (`resetSetup`) rather than in how it is keyed.

Not yet established: whether the ingest actually restarts at 4c. That is the
next measurement, and it is a log question, not a rig question.
