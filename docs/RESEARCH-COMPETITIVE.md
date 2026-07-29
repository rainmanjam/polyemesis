# What people actually ask for

Research done 2026-07-27, before starting multi-source (Option A). Sources: the
`datarhei/restreamer` issue tracker (263 open, 548 closed), the `datarhei/core`
tracker (12 open), the Restreamer and Core documentation, and restream.io's
published plan comparison.

Reaction counts are the ranking signal throughout. They are a weak signal in
absolute terms — a 46-reaction issue on a project this size is a lot, a
2-reaction issue is nearly noise — but the *ordering* is informative, and the
repeats are more informative still.

## The headline: most of the top asks, polyemesis already has

This is the surprising result. Of the fifteen most-reacted feature requests on
Restreamer, polyemesis already ships ten.

| Ask on Restreamer | Reactions | polyemesis | Notes |
|---|---|---|---|
| Fallback stream/image when ingest disconnects | **46** (top open issue) | **have** | Slate + failover; `SwitchSource` takes `primary\|backup\|slate\|auto` |
| Multiple external providers simultaneously | 13 (closed) | **have** | The whole product |
| **Multiple audio tracks for Twitch** | **8** | **have** | Literally the thesis. They are asking Restreamer for what polyemesis is |
| Text overlays | 6 (closed) | **GAP** | |
| WebRTC / WebTransport | 6 (closed) | **GAP** | |
| Embed logo in stream | 5 (closed) | **GAP** | |
| Login page for player site | 5 | have | Playout token protection |
| Start/stop by API (Home Assistant) | 9 (closed) | have | REST + API tokens |
| On-demand streaming | 4 (closed) | have | |
| Decklink video output | 4 | **GAP** | |
| Combined chat | 4 (closed) | have | Four platforms, one hub |
| Playlist files | 4 (closed) | partial | Pull ingest takes `file://`, no playlist sequencing |
| Add stream recording | 4 | have | Plus catalogue, clips, transcripts |
| Overlay channel logo and name | 4 | **GAP** | |
| Scheduling | 3 (closed) | have | Schedules API |
| Audio-only MP3/HTTP publication | 3 | have | Icecast + audio-only destinations |
| HDR 10-bit HEVC | 3 (closed) | partial | `libx265`, `hevc_nvenc`; no HDR tone-map path |
| Cropping source stream | 2 | have | Rendition `aspectMode: crop` |
| Decklink video input | 2 | **GAP** | |
| Analytics / statistics | 2 (closed) | have | Playout analytics, stats |
| Low latency mode | 2 (closed) | partial | SRT latency is configurable; no LL-HLS |
| Video grid / multiple inputs | 1 (closed) | **GAP** | |
| Deinterlacing | 1 (closed) | **built, unreachable** | See the correction below |
| MQTT (core tracker) | — | **GAP** | Alert webhooks exist; no MQTT |
| Single token per RTMP endpoint (core) | — | **GAP** | Falls out of multi-source |

Verified against the code, not assumed. Two initial readings were wrong and are
corrected here: the only `overlay=` in the tree is inside the blurred-pad
compositor, not a branding feature, and the only WHIP mention is prose
describing *a platform's* ingest, not ours.

### Correction, 2026-07-28: deinterlacing is built but unreachable

This table said **GAP**. It was wrong, and the way it was wrong is worth
recording.

Deinterlacing is implemented in Go and has been for some time:
`DeinterlaceMode` with `off`/`auto`/`all`, a `deinterlaceFilter` that emits
`bwdif` and places it first in the chain, a `Deinterlace` field on
`RenditionSpec`, and a `deinterlace` column on the renditions table with its
migration — [internal/ffmpeg/rendition.go:302-360](../internal/ffmpeg/rendition.go),
[internal/db/renditions.go:116](../internal/db/renditions.go).

**There is no control for it in the UI.** `grep -rn deinterlace ui/src` returns
nothing. So an operator cannot switch it on, and the feature is complete in
every layer except the one a user can reach.

That is the same failure this project already documented once, in
[DESIGN-ONE-PORT-ONLY.md](DESIGN-ONE-PORT-ONLY.md): the shared ingest port was
built, tested and documented, defaulted to off, and was published on a port
nothing could reach. *"A feature that is off by default and broken when turned
on is not really a feature."* Deinterlacing is the same shape — built, correct,
and invisible.

The remediation is a select in the rendition editor, next to aspect mode. It is
the cheapest item on the whole roadmap, and it should be measured as done only
when a user can reach it.

## The strategic signal

Two of the four most-reacted open issues are not feature requests at all:

