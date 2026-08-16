# Platform broadcast lifecycle and operations, checked 2026-08-16

Gathered by an independent research pass with a source URL required for every
claim, then re-verified by a second adversarial pass on 2026-08-16 that tried to
break each finding before it was recorded here. Where the second pass refuted a
sub-claim, the refutation is written in place — the wrong version is not
silently deleted, because code may already lean on it.

## What may be relied on

Every claim must trace to a **dated, citable source** — for an API, that means
the platform's own reference documentation, fetched on the date in the title,
with the operative sentence quoted verbatim.

Two kinds of source qualify:

1. **This file.** Each cell below carries a URL and was read on 2026-08-16.
2. **A primary source, quoted.** A sentence from the platform's own reference
   page, reproduced with its URL and the date it was read.

**A PAGE-LEVEL PERMISSIONS LIST IS NOT A SOURCE FOR A PER-ENDPOINT SCOPE CELL.**
This is the API version of the footer rule from the competitor file: Facebook's
Live Video API index says "Most endpoints require a mix of the following
permissions" and defers to per-endpoint reference pages. Where the per-endpoint
page is unreadable, the scope is recorded as *inferred*, not documented, and the
table says so.

**AN HTTP STATUS CODE IS NOT A SOURCE EITHER.** Two traps were found and
reproduced during this pass, and any future re-fetch must defend against both:

* `docs.kick.com` returns **HTTP 200 with a GitBook "Page Not Found" body** on
  `.md` URLs for pages that do not exist. A 200 on a Kick `.md` URL proves
  nothing until the body is read.
* `developers.facebook.com` returns **HTTP 200 with a ~138 KB "Page Not Found -
  Meta for Developers" body** on the `.md` renderings of several Graph edge
  references. A naive fetcher will silently conclude "the field is absent" from
  a page that was never served.

What remains forbidden is the thing that matters: an endpoint, scope, limit, or
error behaviour asserted with **no** dated source. A quota figure from memory, a
scope someone assumed, a numeric limit invented because the docs refused to give
one. Several such claims from the first research pass were caught and are
withdrawn by name below; do not resurrect them.

**NUMERIC LIMITS THE DOCS REFUSE TO STATE MUST STAY UNSTATED IN CODE.** YouTube
documents *that* concurrent broadcasts are capped but not *at what number*;
Twitch documents a commercial cooldown only via `retry_after`; Kick publishes no
request budget at all. Hardcoding a guessed number is the exact overclaim this
file exists to prevent — handle the refusal error instead.

---

## YouTube (Data API v3, live endpoints)

