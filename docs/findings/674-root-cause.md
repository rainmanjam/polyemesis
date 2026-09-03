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

---

# CORRECTION (2026-09-03, later) — the "GROUND TRUTH" section above is ALSO wrong

That section claimed the relay carries zero audio for the whole E-RTMP window of
4c. It does not. That reading came from a capture zeroed with `: >`, which left
the relay's open fd at its old offset and produced a 9 MB SPARSE HOLE, and from
slicing the remainder by packet count across a ~110s window in which the
publisher is only alive for 40s. Both artefacts, neither the product.

Captured again, bracketed by BYTE OFFSET between the publisher starting and the
destinations being stopped -- so the range is exactly what the relay carried
while E-RTMP was on air:

	13,393,120 bytes
	1,aac,...,0x101, duration 39.988011, 2318 packets
	2,aac,...,0x102, duration 39.988011, 2318 packets
	3,aac,...,0x103, duration 39.988011, 2318 packets

Three audio streams, the expected PIDs, 39.99s of audio against a 40s publisher.
**The relay carries all three tracks.**

## So the original diagnosis was right

Every component is now individually cleared by measurement:

	RTMP server      drop counter reads a true 0 (once it could fire)
	setup cache      PMT declares all three AAC streams
	ingest -> file   3 streams at 48000,2 (mutation-tested)
	ingest -> UDP    789,600 bytes, 3 streams, SHIPPED argv from /proc
	relay hub        2048-byte reads vs 1316-byte datagrams; capture proves audio
	destinations     never had audio to work with

And the ordering in 4c is the whole story:

	~line 485   the RTMP destination is created and starts
	 line 507   the publisher starts, ~20s LATER
	            relay carries audio for the next 40s
	            the destination reads 0 audio packets for its entire life

A destination's FFmpeg characterises its input's streams ONCE, bounded by
analyzeduration, and never re-probes. Starting it into a relay that carries
video and no audio yet means no audio stream is ever resolved -- permanently,
for that process, however much audio arrives afterwards.

## Why widening analyzeduration did not fix it

Because the gap is not a few seconds of sparsity, it is ~20s of NO audio, and
then the destination is already committed. 15s -> 45s only moved the deadline
and regressed 4d by tripling every destination's startup wait.

## What the fix has to do

Not "wait longer". Either do not start a destination until the relay carries
audio, or restart one whose input declares audio streams and has read 0 audio
packets while the relay is known to be carrying them.

The first was tried and reverted: refusing to start on RxBytes()==0 breaks
failover, and the suite says so -- "a destination added during a failover has to
be able to start and carry the slate".

The second was tried as guardSilentPublish and removed: "receiving but
publishing nothing" is ALSO the steady state of a publisher that does not match
the profile, and restarting there splits the recording. The trigger has to be
narrower than that -- audio streams DECLARED but zero packets READ, which is
specific to this fault and false for both healthy states.

---

# FALSIFIED (2026-09-03, 23:40) — the start-ordering theory does not survive the timing

The correction above said a destination starts before its ingest carries audio,
probes an audio-less relay, and is stuck for life. The timing refutes it.

	06:35:29.956  RTMP sink starts
	06:35:30.326  destination R-track2 starts
	~06:35:34     E-RTMP publisher starts (script sleeps 4s), lives 40s
	06:36:47.689  the destination's probe finally gives up:
	              "Consider increasing 'analyzeduration' (15000000)" x3

That probe ran for **77 seconds**, not 15 -- analyzeduration bounds STREAM time,
not wall clock, and audio streams that yield no packets never advance it. So the
probe window covered the publisher's ENTIRE 40 seconds, with audio provably on
the relay throughout (13,398,760 bytes, 2,319 packets per audio PID, 39.99s).

**The destination read the bytes and could not characterise the audio anyway.**
It is not a race, and it is not start ordering.

## What that leaves, precisely

The same bytes are parseable one way and not the other:

	ffprobe, on the captured relay FILE      -> 3 audio streams, 48000/2, fine
	the destination, on the live UDP stream  -> 0 audio packets, 0 frames

Both read what fanout() forwards. The difference is a complete seekable file
versus a subscriber joining a live stream mid-flow. That is now the whole
question, and it is the one measurement not yet made: capture what a
DESTINATION's own subscriber port receives, rather than what the hub reads.

## Two repairs were built on the falsified theory

Both are kept, because both are correct detections with mutation-tested guards,
and neither fires on a healthy state. NEITHER FIXES 4c:

