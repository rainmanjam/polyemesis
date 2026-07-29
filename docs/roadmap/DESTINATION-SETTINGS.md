# Per-destination settings and platform metadata

**Status:** researched, not started.
**Effort:** ~14–19 days total, in four independently shippable parts.
**Dependencies: none.** The SDK audit says hand-roll everything.

Grounded in the platforms' own API references and in probe builds, then handed
to an adversarial reviewer. **Two of the ideas that motivated this document did
not survive**, and both are recorded below rather than quietly dropped.

---

## Two findings that change decisions

### 1. YouTube multitrack live audio does not exist — REFUTED

This was going to be the headline: polyemesis is the only tool in its category
that already holds several distinct audio mixes per destination, so sending
YouTube several language tracks from one ingest looked like the differentiator
applied to the one platform that could receive it.

**It cannot receive it.** Verified against Google's own documentation:

- **Multiple audio streams in one live ingest — explicitly refused.** The
  `liveStreams` resource documents an ingest-validator issue type
  `multipleAudioStreams`: *"The ingestion stream contains multiple audio
  streams, but it must only contain one audio stream."* YouTube has a
  health-check error for exactly this attempt.
- **HLS ingest says it twice.** *"The supported audio codec is AAC, and only
  single-track audio is supported."*
- **N separate ingests joined into one broadcast — also refused.**
  `contentDetails.boundStreamId` is a **singular scalar**, `liveBroadcasts.bind`
  takes one `streamId`, and omitting it *removes* the binding rather than adding
  one. The documented cardinality is the inverse: one stream may serve many
  broadcasts.
- **The multi-language feature is upload/VOD only** — Studio-only, file-based,
  and gated to creators with Advanced features.

**The near-miss that will mislead somebody.** YouTube's encoder-settings page
says *"5.1 surround sound audio is only supported for AAC in RTMP/RTMPS."* That
is **one stream carrying six channels**, not six tracks. There is no per-channel
language selector in the player, so packing languages into discrete 5.1 channels
produces one garbled surround mix, not selectable languages. Do not let "5.1 is
supported" get read as "multitrack is supported".

**Do not build this.** It is dead on all four possible mechanisms.

### 2. Kick's stream key IS retrievable — this repo is wrong in three places

[PLATFORMS.md](../PLATFORMS.md) states, in the capability matrix and twice in
prose, that Kick's stream key is absent from every published endpoint and must
be pasted by hand.

**It is returned as `stream.key` on `GET /public/v1/channels`,** and Kick's OAuth
defines a `streamkey:read` scope for it.

Two subtleties that make the original conclusion understandable:

- There is **no dedicated stream-key endpoint**. The key is a side-effect field
  on the channels response, not `/streamkey` or similar — which is why an
  endpoint-by-endpoint reading missed it.
- The Get Channels page lists only `channel:read` under "Required scopes".
  **`channel:read` does not cover the key**; `streamkey:read` must be requested
  separately, and polyemesis does not currently request it.

This turns Kick from *"sign in **and** paste a key"* into a fully automated
platform, and it removes a documented limitation from the README, the capability
matrix and the comparison doc. **Highest value-per-day item in this document.**

---

## The SDK question: hand-roll everything

Every count below is `comm -23` of a probe build's linked module set against
polyemesis's measured 22 linked modules — not a README number. All five probes
cross-compile clean with `CGO_ENABLED=0` on all four shipping targets, so the
project's own constraints eliminate nobody. The decision rests on module count
and on whether hand-rolling is the bigger cost.

| Candidate | Net-new modules | Verdict |
|---|---|---|
| `google.golang.org/api/youtube/v3` | **20** | **Reject** |
| `github.com/nicklaw5/helix/v2` (Twitch) | 2 | Reject on surface, not cost |
| `github.com/huandu/facebook` | 1 | Reject on surface, not cost |
| `github.com/glichtv/kick-sdk` | 1 | Reject on surface, not cost |
| `golang.org/x/oauth2` | 1–2 | Unverified — the one open question |

