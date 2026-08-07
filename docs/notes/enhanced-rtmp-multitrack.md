# Accepting Enhanced RTMP multitrack from OBS

**Status: the documented blocker is stale, but only on a new enough FFmpeg.**
Every doc in this repo says RTMP ingest is single-track "by protocol". The
protocol part is no longer true — FFmpeg merged multitrack FLV demuxing (Timo
Rothenpieler, late 2024) and the existing ingest command carries multiple audio
tracks over RTMP unchanged.

**It is an FFmpeg version dependency, not a free win.** Verified on the OVH
deployment, which runs Ubuntu 24.04's stock FFmpeg 6.1.1:

```
$ ffmpeg ... -map 1:a -map 2:a -f flv out.flv
[flv @ ...] at most one audio stream is supported in flv
```

6.1.1 can neither write nor read multitrack FLV. So this is not the pure
documentation change the effort table below first suggested: it needs a minimum
FFmpeg version, and a capability probe so an operator on an older build is told
why their extra tracks vanished rather than silently receiving one. The bundled
Docker image pins a new enough FFmpeg; binary-mode installs on Ubuntu 24.04 do
not.

Note the direction of travel is not uniformly "newer is more permissive": the
same 6.1.1 accepts an 80-channel `amerge` where 8.1.2 refuses past 64, because
the 7.0 channel-layout rework introduced a hard cap. Capping at 64 is therefore
correct on both.

## What was verified

FFmpeg 8.1.2, publishing into the exact argv `IngestArgs` builds
(`internal/ffmpeg/build.go:304`):

```
ffmpeg -listen 1 -fflags +genpts -i rtmp://…/live/test \
       -map 0 -c copy -f mpegts -flush_packets 1 <relay>
```

> That argv is what `IngestArgs` built at the time. Since 2026-08-06 the ingest
> ffmpeg *subscribes* to `internal/rtmpserver` instead —
> `-i rtmp://127.0.0.1:1935/live/<key>`, no `-listen 1`. The finding is
> unaffected: the listener relays RTMP messages without decoding them, so the
> tracks reach this same `-map 0 -c copy` in the same order. Anyone re-running
> the verification should use the current argv rather than the one recorded
> here.

**Six audio tracks — `MaxTracks` — arrive intact, in the published order, on
every run.** Stream *count* cannot detect a reordering, so each track carried a
distinct tone (300/500/700/1100/1300/1700 Hz) and was identified on arrival by
its content via a Goertzel detector rather than by its index:

| publisher shape | runs | probe result | track order |
|---|---|---|---|
| AAC, track 0 legacy `0xaf` | 5 | `1v + 6a` | exact match, every run |
| Opus, track 0 ExHeader `0x91` | 3 | `1v + 6a` | exact match, every run |

The harness is mutation-verified: republishing with the tracks deliberately
shuffled `[3,0,5,1,4,2]` made it report exactly that permutation, so it is
reading content and not stream position.

Those probe results come from polyemesis's own `ProbeArgs`
(`internal/ffmpeg/build.go:1074`), not an ad-hoc ffprobe invocation.

The harness is `scripts/verify_ertmp_multitrack.go`:

```sh
go run scripts/verify_ertmp_multitrack.go -runs 5            # both codecs
go run scripts/verify_ertmp_multitrack.go -runs 1 -shuffle   # prove it can fail
```

It checks FFmpeg's own multitrack conformance, not polyemesis's ingest path —
that stopped being `ffmpeg -listen 1` when the listener became
`internal/rtmpserver`. The shipped path is covered by
`TestEnhancedRTMPMultitrackSurvivesTheSharedListenerInOrder`.

### It is genuinely E-RTMP v2, and genuinely the mode OBS uses

Tag inspection of the published stream:

| audio tag header | meaning |
|---|---|
| `0xaf` | legacy `SoundFormat=10` (AAC) — track 0, AAC case |
| `0x91` | `ExHeader`, `AudioPacketType=1`, fourCC `Opus` — track 0, Opus case |
| `0x95` | `ExHeader`, `AudioPacketType=5` **Multitrack** — tracks 1..5 |

And OBS's own muxer (`plugins/obs-outputs/flv-mux.c`) writes audio this way:

```c
s_w8(&s, AUDIO_HEADER_EX | (is_multitrack ? AUDIO_PACKETTYPE_MULTITRACK : type));
if (is_multitrack) {
        s_w8(&s, MULTITRACKTYPE_ONE_TRACK | type);
        s_wa4cc(&s, codec_id);
        s_w8(&s, (uint8_t)idx);          // trackId
} else {
        s_wa4cc(&s, codec_id);
}
```

Two things follow. First, OBS uses **`MULTITRACKTYPE_ONE_TRACK`** — of the three
modes E-RTMP v2 defines, it is the one exercised above. Second, OBS writes
track 0 as `AUDIO_HEADER_EX | type` (ExHeader + fourCC) rather than as a legacy
tag; that is why the Opus run matters, since Opus has no legacy FLV
`SoundFormat` and forces FFmpeg down the same ExHeader shape. Both track-0
encodings were verified.

Track discovery is already protocol-agnostic: `ProbeArgs` probes
`RelayInputURL(…)`, i.e. the MPEG-TS relay *after* ingest. Nothing in routing
keys off `IngestRTMP`, and `MaxTracks` (`internal/routing/profile.go`) is a
global cap of 32, not an SRT one.

