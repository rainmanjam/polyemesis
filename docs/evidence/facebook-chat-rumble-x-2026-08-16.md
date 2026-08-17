# Facebook chat, Rumble and X, checked 2026-08-16

Three findings — Facebook Live chat-send, Rumble's entire developer surface,
and X's live-video family — each gathered by an independent research pass with
a source URL required for every claim, then re-verified by a second adversarial
pass on 2026-08-16 that tried to break each finding before it was recorded
here. Where the second pass refuted a sub-claim, the refutation is written in
place — the wrong version is not silently deleted, because code may already
lean on it. This file sits beside
`platform-lifecycle-apis-2026-08-16.md` and is held to the same rules.

## What may be relied on

Every claim must trace to a **dated, citable source** — the platform's own
reference documentation, fetched on the date in the title, with the operative
sentence quoted verbatim. Two kinds of source qualify: this file (each cell
carries a URL read on 2026-08-16), or a primary source quoted with its URL and
read date.

**A GENERIC CROSS-NODE REFERENCE IS NOT A SOURCE FOR A PER-ENDPOINT CELL.**
This pass produced the sharpest instance yet: Facebook's generic
`/{object-id}/comments` page implies a comment POST on Live Video, while the
per-endpoint Live Video Comments page refuses it in so many words. The
standing rule — **per-endpoint governs, generic defers** — adjudicates that to
refused. The generic page is also *structurally* unfaithful (its parameter
list and the Video node's disagree in three fields out of seven), so it may be
cited for orientation only, never for any node's contract.

**AN HTTP STATUS CODE IS NOT A SOURCE, AND NEITHER IS A BODY-SIZE CHECK.**
The two traps recorded in the lifecycle file were joined by four more during
this pass. Any future re-fetch must defend against all of them:

* `developers.facebook.com` still returns **HTTP 200 with a ~138 KB "Page Not
  Found - Meta for Developers" body** on many URL forms. The body is **not
  byte-stable** across fetches (137,516–137,813 today vs 137,479–137,807
  before) — cite the size only as order-of-magnitude evidence.
* **A third Meta trap, distinct from both recorded ones:**
  `https://developers.facebook.com/documentation/live-video-api/reference/live-video`
  returns **HTTP 200, 846,641 bytes, with the string "Page Not Found"
  appearing zero times** — and renders, under real Chromium, to **438
  characters of nav chrome** with `document.title === "Developer Platform"`.
  HTTP 200, no Page-Not-Found body, and still empty after JavaScript.
  **The rendered text length is the test**, not the status code and not the
  byte count.
* `rumble.support` returns **HTTP 200 with a knowledge-base shell containing
  every real article's title and link** for slugs that do not exist — a
  keyword match on the body will false-positive. Shell byte sizes drift
  between fetches and must not be compared against recorded constants. **The
  only reliable discriminator is the `Last updated on <date>` line**: count 1
  on every real article, count 0 on every fabricated slug.
* `rumble.com` has the **inverse** trap: any existing *channel* slug returns
  200 with a full page (`/developers` → `<title>Developers</title>`,
  `<h1>Developers</h1>` — a 4-follower channel, not a portal), while
  genuinely missing paths return an honest 404 (`<title>404 Not found</title>`,
  "404 error, this page does not exist."). On `rumble.com` a 200 does not
  mean documentation; a 404 is trustworthy.
* `docs.x.com` is the one host here where absence is safe to conclude: five
  negative controls on the Broadcasts path prefix all returned hard 404 with
  no `<title>`, while all 13 real pages returned 200 with distinct correct
  titles. **But wrong `.md` URLs return 404 with a 4-byte body, `null`** —
  two of the prior pass's "absences" were drawn from pages never served.
  Take canonical URLs from `sitemap.xml`, then read the body.

**RENDERED-CHARACTER COUNTS ARE EXTRACTOR ARTIFACTS, AND BYTE COUNTS ARE NOT
IDENTIFIERS.** Three prior character counts failed to reproduce through a
different extractor, and byte counts drifted within days on all three
platforms (X's spec grew ~28 bytes and its prose corpus ~10 KB with **no
version bump** — `2.167` throughout). Re-derive counts on every check. The
only counts in this file asserted as exact are ones reproduced twice:
Facebook `reference.md` **11,428 bytes**, `overview.md` **3,230 bytes**.

**NUMERIC LIMITS THE DOCS REFUSE TO STATE MUST STAY UNSTATED IN CODE.**
Rumble publishes no request budget (its one stated number, 50, is a *result*
cap, not a rate limit); X publishes no rate limit, quota, cooldown or
concurrent-broadcast ceiling for any Broadcasts endpoint; Facebook's rate
formulas are app-wide, not per-endpoint. Handle the refusal error instead.

---

## Facebook Live — can polyemesis SEND a chat message?

The direct answer: **`POST /{live-video-id}/comments` is documented as
impossible**, and the evidence is now stronger than the version it replaces.
[Live Video Comments](https://developers.facebook.com/docs/graph-api/reference/live-video/comments/)
(v26.0, HTTP 200, real body, read 2026-08-16) carries the heading *"Creating"*
followed by *"You can't perform this operation on this endpoint."* — the
identical sentence also under *"Updating"* and *"Deleting"*.

| capability | verdict | endpoint | scope | source |
|---|---|---|---|---|
| post a comment on a live video (the chat-send polyemesis wants) | **documented as REFUSED** | `POST /{live-video-id}/comments` — the edge documents Reading only | n/a — no scope, the operation is disallowed | [Live Video Comments](https://developers.facebook.com/docs/graph-api/reference/live-video/comments/) v26.0, read 2026-08-16: *"Creating"* → *"You can't perform this operation on this endpoint."* |
| same, per a sweep of every readable LiveVideo edge reference | **absent — enumeration done at the per-endpoint level** | none | n/a | All five readable LiveVideo edge pages fetched 2026-08-16, each parsed for its own `Creating` heading: `comments` → refusal sentence; `reactions` → same; `crosspost_shared_pages` → same; `polls` → *"You can make a POST request to"*; `input_streams` → *"You can make a POST request to"*. Two of five document a POST; neither is comments. (`live-video/likes/` is a hard 404.) |
| same, per the Live Video API endpoint index | absent — **but the index is corroboration, not authority (see caveats)** | no `POST …/comments` row | n/a | [live-video-api/reference.md](https://developers.facebook.com/documentation/live-video-api/reference.md), HTTP 200, **11,428 bytes exactly**. Re-derived: **24 rows** (20 unique) = 13 GET, 10 POST, 1 DELETE. Only comments row: `GET /{live-video-id}/comments` — *"Get a collection of Comments on a LiveVideo."* |
| same, per the generic cross-node edge page | **IMPLIED, NOT DOCUMENTED — the prior "documented as ALLOWED" is REFUTED as an overclaim** | the page writes `POST /v26.0/{object-id}/comments`; **it never writes `/{live-video-id}/comments` anywhere** | *"A Page access token requested by a person who can perform the `MODERATE` task on the Page"* + *"The `pages_manage_engagement` permission"* | [`/{object-id}/comments`](https://developers.facebook.com/docs/graph-api/reference/v26.0/object/comments), read 2026-08-16. Node list, 10 entries in page order: Album, Comment, Event, Link, **Live Video**, Photo, Post, Thread, User, Video |
| reply to a specific comment | **documented as REFUSED** | `POST /{comment-id}/comments` | n/a | [Comment Comments](https://developers.facebook.com/docs/graph-api/reference/comment/comments/) v26.0: *"Creating"* → *"You can't perform this operation on this endpoint."* The [Comment node](https://developers.facebook.com/docs/graph-api/reference/comment/) names exactly one creation path: *"/{video_id}/comments"* |
| post a comment on a **Video** node | documented | `POST /v26.0/{video_id}/comments`; params exactly six: `attachment_id`, `attachment_share_url`, `attachment_url`, `is_offline`, `message`, `text` | **undocumented — confirmed by full heading enumeration; the page has no Permissions section at all** | [Video Comments](https://developers.facebook.com/docs/graph-api/reference/video/comments/) v26.0, read 2026-08-16. Return type: read-after-write, `Struct { id: Comment ID }` |
| bridge live-video-id → video-id | **UNRESOLVED — the prior "404, reproduced five URL forms" is REFUTED** | would need the LiveVideo node's field list | unknown | 12 URL forms swept 2026-08-16: eleven hard 404; the twelfth is the 200-shell trap described in the preamble |
| chat-send permission for a **personal profile** | **absent from every readable page** | — | none documented | `publish_video` occurs **0 times** on the `/{object-id}/comments` page (v26.0 and v23.0 alike); the only publishing permission block there is Page-only |
| refusals, live comments edge (Reading) | documented | — | — | error table, page order: `100` Invalid parameter · `200` Permissions error · `190` Invalid OAuth 2.0 Access Token · `104` Incorrect signature |
| refusals, comment creation (Video node, Creating) | documented | — | — | `100` · `190` · `368` *"The action attempted has been deemed abusive or is otherwise disallowed"* · `200` · `1705` *"There was an error during posting."* · `459` *"The session is invalid because the user has been checkpointed"* |
| rate limits actually stated | documented, none of it per-endpoint | — | — | [Rate Limits](https://developers.facebook.com/docs/graph-api/overview/rate-limiting/), read 2026-08-16 — every quoted sentence re-verified by literal substring match |

### Caveats an implementer must carry

**REFUTED — "404, reproduced five URL forms".** The prose listed four forms,
not five, and the sweep missed the one direction that mattered. Twelve forms
swept 2026-08-16: eleven (`/docs/graph-api/reference/live-video/` and
variants, versioned and `.md` and renamed) are hard 404 with the
Page-Not-Found body; the twelfth —
`/documentation/live-video-api/reference/live-video` — is the 200-shell trap
in the preamble: 846,641 bytes, zero "Page Not Found", **438 characters of
rendered text** under real Chromium, while the sibling
`/documentation/live-video-api/reference` renders full content in the same
browser session. Practical consequence: the lifecycle file's *resolve-by* —
"a human opening `/docs/graph-api/reference/live-video` in a logged-in
browser" — **will not work**; that URL is a hard 404 for everyone and login is
irrelevant. The only documentary resolution left is a
`GET /{live-video-id}?fields=video` probe at the wire, or Meta restoring the
node page.

**REFUTED — "documented as ALLOWED" on the generic edge page, and the
contradiction is smaller than recorded.** The `/{object-id}/comments` page
never writes `/{live-video-id}/comments`. Verbatim: *"This reference
describes the `/comments` edge that is common to multiple Graph API nodes.
The structure and operations are the same for each node."* — then ten nodes
including Live Video — then *"Publishing — Publish new comments to any
object."* over a sample reading `POST /v26.0/{object-id}/comments`. Reaching
"the LiveVideo POST is documented" takes two inference steps. So this is
**not** two per-edge references disagreeing at equal weight: it is one
explicit per-endpoint refusal against one generic implication, and the
standing rule resolves it to **refused**. The residual doubt is empirical,
not documentary. The prior conclusion that nothing adjudicates them is
withdrawn — do not leave "the docs contradict each other" anywhere in the
codebase, because the next reader will re-derive a contradiction that is not
there.

**The generic page's uniformity claim is falsified a second way — by
parameter lists, not just by verbs.** Generic Publishing fields:
`attachment_id`, `attachment_share_url`, `attachment_url`, `source`,
`message`. Video Comments Creating parameters: `attachment_id`,
`attachment_share_url`, `attachment_url`, `is_offline`, `message`, `text`.
**`source` exists only on the generic page; `is_offline` and `text` exist
only on the per-node page.** *"The structure and operations are the same for
each node"* is false about structure as well as operations, on Meta's own two
pages.

**REFUTED as an authority, retained as corroboration — `reference.md` as
"the complete endpoint table".** Its counts are right (24 rows / 13 GET / 10
POST / 1 DELETE, re-derived independently), but it over-lists in at least one
verifiable place: it carries `GET /{page-id}/live_videos` while that
per-endpoint page says, verbatim, *"Reading — You can't perform this
operation on this endpoint."* It also links `GET /{live-video-id}/likes` to a
hard 404, and every LiveVideo link in it points at the dead node URL. **An
index that over-lists once and links into two dead pages cannot carry an
absence on its own.** The absence is carried by the five-page per-endpoint
sweep in the table above. Product-side corroboration survives:
[overview.md](https://developers.facebook.com/documentation/live-video-api/overview.md),
**3,230 bytes exactly**, contains *"comment"* **0 times** and frames audience
interaction as polls (*"`POST /LIVE_VIDEO_ID/polls`"*).

**Video Comments carries no Permissions section at all — established by full
heading enumeration, not by failing to find one.** Complete heading list:
Reading (New Page Experience, Example, Parameters, Fields, Error Codes),
Creating (Parameters, Return Type, Error Codes), Updating, Deleting.
*"Permissions"* occurs twice, both as the `200 Permissions error` row of an
error table; `pages_manage_engagement` 0×; `publish_video` 0×. The scope of
`POST /{video_id}/comments` is **undocumented, not inherited** — importing
the generic page's Page-only block into that cell would be exactly the
footer violation this file forbids.

**The `pages_manage_engagement` citation is a redirect, and its Allowed
Usage has four bullets, not one.**
`/docs/permissions/reference/pages_manage_engagement` 302s to the anchor
`https://developers.facebook.com/docs/permissions#pages_manage_engagement` —
cite the anchor. Verbatim: *"The `pages_manage_engagement` permission allows
your app to create, edit and delete comments posted on the Page."*
Dependencies: `pages_read_user_content`, `pages_show_list`. Allowed Usage:
*"Publish a comment on a Page post."* · *"Update your comment on a Page
post."* · *"Delete a comment on a Page post."* · *"Like a Page post or
remove your Like from a Page post."* The cost conclusion stands: chat-send
would need a `ScopeVersion` bump adding at least `pages_manage_engagement` +
`pages_read_user_content`, a re-consent for every connected account, and
fresh App Review.

**Code citations corrected — two of three prior line numbers were in the
wrong file.** `Scopes()` at `internal/oauth/facebook.go:84` returns exactly
`public_profile, publish_video, pages_show_list, pages_manage_posts,
pages_read_engagement` — **`pages_manage_engagement` is absent** — and
`ScopeVersion()` returns `1`. `fbRefUser` is `internal/oauth/facebook.go:188`;
`/me/accounts` is `internal/oauth/facebook.go:254`; the 200/10/3 branch of
`fbAdvice` is `internal/oauth/facebook.go:1109`. The comment poll is
`internal/chat/facebook.go:271` with `fbPollInterval = 5 * time.Second` at
`internal/chat/facebook.go:34`. **`internal/oauth/facebook.go:54` pins
`https://graph.facebook.com/v24.0` while every reference page read here
renders v26.0.**

**Rate limits — every quoted sentence re-verified by literal substring
match.** *"Calls within one hour = 200 \* Number of Users"*; *"Calls within
24 hours = 4800 \* Number of Engaged Users"*; *"All calls count towards the
rate limits, not just individual API requests"*; *"each ID counts as one API
call"*; *"Due to privacy concerns, we do not reveal actual call count values
for users"*. Codes `4` and `17` (app / user rate limit), `32` ("Page request
limit reached"), `613` (custom limit), `1996`, and `17` with subcode
`2446079` are all present. **`80001` occurs 0 times on the rate-limiting
page** — it exists only on the comments edge pages. Any "~720 calls/hour"
figure is arithmetic on a code constant, not a documented number; hardcode
none of the formulas' outputs.

**`368` is the refusal to plan for on any future send path.** An abuse
refusal is what a send-path bot trips; it is not a permission error, and
`fbAdvice`'s 200/10/3 branch at `internal/oauth/facebook.go:1109` passes it
through as raw Meta text. `459` (checkpointed session) is likewise unhandled
and not curable by reconnecting.

**Threaded replies — top-level-only is the safe assumption.**
`/{comment-id}/comments` refuses Creating; the Comment node's Creating
section names exactly one path, `/{video_id}/comments`. Yet replies exist as
data: `can_comment` (*"Whether the viewer can reply to this comment"*),
`parent`, `comment_count` (*"Number of replies to this comment"*), and
`live_broadcast_timestamp` (*"Time the comment was made on a live video"* —
direct evidence that live-thread comments are ordinary Comment nodes). **No
reachable page documents the call that creates a reply.**

**Read-path corroborations survive verbatim.** `order`: *"The best practice
for querying comments on a Live video is to continually poll for comments in
the reversechronological ordering mode."* — the misspelling is Meta's,
reproduced exactly, and matches `order=reverse_chronological` at
`internal/chat/facebook.go:271`. `live_filter`: *"Default value:
`filter_low_quality`"* — filtering is on by default for live comments, *"In
all other circumstances this parameter is ignored."* `live_filter=no_filter`
is not redundant and the code comment saying so is correct.

---

## Rumble

The headline: Rumble's published developer surface is **one help-center
article documenting one read-only GET**. Nine supporting claims from the
first pass did not survive; refutations are in place below.

**What was enumerated.** `https://rumble.support/sitemap.xml` (read
2026-08-16, 60,991 bytes) contains **158 `<loc>` entries: 155 `/help/`
articles plus 3 roots**. `https://rumble.support/robots.txt` (200, 115 bytes)
declares exactly one sitemap, verbatim:
`Sitemap: https://rumble.support/sitemap.xml` — a completeness citation.
Matching all 158 slugs against
`api|develop|oauth|token|sdk|webhook|integrat|third.?party|bot` returns **one
hit**: `how-to-use-rumble-s-live-stream-api`. **Scope limit:** the corpus is
English-only; the `/de/` and `/es/` article trees are absent from the sitemap.
**A gap in the first pass, now closed:** it probed `developers.rumble.com`
and `api.rumble.com` (both genuinely `ENOTFOUND`, reconfirmed) but never
requested `https://rumble.com/developers` — which is a 200, and is a
**Rumble channel** (`/c/c-696945`, "4 Followers", "No videos found"), not a
developer portal. The conclusion holds; the method did not.

| capability | verdict | endpoint | scope | source |
|---|---|---|---|---|
| developer documentation exists | documented — one help-center article, API self-identified as **v1.1** | n/a | n/a | ["Rumble's Live Stream API", "Last updated on 20 Nov, 2025"](https://rumble.support/help/how-to-use-rumble-s-live-stream-api), verbatim: "Our v1.1 live streaming API will allow you to integrate real time notifications and overlays through your streaming software", read 2026-08-16 |
| API auth model | documented — **there is none** | n/a | verbatim: "The API URL includes your user UI as well as your live stream key. **Authentication is not required for this version of the API.** Please only share this URL to trusted third party sources. You can reset your URL to revoke access any time." | same article, read 2026-08-16 |
| chat read | documented (payload); path verified by observation only | `GET https://rumble.com/-livestream-api/get-data?key=<key>` | key in query string; no OAuth, no header | payload `chat.recent_messages[] {username,badges,text,created_on}`; bare GET → **HTTP 400** `{"errors":[{"code":"invalid_request","message":"No access token found in the request",…}]}`, reproduced 2026-08-16 |
| chat result cap | **documented — the one number Rumble states** | same GET | n/a | article, verbatim: **"A maximum of 50 results total are output for recent chat messages and rants"**; corroborated by `"max_num_results" : 50` in the example payload |
| viewer count | documented | same GET, `livestreams[].watching_now` | same | field list verbatim: `"Watching Now" viewer count`; payload `"watching_now" : 19`, read 2026-08-16 |
| stream key **read** | documented **by example payload only — not in the field list** | same GET, `livestreams[].stream_key` | same | `"stream_key" : "XXXXXX"` appears in the example JSON; the 15-item field list has no stream-key entry |
| title / category **read** | documented | same GET, `livestreams[].title`, `.categories.primary/.secondary` | same | field list verbatim: `Video title`, `Primary and secondary category of stream` |
| title / category **write** (CapMetadata) | **absent from the published surface**; the documented substitute is a UI template | none published | n/a | field list is a closed set — verbatim "In summary, the live stream API includes the following data fields and is updated in real-time:" — **15 items, reproduced in full below** |
| chat **send** (CapChatSend) | **absent from the published surface**; the read interface does not write | none published | n/a | same enumeration. `get-data` is a snapshot document; the article documents no second path, no method, no request body |
| moderation — delete / mute / ban (CapModeration) | **absent from the API**; documented **UI-only** | none published | n/a | ["How to Moderate Your Channel and Add Moderators"](https://rumble.support/help/how-to-moderate), "Last updated on 20 Mar, 2026", **`\bAPI\b` count = 0**. Verbatim: "Select the three dots next to a message in live chat or under the video's comments to open moderation options." |
| OAuth / sign-in for applications (CapSSO) | **absent from the published surface** | none published | n/a | enumeration above; `developers.rumble.com`/`api.rumble.com` NXDOMAIN; `rumble.com/oauth/authorize` **real 404** ("404 error, this page does not exist.") |
| stream key fetch for setup (CapStreamKey) | **SupportManual — but the first pass's reason was wrong**, see caveats | n/a | n/a | API field is live-only, **but** a pre-obtainable ["Static Stream Key"](https://rumble.support/help/static-stream-key-instructions) exists in the account UI, "Last updated on 02 May, 2026", `\bAPI\b` count = 0 |
| rate limit / request budget | **UNSTATED — leave it unstated** | n/a | n/a | `rate limit`, `Retry-After`, `quota` each occur **0 times** in the article. The 50-result cap is a *result* cap, not a request budget — do not conflate them |

### Caveats an implementer must carry

**REFUTED — "the article's own body text is 9,913 characters."** Not
reproducible; a tag-stripped extraction of the entire page today is 9,291
characters. An extraction-method artifact — struck. The load-bearing facts
(158-entry sitemap, one keyword hit, verbatim quotes) reproduce exactly.

**REFUTED — "0 occurrences of POST, PUT, PATCH, DELETE, send, ban, mute,
update or write."** False twice over as literally written: case-insensitive
substring matching finds `PUT` 2× inside "output" and the `updat*` family 4×.
The defensible claim, re-run case-sensitively with word boundaries: **POST 0,
PUT 0, PATCH 0, DELETE 0, write 0, send 0, ban 0, mute 0 — and `GET` 0 as
well.** The article names no HTTP method at all, not even the read one.

**REFUTED — the redirect target, and nobody has read `auth.rumble.com`.**
`/account/livestream-api` is a **two-hop chain**: `302 →
/login.php?next=%2Faccount%2Flivestream-api`, then `308 →
https://auth.rumble.com?theme=s&redirect_uri=…&lang=en_US`. `auth.rumble.com`
answered every automated fetch with **HTTP 403**. "A first-party session
login, not a grant flow" is therefore *inference from the URL shape* — it
carries `redirect_uri` but **no `client_id`, no `response_type`, no `scope`**,
which no OAuth authorization endpoint can omit. The conclusion stands as
inference; it is labelled as inference.

**The endpoint path is verified by observation, not publication.** The
article never prints `https://rumble.com/-livestream-api/get-data`; it says
the URL is issued at `rumble.com/account/livestream-api`. Reproduced
2026-08-16: bare GET → **400** "No access token found in the request";
`?key=zzzzinvalidzzzz` → **403** `{"code":"403","message":"Forbidden"}`. The
400/403 split corroborates that `key=` is consumed rather than ignored —
inference from two error codes; the parameter name remains undocumented. The
example payload echoes **`"since" : null`** and **`"max_num_results" : 50`**
as top-level response fields — documentary evidence that both are accepted
request parameters, still short of proof.

**REFUTED IN ITS REASONING — the stream key IS obtainable in advance, just
not by API.** The live-only constraint is verbatim ("All of the values under
'livestreams' including chat messages and rants are **only populated during a
live stream**"), but Rumble documents a **Static Stream Key**, verbatim: "A
Static Stream Key is a universal stream URL and key that does not change."
and "Use Rumble alongside other platforms in your restreaming workflow."
`CapStreamKey: SupportManual` remains the correct terminal value — but
because the key is **UI-only**, not because keys cannot be had before
broadcast. Write the right reason down; the wrong one invites someone to
"fix" it by polling `get-data`.

**Templates are Rumble's documented answer to both broadcast-create and
metadata-set.** Verbatim: "Optional: Associate a live stream template with
your static key using the dropdown menu." and "you can go live directly from
your streaming software, and **Rumble will automatically create a new stream
using the details from your selected template**. This means you do not need
to create the live stream on Rumble first." Title and category come from
that dropdown. This is why metadata write is *absent* rather than merely
unimplemented — Rumble's answer to "set the title programmatically" is "pick
a template in the web UI beforehand."

**The security note stands: one unauthenticated GET returns chat and a live
secret in the same JSON.** `stream_key` ships in the payload without being
advertised in the 15-item field list. Verified in-repo:
`/Users/rainmanjam/Documents/polyemesis/.claude/worktrees/platcap/internal/chat/rumble.go`
deliberately never decodes it — comment verbatim: "the `stream_key` omission
is the important one and is not an oversight: a field that is never
unmarshalled cannot be accidentally logged, put in an error, or serialised
into a health detail by some later change. See #310." Keep it that way.

**REFUTED — `rumbleMinPoll` is 5 seconds, not 10.** Two constants were
conflated. Actual, in `internal/chat/rumble.go`: **`rumbleMinPoll = 5 *
time.Second`** (the floor), **`rumbleDefaultPoll = 10 * time.Second`** (the
default), plus `rumbleMaxPoll = 60s`, `rumbleOfflinePoll = 30s`. The file
records its own probe: "five requests in close succession drew no 429 and no
Retry-After." Rumble publishes no limit, so no number may be *set* from
documentation. A third-party library (cocorum, `static.py`) carries
`api_refresh_minimum = 5` with docstring "Minimum refresh rate for the main
API, as defined by Rumble" — an unofficial assertion about an undocumented
limit; it coincides with the existing floor and tends to confirm the setting
is not reckless, but **it is not a source and must not be cited to raise the
poll rate.**

**REFUTED — the cocorum README quote was a misquote.** Actual sentence,
verbatim: "I, Wilbur Jaywright, and my brand, Marswide BGL, have no official
association with Rumble Corp. beyond that of a normal user and/or channel on
the Rumble Video platform. **This wrapper is not officially endorsed by
Rumble Corp. or its subsidiaries.**" The substance is unaffected; the quote
is fixed here.

**REFUTED — cocorum function names were attributed to the wrong module.**
`send_message`, `delete_message`, `pin_message`, `unpin_message`,
`mute_user`, `unmute_user`, `command` live in `chatapi.py`, not
`servicephp.py`. `servicephp.py`'s own defs include `comment_add`,
`comment_delete`, `raid_confirm`, and — missed by the first pass —
**`reset_rls_api_key`**, an undocumented `service.php` method that rotates
the Live Stream API key. Login goes salts-first (`user.get_salts` →
`calc_password_hashes` → `user.login`; `rumble.com/service.php?name=user.login`
answers 200 `application/json`). The chat URL is real:
`https://web7.rumble.com/chat/api/chat/{stream_id_b10}`. **These are private
frontend interfaces driven by a stored password.** The objection to building
on cocorum stands on its real grounds: it would mean storing a user's Rumble
**password**, it is explicitly unendorsed by Rumble, and it breaks on any
frontend change.

**REFUTED — robots.txt does not carry the argument it was asked to carry.**
`rumble.com/robots.txt` (200, 170 bytes) does contain `Disallow: /api/` —
but `/service.php` and `/chat/command` are not under `/api/`, and
`web7.rumble.com` is a different host. It also does not disallow the
documented `/-livestream-api/get-data`. Drop robots.txt from the argument;
the conclusion (`CapChatSend: SupportNo`) is unchanged and rests on the
enumeration.

**"I could not find it" versus "it does not exist" — this is the former made
into the latter.** For SSO, chat send, moderation and metadata write this is
not a failed search: it is the complete English knowledge base named by
Rumble's own sitemap and declared by its own robots.txt, plus the full text
of the only article that mentions an API, plus a direct read of
`rumble.com/developers`. That satisfies `SupportNo` — the platform publishes
no API for it. What is **not** established is that no private interface
exists; the article says as much, verbatim: "We are also going to be
partnering with some for a deeper and robust integration." Partnership, not
registration — the TikTok and LinkedIn shape. A partner holding a signed
agreement is not contradicted by these cells.

**The 15 documented fields, reproduced in full so the closed-set claim is
auditable:** total followers of profile-or-channel; total followers across
all channels and profile; most recent 50 followers by username; most recent
follower by username; chat messages incl. username and badges; rants incl.
username, message and dollar amount; most recent rant; subs incl. username;
most recent sub by username; total number of subs; gifted subs incl.
username and number of gifts purchased; video title; number of likes and
dislikes (thumb ratings); primary and secondary category of stream;
"Watching Now" viewer count. **Fifteen — and `stream_key` is not among
them.**

**Do not confuse three unrelated products.** `rumbleup.com` (SMS marketing)
and `rumbletalk.com` (chat widget with OAuth 2.0) both surface in searches
for "Rumble API OAuth"; neither is Rumble Video. `docs.rumble.cloud` is
Rumble's own but is an IaaS/OpenStack product. None bear on any cell above.

---

## X (Twitter) — live video

The headline: **X ships a `Broadcasts` family, and the summary at
`internal/oauth/capabilities.go` line 310 ("not live-video ingest or its
viewer numbers") is wrong.** Enumeration reproduced independently:
`GET https://api.x.com/2/openapi.json`, read 2026-08-16 — `"title": "X API
v2"`, `"version": "2.167"`, **149 paths, 178 operations, 21 tags, 23 OAuth
2.0 scopes**, tag counts `Broadcasts: 13`, `Chat: 16`. Tag gloss verbatim:
`"Endpoints related to live broadcasts and their chat"`. The soft-404 trap
does not bite on docs.x.com (see the preamble), so absence on that host is
safe to conclude.

| capability | verdict | endpoint | scope | source |
|---|---|---|---|---|
| broadcast go-live (publish) | documented | `POST https://api.x.com/2/broadcasts/scheduled/{id}/live` | `broadcast.read` + `broadcast.write` — **or OAuth 1.0a `UserToken`** | openapi.json `goLiveScheduledBroadcast.security`; [go-live](https://docs.x.com/x-api/broadcasts/go-live-on-a-scheduled-broadcast), read 2026-08-16 |
| broadcast create (scheduled, one-off or recurring) | documented | `POST .../2/broadcasts/scheduled` | `broadcast.write` + `broadcast.read` — or `UserToken` | openapi.json `createScheduledBroadcast`; [create](https://docs.x.com/x-api/broadcasts/create-a-scheduled-broadcast), read 2026-08-16 |
| broadcast update / delete | documented | `PUT`, `DELETE .../2/broadcasts/scheduled/{id}` | same | [update](https://docs.x.com/x-api/broadcasts/update-a-scheduled-broadcast), [delete](https://docs.x.com/x-api/broadcasts/delete-a-scheduled-broadcast), read 2026-08-16 |
| broadcast list / get (live) | documented | `GET .../2/broadcasts`, `GET .../2/broadcasts/{id}` | `broadcast.read` — or `UserToken` | [list-broadcasts](https://docs.x.com/x-api/broadcasts/list-broadcasts), read 2026-08-16 |
| scheduled list / get | documented | `GET .../2/broadcasts/scheduled`, `GET .../2/broadcasts/scheduled/{id}` | `broadcast.read` — or `UserToken` | [list-scheduled](https://docs.x.com/x-api/broadcasts/list-scheduled-broadcasts), [get-scheduled](https://docs.x.com/x-api/broadcasts/get-a-scheduled-broadcast), read 2026-08-16 |
| metadata (title, description, locale, slate, replay, lock, chat option) | documented — **scheduled objects only** | `POST`/`PUT .../2/broadcasts/scheduled[/{id}]` | `broadcast.write` + `broadcast.read` | openapi.json `CreateScheduledBroadcastRequest`, read 2026-08-16 |
| stream key — **mint** | **absent** | none | none | openapi.json full 149-path sweep, 2026-08-16 |
| stream key — **enumerate / list sources** | **absent** | no `/2/sources`; `/2/sources` returns live **404**, 0-byte body | none | 149-path sweep + live probe, 2026-08-16 |
| stream key — **read back an already-bound key** | **documented — the prior "absent" was wrong** | `GET .../2/broadcasts/{id}`, `GET .../2/broadcasts/scheduled/{id}`, and the create/go-live responses → `data.source_id` | `broadcast.read` | openapi.json `Broadcast.source_id`, described verbatim "Bound ingest / source id (`rtmp_stream_key`)." |
| stream key — consume | documented (input) | `source_id`, **required** at create | `broadcast.write` | openapi.json `CreateScheduledBroadcastRequest.required` |
| viewer count (concurrent + cumulative) | **fields documented, semantics undocumented** | `GET .../2/broadcasts[/{id}]` → `data.total_watching`, `data.total_watched` | `broadcast.read` | openapi.json `Broadcast` schema — both `{"type": "string"}`, **no description** |
| broadcast end — **command** | **absent** | none | none | 149-path sweep; no stop/end operation |
| broadcast end — **observe** | **documented — the prior "RTMP-drop inference" was wrong** | `data.state`, `data.end_ms` on the broadcast object | `broadcast.read` | openapi.json — `state` verbatim "Scheduler state (Created, Scheduled, Running, …)." |
| chat read (history, poll) | documented | `GET .../2/broadcasts/{id}/chat` | `broadcast.read` — or `UserToken` | [chat-history](https://docs.x.com/x-api/broadcasts/get-broadcast-chat-history), read 2026-08-16 |
| chat read (real-time push) — **transport** | documented | `GET /2/activity/stream` (tag `Stream`, not `Activity`) | **`BearerToken` app-only — NOT `broadcast.read`; the prior scope cell is refuted** | openapi.json `activityStream.security` = `[{"BearerToken":[]}]` |
| chat read (real-time push) — **event entitlement** | documented | `broadcast.chat` event on an XAA subscription | `broadcast.read` (on the subscription, not the stream) | [activity/introduction](https://docs.x.com/x-api/activity/introduction), read 2026-08-16 |
| chat send | documented | `POST .../2/broadcasts/{id}/chat`; `text` 1–140 chars | `broadcast.read` + `broadcast.write` — or `UserToken` | [send-a-chat-message](https://docs.x.com/x-api/broadcasts/send-a-chat-message-to-a-live-broadcast), read 2026-08-16 |
| moderation (mute / unmute / delete message) | documented | `POST .../chat/mutes`, `DELETE .../chat/mutes/{user_id}`, `DELETE .../chat/{message_id}` | same | [mute](https://docs.x.com/x-api/broadcasts/mute-a-user-in-a-broadcast-chat), [remove-message](https://docs.x.com/x-api/broadcasts/remove-a-chat-message-from-a-live-broadcast), read 2026-08-16 |
| SSO for live video | documented (OAuth 2.0 authorization code); **PKCE not stated in the spec** | `authorizationUrl: https://api.x.com/2/oauth2/authorize` | `broadcast.read`, `broadcast.write` | openapi.json `securitySchemes.OAuth2UserToken.flows.authorizationCode` |
| documented error taxonomy for any Broadcasts op | **none beyond a generic default** | n/a | n/a | every Broadcasts op declares only `200`/`201` + `default` = `"The request has failed."` |
| enterprise gate on the 13 REST endpoints | **UNRESOLVED — see caveats** | n/a | n/a | no tier or pricing page names Broadcasts, 2026-08-16 |

### Caveats an implementer must carry

**REFUTED — the real-time-chat scope cell named a permission the endpoint
does not declare.** `GET /2/activity/stream` declares `"security":
[{"BearerToken":[]}]` — OAuth 2.0 App-Only, no user scope of any kind. What
carries `broadcast.read` is the *event*, verbatim: "**Private event:**
`broadcast.chat` is a private event and requires user-context (OAuth 2.0)
authentication with the `broadcast.read` scope." A user-authorized
*subscription* decides what events exist for you; an app-only *bearer* opens
the transport that delivers them. Wiring `broadcast.read` onto the stream
connection will not work; wiring app-only bearer onto the subscription will
not deliver broadcast chat. The operation is tagged `Stream`, not `Activity`
— tag-filtered enumeration concluded the path was absent and was wrong; it
is path #1 of the 149. **Tag-filtered enumeration is a trap.**

**REFUTED — "stream key: MINT or FETCH — absent" was half wrong, and the
wrong half is the useful half.** Mint is genuinely absent, and so is any
source directory (`/2/sources` → live 404, 0 bytes). But **fetch-back is
documented**: `source_id` appears on `Broadcast` and every
scheduled-response schema — "Bound ingest / source id (`rtmp_stream_key`)."
So X will hand back the key already bound to a broadcast; it will not issue
one or list the ones you have. Polyemesis can verify that what X thinks is
bound matches what it is sending, instead of trusting its own stored copy.

**REFUTED — the millisecond claim asserted a unit for four fields the spec
never annotates.** Only `scheduled_start_ms` / `scheduled_end_ms` carry "ms
since Unix epoch (decimal string)" with a pattern. `created_at_ms`,
`updated_at_ms`, `start_ms`, `end_ms` on `Broadcast` are bare
`{"type": "string"}` with no description and no pattern. Milliseconds is
inferred from the suffix. Parse defensively. The entire `Broadcast` schema
is undescribed — **not one of its 26 properties carries a `description`** —
which is also why `total_watching` / `total_watched` have no documented
semantics: both string-typed, both undescribed, `viewer` appears **0 times**
in the whole spec, and the schema is `$ref`'d from exactly two sites
(`GetBroadcastResponse`, `ListBroadcastsResponse`), so those two endpoints
are the only way to reach the numbers. Concurrent-vs-cumulative is a reading
of the names. **Do not surface either as an authoritative concurrent-viewer
count.**

**PARTIALLY REFUTED — "no commanded STOP" is right; "ending is an RTMP-drop
inference" is not.** No stop/end operation exists among the 13, but the end
is *observable*: poll `state` and `end_ms`. Two constraints: the state list
is published with a **trailing ellipsis** — X is explicitly refusing to
enumerate its states, so never write an exhaustive switch over it — and
`Broadcast.state` is undescribed, so it may not share the scheduled-response
vocabulary.

**REFUTED — required-field lists were understated on both write paths.**
Create requires **three** fields: `"required": ["source_id",
"scheduled_start_ms", "scheduled_end_ms"]`. Update requires three:
`"required": ["scheduled_broadcast_id", "scheduled_start_ms",
"scheduled_end_ms"]`. Building from the prior single-field lists yields a
400. `PUT` is a full replace, verbatim: "Fully replaces schedule fields for
a broadcast… re-send any fields that should be kept."

**Two id spaces, with a documented length ceiling the prior pass missed.**
`broadcast_id` — "Alphanumeric UBS broadcast id (path `:id` for
get/update/delete/live)." — path pattern `^[a-zA-Z0-9]{1,13}$`.
`scheduled_broadcast_id` — "Numeric scheduler id from create/list/get."
(request-body description; the response schemas word it differently). They
are not interchangeable.

**A type trap: `chat_option` changes type across the boundary.** Request:
`{"type": "string", "pattern": "^[0-9]{1,19}$"}`. `Broadcast.chat_option`:
`{"type": "integer"}`. You send a numeric string and read back an integer;
a round-trip that assumes symmetry fails deserialization.

**REFUTED — the scope arithmetic implied a containment that does not hold.**
The human OAuth page lists 22 scopes, the spec 23, and the lists **diverge
in both directions** (spec-only: `broadcast.read`, `broadcast.write`,
`timeline.read`; docs-only: `users.email`, `block.write`). "Trust the spec"
is the wrong general lesson — the spec is also incomplete, it merely fails
differently. Trust it for the broadcast scopes specifically: swept across
all 178 operations, `broadcast.*` appears on exactly the 13 Broadcasts ops
and nowhere else. Definitions verbatim: `broadcast.read` = "View your live
broadcasts and their chat." / `broadcast.write` = "Manage your live
broadcasts and send chat messages on your behalf."

**Every one of the 13 operations also accepts OAuth 1.0a.** Each declares
`OAuth2UserToken:[…] OR UserToken:[]`. Reproduced live 2026-08-16,
`GET /2/broadcasts`: unauthenticated → 401; junk bearer → 403, verbatim
(with X's own double space): `"Authenticating with OAuth 2.0
Application-Only is forbidden for this endpoint.  Supported authentication
types are [OAuth 1.0a User Context, OAuth 2.0 User Context]."` **PKCE is not
stated anywhere in the spec's `securitySchemes`** — it is X's documented
practice elsewhere but not a fact this spec supplies; do not cite the spec
for it.

**REFUTED as cited, sustained on the merits — two "absences" were drawn
from URLs that 404 with a 4-byte `null` body.** Both conclusions hold at the
canonical sitemap URLs: `https://docs.x.com/changelog.md` (200, 111,680
bytes, 205 dated entries, newest Aug 13, 2026) contains **zero** Broadcasts
announcements; the v2 authentication mapping (200, 28,769 bytes) has 0
broadcast mentions; both rate-limit pages (20,831 and 3,761 bytes) have 0
broadcast mentions. **No rate limit, quota, cooldown or concurrent-broadcast
ceiling is published for any Broadcasts endpoint. Do not hardcode one.**

**And there is no refusal code to key retries off.** Every Broadcasts
operation documents only `200`/`201` plus `default` ("The request has
failed."). The go-live precondition is documented as behaviour — "Publishes
a schedule that was created or updated with `manual_publish: true`. Without
that flag the coordinator auto-publishes at start and this call is rejected."
— but no status code, `type` URI or error title is published for that
rejection. Treat the refusal as opaque and log the body.

**Lifecycle narrowness is the load-bearing constraint.** There is no
"create a live broadcast now" call. The only route to live: create a
scheduled broadcast with `manual_publish: true` and a `source_id`, then
`POST .../scheduled/{id}/live` — which takes no request body. `recurrence`
is a closed enum of exactly `Daily` and `Weekly`.

**The family is published, routed, crawlable, and in the prose corpus, but
missing from the navigation index and never announced** — not
"undocumented". `llms.txt` (382 entries, 29 sections) has no Broadcasts
section and 0 broadcast mentions; but `llms-full.txt` carries all 13
Broadcasts pages, and `sitemap.xml` (1,037 URLs) lists exactly 13 broadcast
pages, one per operation.

**Do not confuse the `Chat` tag with broadcast chat.** `Chat` (16 ops) is
glossed "Endpoints related to Chat encrypted messaging" (not "end-to-end
encrypted" — that was not X's wording) and carries `dm.read`, `dm.write`,
`media.write`, `users.read`, `tweet.read`. Broadcast chat lives entirely
under `Broadcasts` / `broadcast.*`.

---

## WHAT IS ABSENT, AND MUST NOT BE BUILT

Each absence below was established by enumerating a complete, bounded corpus
— not by failing to find a page. The corpus and its size are stated so the
enumeration can be re-run.

**Facebook: no documented comment POST on a live video.** Established at the
per-endpoint level: all **five readable LiveVideo edge reference pages**
(`comments`, `reactions`, `crosspost_shared_pages`, `polls`,
`input_streams`) fetched 2026-08-16 and each parsed for its own `Creating`
heading — two document a POST (polls, input_streams) and neither is
comments; the other three carry the refusal sentence verbatim. Corroborated
by the 24-row endpoint index (`reference.md`, 11,428 bytes — corroboration
only, since it demonstrably over-lists) and by `overview.md` (3,230 bytes,
"comment" 0×, audience interaction framed as polls). **Do not build a
Facebook live chat-send on `POST /{live-video-id}/comments`; the documents
refuse it.** The wire remains untested — see UNRESOLVED 1.

**Facebook: no personal-profile chat-send.** `publish_video` occurs 0 times
on the generic `/{object-id}/comments` page in both v26.0 and v23.0; the
only publishing permission block anywhere in this set is Page-only. No
permission, no token form, no endpoint documented on any readable page.

**Rumble: no chat send, no moderation API, no metadata write, no OAuth/SSO.**
Established by the complete English knowledge base: **158 sitemap entries
(60,991 bytes), declared the sole sitemap by robots.txt (115 bytes)**, one
API-related article among them, that article read in full (it names no HTTP
method and documents one snapshot GET with a closed 15-field list), the
moderation article read in full (`\bAPI\b` = 0, moderation documented as a
three-dots UI action), `developers.rumble.com` and `api.rumble.com` NXDOMAIN,
`rumble.com/oauth/authorize` an honest 404, and `rumble.com/developers` read
end-to-end (a 4-follower channel). **There is no API for these.** The bots an
operator has seen working drive Rumble's private frontend with a stored
password via an unendorsed wrapper — that is not an API and must not be
built.

**X: no stream-key mint, no source directory, no commanded broadcast end, no
immediate go-live, no published Broadcasts rate limit, no error taxonomy.**
Established by a full sweep of the served spec — **openapi.json, 856,451
bytes, 149 paths, 178 operations, every `$ref` walked** — plus live 404
probes (`/2/sources`, `/2/live_video_ingest`: 404, 0 bytes), the full
changelog (111,680 bytes, 205 entries, zero Broadcasts announcements), and
both rate-limit pages (20,831 + 3,761 bytes, zero broadcast mentions). The
only route to live is scheduled-create + go-live; the only end is observed
via `state`/`end_ms`.

**Refuted sub-claims that must not be resurrected:** Facebook's "the generic
edge page documents the LiveVideo POST" (it implies, two inference steps);
any rendered-character count as a page property; Rumble's "the stream key
can never be fetched before broadcast" (the Static Stream Key exists,
UI-only); Rumble's "rumbleMinPoll is 10 seconds" (it is 5; 10 is the
default); X's "`broadcast.read` on `GET /2/activity/stream`" (app-only
bearer); X's "no way to read back a bound stream key" (`source_id` is
echoed); X's "ending is RTMP-drop inference" (it is observable); X's
single-required-field create/update bodies (three each).

---

## UNRESOLVED

Anything nobody could confirm, and what would resolve it. **The distinction
that matters for the matrix: "the platform publishes no API for it" maps to
`SupportNo`; "the page that would settle it could not be read" maps to
`SupportUnknown`.** Conflating them puts a false "Not possible" in front of
users.

### Facebook

1. **Does `POST /{live-video-id}/comments` work at the wire?** The documents
   say no; but Meta's own index over-lists at least one operation its
   per-endpoint page denies, so the docs are not a perfectly reliable
   negative either. *Resolve by:* one POST against a real live broadcast
   with a Page token holding `pages_manage_engagement`, recording the
   literal status, body, and — if an id comes back — whether the comment
   appears in the `GET …/comments` thread polyemesis already polls.
2. **Personal-profile chat-send.** Nothing readable documents it in either
   direction. *Resolve by:* the same live test with a user token and
   `publish_video`, expecting `200` or `283`; record the exact refusal.
3. **`LiveVideo.video` → `POST /{video_id}/comments`.** The Video node page
   contains zero mentions of live video; the LiveVideo node reference is
   dead at all twelve URL forms. *Resolve by:* a
   `GET /{live-video-id}?fields=video` probe at the wire — **not** "a human
   opening it in a logged-in browser", which the twelve-form sweep disproves.
   Guessing that the live thread and the video thread are the same thread is
   exactly the inference this file exists to stop.
4. **Threaded replies.** Replies exist as data (`parent`, `comment_count`)
   but no reachable page documents the call that creates one. *Resolve by:*
   `POST /{comment-id}/comments` against a live comment, and separately
   `POST /{video_id}/comments` carrying a parent hint; record both refusals.
5. **Scope of `POST /{video_id}/comments`.** No Permissions section,
   established by full heading enumeration. *Resolve by:* empirical testing
   with minimally-scoped tokens — never by importing the generic edge page's
   Page-only block.
6. **The `/documentation/…/reference/live-video` 200-shell — broken route or
   withdrawn page?** Neither a 404 nor a served document. *Resolve by:*
   Meta's changelog or a support ticket; until then treat it as the third
   trap and test rendered text length.

### Rumble

7. **Does `get-data` accept `since` and `max_num_results` as request
   parameters?** The response echoes both at top level — strong, still
   inference. *Resolve by:* one call with a real key varying
   `max_num_results` and comparing array lengths.
8. **Does any second path exist under `/-livestream-api/`?** The 400/403
   probe cannot enumerate a namespace. *Resolve by:* a real key, or a Rumble
   publication.
9. **Does a partner-tier API grant SSO, chat send or metadata write?** By
   construction unpublished ("We are also going to be partnering with some
   for a deeper and robust integration"). *Resolve by:* a written response
   from Rumble. This is the one Rumble question that could someday move a
   `SupportNo` cell — but only with a signed agreement in hand, not on
   speculation.
10. **What does `auth.rumble.com` serve?** It returned 403 to every
    automated fetch; "session login, not grant flow" is inference from a
    `redirect_uri` with no `client_id`/`response_type`/`scope`. *Resolve
    by:* a human opening it in a browser and recording whether it presents
    a consent screen or a bare login.
11. **The Rumble rate limit** — unstated by Rumble and it must stay unstated
    in code. The 5-second `rumbleMinPoll` floor is the correct handling of a
    refusal-to-state, not a number awaiting correction.

### X

12. **Tier and metering on the 13 Broadcasts REST endpoints.** The pricing
    page (13,981 bytes) has 0 broadcast mentions; `broadcast.chat` is *not*
    among XAA's enterprise-restricted events (only `news.new` is so
    marked); the XAA subscription-limit table (1,500 / 75,000 / 150,000) is
    a subscription ceiling, not a request budget, and constrains XAA only;
    Pay-Per-Use launched Feb 6, 2026 per the changelog. *Resolve by:* an
    authenticated `GET /2/broadcasts` from a self-serve pay-per-use app,
    plus the per-endpoint pricing table in the Developer Console at
    console.x.com. **Encode no tier assumption in either direction.**
13. **`total_watching` / `total_watched` semantics.** Fields exist,
    string-typed, undescribed, unmentioned in 5.4 MB of prose. *Resolve by:*
    observing both against a live broadcast with a known audience, or an X
    description landing in the spec.
14. **The full `Broadcast.state` vocabulary.** Published with a trailing
    ellipsis — X is refusing to enumerate. *Resolve by:* nothing
    documentary; log every observed value and never switch exhaustively.

---

## WHAT THIS MEANS FOR THE CAPABILITY MATRIX

Twelve cells in `internal/oauth/capabilities.go` are settled or corrected by
this document: Facebook's one remaining `SupportUnknown` in this area,
Rumble's five `SupportUnknown`s, and the six X cells sitting on a wrong
`SupportNo`. Where the honest answer is still `SupportUnknown`, it says so —
an unknown cell is a legitimate outcome and better than a guess.

### Facebook

1. **`CapChatSend` → `SupportNo`.** Reason: "The Live Video Comments
   reference documents comment creation as refused — 'You can't perform this
   operation on this endpoint.' — and a sweep of every readable LiveVideo
   edge page finds no comment POST; the generic /comments edge page only
   implies one, and per-endpoint references govern." The recorded reason
   MUST NOT say "the docs contradict each other" — that premise was refuted.
   `SupportUnknown` remains defensible only on the separate ground that
   nothing was tested at the wire (UNRESOLVED 1); if that ground is chosen,
   write that reason, not the contradiction. Note alongside either cell that
   `internal/oauth/facebook.go` pins v24.0 while every page read here
   renders v26.0.

### Rumble

2. **`CapSSO` → `SupportNo`.** Reason: "Rumble publishes no OAuth and no
   developer sign-in: the complete 158-article knowledge base contains one
   API article, which states 'Authentication is not required for this
   version of the API', and rumble.com/oauth/authorize is an honest 404 —
   only an unpublished partner agreement could change this."
3. **`CapChatSend` → `SupportNo`.** Reason: "The entire published API is one
   read-only snapshot GET; no send path, method, or request body is
   documented anywhere in Rumble's knowledge base, and the working chat
   bots an operator may have seen drive Rumble's private frontend with a
   stored password, which polyemesis will not do."
4. **`CapModeration` → `SupportNo`.** Reason: "Rumble documents moderation
   as a UI action only — 'Select the three dots next to a message in live
   chat' — and its moderation article contains the word API zero times."
5. **`CapMetadata` → `SupportManual`.** Reason: "No write API exists;
   Rumble's documented mechanism is a live-stream template chosen in the
   account UI, which it applies automatically each time the static key goes
   live — set title and category there, before the broadcast."
6. **`CapViewerStats` → `SupportYes`** (once the poller reads it — the field
   is in the same `get-data` response chat already fetches). Reason:
   "watching_now is in the documented 15-field list of the same get-data
   snapshot the chat poller already holds; populated only while live." Until
   the field is actually unmarshalled the cell stays `SupportUnknown` by the
   house rule that a capability nothing implements is not a capability — but
   the documentation side is settled, and the implementation is a field read
   in an existing poll.
7. **`CapStreamKey` keeps `SupportManual`, with its reason corrected** (not
   one of the twelve, recorded so the wrong reason dies): the key IS
   pre-obtainable — the Static Stream Key — but UI-only. The old reason
   ("can only be read once already broadcasting") invites someone to "fix"
   it by polling `get-data`, which would also mean unmarshalling the secret
   `internal/chat/rumble.go` deliberately refuses to decode.

### X

The `Summary`/`ReadFirst` strings at `capabilities.go:310-311` and the
comment at 315-316 ("Everything below hangs off a live broadcast object that
the X API does not expose to third parties in the first place") are
factually wrong — `GET /2/broadcasts/{id}` is that object — and must be
rewritten. Every SupportYes below carries the shared caveat that access is
credit-based/paid and the Broadcasts tier is UNRESOLVED 12.

8. **`CapSSO` → `SupportYes`.** Reason: "OAuth 2.0 authorization code at
   api.x.com/2/oauth2/authorize with the broadcast.read and broadcast.write
   scopes, declared in X's served OpenAPI spec; PKCE is X practice but is
   not stated in the spec."
9. **`CapMetadata` → `SupportYes`.** Reason: "Title, description and chat
   options are set on scheduled broadcast objects via POST/PUT
   /2/broadcasts/scheduled — there is no metadata update on an
   already-live broadcast."
10. **`CapChatRead` → `SupportYes`.** Reason: "Chat history over GET
    /2/broadcasts/{id}/chat with broadcast.read; real-time push needs two
    auth objects — a user-context XAA subscription for the broadcast.chat
    event plus an app-only bearer on GET /2/activity/stream."
11. **`CapChatSend` → `SupportYes`.** Reason: "POST /2/broadcasts/{id}/chat
    with broadcast.read plus broadcast.write, text 1–140 characters."
12. **`CapModeration` → `SupportYes`.** Reason: "Mute, unmute and delete a
    chat message over the /2/broadcasts/{id}/chat/mutes and
    chat/{message_id} endpoints with broadcast.write."
13. **`CapViewerStats` → `SupportUnknown`, not `SupportYes`.** Reason:
    "total_watching and total_watched exist on GET /2/broadcasts but are
    undescribed string fields with no documented semantics or unit —
    readable, but not presentable as an authoritative viewer count until X
    documents them or a live broadcast calibrates them." The fields'
    existence is documented; their meaning is not, and a viewer number shown
    to an operator asserts a meaning.
14. **`CapStreamKey` keeps `SupportManual`, with its reason corrected** (not
    one of the twelve): X consumes and **echoes** a key it will not mint or
    enumerate — `source_id` is required at create and read back on every
    broadcast object, so polyemesis can verify a binding instead of trusting
    its stored copy, but the user must still paste the key a first time.
