# OBS setup

polyemesis accepts a stream from anything that can push MPEG-TS over SRT or a
single track over RTMP. OBS is what most people use, so it gets its own page.

Only one of these configurations unlocks per-destination audio routing:
**multitrack over SRT**. The others work, and are documented here so that
choosing one is deliberate rather than accidental.

- [Multitrack over SRT](#multitrack-over-srt) — the configuration the product
  exists for
- [Standard RTMP](#standard-rtmp-single-track) — one stereo pair, for encoders
  that cannot do SRT
- [Enhanced RTMP multitrack](#enhanced-rtmp-multitrack-not-implemented) — not
  supported, and why

---

## Multitrack over SRT

OBS sends several audio tracks in one MPEG-TS stream over SRT, and polyemesis
gives each destination its own mix of them.

### 1. Enable the audio tracks

`Settings → Output → Audio` (Advanced mode). Set a bitrate for each track you
intend to use — 160 kbps for tracks 1–3 is a reasonable start.

### 2. Assign sources to tracks

In the Audio Mixer, click the gear icon → **Advanced Audio Properties**. Each
source has six **Tracks** checkboxes. A typical layout:

| Source | 1 | 2 | 3 | Meaning |
|---|---|---|---|---|
| Desktop audio (music) | ✓ | | | full mix only |
| Game audio | ✓ | ✓ | | full + clean |
| Microphone | ✓ | ✓ | ✓ | everywhere |

Track 1 = everything, track 2 = clean (no music), track 3 = mic only.

### 3. Configure the Custom FFmpeg output

`Settings → Output → Output Mode: Advanced → Recording tab`, set
**Type: Custom Output (FFmpeg)**, then:

| Field | Value |
|---|---|
| FFmpeg Output Type | **Output to URL** |
| File path or URL | `srt://YOUR_SERVER:6000?mode=caller&transtype=live&latency=200000&streamid=YOUR_TOKEN` |
| Container Format | `mpegts` |
| Muxer Settings | *(leave blank)* |
| Video Bitrate | e.g. `6000 Kbps` |
| Keyframe interval (frames) | `60` (2 s at 30 fps) |
| Video Encoder | `libx264` (or `h264_nvenc`, `h264_videotoolbox`) |
| Video Encoder Settings | `preset=veryfast tune=zerolatency` |
| Audio Bitrate | `160 Kbps` |
| Audio Encoder | `aac` |
| **Audio Track** | tick **1, 2, 3** (up to 6) |

> **`latency` is in microseconds.** `200000` is 200 ms, and it must match the
> latency set in polyemesis → *Settings → Ingest*. Writing `200` gives you a
> 0.2 ms buffer and a stream that falls apart on the first jitter.

> **`streamid` is the address, not an extra.** One SRT port serves every source
> and they are told apart by their publish token, so a publisher that presents
> no token — or an unrecognised one — is refused rather than guessed at. Copy
> the whole URL from **Sources**, which fills the token in for you. Rotating a
> token keeps the old one working for five minutes, so you can change it
> without cutting a live stream. See
> [DESIGN-ONE-PORT-ONLY.md](DESIGN-ONE-PORT-ONLY.md) for why.

Copy the exact URL from the polyemesis dashboard rather than assembling it by
hand — it renders your server's hostname and current settings, including the
passphrase if you set one.

### 4. Start it

Press **Start Recording**, not Start Streaming. With `Output to URL`, OBS's
"recording" *is* the SRT push.

### 5. Verify

The polyemesis dashboard should show your track count and video format. Open
**Audio meters**, play music, and confirm track 1 moves while track 2 stays
flat.

That last check is the whole product working. Do it before you go live rather
than after — a "clean" track that is not actually clean is the failure mode this
software exists to prevent, and it is silent until somebody files a copyright
claim.

---

## Standard RTMP (single track)

For encoders that cannot do SRT. RTMP carries one audio track by protocol, so
per-destination *track* routing does not apply — though gain, matrix panning and
5.1 downmix still do.

In polyemesis: `Settings → Ingest → Mode: RTMP`. Note the app name and stream
key. In OBS: `Settings → Stream`, Service **Custom...**:

| Field | Value |
|---|---|
| Server | `rtmp://YOUR_SERVER:1935/live` |
| Stream Key | the key from polyemesis Settings |

Then press **Start Streaming**.

**At most one source may use RTMP ingest.** polyemesis has no RTMP server of its
own — it uses `ffmpeg -listen 1`, a single-connection receiver that cannot
demultiplex by path — so a second RTMP source is refused with an error that says
so, rather than accepted and silently starved. Any number of sources can share
the SRT port.

---

## Enhanced RTMP multitrack (not implemented)

OBS 30.2+ can send multiple audio tracks over Enhanced RTMP/FLV. **polyemesis
does not support this.** RTMP ingest is single-track.

`config.yaml` used to declare an `enhancedRtmp` key. It has been removed: it
was an inert placeholder that read as a switch, and it was kept on the belief
that old config files needed it to keep parsing. They do not — unknown keys are
ignored — so the key is gone and a config that still names it loads fine.

For multiple audio tracks, use SRT ingest.

---

## See also

- [QUICKSTART.md](QUICKSTART.md) — first stream in about five minutes
- [AUDIO-ROUTING.md](AUDIO-ROUTING.md) — what to do with the tracks once they
  arrive
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md#the-ingest-never-goes-live) — when the
  ingest never goes live
- [INSTALL.md](INSTALL.md#the-ffmpeg-requirement) — checking your FFmpeg has SRT