**The rumour about `google.golang.org/api` is true, and specifically for
`youtube/v3`.** Twenty net-new modules including gRPC, protobuf, five
OpenTelemetry modules, and `go-logr/stdr` — *a second logging abstraction in a
binary that standardised on `log/slog`*, which is verbatim the objection that
killed `yutopp/go-rtmp` over logrus.

And it is **not escapable**: a probe using only
`option.WithHTTPClient(http.DefaultClient)` links an identical 25-module set.
There is no lean path.

Real in-repo cost, measured by adding it to a full copy of polyemesis:
**+11.9 MB on a 25.4 MB binary (+46.7%)** to replace
`internal/oauth/youtube.go`, which is **175 lines**.

### What the review corrected in its own audit

Worth recording, because the errors all pointed the same way:

- **Every binary-size figure was inflated**, because they were measured against
  an `fmt`-only hello world rather than against polyemesis, which already links
  `net/http`, `crypto/tls` and `encoding/json`. Real deltas: helix
  **+128 KB (+0.5%)**, not +6.4 MB — overstated ~50×. `huandu/facebook`
  **+156 KB (+0.6%)**, not +6.5 MB — overstated ~42×.
- **Two rejections rested on misapplied precedent.** `DEPENDENCIES.md`'s *"do
  not let anything pull the old path back in"* refers to the **abandoned
  `dgrijalva/jwt-go`**, not to `golang-jwt/jwt/v4`, a maintained sibling major of
  the same org. helix is not disqualified on that basis.

So the small SDKs are rejected on **surface** — they wrap three or four calls
polyemesis already makes correctly in a few hundred lines — not on cost. That is
a much weaker rejection than the audit first claimed, and if any of them later
covers something hand-rolling cannot, the door is open.

**The one genuinely open question is `golang.org/x/oauth2`.** Its `TokenSource`
refresh-and-cache concurrency handling is easy to get subtly wrong by hand, and
polyemesis's existing refresh path was not audited for those races. That is worth
checking on its own merits, separately from this work.

---

## Part A — Transport and muxer (~2 days)

**Probe the flag before designing around it.** A third of the first draft of this
section did not survive `ffmpeg -h`:

| First draft said | FFmpeg 8.1.2 actually says |
|---|---|
| `-rtmp_live live` is a missing destination setting | Flagged `.D.........` — **demuxer/input only.** Cannot be set on an output |
| `-max_muxing_queue_size` guards audio/video interleave divergence | *"packets buffered while waiting for all streams to **initialize**"* — stream init, not steady state. The steady-state knob is `-muxing_queue_data_threshold` |
| `-flvflags no_duration_filesize` | Confirmed `E..........`, genuinely available |

What survives, as an optional advanced block per destination:

| Setting | Why |
|---|---|
| `-flvflags no_duration_filesize` | Some RTMP ingests choke on the duration field |
| `-muxing_queue_data_threshold` | The audio path has variable latency — `loudnorm` has lookahead — so interleave genuinely can diverge here |
| Output `-rw_timeout` | A half-open TCP connection currently hangs until the supervisor notices |
| SRT `latency` / `passphrase` / `pbkeylen` | Already works by typing it into the URL. The gap is **validation and discoverability**, not capability |

Note the SRT row: these are reachable today, so this is a UI and validation task,
which makes it the cheapest and least urgent part.

## Part B — Audio encoding (~3 days)

| Setting | Today | Why |
|---|---|---|
| **AAC profile** (LC / HE-AAC v1 / v2) | fixed LC | Meaningfully better below ~64 kbps; matters for audio-only destinations |
| **Mono output** | stereo always | Talk content halves its audio bitrate for no perceptual loss |
| **Codec choice** | AAC fixed for video destinations | Icecast already picks its own; SRT receivers may want Opus |

Mono is the awkward one: the routing model has exactly `OutL`/`OutR`, so mono is
a change to the matrix, not a flag.

## Part C — Resilience (~2 days)