- `reprobeOnUncharacterisedAudio` restarts on the demuxer's own "could not find
  codec parameters ... (Audio". Correct, but it fires 77s in -- after the window
  it needs has closed. Measured: fired 06:26:08 for a destination started
  06:24:43, against a 40s publisher.
- `reprobeDestinationsThatNeverPublished` fires when the ingest layout is
  measured. Correct discriminator ("has it ever published" is false for #674 and
  true for both a failover switch and a mismatched publisher), but the layout
  does not CHANGE between 4b and 4c, so it fired once at 06:36:47.664 -- 32ms
  before the sink gave up.

Kept as detection, not sold as a fix. 4c is still red: 48 passed, 2 failed.

---

# THE ANOMALY, on a clean baseline (2026-09-03, 00:25)

Both #674 repairs DISABLED, so nothing of mine is in the measurement:

	07:20:09.114  engine: "destination starting" dest=R-track2
	07:21:22.109  the child's first decode        <-- 73 SECONDS LATER
	07:21:36.735  max_analyze_duration 15000000 reached ... st:0
	              "Could not find codec parameters" x3 (audio)

The 4c publisher runs ~07:20:13-07:20:53. So the destination receives its FIRST
PACKET about 29 seconds AFTER the publish ended. It never sees the live stream
at all, and `st:0` shows the probe timed out on VIDEO, having had no audio to
measure. The same 73s gap appeared with the repairs enabled, so it is not mine.

## Ruled out, by reading the code and the run

	subscription order   hub.Subscribe() is line 808, proc.Start() line 950 --
	                     the port is registered BEFORE the child starts
	fan-out registration Subscribe() calls rebuildTargets(), which republishes
	                     the slice fanout() walks. New subscribers are visible.
	stagger              Destinations.StaggerMS defaults to 0, and the cap is
	                     5000ms. It cannot produce 73 seconds.
	ingest backoff       MinBackoff 500ms / MaxBackoff 5s (selector.go:1872).
	                     It cannot produce a 40-second late join either.
	two hubs             relaycap.49356.ts is the hub's INPUT port; 21018 is the
	                     destination's SUBSCRIBER port. One hub, not two.

## The contradiction to resolve

The hub receives a 40-second stream with audio on all three PIDs -- measured
four times, in four separate runs. The destination is subscribed to that hub
before its child starts. And the child receives nothing for 73 seconds.

Those cannot all be true. The next measurement is the one that says which is
false: log the instant the child process actually EXECs (not when the engine
decides to start it), alongside the hub's RxBytes and subscriber list at that
instant. Every timing conclusion in this investigation has been drawn from
"destination starting", which is logged after proc.Start() returns and says
nothing about when the child reached its first read.

---

# CORRECTED AGAIN (00:52) — the child starts instantly, and it DOES exit

`child exec`, logged in supervisor.runOnce immediately after cmd.Start(),
settles two things that were assumed for this entire investigation:

	07:48:21.055  engine: "destination starting" dest=R-track2
	07:48:21.056  child exec dest:4 pid=1379     <-- ONE MILLISECOND later
	07:49:38.573  child exec dest:4 pid=1444     <-- 77s later: it EXITED
	07:49:55.521  child exec dest:4 pid=1527

## Two load-bearing claims were false

1. **"The child starts late."** It does not. It execs 1ms after the engine
   decides to. The 73-second gap between the engine's log and the child's first
   decode is the first incarnation's own failing probe, not a scheduling delay.
   StartDelay, stagger and backoff were never involved.

2. **"It runs indefinitely without exiting, so AutoRestart never sees it."**
   FALSE, and it was load-bearing for BOTH repairs built in this session. The
   process exits after ~77s and the supervisor restarts it; dest:4 execs three
   times, dest:1-3 five times each. A repair designed to restart a process that
   is already restarting itself cannot help.

That first claim came from one run's exit statistics and was never checked.
Every timing conclusion drawn from "destination starting" inherited it, and it
falsely killed the ordering hypothesis earlier tonight.

## What the run also shows

	ingest      execs ONCE for the whole run
	dest:1-3    five execs each
	dest:4      three execs
	loudness    three execs each

The ingest is a single long-lived process spanning 4b and 4c, so the relay is
ONE continuous timeline -- which is why a destination joining in 4c sees
`start: 39.988011` rather than 0. It is not joining a fresh stream; it is
joining a stream that has been running for forty seconds.

## Where this leaves it

The first destination child starts at the right instant, with the publisher
arriving ~4s later, and still fails to characterise audio in 77 seconds. The
same configuration reproduced in isolation -- reader first, into silence,
publisher afterwards, destination's own filtergraph and encoder -- PASSES in 20
seconds (TestAReaderStartedBeforeAnyDataStillPublishesItsAudio).

The difference between the two is no longer the media path, which is cleared end
to end, nor start ordering, nor the process lifecycle. What the rig has that the
harness does not: nine concurrent subscribers, and a relay whose timeline has
been running for forty seconds before the destination joins. The next
reproduction should carry BOTH.

---

# THE ELIMINATION IS COMPLETE, AND THE FIX IS NOT WRITTEN (00:55)

Twelve reproductions now exist, every one on the shipped ffmpeg 8.1.2, every one
PASSING, every one built because it was expected to fail:

	ingest -> file                          3 streams, 48000/2
	ingest -> UDP, shipped argv from /proc  789,600 bytes, 3 streams
	reader joining mid-stream               3 streams
	reader started before ANY data          1 encoded stream
	destination shape (filter + encoder)    1 encoded stream
	hub hop, byte for byte                  400/400 datagrams identical
	full chain ingest -> hub -> destination 1 stream, dropped=0 tsLost=0
	reader joining BETWEEN two publishes    1 stream, 897,700 bytes

And in the rig, measured directly:

	RTMP server drop counter    a true 0, once it could fire
	relay content               40s of audio on all three PIDs, four runs
	hub stats, live             rx=20588 tx=115146 dropped=0 loss=0.031%
	child exec                  1ms after the engine decides
	PMT                         all three AAC streams declared throughout

The acceptance rig still fails: 48 passed, 2 failed.

## What is left

Two differences remain between every passing reproduction and the failing rig:

1. **Nine concurrent subscribers** (dest:1-4, loudness:1-4, meters). Every
   reproduction has one.
2. **The engine itself** -- reconcile, restarts, and the destination lifecycle,
   as opposed to hand-started processes.

Everything else is eliminated by measurement rather than argument.

## What was learned that outlives this bug

Ten instrument defects, each of which produced a result that looked like a
finding: a drop counter that could not fire at its most useful setting; ffprobe
stderr discarded twice; counting matches inside a `tail` window and reporting it
as a trend; a gate that printed OK without gating; SIGKILL losing an output
buffer; `-t` that cannot fire without input; a bind race from closing a reserved
socket; `%%` in awk; a sparse-file hole from `: >` truncation; and a format
string with two verbs and one argument.

Every one of them produced a 0 or an empty section, and a 0 is indistinguishable
from the finding being sought. Three conclusions were published from such
artefacts and later retracted.

---

# ROOT CAUSE LOCATED (01:05) — the hub does not deliver to the destination for 73s

`relay first delivery`, logged once per subscriber on the first successful
WriteToUDP, is the number this investigation needed from the start:

	08:01:04.877  engine: "destination starting" dest=R-track2
	08:01:04.878  child exec dest:4 pid=1377              <-- 1ms later
	08:02:17.702  relay FIRST DELIVERY dest:4 port=21018  <-- 72.8 SECONDS LATER
	08:02:22.297  child exec dest:4 pid=1439              <-- the first child had died

The destination is subscribed (hub.Subscribe runs at destinations.go:808, before
proc.Start() at :950) and its child is alive from 08:01:04.878. The hub sends it
NOTHING for 72.8 seconds. It gets its first byte about five seconds before the
process exits, having failed to characterise audio it was never sent.

**The destination is starved by the relay, not by FFmpeg.** That is why all
twelve isolated reproductions pass: every one of them had a hub that delivered.

## The likely mechanism, not yet confirmed

Subscribers appear in generations, each on a fresh port range:

	21000-21007   probe, meters, dest:1-3, loudness:1-3
	21009-21016   the same names again
	21017-21019   probe, dest:4, loudness:4

A sibling on the ADJACENT port of the same generation -- probe on 21017 -- got
its first delivery at 08:00:56, eighty-one seconds before dest:4 on 21018. So
the hub that fed 21017 was live and fanning out while 21018 received nothing.

That points at a hub-generation mismatch: the destination subscribes to one hub
while the ingest feeds another, and only a later reconcile re-points it. The
engine swaps hubs on an ingest-mode change, and destinations.go says a
destination reads "the ingest hub for a passthrough destination, its rendition's
own hub otherwise".

## What this retires

Everything about probing, analyzeduration, mid-stream joins, ordering, the
interleaver, channel layouts, and FFmpeg 8.1.2 generally. The bytes never
arrived. A reader cannot characterise audio it is not sent.

---

# THE HUB THE INGEST FEEDS IS NOT THE HUB THE DESTINATIONS READ (01:18)

`relay first delivery` per subscriber, with the fix for the hub-SWAP case
already in place and making no difference:

	08:14:19.392  child exec dest:4 pid=1377
	08:15:32.142  relay first delivery  dest:1, dest:2, dest:3, dest:4,
	                                    loudness:2, loudness:3, loudness:4
	                                    -- ALL OF THEM, the same millisecond

dest:4 was never singled out. EVERY subscriber on that hub is starved for the
same 73 seconds and they all receive their first byte together. A hub delivers
to all its targets in one fanout loop, so simultaneous first delivery means the
hub itself RECEIVED nothing until 08:15:32 -- 29 seconds after the 4c publish
ended.

And the relay capture, taken in the same runs, shows 40 seconds of audio on all
three PIDs DURING that publish.

Both are only true if there are two hubs: **the ingest writes to one, the
destinations read another.** upstreamHub() returns e.selectorHub() when the
selector has one and e.sourceHub() otherwise; the ingest writes to whichever
hub.InputURL() the feed was built with. During an ingest-mode switch those can
be different objects, and nothing reconciles them.

## What this makes of the earlier work

- The hub-swap fix (keepDestination now requires a running destination to be on
  the hub it would be given NOW) is a REAL latent defect and is kept: destSpec
  blanks the relay URL, so a swapped hub previously left a destination alive on
  a port nothing writes to. It is not this bug.
- Twelve media-path reproductions pass because they all build ONE hub and give
  it to both sides. The rig's two-hub state cannot occur in them.
- Every reader-side theory is dead. The readers were never sent anything.

## The remaining question, precisely

Which hub does the selector's feed write to during the 4c mode switch, and which
does upstreamHub return at the moment destinations are (re)started? They must be
the same object. That is a bounded question in selector.go and destinations.go.

---

# WHERE IT ACTUALLY STANDS (01:37)

The fault is localised to ONE fact, reproduced in every run:

	08:33:12.758  child exec dest:4                     (subscribed, hub receiving)
	08:34:25.622  relay first delivery dest:4 port=21018  <-- 72.9s LATER
	08:34:57.328  relay subscriber removed dest:4         (AFTER first delivery)

The hub, the capture and every subscription are on the SAME port (49108 this
run, 53950 the last), and the capture holds 13.4 MB with 40s of audio across
the publish. So a hub that is receiving does not deliver to a subscribed
destination for ~73 seconds, and the destination dies having been sent nothing.

## Ruled out, each by measurement

	the media path        12 reproductions, all passing, all on ffmpeg 8.1.2
	ordering              reader-before-data passes
	concurrency           nine subscribers passes
	the process lifecycle child execs 1ms after the engine decides
	a hub MISMATCH        capture and subscriptions share one hub port
	unsubscribe-while-running  the removal is AFTER first delivery, not before
	the fan-out itself    byte-identical, dropped=0, loss=0.031%

## The one open question

Why is a subscribed target not written to while its hub is receiving? Either the
subscription is absent from `targets` during that window, or the hub is not
receiving during it and the capture's 13.4 MB was written outside the bracket.

Those are distinguishable with one more line: log `len(*targets)` and the hub's
rx counter together, on a ticker, so "receiving with N targets" is visible
directly rather than inferred from two separate logs.

## A caution for whoever picks this up

The acceptance dump prints TAILS. Twice tonight a conclusion was drawn from a
truncated section and was wrong -- once counting matches inside `tail` and
reporting the count as a trend, once reading an absent log line as proof a
subscription never happened. Before believing any absence in that dump, widen
the grep and confirm the line is absent from the whole log rather than from the
last N lines of it.

---

# THE HUB IS INPUT-STARVED (01:47) — not the fan-out, not the subscribers

`relay fanout state` logs rxPackets and len(targets) together, every 500
datagrams, so "receiving with N targets" is one observation:

	08:43:02.647  rxPackets=16501  targets=2
	08:44:23.534  rxPackets=17001  targets=10   <-- 500 packets in 81 SECONDS
	08:44:27.241  rxPackets=17501  targets=9    <-- 500 packets in 3.7 seconds
	08:44:31.104  rxPackets=18001  targets=9
	...steady at ~135 packets/second thereafter

**~6 packets per second for 81 seconds, then ~135.** About 4% of rate. targets
is non-zero throughout, so there were always consumers to send to.

The hub is not failing to deliver. IT HAS NOTHING TO DELIVER. Every destination
subscribed to it starves together for exactly that window, which is why they all
report their first delivery in the same millisecond, and why a destination that
probes during it reads video at a trickle and no audio at all.

## This relocates the fault upstream of the relay

	relay fan-out    byte-identical, dropped=0 -- cleared
	relay targets    non-zero throughout the starvation -- cleared
	subscriptions    present, on the right hub, removed only afterwards -- cleared
	the media path   twelve reproductions, all passing -- cleared

What remains is what FEEDS the hub: the ingest child, and the RTMP server's
delivery to it. The ingest execs ONCE for the whole run, so it is not crashing
or restarting -- it is running and producing almost nothing for 81 seconds.

## Note on the capture

The capture on this hub holds 13.4 MB with 40s of audio. That is consistent with
the starvation only if those bytes were written OUTSIDE the starved window --
i.e. the ingest delivers the publish late, in a burst, rather than live. The
capture bracket (CAPSTART..CAPEND) was taken by wall clock around the publisher
and cannot distinguish the two. That is worth resolving before trusting any
timing read off the capture.

## Next

Instrument the ingest's OUTPUT rate the way the hub's input is now instrumented:
its -progress `total_size` over time, or the RTMP server's per-subscriber
delivery counter. The question is whether the ingest is being starved by the
RTMP server or is itself stalling.

---

# A REAL DEFECT FIXED, AND IT IS NOT THE ONE (02:08)

The RTMP server never ended its subscribers when a publish finished. The ingest
child is such a subscriber, so it sat attached to a stream that would never
produce again. Measured before the fix:

	ingest out_time FROZEN at 40021ms   -- the publisher's own duration
	zero bytes written for ~80 seconds  -- speed decaying 0.98 -> 0.36

After ending subscribers with the publish, the ingest no longer freezes: its
out_time advances, and it restarts cleanly into the next publish at a steady
~2.26 MB per ten-second sample. The relay hub receives at ~277 packets/second
where it previously fell to ~6.

engine.go always said this was the intent -- "The ingest listener is expected to
exit whenever the streamer stops" -- and it could not, because the end of the
publish was never delivered to it.

**4c is still red.** dest:4 execs at 09:03:19.474 and gets its first byte at
09:04:33.035: 73.5 seconds, unchanged. So the ingest freeze was real, is fixed,
and is NOT what starves the destination.

## What that leaves

The hub is receiving healthily and the destination is subscribed, yet gets
nothing for 73 seconds. The fanout samples covering 09:03:19-09:04:33 were not
captured -- the dump prints tails -- so the one thing still unmeasured is
`targets` DURING the starvation for the destination that is starving. Every
sample I have is from after it ended.

That is the next measurement, and it is narrow: log fanout state on a TIME
ticker rather than every 500 packets, so a starved window produces samples
instead of silence. Sampling per-packet cannot describe a period with no packets.

---

# STATE AT HANDOFF (02:17)

## Fixed, tested, committed — two real defects, neither of them #674

1. **`048f97f9` — a destination whose hub is replaced under it now restarts.**
   destSpec hashes the argv with the relay URL BLANKED, so a hub swap did not
   change the spec and stopDestinations kept the destination alive on a port
   nothing writes to. Mutation-tested both ways: dropping the equality keeps a
   stale destination, making it a liveness test restarts healthy ones (which is
   how an earlier repair broke acceptance-failover's zero-restart pin).

2. **`6e2f5a60` — the RTMP server now ends subscribers when a publish ends.**
   It never did. The ingest child sat attached to a stream that would never
   produce again: out_time FROZEN at 40021ms, zero bytes for ~80s, speed
   decaying 0.98 -> 0.36, the relay hub down to ~6 packets/second. After the
   fix the ingest advances, restarts cleanly into the next publish, and the hub
   runs at ~277 packets/second. engine.go had asserted this behaviour all along.

## Still open

4c fails: 48 passed, 2 failed. dest:4 execs and receives its first byte ~73
seconds later, unchanged by either fix.

## The one measurement still missing, and why

Clock-sampled hub state was supposed to show `targets` and `rxPackets` DURING
the starvation. It does not, because the samples begin only at 09:14:25 while
dest:4 execs at 09:13:13 -- so either the hub did not exist before then, or the
dump truncated them.

**Every section of that dump is a tail.** Three conclusions in this
investigation were drawn from truncated output and were wrong. Before believing
any absence there, dump the WHOLE `docker logs` to a file and grep that.

The next step is exactly that: capture the full container log as an artefact, as
the relay capture already is, and read the window 09:13:13-09:14:25 from it. If
the hub genuinely did not exist before 09:14:25, then destinations are started
against a hub that is created later, and that is #674.