- **"State of Restreamer"** — 31 reactions, 16 comments
- **"Future release plans? (last release September 2024)"** — 11 reactions

That is roughly a fifth of all reaction weight on the open tracker asking
whether the project is alive. Restreamer is well-built and widely deployed, and
its users are visibly looking for signs of maintenance. An actively developed
alternative that imports their mental model does not need to win on features
alone.

## The genuine gaps, ranked by evidence

**1. Overlays — text, logo, watermark, channel name.**
Asked five separate times (6 + 5 + 4 + 2 + 1 reactions across distinct issues),
which makes it the most-repeated unmet request in the tracker. Every instance
was closed without implementation.

This is also where polyemesis can do something neither competitor does.
Overlays elsewhere are a property of *the stream*. Here they could be a property
of *the destination* — a different sponsor card for Twitch than for YouTube, a
"clean" feed with no branding for the archive, a vertical-safe lower-third that
only applies to the 9:16 rendition. Nobody ships per-destination branding.

The cost is real and must be stated: an overlay forces a video re-encode on the
destinations that carry one. polyemesis's central promise is that video is
copied, never touched. Overlays therefore belong on *renditions*, where
re-encoding is already the contract, and a destination with an overlay must
visibly stop being a passthrough. Getting that wrong would quietly undo the
product's main guarantee.

**2. WebRTC/WHIP output.** 6 reactions, closed unimplemented; Media-over-QUIC
raised separately. The honest use case is sub-second *monitoring* — seeing your
own feed without HLS's 10-30s delay — not public playback. Sizeable subsystem.

**3. Decklink / SDI capture, in and out.** 4 + 2 reactions. This is the
broadcast crowd. It needs an FFmpeg built with `decklink`, which Alpine's
package is not, so it implies a third image variant. Real work, narrow audience.

**4. Deinterlacing.** 1 reaction, and a prerequisite for anyone feeding
polyemesis from SDI or legacy broadcast kit. **Already built in Go — the gap is
the UI control, nothing else.** See the correction above. Still the cheapest
item on this list, now by a wider margin.

**5. Playlist / scheduled pre-recorded broadcast.** Asked as "playlist files"
(4), "on demand streaming" (4), "stream from MP4" (2), "virtual input: looping
image or video" (3), "scheduling" (3) — five issues circling one capability:
go live from a file, on a schedule, without an encoder attached. polyemesis has
most of the parts already (pull ingest reads `file://`, schedules exist), so
this may be wiring rather than building.

**6. Multi-input compositing / video grid.** 1 reaction on Restreamer, but it is
the answer to restream.io's Studio, and it becomes natural *after* multi-source
lands: once N sources exist, picture-in-picture and side-by-side are a filter
graph, not an architecture.

## Where restream.io wins today

restream.io is SaaS, so self-hosting beats it on cost, privacy and limits by
construction — their plans cap *simultaneous channels* at 2/3/5/8 by tier, which
is a billing artefact rather than a technical one. What they genuinely have that
polyemesis does not:

| restream.io feature | polyemesis |
|---|---|
| Studio — browser production, remote guests | none; multi-source + compositing is the closest path |
| Pre-recorded upload, go live later | partial (see gap 5) |
| Teams, roles, multiple workspaces | single admin account |
| Hosted chat across platforms | have, self-hosted |
| Live health monitor | have |

Teams/roles is worth noting: polyemesis has exactly one admin identity. Any
multi-operator deployment — a church A/V team, an agency running several
clients — hits that immediately, and multi-source makes it more likely by
letting one install serve several programmes.

## Recommendation

Fold into multi-source (Option A) now, because the data model has to carry them
anyway and retrofitting a key is worse than designing it in:

- **Per-source ingest tokens** — with N sources on one install, "anyone who can
  reach the port can publish to any source" stops being acceptable. This is the
  Core tracker's "Single Token per RTMP Endpoint", and it is nearly free while
  the schema is already being changed.
- **Source-scoped everything** — destinations, renditions, recordings, clips,
  transcripts, chat. The migration is the expensive part; doing it once is much
  cheaper than doing it per-feature later.

Do next, in this order:

1. **Deinterlacing** — hours, not days, and it removes a real blocker.
2. **Per-destination overlays on renditions** — the most-repeated unmet ask, and
   the per-destination angle is genuinely novel. Requires care not to erode the
   passthrough guarantee.
3. **Playlist / scheduled file broadcast** — mostly wiring existing parts.
4. **Compositing** — only once multi-source is settled and in use.

Defer: WebRTC/WHIP, Decklink, MQTT, teams/roles. All defensible, none cheap, and
none blocking the horizontal-plus-vertical case that started this.