| Setting | Today |
|---|---|
| Per-destination reconnect policy | One global `reconnectDelayMaxSeconds`, and it is for **pull ingest**, not destinations |
| Give-up threshold plus alert | Retries indefinitely; a destination retrying forever looks identical to one that works |
| Staggered go-live | All destinations connect at once, spiking CPU and upstream |

## Part D — Metadata (~7–12 days, and the real gap)

polyemesis has **three** metadata fields — `title`, `description`, `category`.
The platforms document far more, and some are not optional in the legal sense.

### The compliance items

| Field | Platform | Why it is not a nicety |
|---|---|---|
| `selfDeclaredMadeForKids` | YouTube | COPPA. Currently unsettable |
| `content_classification_labels` | Twitch | Twitch *requires* labels for mature games, sexual themes, drugs, gambling, violence |
| `privacyStatus` | YouTube | Going live publicly by accident is unrecoverable |

### YouTube — the traps that will break a go-live

- **`liveBroadcasts.update` requires four properties on every call**: `id`,
  `snippet.scheduledStartTime`, `contentDetails.monitorStream.enableMonitorStream`
  and `.broadcastStreamDelayMs`. A partial update fails.
- **It is destructive by *part*, not by field.** Sending `part=status` without
  `privacyStatus` *"will remove the existing privacy setting and revert to the
  default"*. A naive PATCH-shaped implementation can make a private broadcast
  public.
- **Most `contentDetails` toggles freeze once the broadcast leaves
  created/ready** — documented 403s including `enableDvrModificationNotAllowed`
  and `enableAutoStartModificationNotAllowed`. Metadata has a *window*, and the
  UI must reflect that or every edit after go-live silently fails.
- **`selfDeclaredMadeForKids` is settable on `insert` but absent from `update`'s
  settable list** — it has to be set through `videos.update` afterwards.
- Category and tags are **not on the broadcast** — they are `videos.update`
  `snippet.categoryId` / `snippet.tags[]` against the broadcast id.

### Twitch — the write shape differs from the read shape

CCLs read back as a flat list but are **written** as
`[{"id":"Gambling","is_enabled":true}]`. Copying the read shape into a write
produces a broken go-live.

The writable ids are exactly: `DebatedSocialIssuesAndPolitics`,
`DrugsIntoxication`, `SexualThemes`, `ViolentGraphic`, `Gambling`,
`ProfanityVulgarity`. **`MatureGame` is readable but not writable.**

Twitch also exposes `delay` (stream buffer) and `is_branded_content`, neither of
which polyemesis can set.

### Facebook and Kick

Facebook's surface is the largest — `content_tags`, `enable_backup_ingest`,
`stop_on_delete_stream`, crossposting, audience targeting, spatial audio, 360
projection, and a frame-accurate `inband_go_live`. `planned_start_time` is gone;
scheduling now goes through `event_params`. `overlay_url` is confirmed removed.

**Kick's entire metadata surface is three fields** — `stream_title`,
`category_id`, `custom_tags` — via `PATCH /public/v1/channels`. No description,
no thumbnail, no scheduling.

---

## Recommended order

| | Work | Effort | Why here |
|---|---|---|---|
| 1 | **Kick stream key** + doc corrections | **0.5 d** | Removes a documented limitation and three wrong claims. Needs only the `streamkey:read` scope |
| 2 | Compliance metadata — made-for-kids, privacy, CCLs | 3–4 d | The only items with a legal or policy edge |
| 3 | Part A transport, Part C resilience | 4 d | Independent, low risk |
| 4 | Remaining YouTube metadata + the update-window UI | 4–6 d | Largest, and the traps above make it the easiest to get subtly wrong |
| 5 | Part B audio encoding | 3 d | Mono touches the routing matrix |

**Do not build:** YouTube multitrack live audio. See above.

---

## See also

- [ROADMAP](README.md)
- [../PLATFORMS.md](../PLATFORMS.md) — needs the Kick correction
- [../DEPENDENCIES.md](../DEPENDENCIES.md) — the bar the SDKs were measured against
