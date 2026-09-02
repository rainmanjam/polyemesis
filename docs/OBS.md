# OBS setup

polyemesis accepts a stream from anything that can push MPEG-TS over SRT or a
single track over RTMP. OBS is what most people use, so it gets its own page.

> **On Enhanced RTMP.** OBS 30.2+ can send several audio tracks over RTMP using
> the Enhanced RTMP multitrack format. FFmpeg gained multitrack FLV demuxing in
> late 2024, so a new enough build carries those tracks through polyemesis's
> existing ingest command unchanged — verified up to six tracks on FFmpeg 8.1.
> Ubuntu 24.04's stock FFmpeg 6.1.1 cannot: it refuses with *"at most one audio
> stream is supported in flv"*.
>
> **OBS cannot either, and that is now measured rather than open.** OBS 30.2.3
> was run headless against a real polyemesis with three tracks configured,
> each on its own mixer, `StreamMultiTrackAudioMixes=7` and a custom RTMP
> service — and the captured wire bytes are `0xaf legacy ×3541` with no
> multitrack tag anywhere. The path is gated on
> `supports_additional_audio_track`, which **no service in OBS's
> `services.json` declares (0 of 91)**, so it is unreachable even for custom
> RTMP. A weekly job re-checks it. **If your encoder is OBS, use SRT.** See
> `evidence/enhanced-rtmp-multitrack.md`.

Only one of these configurations unlocks per-destination audio routing:
**multitrack over SRT**. The others work, and are documented here so that
choosing one is deliberate rather than accidental.

- [Multitrack over SRT](#multitrack-over-srt) — the configuration the product
  exists for
- [Standard RTMP](#standard-rtmp-single-track) — one stereo pair, for encoders
  that cannot do SRT
- [Enhanced RTMP multitrack](#enhanced-rtmp-multitrack-works-on-ffmpeg-71-not-from-obs)
  — works on FFmpeg 7.1+, and OBS does not send it

---

## Multitrack over SRT

OBS sends several audio tracks in one MPEG-TS stream over SRT, and polyemesis
gives each destination its own mix of them.

### 0. Create the source you are pushing to

A fresh install has no source, and `YOUR_TOKEN` below is a *source's* publish
token — so there is nothing to point OBS at until one exists. Add one on the
**Sources** page; a name is all it asks for. That page then shows the whole
publish URL, token included, which is the value to paste into the *File path or
URL* field in step 3.

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
> latency set on that source's own ingest, on the **Sources** page. (*Settings →
> Ingest* edits the same numbers for the default source, and on an install with
> no source at all it has nothing to edit and says so.) Writing `200` gives you a
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

> **Passphrases use letters, digits and `- _ . ~`.** Anything else is refused
> when you set it, and the reason is worth knowing: the passphrase travels in
> the URL above, and an encoder does **not** un-escape it. A `;` would be
> written `%3B` in the URL, sent as the literal text `%3B`, and compared
> against the `;` you stored — so a correct passphrase would be refused on
> every connection, with nothing on screen to say why.

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

For encoders that cannot do SRT. *Classic* RTMP carries one audio track, so
per-destination *track* routing does not apply — though gain, matrix panning and
5.1 downmix still do.

In polyemesis: open the source under **Sources** and set `Ingest → Mode: RTMP`.
It gets its own app name and stream key. In OBS: `Settings → Stream`, Service
**Custom...**:

| Field | Value |
|---|---|
| Server | `rtmp://YOUR_SERVER:1935/live` |
| Stream Key | that source's stream key, from **Sources** |

Then press **Start Streaming**.

> **The stream key is the address, not an extra.** One RTMP port serves every
> source and they are told apart by the key in the publish URL, exactly as SRT
> tells them apart by token — so a publisher presenting an unrecognised key is
> refused rather than landing on whichever source happens to be there. Copy the
> Server and Stream Key from **Sources** rather than assembling them by hand:
> each source has its own pair, and pasting one source's key under another
> source's name is the mistake this shape makes easy to notice and easy to fix.

**Any number of sources may use RTMP ingest**, the same as SRT. A second RTMP
encoder is an ordinary second source; it needs no extra port and no
configuration beyond its own stream key. See
[DESIGN-ONE-PORT-ONLY.md](DESIGN-ONE-PORT-ONLY.md#rtmp) for how that came to be
true — until 2026-08-06 an install could carry exactly one RTMP source.

What RTMP still does not give you is per-destination *track* routing, which is
a limit of the format rather than of the listener. If your encoder can speak
SRT, use SRT.

---

## Enhanced RTMP multitrack (works on FFmpeg 7.1+, not from OBS)

OBS 30.2+ can send multiple audio tracks over Enhanced RTMP/FLV.

It works on a new enough FFmpeg. Multitrack FLV demuxing landed in FFmpeg 7.1,
and from there the tracks arrive through polyemesis's existing ingest command
unchanged — verified end to end: a destination configured for tracks 1 and 3
received exactly those two and neither of the others. It does **not** work on
FFmpeg 6.1.1, which is Ubuntu 24.04's stock build: that refuses with *"at most
one audio stream is supported in flv"*, and the extra tracks are lost with no
error at either end.

**That build is what one of the images we ship runs.** `Dockerfile.cuda` is
based on `nvidia/cuda:12.6.3-base-ubuntu24.04` and pins `7:6.1.1-3ubuntu5`
(`Dockerfile.cuda:132`), so this section does not apply to the CUDA image at
all — it is below the 7.1 floor. The default Alpine image (FFmpeg 8.1.2) and
`Dockerfile.vaapi` (8.0.1) are above it. If you need NVENC *and* multitrack
RTMP ingest, you cannot get both from the images in this repository today.

What has not been done is a run with OBS itself as the publisher — the testing
used FFmpeg. OBS writes the same `MULTITRACKTYPE_ONE_TRACK` format (read from
its `flv-mux.c`), so the wire format is not in question, but the handshake and
metadata path are unconfirmed. SRT remains the operated path.

See `evidence/enhanced-rtmp-multitrack.md`.

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