| capability | verdict | endpoint | scope | source |
|---|---|---|---|---|
| broadcast create | documented | `POST https://www.googleapis.com/youtube/v3/liveBroadcasts` | `youtube` OR `youtube.force-ssl` | [liveBroadcasts/insert](https://developers.google.com/youtube/v3/live/docs/liveBroadcasts/insert), read 2026-08-16 |
| transition → testing | documented | `POST .../liveBroadcasts/transition?broadcastStatus=testing&id=<id>&part=<parts>` | `youtube` OR `youtube.force-ssl` | [liveBroadcasts/transition](https://developers.google.com/youtube/v3/live/docs/liveBroadcasts/transition), read 2026-08-16 |
| transition → live | documented | `POST .../liveBroadcasts/transition?broadcastStatus=live&...` | `youtube` OR `youtube.force-ssl` | same page |
| transition → complete | documented | `POST .../liveBroadcasts/transition?broadcastStatus=complete&...` | `youtube` OR `youtube.force-ssl` | same page |
| bind broadcast to stream | documented | `POST .../liveBroadcasts/bind` | `youtube` OR `youtube.force-ssl` | [liveBroadcasts/bind](https://developers.google.com/youtube/v3/live/docs/liveBroadcasts/bind), read 2026-08-16 |
| stream create (yields ingestion key) | documented | `POST .../liveStreams` | `youtube` OR `youtube.force-ssl` | [liveStreams/insert](https://developers.google.com/youtube/v3/live/docs/liveStreams/insert), read 2026-08-16 |
| schedule via scheduledStartTime | documented | `POST .../liveBroadcasts` (body: `snippet.scheduledStartTime`) | `youtube` OR `youtube.force-ssl` | [liveBroadcasts resource](https://developers.google.com/youtube/v3/live/docs/liveBroadcasts), read 2026-08-16 |
| stream health readback | documented | `GET .../liveStreams?part=status&id=<streamId>|mine=true` | `youtube.readonly` OR `youtube` OR `youtube.force-ssl` | [liveStreams resource](https://developers.google.com/youtube/v3/live/docs/liveStreams) + [liveStreams/list](https://developers.google.com/youtube/v3/live/docs/liveStreams/list), read 2026-08-16 |
| thumbnail set | documented | `POST https://www.googleapis.com/upload/youtube/v3/thumbnails/set` | `youtubepartner` OR `youtube.upload` OR `youtube` OR `youtube.force-ssl` | [thumbnails/set](https://developers.google.com/youtube/v3/docs/thumbnails/set), read 2026-08-16 |
| VOD privacy (videos.update) | documented | `PUT .../videos` | `youtubepartner` OR `youtube` OR `youtube.force-ssl` (NOT `youtube.upload`) | [videos/update](https://developers.google.com/youtube/v3/docs/videos/update), read 2026-08-16 |
| add VOD to playlist | documented | `POST .../playlistItems` | `youtubepartner` OR `youtube` OR `youtube.force-ssl` | [playlistItems/insert](https://developers.google.com/youtube/v3/docs/playlistItems/insert), read 2026-08-16 |
| viewer stats (concurrent viewers) | documented | `GET .../videos?part=liveStreamingDetails&id=<videoId>` | **unknown — see caveats** | [videos resource](https://developers.google.com/youtube/v3/docs/videos), read 2026-08-16 |

### Caveats an implementer must carry

**Create.** Required body fields, quoted verbatim from the insert reference:
"You must specify a value for these properties: snippet.title /
snippet.scheduledStartTime / status.privacyStatus". The insert page carries
**no "Quota impact" line** — confirmed by targeted search of the page and by an
independent pass against the quota calculator. There is no basis for assuming
50 units; the cost is unknown. Refusals on
[/youtube/v3/live/docs/errors](https://developers.google.com/youtube/v3/live/docs/errors)
(read 2026-08-16) include `insufficientPermissions/liveStreamingNotEnabled`
("The user that authorized the request is not enabled to stream live video on
YouTube"), `invalidValue(400)` on `invalidScheduledStartTime`,
`invalidScheduledEndTime` ("The scheduled end time must follow the scheduled
start time"), `invalidPrivacyStatus`, `invalidDescription` (description ≤ 5000
chars), plus `invalidAutoStart`, `invalidAutoStop`, `invalidEmbedSetting`,
`invalidLatencyPreferenceOptions`, `invalidProjection`.

**Transitions are a server-enforced state machine.** The literal required query
parameter is `broadcastStatus`. Both preconditions for `testing` come from the
same required-parameter cell: monitor stream must be enabled
("contentDetails.monitorStream.enableMonitorStream property is set to true")
and "the status.streamStatus must be active for the stream that the broadcast
is bound to." Refusals, verbatim: `forbidden(403)/errorStreamInactive` ("The
requested transition is not allowed when the stream that is bound to the
broadcast is inactive"), `forbidden(403)/invalidTransition` ("The live
broadcast can't transition from its current status to the requested status"),
`forbidden(403)/redundantTransition`.

**Concurrency ceilings refuse at TRANSITION, not at create**, and **neither
numeric limit appears anywhere in the docs** — treat both as unknown values,
never as "no limit". Verbatim: `rateLimitExceeded(403)/concurrentBroadcastsExceedLimit`
("The channel already has the maximum number of concurrent live broadcasts. One
or more broadcasts that are already live must be stopped before another
broadcast can start on the channel.") and
`rateLimitExceeded(403)/sharedIngestionBroadcastsExceedLimit`. Also
`rateLimitExceeded(403)/userRequestsExceedRateLimit`.

**WITHDRAWN CLAIM — do not build on it.** The first pass asserted
`required(400)/idRequired` and `required(400)/statusRequired` on transition,
and a naming discrepancy between "status" and "broadcastStatus" across pages.
The re-read of the transition error table found **no `required(400)` row of any
kind** for this method. Do NOT write retry logic keyed off `statusRequired`.
What does hold: `backendError/errorExecutingTransition` ("An error occurred
while changing the broadcast's status") — the retryable-looking failure.

**Bind cardinality is asymmetric and load-bearing for multi-destination
design**: "A broadcast can only be bound to one video stream, though a video
stream may be bound to more than one broadcast." One stream → many broadcasts
is allowed; the reverse is not. `streamId` is NOT a required parameter —
omitting it is how a binding is removed ("or removes an existing binding").
Binding is state-gated: `forbidden(403)/liveBroadcastBindingNotAllowed` ("The
current status of the live broadcast does not allow it to be bound to a
stream").

**Stream create.** `cdn` object is mandatory (`required(400)/cdnRequired`:
"The liveStream resource must contain the cdn object"), and `cdn.resolution` /
`cdn.frameRate` must be set together or omitted together — the paired refusals
`frameRateRequired` and `resolutionRequired` are quoted verbatim in both
directions on the errors page. A third combination exists: `cdn.resolution:
"variable"` "must also set cdn.frameRate to variable". Title 1–128 chars,
description ≤ 10000 chars.

**Scheduling.** `snippet.scheduledStartTime` read back as **Unix epoch zero
means "unscheduled", NOT "scheduled for 1970"**, and "this value cannot be
changed using the API or in Creator Studio" — one continuous passage in the
property table. The start-time horizon is bounded by
`invalidScheduledStartTime` ("must be in the future and close enough to the
current date...") — **the doc gives no numeric horizon; do not hardcode one.**
`scheduledEndTime` omitted means "the broadcast is scheduled to continue
indefinitely."

**Health readback is TWO fields, and conflating them is a bug.**
`status.streamStatus` (active/created/error/inactive/ready) is ingest liveness;
`status.healthStatus.status` (good/ok/bad/noData) is quality. Only
`streamStatus == "active"` satisfies the transition precondition; healthStatus
is advisory. **Ordering trap, verbatim from the doc: `good` is BETTER than
`ok`** — "good – There are no configuration issues for which the severity is
warning or worse. ok – There are no configuration issues for which the
severity is error." The reverse of the usual convention. A poller can hold the
strictly narrower `youtube.readonly` grant — confirmed directly on the
liveStreams.list Authorization table. `maxResults` "Acceptable values are 0 to
50, inclusive. The default value is 5."

**Thumbnails.** The ONLY media-upload host path in this set:
`/upload/youtube/v3/...`, not `/youtube/v3/...`. Max 2 MB; image/jpeg,
image/png, application/octet-stream; "quota cost of approximately 50 units."
`youtube.upload` IS accepted here — thumbnails need not force a full `youtube`
grant. The errors page carries **two distinct `forbidden(403)/forbidden` rows**
— one generic, one a channel-eligibility gate ("The authenticated user doesn't
have permissions to upload and set custom video thumbnails") — and they cannot
be told apart by error code alone. `tooManyRequests(429)/uploadRateLimitExceeded`
has no documented numeric rate.

**videos.update is DESTRUCTIVE, confirmed from two places independently.** "If
your request does not specify a value for a property that already has a value,
the property's existing value will be deleted" and, on the `part` parameter:
"If the request body does not specify a value, the existing privacy setting
will be removed and the video will revert to the default privacy setting." A
privacy-only change must send `part=status` ALONE and must NOT include
`snippet`, or title/description/tags are wiped; if `snippet` is sent,
`snippet.title` and `snippet.categoryId` become required. `status.publishAt`
requires `privacyStatus=private` and "the video has never been published."
`youtube.upload` is NOT accepted here, unlike thumbnails.set — verified by
reading both scope tables side by side. Quota: no inline "Quota impact" line
surfaced on the page; the 50-unit figure rests on the quota calculator only.

**playlistItems.insert is NOT idempotent.** A retried insert errors:
`duplicate/videoAlreadyInPlaylist` ("The video that you are trying to add to
the playlist is already in the playlist.") — this changes retry logic. Cost is
a documented "50 units". The Uploads playlist is not a valid target
(`playlistOperationUnsupported`); `playlistContainsMaximumNumberOfVideos` has
no documented numeric limit; `manualSortRequired` is fixed "by removing the
snippet.position element". **WITHDRAWN CLAIMS:** `playlistIdRequired` /
`channelIdRequired` / `resourceIdRequired` refusals and a
"contentDetails.note max 280 chars" limit could not be surfaced — do not build
validation against them. **TIMING RISK UNRESOLVED:** nothing documents how
soon after a broadcast completes its archive video ID becomes a valid target.
Expect `videoNotFound` on an eager call; retry is an engineering necessity,
not a documented guarantee.

**Viewer stats: a missing key is "unknown", NEVER zero.**
`liveStreamingDetails.concurrentViewers` is absent under three conditions, all
in one documented passage: no current viewers; owner has hidden the viewcount;
after the broadcast ends. An ended broadcast and a zero-viewer broadcast are
indistinguishable. **The videos.list page documents NO Authorization/scope
section** — unique in this set; the "unknown" scope cell is deliberate and
survived a refutation attempt. Do not invent a scope; verify against a live
request. Cost is a documented 1 unit, but the project-wide budget governs any
polling loop, quoted verbatim from
[determine_quota_cost](https://developers.google.com/youtube/v3/determine_quota_cost)
(read 2026-08-16): "Projects that enable the YouTube Data API have a default
quota allocation of 100 search.list calls, 100 videos.insert calls, and 10,000
units per day combined for all other endpoints ... Daily quotas reset at
midnight Pacific Time (PT)." and "All API requests, including invalid
requests, incur a quota cost of at least one point." **At 1 unit per poll, a
2-second poll on one broadcast exhausts the entire shared 10,000-unit ceiling
in under 6 hours.**

---

## Twitch (Helix)

| capability | verdict | endpoint | scope | source |
|---|---|---|---|---|
| start/stop a broadcast | **absent** | none | none | [Helix reference](https://dev.twitch.tv/docs/api/reference/), full enumeration 2026-08-16 |
| ad break / commercial | documented | `POST https://api.twitch.tv/helix/channels/commercial` | `channel:edit:commercial` | [Start Commercial](https://dev.twitch.tv/docs/api/reference/#start-commercial), read 2026-08-16 |
| stream markers | documented | `POST https://api.twitch.tv/helix/streams/markers` | `channel:manage:broadcast` | [Create Stream Marker](https://dev.twitch.tv/docs/api/reference/#create-stream-marker), read 2026-08-16 |
| clip creation | documented | `POST https://api.twitch.tv/helix/clips` | `clips:edit` | [Create Clip](https://dev.twitch.tv/docs/api/reference/#create-clip), read 2026-08-16 |
| stream health / Get Streams | documented (liveness only) | `GET https://api.twitch.tv/helix/streams` | none — "Requires an app access token or user access token" | [Get Streams](https://dev.twitch.tv/docs/api/reference/#get-streams), read 2026-08-16 |
| polls | documented | `POST https://api.twitch.tv/helix/polls` | `channel:manage:polls` | [Create Poll](https://dev.twitch.tv/docs/api/reference/#create-poll), read 2026-08-16 |
| predictions | documented | `POST https://api.twitch.tv/helix/predictions` | `channel:manage:predictions` | [Create Prediction](https://dev.twitch.tv/docs/api/reference/#create-prediction), read 2026-08-16 |
| raid | documented | `POST https://api.twitch.tv/helix/raids` | `channel:manage:raids` | [Start a raid](https://dev.twitch.tv/docs/api/reference/#start-a-raid), read 2026-08-16 |

### Caveats an implementer must carry

**Commercials are broadcaster-only and cooldown-gated by response data, not by
a published number.** Verbatim: "Only partners and affiliates may run
commercials and they must be streaming live at the time"; "Only the broadcaster
may start a commercial; the broadcaster's editors and moderators may not start
commercials on behalf of the broadcaster"; "The broadcaster may not run
another commercial until the cooldown period expires. The retry_after field in
the previous start commercial response specifies the amount of time the
broadcaster must wait between running commercials" — this sentence appears in
BOTH the 400 and 429 rows. **`retry_after` must be persisted across calls, not
hardcoded** — the section carries no per-endpoint Rate Limit line. `length` is
advisory: "Twitch tries to serve a commercial that's the requested length, but
it may be shorter or longer"; max 180 seconds, and longer requests are clamped
to 180.

**Markers, unlike commercials, allow EDITORS.** Verbatim from the 403 row:
"The user in the access token must own the video or they must be one of the
broadcaster's editors." Preconditions verbatim: "You may not add markers: If
the stream is not live. If the stream has not enabled video on demand (VOD).
If the stream is a rerun of a past broadcast."

**Clip creation is asynchronous — a 202 does not mean a clip exists.**
Verbatim: "Creating a clip is an asynchronous process that can take a short
amount of time to complete. To determine whether the clip was successfully
created, call Get Clips using the clip ID that this request returned. ... If
after 60 seconds Get Clips hasn't returned the clip, assume it failed."
Downstream code MUST poll Get Clips with a 60-second failure deadline. The
`edit_url` "is valid for up to 24 hours or until the clip is published,
whichever comes first." Refusals include follower/subscriber-restricted clips,
clips disabled on the channel, "The category is not clippable", and "The
title did not pass AutoMod checks". A separate endpoint exists: `POST
/helix/videos/clips` (Create Clip From VOD, flagged NEW; scopes
`editor:manage:clips` / `channel:manage:clips`; duration 5–60 s, default 30).

**Get Streams reports liveness and metadata, never encoder health.** The word
"bitrate" appears **zero** times in the entire 1.4 MB reference page; no
framerate, dropped-frame, resolution, ingest or health field exists on Get
Streams. Pagination warning, verbatim: "Because viewers come and go during a
stream, it's possible to find duplicate or missing streams in the list as you
page through the results."

**The global rate limit IS documented — a prior "unverified" hedge was false
and is corrected here.** The [API guide](https://dev.twitch.tv/docs/api/guide/)
(fetched 2026-08-16) section "Twitch Rate Limits": "Twitch uses a token-bucket
algorithm... Each endpoint is assigned a points value (the default points
value per request for an endpoint is 1)... If your bucket runs out of points
within 1 minute, the request returns status code 429", with separate buckets
per app and user token and headers `Ratelimit-Limit` / `Ratelimit-Remaining` /
`Ratelimit-Reset` (documented example value 800). **Read the headers; do not
hardcode 800.**

**Polls and predictions are singletons with no scheduling.** "The poll begins
as soon as it's created. You may run only one poll at a time." / "The
prediction runs as soon as it's created. The broadcaster may run only one
prediction at a time" — and the explicit refusal: 400 "The broadcaster
already has a prediction that's running; you may not create another prediction
until the current prediction is resolved or canceled." No queueing. Poll
shape: title ≤ 60 chars, 2–5 choices, choice title ≤ 25 chars. **Bits voting
is dead** ("Not used; will be set to 0" / "Not used; will be set to false") —
do not build against it; Channel Points voting remains. A read-only
integration should request `channel:read:polls`. A prediction 400 may be
content-driven ("The outcome's title failed AutoMod checks"), not
schema-driven. Prediction 429 is documented with **no description text** — do
not invent a prediction-specific limit. End via `PATCH /helix/polls` and
`PATCH /helix/predictions`.

**Raid is the ONLY endpoint here with a per-endpoint rate limit, and a 200 is
not a completed raid.** Verbatim: "Rate Limit: The limit is 10 requests within
a 10-minute window." and "The raid occurs when the broadcaster clicks Raid Now
or after the 90-second countdown expires ... you must subscribe to the Channel
Raid event." Confirmation requires an EventSub `channel.raid` subscription.
409: "The broadcaster is already in the process of raiding another channel."
Cancel: `DELETE /helix/raids`, same scope.

---

## Kick (public API, docs.kick.com)

| capability | verdict | endpoint | scope | source |
|---|---|---|---|---|
| broadcast start (go live via API) | **absent** | none | none | [channels.md](https://docs.kick.com/apis/channels.md), full 25-page enumeration 2026-08-16 |
| broadcast stop (end via API) | **absent** | none | none | [scopes.md](https://docs.kick.com/getting-started/scopes.md), read 2026-08-16 |
| start/stop DETECTION (the documented substitute) | documented | `POST /public/v1/events/subscriptions` — webhook event `livestream.status.updated` v1 | `events:subscribe` (app token also accepted) | [event-types.md](https://docs.kick.com/events/event-types.md), read 2026-08-16 |
| clip creation | **absent** | none | none | [llms.txt corpus](https://docs.kick.com/llms.txt), machine count 2026-08-16 |
| viewer count, per channel | documented | `GET /public/v1/channels` (`stream.viewer_count`) | `channel:read` (app token also accepted) | [channels.md](https://docs.kick.com/apis/channels.md), read 2026-08-16 |
| viewer count via livestream listing | documented | `GET /public/v2/livestreams`; `GET /public/v1/users/livestreams` | none named — document-level UserAccessToken/AppAccessToken | [livestreams.md](https://docs.kick.com/apis/livestreams.md), read 2026-08-16 |
| global livestream count (NOT per-channel) | documented | `GET /public/v1/livestreams/stats` (`total_count` only) | none named | [livestreams.md](https://docs.kick.com/apis/livestreams.md), read 2026-08-16 |
| stream markers | **absent** | none | none | [llms.txt corpus](https://docs.kick.com/llms.txt), machine count 2026-08-16 |
| ads / commercials | **absent** | none | none | [scopes.md](https://docs.kick.com/getting-started/scopes.md), read 2026-08-16 |
| rate limits (whole API) | **unresolved** | only `POST /public/v1/chat` declares 429 anywhere | n/a | [chat.md](https://docs.kick.com/apis/chat.md), read 2026-08-16 |

### Caveats an implementer must carry

**The evidence base is a complete machine enumeration, and the docs are
CURRENT.** All 25 pages listed in `llms.txt` were fetched and every embedded
OpenAPI block parsed: exactly **27 operations** and exactly **11 scopes**
(`channel:read`, `channel:rewards:read`, `channel:rewards:write`,
`channel:write`, `chat:write`, `events:subscribe`, `kicks:read`,
`moderation:ban`, `moderation:chat_message:manage`, `streamkey:read`,
`user:read`). `sitemap.md` lists the same 25 URLs with zero extras. **A prior
staleness hedge was false and is retracted**: the changelog's newest entry is
**11/08/2026** — five days before this read — and it is a substantive API
change. Absence findings therefore carry more weight, not less.

**A correction to the first pass:** `PATCH /public/v1/channels` is NOT "the
only mutating channel operation" — it is the only channel-METADATA mutation
("Updates livestream metadata for a channel": `category_id`, `custom_tags` ≤
10, `stream_title`). The rewards endpoints (`POST/PATCH/DELETE
/public/v1/channels/rewards*`, redemption accept/reject) also mutate, under
`channel:rewards:write`.

**Going live is RTMP-push inference, not documentation.** `streamkey:read`
("Read a user's stream URL and stream key") appears in **no** operation's
security block among the 27; the `endpoints.Stream` schema on GET
/public/v1/channels does carry `key` and `url` fields, and the linkage between
that scope and those fields is undocumented — verify empirically. Stopping is
done by dropping the RTMP connection; that too is inference from absence.

**Detection is webhook-only.** Payload verbatim shape:
`{"broadcaster":{...},"is_live":true,"title":"Stream Title","started_at":"2025-01-01T11:00:00+11:00","ended_at":null}`.
"Your webhook URL must be accessible over the public internet. Localhost URLs
(e.g. http://localhost:3000) won't work unless you expose them using tools
like Cloudflare Tunnel, ngrok, or similar services." Limits, verbatim: "There
is a subscription limit of 10,000 per event type for a single app." and "For
the 'chat.message.sent' event, there is a limit of 1,000 for unverified apps."
App verification (email developers@kick.com) lifts the **chat cap only** —
it is not a general rate-limit grant.

**`viewer_count: 0` is ambiguous and must never be read as a dead stream.**
Verbatim: "Viewer count will be 0 if the streamer has opted not to share their
viewer count." On `/public/v1/channels`: no parameters returns the
authenticated user; up to 50 `broadcaster_user_id` OR up to 50 `slug` (max 25
chars each); "You cannot mix broadcaster_user_id and slug parameters in the
same request." `GET /public/v1/livestreams` carries `"deprecated": true` — do
not build on v1; use v2 (`limit` default 100, max 1000, cursor pagination,
"sorted from oldest to newest"). `/users/livestreams` takes up to 100
`user_id`; its rendered HTML shows only UserAccessToken while the embedded
spec also lists AppAccessToken — verify client_credentials polling
empirically. `/livestreams/stats` returns only a platform-wide `total_count` —
it is not viewership and not per-channel; flagged here to prevent misuse.

**No rate-limit documentation exists anywhere.** The literal string "429"
occurs exactly once across all 25 pages (on chat send); "rate limit",
"ratelimit" and "x-ratelimit" occur zero times; no rate-limit headers are
documented; `getting-started/rate-limits` does not exist (its `.md` URL
returns a 200 soft-404 — see the trap in the sourcing rule). **Any polling
interval chosen downstream is an engineering guess, must be written up as
such, and must defend against an undeclared 429 with backoff.** The only hard
published quotas are the webhook subscription caps and the batch caps (50 ids
on /channels; 100 on /users/livestreams; limit ≤ 1000 on /v2/livestreams).

---

## Facebook (Live Video API / Graph API)

| capability | verdict | endpoint | scope | source |
|---|---|---|---|---|
| end a live video | documented | `POST /{live-video-id}?end_live_video=true` | **inferred, not documented** — API-wide list only: User `publish_video`; Page `pages_manage_posts` + `pages_read_engagement` | [getting-started](https://developers.facebook.com/docs/live-video-api/getting-started) (.md fetched, HTTP 200, 2026-08-16) |
| schedule a live video | **unresolved** | `POST /{id}/live_videos?status=SCHEDULED_UNPUBLISHED&event_params=...` — path and status confirmed; **`event_params` encoding contradicted between two live Meta pages** | `publish_video` confirmed for the read-back call only | [scheduling guide](https://developers.facebook.com/docs/live-video-api/guides/scheduling) vs [user/live_videos reference](https://developers.facebook.com/docs/graph-api/reference/user/live_videos/), both read 2026-08-16 |
| viewer stats (`live_views`) | **unresolved** | `GET /{live-video-id}` — the field list could not be read | unknown | [live-video node reference](https://developers.facebook.com/docs/graph-api/reference/live-video) — **404, reproduced four ways 2026-08-16** |
| thumbnail | documented (against a Video node; live applicability is a gap) | `POST /{video-id}/thumbnails`; for scheduled broadcasts, `schedule_custom_profile_image` on `POST /{page_id}/live_videos` | `pages_read_user_content`, `pages_manage_engagement`, `pages_show_list`; Access Tokens "App or Page" | [video/thumbnails](https://developers.facebook.com/docs/graph-api/reference/video/thumbnails/), read 2026-08-16 |
| privacy after the fact | **unresolved** | `POST /{live_video_id}` — the update endpoint is documented to exist; its parameter list is not readable | unknown | [Live Video API reference.md](https://developers.facebook.com/documentation/live-video-api/reference.md), line 73, read 2026-08-16 |

### Caveats an implementer must carry

**Ending, verbatim:** "To end the broadcast, send a `POST` request to the
`/<LIVE_VIDEO_ID>?end_live_video=true` endpoint." and "This ends your
broadcast and saves it as a video on demand (VOD). If you want to delete the
VOD, send a request to the `DELETE /<LIVE_VIDEO_ID>` endpoint."

**The scope cell is weaker than it looks, by the platform's own admission.**
The permissions list is API-wide and self-hedging: "Most endpoints require a
mix of the following permissions. To determine which permissions you need,
refer to the reference documents for each of the endpoints your app uses" —
and the per-endpoint page it defers to (the LiveVideo node reference) 404s.
By this file's own rule, the per-endpoint scope is **inferred**. Code
defensively.

**Version skew is real and two renderings of one page disagree.** The `.md`
renderings of getting-started, scheduling and the reference carry v25.0 sample
URLs while the HTML renderings of the same pages show v26.0. **Do not treat a
sample URL's version as authoritative.**

**Documented ceilings** (re-verified on reference.md, 11,428 bytes, read
2026-08-16): 8-hour broadcast maximum, 60 days, 100 followers. A rate-limit
**error** exists with no numeric ceiling: code 613, "Calls to this api have
exceeded the rate limit."

**Scheduling is the finding that breaks, and it is NOT codeable yet.** The
guide documents `event_params=<UNIX_TIMESTAMP>` as a bare scalar (sample:
`...&event_params=1541539800`); the v26.0 edge reference types `event_params`
as a structured "Live Video Event Parameter" object — "If `start_time` (unix
timecode) is set, LOE's start time will be updated. ... Example: {
start_time: 1641013200, cover: 'https://your.url/image.jpg', }" — scoped to
Live Online Events. **These cannot both be the wire format and nothing
reachable adjudicates them.** What IS safe to build on: the path `POST
/{id}/live_videos`, `status=SCHEDULED_UNPUBLISHED`, the seven-day horizon
("up to seven days from their creation date"), the status enum
`{UNPUBLISHED, LIVE_NOW, SCHEDULED_UNPUBLISHED, SCHEDULED_LIVE,
SCHEDULED_CANCELED}`, and the read-back: `GET
/<ID>/live_videos?broadcast_status=["SCHEDULED_UNPUBLISHED"]` with
`publish_video` — "Note that the `broadcast_status` value must be an array."
`planned_start_time` has 0 occurrences in reference.md, but its nonexistence
everywhere is **unproven, not disproven** — the node reference where it would
live is unreadable. **A human must test one scheduled create against the live
API before this is codeable.**

**Viewer stats: the 404 was reproduced independently, four URL forms**, while
Meta's own live index still links into the dead page. `live_views` occurs 0
times in the 11,428-byte endpoint inventory, which lists no viewer-count or
insights endpoint. **An independent model asserted the opposite from memory
and was rejected**: it produced no URL, no quote, no date, and its own fetches
failed — unsourced recall does not overturn a fetched 404. Viewer stats remain
unimplementable until a human opens the node reference in a browser.

**Thumbnails: every quoted string re-verified character-for-character**, and
an unsourced challenge to the scopes was itself wrong — the live Requirements
table says `pages_read_user_content`, `pages_manage_engagement`,
`pages_show_list`, not the streaming permissions. `source` (image file) is
Required; `is_preferred` defaults false. Thumbnails are immutable once
created: "Updating — You can't perform this operation on this endpoint." **The
applicability gap is real and unclosed**: this is documented against a Video
node, and no reachable page states it accepts a live_video_id. The workaround
(read `LiveVideo.video`, use that ID) is plausible inference only — the field
would be defined on the 404'd node reference. Do not code it as fact. **The
scope split matters operationally**: a token minted for streaming carries none
of the three thumbnail scopes, so thumbnail-setting needs a separately-scoped
token.

**Privacy update: the endpoint row exists — grepped in the raw markdown of
reference.md, line 73, occurring exactly once, distinct from the near-identical
VideoPoll row beside it**: "`POST /{live_video_id}` | Update fields on a
LiveVideo." The codebase comment on `Facebook.UpdateLiveVideoPrivacy` claiming
"Graph documents no update surface for LiveVideo at all" **is wrong and should
be amended**. What remains unproven is whether `privacy` is an accepted
parameter of that call — the #Updating anchor lives on the 404'd node page. An
independent model's claim that `privacy` is "not a documented update
parameter" is a claim of absence sourced to a page nobody can open; it is
recorded as evidence in neither direction. **Keep the defensive code: POST,
read the value back, report Applied only on exact match.** Read-back
verification is the only thing standing between this code and a silent no-op.

---

## WHAT IS ABSENT, AND MUST NOT BE BUILT

This is the list of features struck from the plan. Each absence was established
by reading, and in Kick's case machine-enumerating, the platform's complete
documented surface — not by failing to find a page.

**Twitch: no API starts, stops, or transitions a broadcast.** Established by
full enumeration of the Helix reference (1,407,883 bytes, byte-identical
across two independent reads; 149 distinct anchor-linked endpoints, count
reproduced twice). The complete Streams resource is five endpoints: Get Stream
Key, Get Streams, Get Followed Streams, Create Stream Marker, Get Stream
Markers. A keyword sweep of all 149 names for
start/stop/begin/end/live/broadcast/transition returns only non-lifecycle
operations (Start Commercial, Start a raid, End Poll, ...). An independent
second pass, asked specifically whether `PATCH /helix/channels` goes live,
confirmed it only updates metadata. **Consequence: on Twitch, liveness can
only be OBSERVED, never commanded — there is no state machine to drive and no
invalid-transition error to handle.** Scope of this evidence: the documented
Helix reference only; it is not a claim about internal Twitch APIs.

**Kick: no API starts a broadcast, ends a broadcast, creates a clip, sets a
stream marker, or touches advertising.** Established by parsing every embedded
OpenAPI block on all 25 pages in `llms.txt` (independently confirmed complete
via `sitemap.md`): 27 operations, 11 scopes, and machine counts of zero for
"go live", "start stream", "stop stream", "clip", "marker", "segment",
"advertis", and "commercial" across the whole corpus. No lifecycle scope
exists (`channel:write` is metadata only: "Update livestream metadata for a
channel based on the channel ID"). The only DELETEs in the API are rewards,
chat message, event subscription, and moderation ban. **The docs are five days
old at time of reading (changelog 11/08/2026), so these absences are current,
not stale.** The documented substitute for lifecycle control is the
`livestream.status.updated` webhook; the undocumented substitute for control
is RTMP push/drop with the stream key, which is inference and must be
labelled as such wherever it is built.

**Facebook: no documented viewer-count or insights endpoint in the Live Video
API inventory** (`live_views` occurs 0 times in the complete endpoint table).
This is an absence in what is *reachable* — the node reference that would
settle it is a 404 — so it is also listed under UNRESOLVED. Do not build
viewer stats for Facebook on the current evidence either way.

**YouTube — withdrawn sub-claims that must not be resurrected:** the
`statusRequired`/`idRequired` transition refusals and their supposed naming
discrepancy; the playlistItems `required(400)` triplet; the
"contentDetails.note max 280 chars" limit; any numeric value for the
concurrent-broadcast or shared-ingestion ceilings; any quota cost for
liveBroadcasts.insert or liveStreams.list.

---

## UNRESOLVED

Anything nobody could confirm, and what would resolve it.

1. **Facebook `event_params` wire format** (scalar UNIX timestamp per the
   guide vs structured `{start_time, cover}` object per the v26.0 edge
   reference). *Resolve by:* one scheduled-create test against the live Graph
   API by a human with a Page token, trying the scalar form first and reading
   the created broadcast back.
2. **Facebook viewer stats (`live_views`)** — the LiveVideo node reference
   404s in four URL forms while Meta still links to it. *Resolve by:* a human
   opening `/docs/graph-api/reference/live-video` in a logged-in browser; if
   it renders, quote the fields table into this file.
3. **Facebook privacy-on-update** — the update endpooint exists; whether
   `privacy` is an accepted parameter does not have a readable source.
   *Resolve by:* same node reference, or a live POST + read-back test.
   Until then the defensive read-back code stands.
4. **Facebook per-endpoint scopes generally** — the API-wide list defers to
   per-endpoint pages that are partly unreadable. *Resolve by:* the node
   reference, or empirical testing with minimally-scoped tokens.
5. **Facebook `planned_start_time`** — 0 occurrences in reference.md, but
   nonexistence everywhere is unproven. *Resolve by:* the node reference.
6. **Kick rate limits** — no numeric budget, no 429 declared outside chat
   send, no rate headers documented. *Resolve by:* nothing documentary; only
   empirical probing, or Kick publishing a page. Until then every poller
   ships backoff against an undeclared 429.
7. **Kick stream-key semantics** — whether `streamkey:read` gates the
   `key`/`url` fields on GET /public/v1/channels is undocumented (the scope
   appears in no operation's security block). *Resolve by:* empirical test
   with and without the scope.
8. **Kick app-token polling of `/public/v1/users/livestreams`** — embedded
   spec and rendered HTML disagree on whether AppAccessToken is accepted.
   *Resolve by:* one client_credentials request.
9. **Kick clips on the roadmap** — the GitHub project board
   (https://github.com/orgs/KickEngineering/projects/3) is JS-rendered and
   was not read. *Resolve by:* a human opening it in a browser.
10. **YouTube viewer-stats scope** — videos.list documents no Authorization
    section at all. *Resolve by:* a live request with `youtube.readonly` and
    with no scope; record what succeeds.
11. **YouTube archive-video timing** — how soon after `complete` the VOD ID
    becomes a valid playlistItems target is undocumented. *Resolve by:*
    empirical measurement; until then, retry on `videoNotFound` as policy.
12. **YouTube quota costs for liveBroadcasts.insert and liveStreams.list** —
    no "Quota impact" line on either page. *Resolve by:* the quota calculator
    if Google adds rows, or by observed quota consumption.

---

## Scope inventory

Every OAuth scope any **documented** endpoint in this file needs, so a single
ScopeVersion bump can request them all at once. Scopes marked *(narrower
alternative)* need not be requested if the broader scope is taken.

### YouTube (Google OAuth)

| scope | needed by |
|---|---|
| `https://www.googleapis.com/auth/youtube` | broadcast create, transitions, bind, stream create, health readback, thumbnails, videos.update, playlistItems |
| `https://www.googleapis.com/auth/youtube.force-ssl` | accepted everywhere `youtube` is — either one suffices for the live endpoints |
| `https://www.googleapis.com/auth/youtube.readonly` | *(narrower alternative)* health readback via liveStreams.list only — right-sized for a poller holding no write grant |
| `https://www.googleapis.com/auth/youtube.upload` | *(narrower alternative)* thumbnails.set only — NOT accepted by videos.update |
| `https://www.googleapis.com/auth/youtubepartner` | *(alternative)* thumbnails.set, videos.update, playlistItems.insert |
| *(none documentable)* | videos.list (viewer stats) — the page has no Authorization section; see UNRESOLVED #10 |

Minimum single-scope grant covering every documented YouTube write in this
file: `youtube` (or `youtube.force-ssl`).

### Twitch (user access token unless noted)

| scope | needed by |
|---|---|
| `channel:edit:commercial` | Start Commercial |
| `channel:manage:broadcast` | Create Stream Marker |
| `clips:edit` | Create Clip |
| `channel:manage:polls` | Create Poll / End Poll |
| `channel:read:polls` | *(narrower alternative)* Get Polls only |
| `channel:manage:predictions` | Create/End Prediction |
| `channel:manage:raids` | Start a raid / Cancel a raid |
| `channel:read:stream_key` | Get Stream Key |
| `editor:manage:clips`, `channel:manage:clips` | Create Clip From VOD (NEW endpoint; either appears on the page) |
| *(no scope)* | Get Streams — any app or user access token |

### Kick

| scope | needed by |
|---|---|
| `channel:read` | GET /public/v1/channels (per-channel viewer count; app token also accepted) |
| `events:subscribe` | POST /public/v1/events/subscriptions (livestream.status.updated webhook; app token also accepted) |
| `streamkey:read` | **documented as a scope, attached to no operation** — presumed to gate `stream.key`/`stream.url`; see UNRESOLVED #7 before requesting it on faith |
| `channel:write` | PATCH /public/v1/channels (livestream metadata: title, category, tags) — request only if metadata editing is in scope |
| *(no scope)* | GET /public/v2/livestreams, GET /public/v1/users/livestreams, GET /public/v1/livestreams/stats |

### Facebook (Meta permissions — per-endpoint status is inferred except where noted; see the Facebook caveats)

| permission | needed by |
|---|---|
| `publish_video` | User-context publishing incl. end-broadcast (API-wide list, inferred per-endpoint); **documented** for the scheduled-broadcast read-back call |
| `pages_manage_posts` | Page-context publishing incl. end-broadcast (API-wide list, inferred per-endpoint) |
| `pages_read_engagement` | paired with `pages_manage_posts` (same standing) |
| `pages_read_user_content` | video thumbnails (**documented**, Requirements table read verbatim) |
| `pages_manage_engagement` | video thumbnails (**documented**) |
| `pages_show_list` | video thumbnails (**documented**) |

**Operational note carried up from the caveats:** a Facebook token minted for
streaming carries none of the three thumbnail permissions. Any ScopeVersion
covering Facebook thumbnails must request them explicitly or mint a second
token.

---

## ADDENDUM: YouTube `liveBroadcasts.list`, checked 2026-08-16

The first pass verified the viewer-count READ and never asked how polyemesis
would find the id to read. It stores no video id and no broadcast id, so the
verified endpoint had nothing to point at. This addendum is the missing link,
and it carries far more than viewer stats: the whole Phase 2 broadcast record
depends on knowing which broadcast is live, and `contentDetails.boundStreamId`
is what joins it to the health readback.

Written by a research pass and then corrected by an adversarial one, which
refuted six sub-claims. The refutations are kept in place rather than deleted,
because a wrong version silently removed is one the next reader re-derives.

All seven pages below were fetched fresh, bodies read (not status codes), and every quote matched character-for-character against the rendered text. Footer dates recorded per page: **list `2025-08-28 UTC`**, getting-started `2026-06-01`, liveBroadcasts resource `2026-07-21`, life-of-a-broadcast `2026-06-01`, live errors `2025-12-12`, playlistItems.insert `2026-06-01`, videos.list `2026-07-08`.

| capability | verdict | endpoint | scope | source |
|---|---|---|---|---|
| find the currently-live broadcast | documented | `GET https://www.googleapis.com/youtube/v3/liveBroadcasts?part=id,snippet,status&broadcastStatus=active&broadcastType=all` | `youtube.readonly` OR `youtube` OR `youtube.force-ssl` | [liveBroadcasts/list](https://developers.google.com/youtube/v3/live/docs/liveBroadcasts/list), read 2026-08-16 |
| own-channel filter | documented, mutually exclusive with `broadcastStatus` and `id` | `...&mine=true` | same three scopes | same page, filter-group header |
| broadcast id == video id | documented, explicitly | `liveBroadcast.id` feeds `videos?id=<same string>` | n/a | [Live Streaming API Overview](https://developers.google.com/youtube/v3/live/getting-started), read 2026-08-16 |
| viewer stats, end to end | documented chain, two calls | list → `GET .../videos?part=liveStreamingDetails&id=<that id>` | discovery: three scopes above; read: **undocumented — videos.list has no Authorization section** | both pages, read 2026-08-16 |
| quota cost of `liveBroadcasts.list` | **undocumented** | — | — | list page — the string "quota" occurs **0 times in the raw HTML** |
| quota cost of `videos.list` | **documented, 1 unit** — *original findings omitted this* | — | — | [videos/list](https://developers.google.com/youtube/v3/docs/videos/list), read 2026-08-16 |
| refusals | documented, two rows only | — | — | list page Errors table |
| behavior with no active broadcast | **undocumented** | — | — | see caveats |

---

## What was REFUTED, corrected in place

**1. "Partially unread: the `persistent` gloss was cut off at the chunk boundary." — REFUTED.**
The gloss is fully readable and the enumeration is complete at three values, verbatim: `all` – "Return all broadcasts."; `event` – "Return only scheduled event broadcasts."; `persistent` – "Return only persistent broadcasts." The original marked this UNRESOLVED on a retrieval artifact, not a documentary gap. **Was wrong:** the hedge itself. The operational advice (send `broadcastType=all`) stands and is now fully sourced.

**2. "The default `broadcastType=event` will silently hide a Stream-Now broadcast." — REFUTED as sourced language; the mechanism survives.**
The string **"Stream Now" occurs 0 times** across the list page, the resource page, the overview, life-of-a-broadcast, and the live errors page. The documented term is `persistent`, and **no page in this set defines what a persistent broadcast is** — the word appears only in the `broadcastType` enum on the list page (`persistent` occurs 0 times on the resource page, the overview, and life-of-a-broadcast). Correct statement: *the default `broadcastType=event` returns only scheduled event broadcasts, therefore excludes `persistent` broadcasts.* The equation persistent ≡ "Stream Now" is unsourced product folklore — **UNRESOLVED**; *resolve by:* a YouTube page that glosses `persistent`, or one live list call against a persistent broadcast comparing `broadcastType=event` vs `all`.
Also note: the *consequence* ("a bare `broadcastStatus=active` returns only event broadcasts") is an inference from the documented default, not a quoted sentence. Sound, but it is inference.

**3. "The live errors page carries the identical two rows." — REFUTED.**
The error type/detail *pairs* match; the *description text does not*. On the reference page the second row ends "For more information, see **Feature eligibility**." On [live/docs/errors](https://developers.google.com/youtube/v3/live/docs/errors) it ends "The user can find more information at **https://www.youtube.com/features**." Do not treat the two tables as byte-identical sources for each other. Everything else in that caveat holds: `notFound(404)/liveBroadcastNotFound` sits under `liveBroadcasts.delete` ("The `id` property specified in the `liveBroadcast` resource did not identify a broadcast.") and **not** under `liveBroadcasts.list`.

**4. "The existing `youtube.force-ssl` grant covers this call either way, so no new consent screen is needed." — REFUTED as unsourced.**
`videos.list` documents no Authorization section at all (the word "authorization" appears on that page only inside the `onBehalfOfContentOwner` boilerplate; "requires authorization" occurs zero times). A page that names no scope cannot be cited as accepting one. The call may require no OAuth scope at all (API-key read) — that is equally consistent with the page. **UNRESOLVED**; *resolve by:* one live `videos.list?part=liveStreamingDetails` request with `youtube.readonly`, one with `youtube.force-ssl`, and one with an API key only; record which succeed. Practically the poller will already hold `force-ssl`, so shipping is unblocked — but do not record "covered" as documented.

**5. The overview quote is real but the ellipsis hides a clause.**
Original rendered: "A `liveBroadcast` resource is an extension of a YouTube video resource… As such…". Full text, verbatim: "A liveBroadcast resource is an extension of a YouTube video resource **and sets the video metadata that would be pertinent to a live broadcast but not to other YouTube videos.** As such, a liveBroadcast resource corresponds to exactly one YouTube video resource. In fact, the liveBroadcast resource and the video resource share the same ID." Elision, not fabrication.

**6. The life-of-a-broadcast quote is verbatim but its context was dropped.**
It is Step 6.1, "Poll the Data API for the video's status", inside the **Content ID reference** workflow, and is gated two sentences earlier on "you must have set the broadcast's `contentDetails.recordFromStart` property to `true`". The `part` it polls is `status`, not `liveStreamingDetails`. It corroborates the shared ID; it is **not** a documented precedent for the viewer-stats call.

---

## What SURVIVED refutation (verified verbatim, keep)

**The crux is settled.** Present word for word on [getting-started](https://developers.google.com/youtube/v3/live/getting-started) (read 2026-08-16): **"In fact, the liveBroadcast resource and the video resource share the same ID."** — one occurrence, no hedge. The resource page's own `id` gloss is confirmed weaker and does not state the identity: "The ID that YouTube assigns to uniquely identify the broadcast." The `snippet.title` corroboration is verbatim: "The broadcast's title. Note that the broadcast represents exactly one YouTube video. You can set this field by modifying the broadcast resource or by setting the `title` field of the corresponding video resource." **Cite getting-started, not the resource page.** No number, no scope, and no HTTP-200 inference is involved in this claim.

**Filters are mutually exclusive.** Header verbatim: "Filters (specify exactly one of the following parameters)" over `broadcastStatus`, `id`, `mine`. `broadcastStatus` verbatim: "The broadcastStatus parameter filters the API response to only include broadcasts with the specified status." Enum complete at four: `active` – "Return current live broadcasts."; `all` – "Return all broadcasts."; `completed` – "Return broadcasts that have already ended."; `upcoming` – "Return broadcasts that have not yet started." `mine` verbatim: "The mine parameter can be used to instruct the API to only return broadcasts owned by the authenticated user. Set the parameter value to true to only retrieve your own broadcasts."

**`broadcastType` text, verbatim:** "The broadcastType parameter filters the API response to only include broadcasts with the specified type. The parameter should be used in requests that set the mine parameter to true or that use the broadcastStatus parameter. The default value is event."

**`broadcastStatus=active` is not documented as owner-scoped.** Confirmed by count: "owned by the authenticated user" occurs **exactly once** on the page, in the `mine` row. Nothing scopes `active` to the caller. UNRESOLVED as stated; *resolve by:* a live call checking `items[].snippet.channelId`. Keep the defensive filter on `snippet.channelId`.

**Authorization section.** Verbatim, and the original truncated the second sentence: "This request requires authorization with at least one of the following scopes. **To read more about authentication and authorization, see Implementing OAuth 2.0 authentication.**" Three rows, in page order: `https://www.googleapis.com/auth/youtube.readonly`, `https://www.googleapis.com/auth/youtube`, `https://www.googleapis.com/auth/youtube.force-ssl`.

**Quota absence is real and now stronger.** Not merely "no Quota impact line" — the substring "quota" occurs **zero times in the entire 83,820-byte HTML** of the list page (and zero times on the liveBroadcasts resource page). The contrast holds verbatim on [playlistItems/insert](https://developers.google.com/youtube/v3/docs/playlistItems/insert): "Quota impact: A call to this method has a quota cost of 50 units." (3 occurrences of "quota" in that page's HTML). Cost of `liveBroadcasts.list` is **undocumented** — handle `quotaExceeded`, budget nothing. The one number the list page does literally state, verbatim: "The maxResults parameter specifies the maximum number of items that should be returned in the result set. Acceptable values are `0` to `50`, inclusive. The default value is `5`." (Verified — an earlier automated mismatch was my own whitespace normalization around the `<code>` tags, not a doc discrepancy.) Also literal and not previously recorded: `part` accepts "id, snippet, contentDetails, monetizationDetails, and status", so `part=id,snippet,status` is valid.

**Errors table is exactly two rows**, verbatim from the reference page: `insufficientPermissions`/`insufficientLivePermissions` — "The request is not authorized to retrieve the live broadcast."; `insufficientPermissions`/`liveStreamingNotEnabled` — "The user that authorized the request is not enabled to stream live video on YouTube. For more information, see Feature eligibility."

**No-active-broadcast behavior remains undocumented.** Neither the Errors table nor the Response section states it. Response shape verbatim: `{ "kind": "youtube#liveBroadcastListResponse", "etag": etag, "nextPageToken": string, "prevPageToken": string, "pageInfo": { "totalResults": integer, "resultsPerPage": integer }, "items": [ liveBroadcast Resource ] }`, with `items[]` glossed "A list of broadcasts that match the request criteria." Empty `items` is the structurally natural reading and no 404 row exists for this method — but no sentence says so. **UNRESOLVED**; branch on `len(items) == 0` first, never index `items[0]` blind. *Resolve by:* one authenticated call against a channel with no live broadcast, recording literal status code and body.

**Operational shape.** Unchanged: two calls per viewer-count poll; cache the discovered id for the life of the broadcast; re-list only after a transition or a `videos.list` miss. With `videos.list` documented at 1 unit and `liveBroadcasts.list` at an unknown cost, the poll interval must be governed by the observed `quotaExceeded` refusal and the shared 10,000-unit/day project ceiling, not by a per-call estimate.

---

## ADDENDUM 2: Facebook, re-checked 2026-08-16 on a documentation tree the first pass never found

The first pass recorded six unresolved Facebook questions and a reproduced
404 on the LiveVideo node reference. Most of them are now answered, and the
reason is worth more than the answers: **there are TWO Facebook documentation
trees and the pass only knocked on one.**

* `/docs/graph-api/reference/live-video` — the node reference. Genuinely gone.
  A real HTTP 404 on re-check, not the soft variety. Meta still links to it.
* `/documentation/live-video-api/...` — a separate, maintained guide tree,
  updated **Jul 2, 2026**, that answers most of what the node reference would
  have. The first pass never requested it.

The lesson generalises past Facebook: an absence established against one URL
prefix is an absence in that prefix, not in the platform. The enumeration rule
this file already carries has to name the tree it enumerated.

| capability | verdict | endpoint | scope | source |
|---|---|---|---|---|
| end a broadcast | **documented** — first pass said unresolved | `POST /<LIVE_VIDEO_ID>?end_live_video=true` | `publish_video` (User) / `pages_manage_posts`+`pages_read_engagement` (Page) | [Broadcasting](https://developers.facebook.com/documentation/live-video-api/guides/streaming), read 2026-08-16 |
| confirm the end took | documented | `GET /<LIVE_VIDEO_ID>?fields=status` → `VOD` | same | same page |
| stream health readback | **documented** | `GET /<LIVE_VIDEO_ID>?fields=ingest_streams` → `stream_health` | `publish_video` | same page, and [live-video-input-stream](https://developers.facebook.com/docs/graph-api/reference/live-video-input-stream/) |
| schedule a broadcast | documented, scalar form | `POST /<ID>/live_videos?status=SCHEDULED_UNPUBLISHED&event_params=1541539800` | as above | [scheduling guide](https://developers.facebook.com/docs/live-video-api/guides/scheduling), read 2026-08-16 |
| create a poll | documented | `POST /LIVE_VIDEO_ID/polls` | as above | [overview](https://developers.facebook.com/documentation/live-video-api/overview) |
| read error detail | documented | `GET /<LIVE_VIDEO_ID>?fields=errors` | as above | Broadcasting guide |
| **SEND a chat message** | **STRUCK** | `POST /{live-video-id}/comments` | — | [live-video/comments](https://developers.facebook.com/docs/graph-api/reference/live-video/comments/), read 2026-08-16 |
| viewer count (`live_views`) | **still UNRESOLVED** | — | — | node reference 404s for real |

### Caveats an implementer must carry

**Chat send is refused in Facebook's own words.** The comments edge reference
has a "Creating" section whose entire content is: *"You can't perform this
operation on this endpoint."* That is a stated refusal, not a missing page, and
it is the strongest kind of negative evidence available. **Narrow scope: this
settles the LIVE-VIDEO comments edge**, which is the one a live chat pane would
use. Whether the associated *post* object accepts a comment on its own edge was
NOT checked, and that is a different object with a different reference.

**ENDING A BROADCAST HAS TWO MECHANISMS AND ONLY ONE IS AN API CALL.** Verbatim:
"To end a broadcast, stop streaming live video data from your encoder to the
stream URL **or** send a request to `POST /<LIVE_VIDEO_ID>?end_live_video=true`."
Facebook ends the broadcast on its own when the bytes stop. This matters for the
END policy: on Facebook, unlike YouTube, an encoder crash ALREADY ends the
show, so there is no "leave it live and let it recover" option to preserve.

**Facebook publishes encoder health and Twitch does not.** `stream_health` on
`ingest_streams` carries bitrates and frame rates. The pacing is documented and
must be obeyed: *"Stream health data refreshes every 2 seconds, so limit
queries to no more than once every 2 seconds. A stream timeout will be detected
and reported after 4 seconds of no data being received."* This is a stated
number, so unlike YouTube's concurrency cap it MAY be encoded.

**TWO HARD CEILINGS ON EVERY FACEBOOK STREAM URL, BOTH STATED.** Verbatim: "The
stream URL must be used within 24 hours before expiring. Once used, a stream URL
can be streamed to for up to **8 hours**."

CORRECTION TO AN EARLIER DRAFT OF THIS PARAGRAPH, which claimed "nothing in the
tree currently knows this". It does: `internal/db/platforms.go:540` carries
"Eight hours maximum" in Facebook's video guidance note, sourced to
facebook.com/business/help/162540111070395 and dated 2026-08-06. What is new
here is the 24-hour unused-expiry and the fact that the 8 hours runs from FIRST
USE of the URL rather than from the broadcast.

The same draft said "polyemesis advertises 24/7 playout channels". It does not:
that pitch appears in docs/internal/features-page-gaps.md as a PROPOSED
/features section and in the keyword research, not in shipped copy -- web/src
contains no such claim. So this is not a live broken promise. It is a
constraint on copy that has not shipped yet: a 24/7 section that lists Facebook
among its destinations without carving out the eight-hour cap would be false on
the day it ships. The guidance note is prose in a preset, not a check, so
nothing warns an operator who builds a continuous channel with a Facebook leg.

**GOING LIVE HAS ACCOUNT ELIGIBILITY REQUIREMENTS THAT ARE NOT ABOUT SCOPES.**
Since 2024-06-10: the account must be at least 60 days old, and the Page or
professional-mode profile must have at least 100 followers. A brand-new account
with every permission granted still cannot go live, and the refusal will arrive
as a generic API error. This belongs in the setup guide, not in a retry loop.

**The scheduling contradiction is resolved in favour of the scalar.** The first
pass found `event_params` documented two ways and could not settle it. The
guide carries a literal, copy-pasteable request using the scalar UNIX timestamp
(`event_params=1541539800`). That is what to send. The structured
`{start_time, cover}` object appears on the v26.0 edge reference; if the scalar
is refused, that is the next thing to try, and the read-back test in the first
pass's UNRESOLVED #1 still stands as the way to confirm.

**`live_views` remains genuinely unknown.** The node reference that carries the
field list is a real 404 in every form tried across both passes. Do not build
Facebook viewer stats. *Resolve by:* one authenticated
`GET /<LIVE_VIDEO_ID>?fields=live_views` against a live broadcast, recording
whether the field returns, errors, or is silently dropped.
