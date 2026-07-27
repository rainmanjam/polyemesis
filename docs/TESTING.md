# Testing polyemesis without OBS

Everything here uses FFmpeg only. You do not need OBS, a capture card, or a
streaming account to verify that per-destination audio routing works.

The core idea: generate a stream whose audio tracks are **distinct sine tones**,
route different track combinations to different destinations, then confirm by
ear and by measurement that each destination contains exactly its selected mix
and nothing else.

| Track | Tone | Stands in for |
|---|---|---|
| 1 | 300 Hz (low hum) | full mix, including copyrighted music |
| 2 | 900 Hz (mid tone) | clean mix, no music |
| 3 | 2000 Hz (high beep) | microphone only |

Three octave-ish separated tones are used on purpose: they are trivially
distinguishable by ear, and a narrow bandpass can measure each independently.

---

## 0. Prerequisites

```bash
ffmpeg -version          # must be 6.0 or newer
ffmpeg -protocols | tr ' ' '\n' | grep -x srt   # must print exactly: srt
```

If the second command prints nothing, your FFmpeg has **no SRT support** and
the SRT sections below will fail with `Protocol not found`. Note that
`ffmpeg -protocols | grep srt` is misleading — every build lists `srtp`
(Secure RTP), which is a different protocol. Use the `-x srt` exact match above.

Homebrew's `ffmpeg` bottle currently has no SRT. Options:

- Install a build configured with `--enable-libsrt`.
- Use the **RTMP fallback** (§5) — single audio track, so it exercises the
  pipeline but not multitrack routing.
- Use the **direct-to-relay** method (§6) — full multitrack routing, no SRT.

polyemesis starts and warns rather than refusing, so you can always reach
Settings and switch the ingest to RTMP.

---

## 1. Start polyemesis

```bash
make build
./polyemesis -data ./data
```

Open <http://localhost:8080>, complete first-run setup, and note the SRT port
from Settings → Ingest (default 6000).

---

## 2. Generate a synthetic multi-track SRT stream

One video test pattern plus three mono AAC audio tracks, pushed to the SRT
listener:

```bash
ffmpeg -hide_banner -re \
  -f lavfi -i "testsrc2=size=1280x720:rate=30" \
  -f lavfi -i "sine=frequency=300:sample_rate=48000" \
  -f lavfi -i "sine=frequency=900:sample_rate=48000" \
  -f lavfi -i "sine=frequency=2000:sample_rate=48000" \
  -map 0:v -map 1:a -map 2:a -map 3:a \
  -c:v libx264 -preset ultrafast -tune zerolatency -g 60 -b:v 2500k \
  -c:a aac -b:a 128k \
  -f mpegts "srt://127.0.0.1:6000?mode=caller&transtype=live&latency=200000"
```

Notes on the flags that matter:

- `-re` paces the synthetic source at real time. Without it FFmpeg pushes as
  fast as it can and the relay is flooded.
- `latency=200000` is **microseconds** (200 ms) — FFmpeg's SRT option is not in
  milliseconds. It must match the latency configured in polyemesis.
- `-g 60` gives a keyframe every 2 s, which keeps the HLS preview segments on
  GOP boundaries.

Want six tracks instead of three? Add more `-f lavfi -i "sine=frequency=..."`
inputs and matching `-map N:a` entries, up to six.

**Verify polyemesis saw it.** On the Dashboard, "Audio tracks" should read `3`
and the video line should read `h264 1280×720 @ 30.00fps`. On the Audio meters
page all three tracks should show activity around −18 dBFS peak.

---

## 3. Create two destinations with different routing

In the UI:

1. **Dashboard → Add destination**
   - Name: `A — tracks 1+2`
   - Transport: **Local file**
   - Filename: `a.mkv`
   - Create.
2. Repeat for `B — tracks 1+3` writing `b.mkv`.
3. For each, open **Routing**, tick the tracks it should receive
   (A: 1 and 2. B: 1 and 3), and **Save**.
4. Press **Start** on both cards.

The destination cards should show `Tracks 1, 2 → stereo` and
`Tracks 1, 3 → stereo`, and the generated filter string under the routing
editor should read:

```
[0:a:0]pan=stereo|c0=1*c0|c1=1*c0[a_t0];
[0:a:1]pan=stereo|c0=1*c0|c1=1*c0[a_t1];
[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];
[a_mix]alimiter=limit=0.95:level=disabled[a_norm];
[a_norm]aresample=48000:async=1:first_pts=0[aout]
```

(Shown wrapped for readability; it is one line.)

