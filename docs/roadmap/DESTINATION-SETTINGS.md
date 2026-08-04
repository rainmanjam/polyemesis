# Per-destination settings and platform metadata

**Status: Parts A–D SHIPPED**, 2026-07-29, except the one item named under
[What remains](#what-remains). The original estimate was ~14–19 days.
**Dependencies: none.** The SDK audit says hand-roll everything, and nothing
since has changed that.

Verified against the tree rather than against the PR titles: `ExpertArgs`,
`MuxQueuePackets`/`MuxQueueBytes` and `RWTimeoutSeconds` for Part A, mono and
codec choice for Part B, `DestResilience.GiveUpAfter`/`MinBackoffSeconds` reaching
`engine.go` for Part C, and `MetadataCaps` on all four providers plus
`PushCompliance` for Part D.

**That check confirmed the functions existed, not that anything called them.**
`PushCompliance` passed exactly this verification for a full release while no
caller anywhere invoked it — see [PLATFORMS.md](../PLATFORMS.md#compliance-metadata)
for the push that now exists. "Verified against the tree" should mean grepping
for a caller, not just a definition; the next person to write that phrase
should check which one they did.

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

## What remains

**Two** items from the parts below are not built, and neither is the one this
section used to name. Both are named here rather than left for somebody to
discover by grepping for a field that does not exist.

| Item | Part | Why it is still open |
|---|---|---|
| **Facebook's remaining metadata surface** | D | Facebook now sends `title`, `description` and `tags` on the composer push, `privacy`, `crossposting_actions` and `donate_button_charity_id` when the broadcast is created, and `privacy` again post-create through the compliance push. `event_params` scheduling is now BUILT — Part E creates the broadcast as `SCHEDULED_UNPUBLISHED` ahead of time so a scheduled show has an event page before it starts. `enable_backup_ingest` is BUILT too — Part F's first slice publishes a redundant feed to Facebook's backup endpoint. Still missing: `stop_on_delete_stream`, spatial audio and 360 projection, and frame-accurate go-live — which FFmpeg cannot do at all (the FLV muxer only suppresses metadata, and `-rtmp_conn` is Connect-message only), so it needs an in-process RTMP relay and its own spec |
| **Frame-accurate go-live** | — | Not metadata, and not Part D. `inband_go_live` swaps the ingest URL and requires the encoder to inject an AMF0 `onGoLive` packet at a chosen frame — FFmpeg's RTMP muxer has no flag for it. See [Facebook and Kick](#facebook-and-kick). Judge it on its own merits; it inherited Part D's estimate by being listed in the same sentence as a set of create-time fields |

**Staggered go-live was listed here and should not have been.** It shipped in
`0c5a08d` on 2026-07-29, in the same commit as the other two Part C items and
named in that commit's own subject line. The next day `80c512c` — a
documentation pass titled *"say what shipped"* — introduced the claim that it
was still open, in three places at once: this table, the Part C heading and the
Part C table. Every reader after that trusted the roadmap over the code, which
is what a roadmap is for and exactly why a wrong one is expensive: the next
person to pick this up would have designed and built a feature that already
existed.

Nothing automated could have caught it. The drift guards in this repo compare
code against code — `db.Settings` against `types.ts`, `scheduler.Action` against
the dropdown — and no guard can check a sentence claiming a feature does not
exist. The only defence is the one that found it: look at what the work would
touch before deciding how to do it.

**And it happened again, in miniature, two days later.** The docs pass for the
Facebook metadata work corrected `../PLATFORMS.md` and left this table saying
Facebook sent "title and description only" — because that task was deliberately
scoped to one file to avoid colliding with the pull request fixing the paragraph
above. Avoiding the conflict is what created the staleness. The rule that keeps
surviving contact is the one that task's own brief carried and its scope then
prevented: read every document that describes the thing, not the one you are
editing.

Also deferred as a separate feature: **per-destination stored broadcast
defaults**, so a destination remembers its own title and category rather than the
composer starting empty each time.

## Part A — Transport and muxer (SHIPPED)

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

## Part B — Audio encoding (SHIPPED, with one item refuted)

> **`AAC profile` is NOT BUILDABLE — refuted by probe.**
>
> FFmpeg's native `aac` encoder exposes **no `-profile` option at all**, and
> `-profile:a aac_he` makes it refuse to open outright:
>
> ```
> [aac] Profile not supported!
> ```
>
> producing no output whatever rather than falling back. HE-AAC needs
> `libfdk_aac`, which is nonfree and cannot ship in a redistributable build.
>
> **The goal is met a different way.** What the item actually wanted was good
> audio well below 64 kbps. Opus does that better than HE-AAC, is free, and is
> already in the pinned build — so "codec choice" below answers it, and the
> profile selector is dropped rather than deferred.
>
> **A second probe result worth keeping:** FFmpeg *will* write Opus into FLV —
> it produced a valid 8.6 KB file — because Enhanced RTMP defines a mapping. No
> mainstream ingest accepts it. Opus is therefore refused on RTMP at save time,
> because a stream that muxes cleanly, uploads cleanly and is rejected by the
> platform looks correct everywhere the operator can see.

| Setting | Status |
|---|---|
| ~~AAC profile~~ | **refuted** — needs nonfree libfdk_aac |
| **Mono output** | shipped |
| **Codec choice** (AAC / Opus, non-RTMP) | shipped |

Mono turned out not to be the awkward one. It is a **downmix of the operator's
mix**, not a re-route: the routing matrix still produces `OutL`/`OutR` and
`-ac 1` sums them. Wiring individual tracks to a single channel would be a
change to the matrix, and that is a different feature.

## Part C — Resilience (SHIPPED)

| Setting | Was | Now |
|---|---|---|
| Per-destination reconnect policy | One global `reconnectDelayMaxSeconds`, and it was for **pull ingest**, not destinations | `DestResilience.MinBackoffSeconds`/`MaxBackoffSeconds`, per destination |
| Give-up threshold plus alert | Retried indefinitely; a destination retrying forever looked identical to one that works | `GiveUpAfter`, counted on **consecutive** failures so a destination that reconnects hourly for a week never accumulates its way to the limit |
| Staggered go-live | All destinations connect at once, spiking CPU and upstream | `Destinations.StaggerMS`, install-wide, 0 to disable and capped at 5 s. Counted per process actually started, so a sweep that starts one destination beside seven healthy ones does not make it wait seven slots — and it never delays a RECONNECT, because a destination that drops at 3 am has to come back immediately rather than queue behind processes that are fine |

## Part D — Metadata (~7–12 days, and the real gap)

polyemesis has **three** metadata fields — `title`, `description`, `category`.
The platforms document far more, and some are not optional in the legal sense.

### The compliance items

| Field | Platform | Why it is not a nicety |
|---|---|---|
| `selfDeclaredMadeForKids` | YouTube | COPPA declaration. Editable and stored for a full release before anything sent it; now pushed — see [PLATFORMS.md](../PLATFORMS.md#compliance-metadata) |
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

Facebook's surface is the largest. Re-verified against the v26.0 reference on
2026-08-03, and the list holds — `content_tags`, `enable_backup_ingest`,
`stop_on_delete_stream`, crossposting via `crossposting_actions` on the Page
edge, audience targeting, spatial audio, and 360 (`is_spherical`, `projection`,
`stereoscopic_mode`, `encoding_settings`, and the fisheye fields).
`overlay_url` is confirmed removed for v24.0+.

**Two of the things this sentence used to list are not metadata, and saying so
is the point of this paragraph.** Both were named inline with the create-time
fields, which is what made Part D's estimate look like one piece of work.

**Scheduling is not a field.** `planned_start_time` is gone; a scheduled
broadcast is `POST /<ID>/live_videos?status=SCHEDULED_UNPUBLISHED&event_params=
<UNIX_TS>`, rescheduled with `POST /<LIVE_VIDEO_ID>?event_params=<UNIX_TS>`.
**Facebook accepts a start time at most seven days out**, and that bound is not
ours to widen.

**It collides with far less than it first appears, and the correction matters
because it decides the design.** This document previously said a weekly schedule
could name a time Facebook would refuse. It cannot. `internal/scheduler` has
three kinds — `once`, `daily`, `weekly` — and the *next occurrence* of a daily
schedule is at most a day away, of a weekly one at most seven days by
definition. **Only a `once` schedule can be set beyond the window.**

That kills "clamp" outright. Silently moving a broadcast's start time is the
worst option available, and it was only ever needed for a case that does not
exist.

So: **warn on a `once` schedule more than seven days out, at save time, naming
the limit.** Daily and weekly need no special handling at all.

**This said "refuse" until the design was written, and two things changed it.**
The schedule works either way — what the bound limits is the pre-announced event
page, not the go-live path — so refusing a working configuration buys nothing.
And the check cannot be made consistently: `Schedule.DestinationIDs` is empty for
"every destination", which is what "start the show" usually means, so a
save-time refusal cannot tell whether a Facebook destination is involved and
would be silently stricter for schedules that name their targets than for those
that do not.

And refusing costs less than it sounds, because **the schedule still works**.
What is bounded is only the pre-announced Facebook broadcast — a discovery
feature, not the go-live path. The destination still goes live at the scheduled
time; there is simply no Facebook event page for it until someone is inside the
window. If a distant one-off ever needs one, the upgrade is to create the
broadcast when the occurrence enters the seven-day window, using the sweep the
scheduler already runs — which needs a per-occurrence "already created" marker,
and that is why it is not the first version.

There is also an eligibility gate that has nothing to do with our code: the
account must be 60+ days old and the Page or professional profile needs 100+
followers.

**`inband_go_live` is ingest work, not metadata.** It is a nested field modifier
on a GET whose side effect reconfigures the broadcast and returns **a different
ingest URL**:

```http
GET /<LIVE_VIDEO_ID>?fields=secure_stream_url.inband_go_live(require_inband_signal)
```

Create with `status=PREVIEW`, issue that GET, discard the URL you were given at
creation, stream to the new one. The broadcast becomes visible only once the
status is `LIVE` **and** the encoder emits a go-live message inside the RTMP
stream: an AMF0 packet (type `0x12`) carrying the string `onGoLive` and an ECMA
array whose single pair is `timestamp` → the timestamp of the first publicly
visible frame.

So it means re-pointing a running FFmpeg output at a replacement URL and
injecting a hand-built AMF0 packet at a chosen frame, which FFmpeg's RTMP muxer
has no flag for. Different layer, different risk, and it should be judged on its
own rather than inheriting a metadata estimate.

It is also the one field here that the Graph API **reference** does not document
at all — it appears in none of the User, Page or Group `live_videos` parameter
tables, only in the [Broadcasting
guide](https://developers.facebook.com/docs/live-video-api/guides/streaming/)
under "Frame-Accurate Go-Live". Meta's own `LiveVideo` node reference currently
404s. Verifying it meant reading the guide, not the reference, which is why it
survived this long as a one-word claim.

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
- [../PLATFORMS.md](../PLATFORMS.md) — the Kick correction landed; see its
  [Compliance metadata](../PLATFORMS.md#compliance-metadata) section for where
  Part D's compliance fields actually go now
- [../DEPENDENCIES.md](../DEPENDENCIES.md) — the bar the SDKs were measured against