So the pipeline from the relay onward needs no work at all.

## What actually remains

### 1. A confirmation run against real OBS

Everything above used FFmpeg as the publisher. What that leaves unverified is
narrow but real: OBS's RTMP `connect`/handshake, the `onMetaData` it sends
(including `audioTrackIdInfoMap`), and behaviour under sustained load. The wire
*format* of the audio tags is no longer in question — it was read out of OBS's
source and matched byte-shape against what was tested.

This is now a confirmation, not an investigation. Half a day.

### 2. Track identity — largely answered

The original worry was that `ffprobe` exposes no `trackId`, only ordering, while
polyemesis routes by index. Eight runs across two codecs all preserved order
exactly, and OBS assigns `trackId = idx` in publish order.

But every one of those runs published a **contiguous 0..5**, and the spec is
explicit that this is not something a receiver may assume:

> Additional variants … SHOULD use distinct positive trackId values (1, 2, 3, …).
> These values are identifiers only and **do not imply any inherent ordering**,
> priority, or quality ranking.

So the open case is a **sparse or reordered track set** — a publisher sending
trackIds 0, 2 and 5, or reconnecting mid-session with a different selection
enabled. Position and trackId diverge there, and since polyemesis routes by
index, that is precisely where audio would reach the wrong destination while
every screen still looks correct.

Worth noting the risk is asymmetric: too few tracks is visible immediately,
whereas a silently shifted mapping is not visible at all until someone listens
to the wrong platform's output. That argues for reconciling against `trackId`
rather than trusting arrival order, even though arrival order held in testing.

### 3. Codec breadth

E-RTMP v2 permits Opus, AC-3, E-AC-3 and FLAC on audio tracks. Opus was
confirmed above to survive `-c copy` into MPEG-TS and probe correctly, so
*transport* is fine — but the downstream mixing path assumes AAC-shaped input. If OBS can be configured to send anything other
than AAC, that path needs either support or an explicit refusal — refusal being
consistent with how Opus-on-RTMP is already handled for destinations.

### 4. Documentation and copy

Six documents assert RTMP is single-track and would become wrong:

- `docs/FAQ.md:51` — "What about Enhanced RTMP / multitrack FLV from OBS 30.2+?"
- `docs/CONFIGURATION.md:104` — the `enhancedRtmp` removal note
- `docs/ARCHITECTURE.md:98`, `docs/DESIGN-ONE-PORT-INGEST.md:139`,
  `docs/DESIGN-ONE-PORT-ONLY.md:108`, `docs/INSTALL.md:206`
- `internal/db/settings.go:23` — the `IngestRTMP` doc comment
- `internal/config/config.go:50`

Plus one UI string, `set.modeRtmp` = "RTMP — single track", and its 15
translations.

Note the `enhancedRtmp` config key does **not** need reinstating. It was removed
because it changed nothing; if this works it still changes nothing, because it
works by default with no flag.

## What does *not* change

**Multitrack and multi-source are independent problems**, and nothing in this
note touches the second one. That separation is the point and it still holds:
Enhanced RTMP is about how many audio tracks ride one connection, not about how
many connections the listener can tell apart.

> **Correction, 2026-08-06.** This section previously read "**RTMP remains one
> source**", and gave as the reason that closing the limit would need either a
> Go RTMP server or the rejected `yutopp/go-rtmp` dependency. It needed the
> former, and got it: `internal/rtmpserver` wraps `bluenviron/gortmplib` — a
> different dependency from the rejected one — and `checkRTMPExclusive` is gone.
> RTMP is now one port, addressed by stream key, any number of sources. See
> [multi-source-rtmp.md](multi-source-rtmp.md) and
> `DESIGN-ONE-PORT-ONLY.md#rtmp`.
>
> Nothing about the multitrack findings below or above depends on that. The
> tracks ride through the new listener untouched, because it relays RTMP
> messages and never decodes them — the same reason they rode through
> `ffmpeg -listen 1` untouched. The FFmpeg version dependency is unchanged:
> 7.1+ verified, 6.1.1 refuses, OBS-as-publisher still unconfirmed.

The reasoning in `DESIGN-ONE-PORT-ONLY.md` — "no amount of RTMP work enables
per-destination multitrack routing" — is the part that has aged. It is now
enabled by FFmpeg, for free, without any RTMP work at all. That argument was
also the load-bearing reason multi-source RTMP was deferred, which is how the
two ended up connected after all.

## Rough shape of the effort

| | |
|---|---|
| Minimum-FFmpeg check + capability probe | half a day — **newly required, see above** |
| Confirmation run against real OBS | half a day |
| Reconnect-with-different-tracks test | half a day (order itself is verified) |
| Codec guard | half a day |
| Docs + UI string + 15 locales | half a day |
| Multi-source RTMP | out of scope here — done separately on 2026-08-06, see [multi-source-rtmp.md](multi-source-rtmp.md) |

On the evidence gathered the ingest path already does this — on a new enough
FFmpeg. What turns it from a documentation change into a small piece of work is
the version dependency: the deployment this was tested against cannot do it at
all, and would need to say so rather than quietly dropping tracks.