Let both run for ~20 seconds, then press **Stop** on each.

---

## 4. Verify the outputs

### 4a. Confirm the container shape with ffprobe

Each destination must carry **exactly one stereo AAC audio stream**, plus the
video copied through untouched:

```bash
ffprobe -v error -show_entries stream=index,codec_type,codec_name,channels,channel_layout \
        -of compact=p=0 data/recordings/a.mkv
```

Expected:

```
index=0|codec_type=video|codec_name=h264
index=1|codec_type=audio|codec_name=aac|channels=2|channel_layout=stereo
```

If you see three audio streams, routing did not run — the destination is
passing tracks through rather than mixing them.

To confirm video really was **copied, not re-encoded**, compare it against the
source: the codec, resolution and frame rate must be identical, and the
destination process must consume almost no CPU on the Monitoring page.

### 4b. Confirm the mix by measurement

Measure the energy in a narrow band around each tone. A tone that is present
reads ~25–40 dB louder than one that was excluded:

```bash
for f in 300 900 2000; do
  printf "%5d Hz: " "$f"
  ffmpeg -v info -i data/recordings/a.mkv \
    -af "bandpass=frequency=$f:width_type=h:width=50,astats=metadata=0:measure_perchannel=none" \
    -f null - 2>&1 | grep "RMS level dB" | tail -1 | awk '{print $NF}'
done
```

> `-v info` is required. astats logs at info level, so `-v error` prints nothing
> and every band looks silent.

Expected for `a.mkv` (tracks 1+2):

```
  300 Hz: -21.07     <- present
  900 Hz: -21.09     <- present
 2000 Hz: -57.85     <- EXCLUDED, ~37 dB down
```

Expected for `b.mkv` (tracks 1+3):

```
  300 Hz: -21.09     <- present
  900 Hz: -49.63     <- EXCLUDED, ~28 dB down
 2000 Hz: -21.09     <- present
```

The excluded band never reaches true silence because a bandpass filter has
finite skirts; anything more than ~20 dB below the loudest band is absent.

### 4c. Confirm by ear

```bash
ffplay -autoexit data/recordings/a.mkv   # low hum + mid tone, no high beep
ffplay -autoexit data/recordings/b.mkv   # low hum + high beep, no mid tone
```

This is the check that matters most: it is exactly how a streamer verifies that
the copyrighted-music track really is absent from the YouTube feed.

---

## 5. RTMP fallback (single track)

RTMP carries one audio track by protocol, so this exercises ingest, relay,
supervision and destinations but not multitrack routing.

Set Settings → Ingest → Mode to **RTMP**, note the app and stream key, then:

```bash
ffmpeg -hide_banner -re \
  -f lavfi -i "testsrc2=size=1280x720:rate=30" \
  -f lavfi -i "sine=frequency=440:sample_rate=48000" \
  -map 0:v -map 1:a \
  -c:v libx264 -preset ultrafast -tune zerolatency -g 60 -b:v 2500k \
  -c:a aac -b:a 128k \
  -f flv "rtmp://127.0.0.1:1935/live/stream"
```

---

## 6. Direct-to-relay (full multitrack, no SRT needed)

If your FFmpeg lacks SRT, you can still test the entire routing engine by
publishing to the internal relay hub exactly as the ingest process would. This
substitutes only the `-c copy` SRT hop; relay fan-out, track probing, routing
compilation and destination muxing are all exercised unchanged.

Find the relay port on **Monitoring** ("Relay in … port NNNNN"), then:

```bash
RELAY_PORT=54719   # read this from the Monitoring page

ffmpeg -hide_banner -re \
  -f lavfi -i "testsrc2=size=1280x720:rate=30" \
  -f lavfi -i "sine=frequency=300:sample_rate=48000" \
  -f lavfi -i "sine=frequency=900:sample_rate=48000" \
  -f lavfi -i "sine=frequency=2000:sample_rate=48000" \
  -map 0:v -map 1:a -map 2:a -map 3:a \
  -c:v libx264 -preset ultrafast -tune zerolatency -g 60 -b:v 2500k \
  -c:a aac -b:a 128k \
  -map 0 -f mpegts -flush_packets 1 "udp://127.0.0.1:${RELAY_PORT}?pkt_size=1316"
```

The relay port is chosen at startup and changes on restart, so re-read it each
time.

---

## 7. Automated smoke test

`scripts/smoketest.go` performs §2–§4 end to end against a running server: it
completes first-run setup, creates two file destinations with tracks 1+2 and
1+3, streams synthetic audio, then measures per-frequency energy in each output
and fails if any tone is on the wrong side of the threshold.

