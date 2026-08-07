# Castr.com — feature gap analysis

Reviewed 2026-08-07 against `castr.com/features` (their own count: 82 features),
`/pricing`, `/multistream`, `/live-transcoder`, `/video-hosting`, `docs.castr.com`.

Castr is a **hosted OTT platform that also multistreams**. polyemesis is a
**self-hosted contribution-side router**. Most of the list below is that
category difference rather than a missing feature — the rows worth acting on are
marked.

## Genuine gaps

| Castr | polyemesis | Worth building? |
|---|---|---|
| **Teams, roles, SSO, granular permissions** | One admin identity; UI access = full control | **Yes.** Already the top row of `COMPARISON.md`'s restream.io table. Any multi-operator deployment hits it on day one |
| **Cloud production** — multi-source switching, live preview, compositing | Missing | **Yes, eventually.** Natural once multi-source settles; already tracked as the "multi-input compositing" gap |
| **WHIP / WebRTC sub-second output** | Missing | **Maybe.** Real use case is sub-second self-monitoring, not delivery |
| **Viewer analytics** — 30-day retention | `internal/playout/sessions.go` counts live viewers; no historical retention | **Small.** The counting exists; persisting and charting it is the missing half |
| **SMS stream alerts** | Webhooks + MQTT + alert events | Low value. A webhook into any SMS provider covers it |

## Category gaps — Castr is a different product here

Delivery-side concerns that only exist because Castr hosts the audience.
polyemesis hands the feed to YouTube/Twitch/Facebook, who do all of this.

- **DRM** (multi-DRM add-on), **geo-blocking**, **domain restriction**, **password-protected playback**
- **Paywall and monetization** — 9% commission, in-stream ads, pay-per-view
- **Video hosting / VOD library** — direct upload, Dropbox import, video replacement, branded player, iframe embed, HLS-for-VOD, scheduled availability. (`internal/uploads` stores operator media for playlists and slates; it is not a viewer-facing library)
- **24/7 channel playout** — programming a schedule of live *and* recorded blocks into an always-on channel, with live insertion. polyemesis loops a playlist as the failover fill tier; it does not programme a channel
- **White-label OTT** — branded web/mobile/TV apps (Roku, Apple TV, Fire TV, Tizen, webOS), content CMS, custom domain
- **Multi-CDN** (Akamai/Fastly/CloudFront), regional ingest, intelligent routing, 99.9% uptime SLA

## Not gaps — polyemesis already has these

Castr markets these as products; polyemesis ships them as features.

| Castr feature | polyemesis |
|---|---|
| Multiple ingest — RTMP, RTMPS, SRT | SRT, RTMP, E-RTMP multitrack (one port) |
| Pull from RTMP / HLS / RTSP / MPEG-TS | `internal/engine` pull ingest, incl. `rtspTransport` |
| IP camera streaming (RTSP) | Same pull path |
| ABR ladder + viewer-facing delivery | `internal/playout` is a public HLS/DASH origin with a variant ladder, a DVR window and viewer session counting. Variants **package** renditions rather than encoding, so the ladder costs one encoder per distinct rendition |
| Looping playlist of pre-recorded files | `internal/playlistmedia` + `-stream_loop -1`, scheduled via `playlist.start`. **Caveat:** it is the failover fill tier, not an independently programmable channel |
| Cloud recording / Live-to-VOD (72 h, kept 3 days) | `internal/recording` — multitrack, stream-copied, retention configurable, **no cap** |
| AI captions and transcripts | `internal/transcribe` |
| Clips — trim without re-uploading | `internal/clipper`, `internal/clips` |
| Overlays and watermarking | Nine-anchor text + image overlays on renditions |
| Webhooks, REST API, player integration | `internal/hooks`, REST API, MQTT + Home Assistant discovery |
| Failover ingest (Ultra plan only) | Backup ingest, platform-neutral |
| Stream health monitoring | Destination health with dwell-debounce |
| Transcoding ladder | Renditions with NVENC / QSV / VA-API / VideoToolbox / AMF |

## What Castr does not do

Their `/multistream` page states it as a feature:

> Every destination receives the same produced feed, so no platform gets a
> different version of your show.

**Nothing in Castr's 82 features is per-destination audio routing.** Their
transcoder reshapes *video* per rendition; the audio bed is one bed. That is
precisely the thing polyemesis exists to do differently — and it means the
comparison is not really Castr-vs-polyemesis on a shared axis.

## Doc correction — done 2026-08-07

Two rows in `docs/COMPARISON.md` still read **Partial — no playlist sequencing**
long after sequencing shipped. Both are now corrected, and the page's
re-verification note records the direction of the drift: **every stale row found
so far has understated the product**, because the page gets written when a gap
is found and nobody returns to it when the gap closes.

Worth a periodic re-check rather than a one-off fix — a drift test over this
page is not obviously possible, since the claims are prose about behaviour
rather than symbols a test can reach.