```bash
# terminal 1
./polyemesis -addr :8099 -data ./smoke-data -log warn

# terminal 2
mkdir -p /tmp/pm-smoke && cp scripts/smoketest.go /tmp/pm-smoke/
cd /tmp/pm-smoke && go mod init smoke >/dev/null 2>&1
go run smoketest.go
```

It ends with `SMOKE TEST PASSED` or a table showing which band was wrong.
It uses the direct-to-relay method from §6 so it runs without SRT.

---

## 8. Surround (5.1 → stereo) routing

Generate a 5.1 track where the rear channels carry a different tone from the
fronts, to verify the downmix coefficients and matrix mode:

```bash
ffmpeg -hide_banner -re \
  -f lavfi -i "testsrc2=size=1280x720:rate=30" \
  -f lavfi -i "sine=frequency=300" -f lavfi -i "sine=frequency=500" \
  -f lavfi -i "sine=frequency=700" -f lavfi -i "sine=frequency=100" \
  -f lavfi -i "sine=frequency=1500" -f lavfi -i "sine=frequency=2500" \
  -filter_complex "[1:a][2:a][3:a][4:a][5:a][6:a]join=inputs=6:channel_layout=5.1[surround]" \
  -map 0:v -map "[surround]" \
  -c:v libx264 -preset ultrafast -g 60 -b:v 2500k -c:a aac -b:a 384k \
  -f mpegts "srt://127.0.0.1:6000?mode=caller&transtype=live&latency=200000"
```

Channel order is FL(300) FR(500) FC(700) LFE(100) BL(1500) BR(2500).

- **Simple mode** on that track should produce
  `pan=stereo|c0=0.4143*c0+0.2929*c2+0.2929*c4|c1=0.4143*c1+0.2929*c2+0.2929*c5`
  — the normalized ITU coefficients, with LFE (100 Hz) dropped. Measuring the
  output should show 100 Hz absent.
- **Mix matrix → "Surround rears only"** should leave only 1500 Hz and 2500 Hz.

---

## 9. Unit tests

The routing engine and every FFmpeg command builder are pure functions and are
tested without spawning a process:

```bash
go test ./...
go test -v ./internal/routing/    # pan strings, 5.1 downmix, validation
go test -v ./internal/ffmpeg/     # ingest/destination/recorder command builders
```

---

## 10. Acceptance suites

Four scripts drive the built binary through a real ingest and assert on what
came out the other end. They need `make build` first, and they are the only
tests that can fail on something the unit tests cannot see.

```bash
./scripts/acceptance.sh              # per-destination audio routing, by measurement
./scripts/acceptance-renditions.sh   # one shared encode serving two destinations
./scripts/acceptance-tls.sh          # every TLS mode, including the old configs
./scripts/acceptance-encoders.sh     # hardware-encoder detection
```

`acceptance-encoders.sh` is the odd one out, because the thing it tests is a
disagreement with the machine it runs on. It builds three shim FFmpegs that
delegate real work to the real binary and lie only about detection:

- **liar** — lists `h264_nvenc` in `-encoders` and fails to encode with it.
  This is a stock Linux FFmpeg on a GPU-less box, staged on a machine that has
  no NVIDIA hardware to stage it with. The suite asserts the encoder is offered
  as unusable with FFmpeg's own reason, that a rendition saved on it is refused
  once rather than crash-looped, and that a `libx264` rendition beside it still
  runs.
- **blind** — every detection command errors. The server must still start, still
  offer every encoder, and still produce a correct 720p encode. Detection that
  could not run must never be the thing that stops a stream.
- **instant** — probes return immediately. It is the control for the startup
  timing: the suite reports the median of five launches with and without
  probing, and fails if the difference exceeds one second.

---

## Troubleshooting

| Symptom | Cause |
|---|---|
| `Protocol not found` on the ingest | FFmpeg built without SRT. See §0. |
| Dashboard shows 6 tracks, not 3 | Nothing has arrived yet; six stereo tracks is the pre-probe default. Start the source. |
| Destination stuck "Reconnecting" | Read its error on the card, or the Monitoring log tail. A refused RTMP connection means the target is not listening. |
| Meters show "no signal" | Check the meters process on Monitoring. It restarts whenever the track layout changes. |
| All bands measure ~-200 dB | You used `-v error`; astats logs at info level. See §4b. |
| Output has 3 audio streams | Routing did not apply — check the destination's compiled filter in the routing editor. |
