# Streaming platforms

Two separate questions, often confused:

1. **Can polyemesis stream there?** Almost always yes. Anything that accepts
   RTMP, RTMPS or SRT works with a pasted URL and key.
2. **Can polyemesis automate the setup?** That depends entirely on what the
   platform's published API offers, and it varies enormously.

This page answers both, then gives the OAuth app setup for each platform that
supports sign-in.

- [Capability matrix](#capability-matrix)
- [The platforms that need a sentence each](#the-platforms-that-need-a-sentence-each)
- [Connecting an account](#connecting-an-account)
- [Multiple accounts](#multiple-accounts)
- [Compliance metadata](#compliance-metadata)

---

## Capability matrix

Streaming itself works the same everywhere: per-destination audio routing,
renditions, reconnect, meters and recording do not depend on a single column
below. The table is what each platform's **published API** allows today.

The same matrix is rendered in `Settings → Platform credentials` and served from
`GET /api/v1/platforms/capabilities`.

| Platform | Sign in | Stream key | Metadata | Chat read | Chat send | Moderation | Viewers | Start / end |
|---|---|---|---|---|---|---|---|---|
| **YouTube Live** | Works | Works | Works | Works | Works | Works | Works | Works |
| **Twitch** | Works | Works | Works | Works | Works | Works | Works | Not possible |
| **Facebook Live** | Works | Works | Works | Works | Not possible | Works | Unverified | Works |
| **Kick** | Works | Works | Works | Works | Works | Works | Works | Not possible |
| **Vimeo Livestream** | Works | By hand | Unverified | Unverified | Unverified | Unverified | Unverified | Unverified |
| **X (Twitter) Live** | Works | By hand | Unverified | Unverified | Unverified | Unverified | Unverified | Unverified |
| **Rumble** | Not possible | By hand | By hand | Works | Not possible | Not possible | Unverified | Not possible |
| **DLive** | Unverified | By hand | Unverified | Unverified | Unverified | Unverified | Unverified | Unverified |
| **Odysee** | Unverified | By hand | Unverified | Unverified | Unverified | Unverified | Unverified | Unverified |
| **Dailymotion** | Unverified | By hand | Unverified | Unverified | Unverified | Unverified | Unverified | Unverified |
| **TikTok LIVE** | Works | By hand | Unverified | Unverified | Unverified | Unverified | Unverified | Unverified |
| **LinkedIn Live** | Unverified | By hand | Unverified | Unverified | Unverified | Unverified | Unverified | Unverified |
| **Instagram Live** | Unverified | By hand | Not possible | Unverified | Not possible | Not possible | Not possible | Not possible |
| *Everything else* | — | By hand | — | — | — | — | — | — |

| Term | Means |
|---|---|
| **Works** | polyemesis does this for you today |
| **By hand** | Supported, with one step you do yourself, usually pasting a key. A pasted key is a fully supported destination, not a degraded one |
| **Unverified** | Not built, and the platform's API not confirmed either way. Never a refusal — nothing stops you trying |
| **Not possible** | Somebody read the platform's published API and the thing is not in it. No amount of setup will produce it |

*Everything else* is the other nineteen entries in the destination preset
catalogue — PeerTube, Owncast, Cloudflare Stream, Mux, AWS IVS and the rest.
The platforms named in the table above are not among them, however faint their
row reads. They stream perfectly over RTMP, RTMPS or SRT with a pasted URL and
key; we simply have not researched their APIs, and "unverified" is the honest
thing to say about an API nobody here has read.

The X row's stream key, *By hand*, was tested live on 2026-11-04: a pasted
server URL and key published to X with picture. The rest of the X row
stays *Unverified*. The details, and a server that could not reach X, are under X
in [the platforms that need a sentence each](#the-platforms-that-need-a-sentence-each).

## The platforms that need a sentence each

**Facebook — read this before you start.** Full support, and Meta requires
**App Review** first. Your own account works immediately as a developer or
tester of your own app, which is all a single-operator setup needs. Publishing
on anyone else's behalf needs Advanced Access to `publish_video` (profiles) or
`pages_manage_posts` plus `pages_read_engagement` (Pages). That review is Meta's
process and yours to complete, not something polyemesis can shorten, and it is
measured in days. Start it before you need it.

**Two ways to get a Facebook key, and they are not the same key.** Connecting
the account is what creates the broadcast: the key polyemesis fetches belongs to
one live video, and pressing *Refresh key* starts a new one. Facebook also
offers a **persistent stream key** — Live Producer → *Advanced settings* — which
is reusable every time you go live and therefore does **not** go stale in a
saved destination. Meta's Graph reference documents no way to mint or read that
one, so polyemesis cannot fetch it: you copy it from Live Producer once and
paste it.

Which you want depends on what else you need from the destination:

| | Connected account | Pasted persistent key |
|---|---|---|
| Key lifetime | one broadcast; refresh makes a new one | reusable, indefinitely |
| Backup ingest | requested automatically | turn on *Backup stream* and paste the second key |
| Title, description, privacy | ✅ | ❌ — no live-video id |
| Live chat and moderation | ✅ | ❌ |
| End broadcast | ✅ | ❌ |
| Simultaneous destinations | one per broadcast object | **one** — Facebook allows a persistent key to carry a single live video at a time |

A destination's saved settings go out on that same create call: the chosen
audience becomes `privacy`, a saved Crosspost list becomes
`crossposting_actions` (each Page in it carries whether to only share the
broadcast or also publish a post as that Page), and a saved charity id becomes
`donate_button_charity_id`. **Crossposting and the donate button are applied
once, at that moment, and stay that way.** Meta's Graph reference documents no
way to update either on a live broadcast, so changing one afterward has no
effect on the broadcast already running — the only way to apply a changed
value is what "Refresh key" already does above: end the broadcast and start a
new one under the new settings.

**Privacy is the exception: it can also be changed after the broadcast is
live**, through the compliance push described under [Compliance
metadata](#compliance-metadata) below. Graph documents no update surface for a
live video at all, so a 200 from that write proves Facebook accepted the
request, not that the value changed — the change is reported applied only once
a follow-up read confirms Facebook's own value actually matches what was sent,
never on the write's 200 alone.

**A Page broadcast is public regardless of what audience is chosen.** Facebook
has no personal audience for a Page, so `privacy` is left off the create call
entirely whenever the target is a Page — the setting applies to profile
broadcasts only, and the omission is silent.

Pushing metadata (title, description) can now also carry tags. `content_tags`
is resolved from the words an operator types, one Graph search per word
against `/search?type=adinterest`. That is an ads-surface endpoint, not the
`publish_video` surface the rest of this integration runs on, and whether
`publish_video` can reach it is **unverified** — this repo has no live
Facebook account to test it against. If Facebook refuses the lookup, tags are
reported as skipped rather than failing the push; the title and description
still land.

**Kick — fully automated, and it took a correction to get there.** Kick's
OAuth 2.1 flow (PKCE, which Kick requires) gets you chat both ways, deleting a
chat message, title, category and tag push, viewer counts — **and the stream
key**.

Kick's entire metadata surface is three fields — `stream_title`, `category_id`
and `custom_tags` — on one channel PATCH. There is no description, no
thumbnail and no scheduling, so the composer skips those for Kick and says so
rather than reporting them as failures. Tags **replace** rather than merge, as
they do on YouTube; clearing the field removes every tag.

This page said for a long time that the key was unfetchable, and the reasoning
was understandable but wrong. Kick publishes no `/streamkey` endpoint, so
reading the endpoint list finds nothing. The key is a field —
`stream.key` — on the **channels resource polyemesis already fetches** for
identity and live state, withheld unless the token carries the `streamkey:read`
scope. Kick's Get Channels page now lists that scope in its scope catalogue
("Read a user's stream URL and stream key"), although the operation itself
still names only `channel:read` as required. This page used to say the page did
not mention it at all; that is no longer true, but the key is still withheld
without the scope, so the advice is unchanged.

**An account connected before this landed must be disconnected and reconnected
once.** Granting a scope never upgrades a token that has already been issued —
and Settings → Platform credentials flags exactly this, so it does not have to be
remembered from a page of documentation.

**Trovo** shut down on 30 June 2026, so it is no longer offered. A Trovo
destination saved before then is kept, switched off, so you can read its
settings and delete it when you are ready.

**X (Twitter) — the Livestream API is real, and polyemesis has it on by default.**
X publishes a Livestream API (the Broadcasts and Chat families, under the
`broadcast.read` and `broadcast.write` scopes, plus `/2/sources` for stream
keys). X's [getting-started page](https://docs.x.com/livestream-api/getting-started)
asks for a developer app, OAuth 2.0 with those two scopes, an account that is
allowed to go live (not protected) and an RTMP encoder; it no longer asks for
an access form or a whitelist. Sign-in, stream-key minting, go-live and end,
metadata, chat read and send, and chat moderation are built in
`internal/oauth/x.go` and `internal/chat/x.go`. **All of it is on by
default.** The switch is the `POLYEMESIS_X_LIVESTREAM` environment variable:
unset or empty is on, and `0` (or `false`, `off`, `no`) switches it off. While it
is off the operator sees "needs X Livestream, which is switched off
(POLYEMESIS_X_LIVESTREAM=0)", and pasting the server URL and stream key from
X's producer tooling works exactly as before. The paste also remains the way in
for an account that cannot use the API.

The calls use the routes in X's
[migration guide](https://docs.x.com/livestream-api/migration-guide): `POST
/2/sources` and `GET /2/sources` (the owner comes from the access token),
`GET /2/sources/{id}/stream_key` for the key, `POST /2/broadcasts` to go live,
`GET /2/broadcasts[/{id}]` to read, and `PUT /2/broadcasts/{id}/state` with
`END` to end. The older `/2/users/{id}/sources` and `/2/users/{id}/broadcasts`
routes are removed by X on 2027-01-01 and are not called.

With the switch on, the lifecycle works like this. X's go-live call creates the
broadcast and makes it public in one step, and X ends it as `TIMED_OUT` if no
video arrives within five minutes, so polyemesis does not call it early. When an
X destination turns on, polyemesis records the destination's stream source and
waits. Once video arrives, polyemesis takes the source live with the
destination's **go-live title**, set in the X console in the destination editor
(at most 280 characters: X says the title is "subject to X post length limits"
and gives no number, PROVISIONAL). During the first 30 minutes of a recurring
show, the show's template title is sent instead (see
[RECURRING-EVENTS.md](RECURRING-EVENTS.md)). **X takes a title only at that
go-live step.** It has no write that changes a running broadcast's title, so the
console makes the field read-only while the broadcast is live and the server
refuses a change then (HTTP 409). Turning the destination off ends the
broadcast. After a go-live the X chat adapter is told the new broadcast at once,
and its own 15-second check stays as the fallback.

**The go-live announcement, once per show by default.** X's go-live step makes
an announcement post on the account unless the request asks it not to
(`should_not_tweet`, a JSON boolean, PROVISIONAL until a live run confirms it). The X console has
a **Go-live announcement** setting with three choices:

- **On the first go-live of a show only** (the default). A show lasts while the
  destination stays on. The broadcasts polyemesis opens again within it, after X
  ends or times out the previous one, go live without a post. Switching the
  destination off ends the show, so the next time it goes live it posts.
- **On every go-live**, renewals included, which is what X does by default.
- **Never.**

Schedules start and stop shows the same way: a start schedule switches the
destination on, and a stop schedule switches it off. With both, each occurrence
is a show of its own and posts once. With a start schedule only, the destination
stays on between occurrences, so a later occurrence that gets a new broadcast is
a renewal and does not post under the default. Choose **On every go-live** for
that.

When a broadcast ends while the destination is still on (X timed it out, or it
was ended in X's own tooling), polyemesis opens the next one only after the
encoder has been seen to stop, so ending a broadcast in the X app while the
encoder keeps sending is an emergency stop that holds. A broadcast X **canceled**
is not replaced by itself: it is shown as a fault, and switching the destination
off and on again starts a new one.

X publishes no rate limits for the Livestream endpoints. polyemesis reads X's
`x-rate-limit-*` headers: after a 429, or an answer that says no calls remain,
it makes no X calls for that account until the reset time X gave (at most 15
minutes), and the wait is not counted as a failure. The account id
(`GET /2/users/me`) is asked for only by the account read, once per token and
kept for ten minutes; the Livestream routes need no user id. A
confidential app authenticates its token requests with HTTP Basic, as X
documents. The consent page is `x.com/i/oauth2/authorize`, as X's guide shows. The spec's
`api.x.com/2/oauth2/authorize` returns HTTP 403 to a browser; a live request on
2026-11-06 confirmed the x.com address shows the consent screen, including with
the broadcast scopes. Tokens still come from `api.x.com/2/oauth2/token`.

Live chat needs three more things, because X delivers `broadcast.chat` **only to
a webhook** ([Accessing Live Chat](https://docs.x.com/livestream-api/chat/accessing-live-chat),
[Webhooks](https://docs.x.com/x-api/webhooks/introduction)):

1. `POLYEMESIS_X_APP_BEARER`, the app's app-only bearer token from console.x.com,
   which the webhook calls need. It is a secret.
2. A **public, trusted HTTPS address without a port**: `tls.hostname` a public
   DNS name, `tls.mode: acme`, and inbound **443** reachable from the internet.
   X rejects a webhook URL with a port and cannot validate a self-signed
   certificate.
3. The app's OAuth 2.0 client secret saved in Settings → Platforms. It keys the
   challenge-response check and the signature on each delivery.

When chat starts, polyemesis registers `https://<tls.hostname>/api/v1/webhooks/x`
with X (reusing it if X already lists it, and re-validating it if X lists it
invalid), subscribes `broadcast.chat` and `broadcast.chat.remove` for the
account to that webhook with the user's token (they persist at X and are reused on every start), and removes them
when the account is disconnected. Without the bearer or the address the chat pane stays empty,
says what is missing, and sending still works. The X console and the setup guide
show whether the Livestream switch is on (it is, unless `POLYEMESIS_X_LIVESTREAM=0`).

**Known rare edge cases of the X Livestream path.** A go-live is sent only after its marker is written. If
polyemesis has to stop tracking a broadcast a go-live may have made, the destination shows the fault "polyemesis may
have left a broadcast live on X" so you can end it in X's producer tooling. The attempt time is not refreshed after
a lost answer, so a second attempt that creates the broadcast more than about 25 seconds after the first is refused
as not started by polyemesis. How X's `created_at_ms`, `Date` header and 200-versus-201 answers behave is modelled
from X's documentation and unconfirmed live.

**Known rare edge cases of the X Livestream path.** A go-live is sent only after its marker is written. If
polyemesis has to stop tracking a broadcast a go-live may have made, the destination shows the fault "polyemesis may
have left a broadcast live on X" so you can end it in X's producer tooling. The attempt time is not refreshed after
a lost answer, so a second attempt that creates the broadcast more than about 25 seconds after the first is refused
as not started by polyemesis. How X's `created_at_ms`, `Date` header and 200-versus-201 answers behave is modelled
from X's documentation and unconfirmed live.

Every cell in the X row stays *Unverified* except the stream key, which is
*By hand*. That is deliberate: no live request from a connected X account has
confirmed any of the built paths yet. Two things apply on top of the switch: X
refuses to take an account live that is not allowed to go live (HTTP 403 from
the go-live call; the deprecated routes answered 412), and X has **no hide
action**, so chat moderation offers mute, unmute and delete only.

**The stream-key path was verified live on 2026-11-04.** polyemesis built from
`main` at `b13944ea`, on a macOS machine on a residential connection, published
to an X destination on `rtmps://a.live.x.com:443/...`, with the server URL and
stream key pasted from X Media Studio Producer. The log showed
`destination publishing` about 1.6 seconds after the start, the stream then ran
steadily for over a minute with no reconnects, and the picture showed in X
Producer's preview. The test feed was 1280×720 at 30 fps, H.264 at 3 Mb/s with
AAC stereo at 128 kb/s; it came in over RTMP ingest and went out on the
passthrough rendition. That verifies publishing with picture through a pasted
key and nothing more: chat, metadata and every Livestream API path stay
*Unverified*, and the stream-key cell stays *By hand*, which is what a pasted key
is.

**X's ingest sits behind Cloudflare, so a server that cannot reach Cloudflare
over IPv4 cannot stream to X.** The same day, the same X destination failed from
an OVH VPS (Ubuntu 24.04, polyemesis v0.12.0): its log read
`Error opening output … Connection timed out` every 6–7 seconds. X was not
refusing that host. The host could not reach Cloudflare's IPv4 network at all:
`https://1.1.1.1` and `https://discord.com` gave no response, and neither did
`x.com`, `api.x.com` or `abs.twimg.com`; pings to `1.1.1.1` and other Cloudflare
IPv4 addresses got no reply. Twitch and YouTube ingest, which are not on
Cloudflare, connected. `https://www.cloudflare.com` did answer, but over IPv6;
`x.com` and `a.live.x.com` have no IPv6 address of their own, so connections to
them go over IPv4. X's ingest addresses (`a.live.x.com` and the `pscp.tv` hosts) resolve
to Cloudflare (172.65.x.x), and a trace to an `x.com` address stopped inside
OVH's network. The cause is likely routing on the provider's side. It was seen
on one OVH VPS; other hosts and providers have not been tested.

Before you rely on X from a server, check from that server that it can reach
Cloudflare over IPv4, and X's ingest host:

```sh
curl -sS -o /dev/null --connect-timeout 10 -w '%{http_code}\n' https://1.1.1.1
nc -vz -w 10 a.live.x.com 443
```

The first prints an HTTP status when it gets a response; on a timeout it prints
`000` and a curl error. The second is a TCP check to the ingest host; use the
host in your own server URL if X Producer gave you a different one. If
Cloudflare is unreachable, contact your provider or use another host or network.
See
[Troubleshooting](TROUBLESHOOTING.md#x-destination-connection-timed-out-when-opening-output).

**Instagram Live streams with a pasted key.** On instagram.com choose
**+ Create → Live video → Go live with streaming software**. Instagram shows a
stream URL of the form `rtmps://edgetee-upload-<region>.xx.fbcdn.net:443/rtmp/`
and a **Stream key** list. Add an **Instagram Live** destination, paste the URL
exactly (the region differs per account, which is why the preset does not
prefill it), and paste the key. The key looks like Facebook's, `<digits>?s_bl=1&…`,
and is joined onto the URL as-is.

- **Use a persistent key** where your account offers one (marked *Ready*, under
  **Manage Persistent Key**). It is pasted once and the destination stays ready
  from one live video to the next. A one-off key works too; paste the new one
  before each live video.
- **Click Go live in Instagram.** Once polyemesis is sending, Instagram shows
  the preview, and the live video starts when you click **Go live** there. You
  have up to five hours to do it. Instagram records from the moment video
  arrives, not from Go live.
- **One hour per live video.** Instagram ends a live video after an hour. Plan a
  longer show as one-hour segments.
- **End in Instagram first**, then stop the destination. Stopping only the
  encoder can leave the live video frozen for a few minutes.
- **Encode vertical:** 9:16 at 720×1280 and 30 fps (60 at most), video 2,250–6,000
  kbps, stereo audio at most 256 kbps. Restream recommends 44.1 kHz audio;
  polyemesis sends AAC at 48 kHz to every destination, and whether Instagram
  prefers 44.1 kHz in practice has not been verified.
- **Account:** a public professional (Creator or Business) account with 1,000+
  followers.

What stays in Instagram for now is everything except the video. Meta's public
API allows reading the comments on a live video while it is broadcasting
(`GET /{ig-user-id}/live_media`, and the `live_comments` webhook), so chat read
is *unverified*: allowed, planned, not built. Creating a broadcast, fetching the
ingest URL or key, Go live and end, viewer counts, and hiding, deleting or
posting live comments have no public endpoint, so those cells read *not
possible*. Sources: [Meta: IG User live_media
reference](https://developers.facebook.com/documentation/instagram-platform/instagram-graph-api/reference/ig-user/live_media.md),
[Restream: stream to Instagram with a stream
key](https://support.restream.io/en/articles/18180232-stream-to-instagram-with-a-stream-key)
and [Restream: stream to
Instagram](https://support.restream.io/en/articles/6379108-stream-to-instagram),
read 2026-11-05.

**TikTok LIVE** and **LinkedIn Live** are *unverified* for a different reason
than DLive: their APIs demonstrably exist and answer — a request to
`open.tiktokapis.com` or `api.linkedin.com` comes back with a structured
authentication error, not a 404 — but the live surfaces sit behind partner
programmes rather than open developer registration. Partner-gated is not the
same as absent, and this project has not applied, so *unverified* is the honest
word. Somebody inside either programme may well find every column is a yes.

TikTok sign-in works, and it is there for **clip publishing**: connect a TikTok
account and the clip editor can post a finished short to it (privately until
TikTok audits the app). It does not stream to TikTok LIVE.

The stream key on both is *by hand* and, worth knowing, **per broadcast**:
TikTok issues it for the LIVE session and LinkedIn for the event, so a saved
destination goes stale between streams rather than persisting like a Twitch key.

**Vimeo — sign-in works for everyone; the live API is Enterprise-only.** That
is Vimeo's own sentence, on its live API reference read 2026-08-26: *"Please
note that our live API is available only to Vimeo Enterprise customers."* It is
a commercial gate, not a permission — no scope, no reconnection and no app
setting lifts it — and it covers the whole live surface: create an event,
activate it, end it, read its ingest status, its RTMP destinations, its M3U8
playback and its thumbnails.

So this row reads **Works / By hand / Unverified ×6**, and the six deserve an
explanation because two of them are not really unverified at all. Metadata and
Start / end were *checked* — Vimeo publishes "Update an event", "Activate an
event" and "End an event" — they are simply not built here, and none of the
four words above says "documented and unbuilt". *Unverified* is the least wrong
of the four because it is the fail-open one and invites you to try; *Not
possible* would be a refusal Vimeo's own reference contradicts, and *Works*
would be a promise no code keeps.

**The stream key stays pasted even on Enterprise**, and for a reason worth
knowing before you go looking for a setting: Vimeo has no permanent key. The
ingest URL and key belong to a live event, so obtaining one means reading or
creating an event — which is the gated surface, and which polyemesis does not
do yet regardless. Create a **recurring** event (Vimeo is deprecating one-time
live events and recommends avoiding them), open its setup panel, and copy the
RTMPS server URL and stream key across.

**Connect an account anyway.** Sign-in is what lets polyemesis *ask* Vimeo
whether your account reaches the live API, with your own token, the moment you
connect — and tell you then. Without it the first evidence is a refusal in the
middle of a broadcast from an API that never uses the word Enterprise. Vimeo
also verifies your client ID and secret directly, so a typo is caught on the
credentials page rather than at consent time.

**Rumble** is the row that changed, and it is worth reading as a case study in
why this table says *unverified* rather than *no*. It used to be unverified all
the way across, on the grounds that `rumble.com/account/api` sits behind a login
and publishes nothing. That was true and it was not the whole picture: Rumble
also runs a **live-stream API** at `rumble.com/-livestream-api/get-data`, keyed
from the operator's own account settings at `rumble.com/account/livestream-api`
rather than from a partner programme, and it carries chat. Nobody had to be
approved by anybody — the row was blank because nobody had looked in the right
place. Chat read now works. Everything else stays unverified: the endpoint
returns data and no endpoint for sending or moderating is published, which is
not the same as one being known not to exist.

Two things to know before relying on Rumble chat. The key goes in the
`RUMBLE_CHAT_API_KEY` environment variable and nowhere else — there is no
sign-in to store it against, and it never goes into the database or onto a
command line. And Rumble publishes no rate limit, so polyemesis polls
conservatively (ten seconds, backing off while chat is quiet) rather than as
fast as the pane could use.

**DLive** stays *unverified* rather than *unsupported*: its developer portal at
`dev.dlive.tv` no longer resolves in DNS, which tells us nothing about what the
API can do. Streaming to it works today with a pasted URL and key.

---

## Connecting an account

Connecting a platform account is what lets polyemesis fetch your stream key,
push your title at go-live and carry chat. It requires **your own** OAuth
developer app: polyemesis cannot ship client secrets, because anyone with the
binary would have them and the platforms would revoke them.

Every platform is optional. With no credentials configured at all, everything
below reads as *not configured* — never an error — and destinations with a
pasted URL and key work exactly as well.

`Settings → Platform credentials` has step-by-step instructions and renders the
exact redirect URI to whitelist. In summary:

**Four of the five platforms can prove the pair you pasted before you connect
anything.** Twitch, Kick, Facebook and Vimeo each publish a client-credentials
grant — an app token, needing no user consent — so polyemesis asks for one with
your client ID and secret and throws the token away; obtaining it at all is the
proof. A tick on the credentials page therefore means **both halves are right**,
and a typo is caught where you typed it rather than at the end of a consent
flow.

**YouTube cannot be checked**, for a reason belonging to Google rather than to
this code: it offers no way to validate a client ID and secret without a user
consent round-trip. So the credentials are checked for **shape only**, and the
real verdict arrives the first time you connect an account: a mistyped YouTube
secret surfaces as a refusal from Google at the end of the consent round-trip,
not on the page where it was entered.

A check that cannot reach the platform is reported as **unreachable** rather
than as a rejection, so a network problem at your end never reads as a bad
secret. A 5xx or a 429 from the platform counts as unreachable for the same
reason: its server breaking, or rate-limiting us, says nothing about whether
the pair is correct. Every other status — 400, 401, 403, 404 — is the
platform's considered answer about the credentials and stands as one. The check
is bounded at **five seconds**, so a platform having a bad day does not become
a settings page that hangs.

### YouTube (Google)

1. <https://console.cloud.google.com/apis/credentials> — create or pick a project.
2. **APIs & Services → Library** → enable **YouTube Data API v3**.
3. **OAuth consent screen** → External; add your own Google account under
   *Test users*.
4. Still on the OAuth consent screen: set the **publishing status to
   In production**, unless you are only trying polyemesis out. See the warning
   below — left in *Testing*, this connection stops working after seven days.
5. **Credentials → Create Credentials → OAuth client ID → Web application.**
6. Add the redirect URI shown on the credentials page, exactly:
   `https://YOUR_HOST/api/v1/oauth/youtube/callback`
7. Paste the **Client ID** and **Client secret** into the fields of the same
   names in polyemesis, then **Connect account**. Both are on the OAuth client's
   page (Google Auth Platform → Clients). Google shows the secret in full only
   when it is created.

Scope requested: `https://www.googleapis.com/auth/youtube`. Write access is
needed because polyemesis creates a reusable ingest stream if your channel has
none.

**An app left in *Testing* disconnects itself every seven days.** Google issues
a refresh token that expires after a week to any External app whose publishing
status is Testing — unless the only scopes it requests are name, email address
and user profile. polyemesis requests the `youtube` scope, so it is not exempt.
The connection stops working on the eighth day, every time, and reconnecting
resets the same clock.

Setting the publishing status to *In production* is what removes the expiry.
An app **only you** sign in to does not need to pass Google's verification
review to do that: verification is what Google asks for before *other people*
can grant it access. A self-hosted install with one operator is the case that
does not need it.

### What YouTube chat costs

Unlike every other platform here, YouTube chat is **metered**. Twitch is an IRC
socket and Kick posts webhooks — both free and real-time. YouTube is polled, and
every poll spends from a daily allowance:

| Call | Units | When |
|---|---|---|
| `liveChatMessages.list` | 5 | every chat poll while a broadcast is live |
| Looking for a broadcast to attach to | 1 | every 30s when nothing is live, backing off to every 5 minutes |
| Sending, deleting or banning | 50 | per action |

A Google Cloud project starts with **10,000 units a day**, resetting at midnight
Pacific. That is roughly 1,960 chat polls — so polyemesis paces itself across
the broadcast rather than spending the day's allowance by mid-afternoon, and
slows down while chat is quiet.

The allowance is **per project, not per channel**. That is why you create your
own rather than signing in to a shared polyemesis app: a shared client would put
every install on one 10,000-unit pool.

If a YouTube API Services audit grants you more, tell polyemesis in
`Settings → Pipeline → Chat history → YouTube daily API quota`. It paces on that number and has no
way to discover it by itself.

**There are two fields there, not one, and the 1,960 above is computed from
both.** Directly beneath the allowance sits *Held back for sending (units)*,
which defaults to **200**. Reading chat and posting into it are billed out of
the same daily allowance, so a poller left to spend the whole of it leaves a
moderator unable to type — and the message that could not be sent is the
timeout, at the moment it was needed. The pacer therefore subtracts the reserve
before dividing what is left by the 5 units a poll costs: (10,000 − 200) ÷ 5 =
1,960. At 50 units a message, 200 units is four messages — enough to say "we
are moving to Twitch" when it matters. Sending, deleting and banning spend
*into* the reserve rather than being refused by it; that is the whole point of
it.

Raising the allowance does not scale the reserve. A project granted a million
units still holds back 200 unless you change the second field, which is usually
fine and is worth knowing rather than discovering.

Two ways the reserve is refused on save, both by the same validator that
refuses a nonsense allowance:

* **More than half the allowance** — "youtube quota reserve 8000 is more than
  half the 10000-unit allowance, which would leave too little to read chat
  with". A reserve that large would pause reading almost immediately while the
  quota panel still showed a full tank, which reads as a bug rather than as a
  setting.
* **Negative** — "youtube quota reserve -1 cannot be negative".

Zero is accepted, and it means *unset* rather than "spend everything on
reading": the pacer reads a zero as the 200-unit default, because a settings row
written before this field existed reads as zero and honouring that literally
would take away your ability to speak on stream. The allowance itself is
different — it is refused below 1 or above 10,000,000, rather than quietly
defaulted, because an allowance that is too generous kills chat mid-broadcast
and leaves it dead until midnight Pacific.

**How many YouTube destinations can be live at once depends on their stream
keys.** Since February 2026 YouTube applies two concurrency limits together:

* **3** live streams sharing one stream key
* **10** live streams on one channel

polyemesis now gives each YouTube destination after the first its own ingest
stream, so they are separate ingestion sources and the channel limit is the one
that binds. The first destination on an account keeps using whatever reusable
stream the channel already has — that is the key your Studio-scheduled events
are bound to, and changing it would break a setup that works.

**If you already have several YouTube destinations, they are still sharing one
key until you say otherwise.** Nothing rotates a key on its own: a stream key
is pasted into an encoder, and moving it while somebody is broadcasting sends
their video to a stream nobody is watching. So an install set up before this
version keeps the ceiling it had until you press **Refresh stream key** on the
extra destinations — one at a time, and not while they are live. Destinations
that already have their own key are left alone when you do.

You will not see the refusal when you *create* a destination. YouTube applies
these limits only to streams that are actually live: a broadcast sitting in the
`upcoming` state does not count, and a channel already running its limit can
still hold many more scheduled. The refusal arrives at the moment a broadcast
tries to start.

If you do hit it, which of the two you hit tells you what to do. A full
*channel* means ending a broadcast that is running. A full *ingestion source*
means two destinations are still sharing a key — refresh one of them.

There are two ways past it that need no keys from us at all, and both work
whatever version you are on.

**Schedule, if your programmes do not need to overlap.** Staggered broadcasts
are not concurrent ones: only the streams actually live count. polyemesis keeps
a separate broadcast per schedule and per occurrence, so a weekly show gets a
new broadcast each week rather than reusing one.

**Or paste your own keys.** Create the broadcasts yourself in YouTube Studio —
each one gets its own stream key — and add a destination per key. Nothing about
a YouTube destination requires a connected account: the account is what lets
polyemesis FETCH a key for you, and a pasted one works exactly as well.

What that gives up is the automation, and it is worth knowing which parts. You
create and name each broadcast by hand, you paste and rotate each key by hand,
and **each broadcast still has to be started.** Whether pushing video to a
scheduled broadcast takes it live depends on that broadcast's auto-start
setting in Studio: with it off, YouTube marks the stream ready and waits for
someone to press Go Live. polyemesis can be publishing to all of them while
none is broadcasting.

These two numbers came from YouTube support rather than from Google's published
API documentation, which states that both limits exist and gives neither
figure. They are recorded here because knowing the order of magnitude is better
than being surprised, and they are deliberately not enforced anywhere in
polyemesis: a limit that is not published can change without notice, and may
depend on your channel's standing. YouTube's refusal is what decides, and
polyemesis reports it rather than predicting it.

**Broadcast settings have an editing window.** Alongside title, description and
category, polyemesis can push tags, the scheduled start, and YouTube's DVR,
auto-start and auto-stop toggles. The last three **stop being editable once a
broadcast leaves `created` or `ready`** — YouTube refuses them with errors such
as `enableDvrModificationNotAllowed` from that point on.

The go-live composer reads each account's broadcast state when it opens and
disables those controls once they have locked, naming the state that caused it.
That is advice, not enforcement: a broadcast can go live between the read and
the write, so the platform's refusal is still what decides, and it is reported
against the account it came from.

Set DVR and auto-start **before** going live. Everything else — title,
description, category, tags, scheduled start — stays editable throughout.

Each toggle also has a *Leave unchanged* option, which is the default and is
not the same as *Off*. Leaving one alone omits it from the request entirely so
YouTube keeps whatever it has; choosing *Off* actively turns the feature off.
The distinction matters because the API is destructive by part: a field omitted
from a part that IS being sent reverts to its default, so polyemesis reads the
current broadcast and carries every untouched field through on every write.

### Twitch

1. <https://dev.twitch.tv/console/apps> → **Register Your Application**.
2. OAuth Redirect URL: `https://YOUR_HOST/api/v1/oauth/twitch/callback`
3. Category: *Broadcasting Suite*. Client Type: **Confidential**.
4. **Manage**, then **New Secret**. Paste the **Client ID** and the
   **Client Secret** into the fields of the same names in polyemesis.

Connect account sends a PKCE challenge (RFC 7636, S256) along with the client
secret, which the app keeps; the client stays **Confidential**. Twitch's discovery
document has advertised S256 since October 2026. There is nothing to configure.
Accounts that were already connected keep refreshing as they did, because PKCE
only applies to a new authorization.

Scopes requested, all eight: `channel:read:stream_key` (the key),
`channel:manage:broadcast` (title and category at go-live), `chat:read` and
`chat:edit` (the unified chat pane), and the three that make the Moderation row
above read *Works* — `moderator:manage:chat_messages` (delete a message),
`moderator:manage:banned_users` (ban, timeout, and lift either) and
`moderator:manage:chat_settings`. The moderation three only work in a channel
the connected account already moderates; Twitch answers 403 otherwise.
`channel:manage:polls` mirrors a [chat poll](POLLS.md) as a native Twitch poll;
Twitch runs polls for Affiliates and Partners only, and on any other channel
the mirror is skipped and the chat poll runs as usual. It was added in scope
version 5, so a Twitch account connected before it is marked "reconnect
needed" once.

Granting a scope does not upgrade a token you already hold — if you connected
Twitch before chat landed, connect it again once. Connecting over an account
that is already there keeps it, and its destinations, linked; disconnecting
first unlinks them. Chat follows the account list both ways, at once:
connecting an account starts its chat, connecting it again restarts its chat
with the new token (which is also how a chat whose login Twitch refused comes
back), and disconnecting it, or removing its platform's credentials, ends it.

polyemesis now says so itself. Each platform carries a scope version that is
stored with the account, and **Settings → Platform credentials marks an account
"reconnect needed"** when the running build asks for more than that account was
granted. Accounts connected before the version existed are judged on the scopes
the platform actually returned, so an account that already holds everything is
left alone rather than nagged.

### Facebook Live (Meta)

**Read this first: Meta requires App Review before anyone but you can connect an
account.** Your own Facebook account works immediately as a developer or tester
of your own app, which is all a single-operator setup needs. Publishing on
anyone else's behalf needs Advanced Access to `publish_video` (profiles) or
`pages_manage_posts` plus `pages_read_engagement` (Pages). Budget days, not
minutes, and start it before you need it.

1. <https://developers.facebook.com/apps> → **Create app**, use case *Other*,
   type *Business*.
2. Add the **Facebook Login** product; under its settings enable Client and Web
   OAuth login and add the redirect URI:
   `https://YOUR_HOST/api/v1/oauth/facebook/callback`
3. **App settings → Basic**: paste the **App ID** and the **App Secret** (click
   **Show** beside it) into the polyemesis fields of the same names.
4. **Connect account**, then pick your profile or a Page.

The key this fetches belongs to one live video, so *Refresh key* on a Facebook
destination starts a new broadcast rather than re-reading an existing one.

If you would rather paste a key that lasts, Live Producer → *Advanced settings*
→ **Persistent stream key** issues one that is reusable every time you go live.
polyemesis cannot fetch it — Meta's API exposes no way to read it — and a pasted
key has no live-video id, so title, chat and *End broadcast* stay with the
connected account. See the table under *Facebook — read this before you start*.

#### What a Facebook destination can be told to do

All of these are set on the destination and applied when the broadcast is
created, so they take effect on the next go-live rather than immediately.

**Audience, crossposting and a donate button.** The audience is suppressed for
Page targets — a Page broadcast has no personal audience for a value like
*only me* to apply to — so the control is not offered there rather than being
accepted and discarded. Crossposting names the Pages a broadcast is shared
with; the donate button names one charity.

**A broadcast announced before it starts.** A destination on a start schedule
gets its Facebook broadcast created ahead of time, so there is a public event
page days early instead of one appearing the moment bytes arrive.

Facebook accepts a start time at most **seven days** ahead. That bounds far
less than it sounds: the next occurrence of a daily schedule is at most a day
away and of a weekly one at most seven, by definition — so only a *one-off*
schedule can be set beyond it. One that is gets no event page, saves anyway,
runs anyway, and says so.

If you delete the scheduled video on Facebook, polyemesis notices after three
consecutive failed attempts to move it and creates a fresh one. A single
network failure changes nothing.

**A redundant backup feed.** Facebook can provision a second ingest endpoint,
and polyemesis will publish to both — so a dropped connection does not drop the
broadcast. It is **off by default** because it doubles that destination's
upload bandwidth and its audio encoding cost.

Turning it on **reconnects the stream once**, and that is unavoidable rather
than careless: a backup endpoint exists only on a broadcast created with one,
and creating that broadcast issues a new stream key. **Enable it before you go
live, not during.**

Facebook decides which of the two feeds it takes. polyemesis publishes both and
does not attempt to choose, because no endpoint reports which one is being
ingested. The card shows the backup's own state beside the primary's — a backup
that has quietly died is worse than none, since you would believe you had one.

### Kick

Kick signs in and the stream key is fetched, like every other platform here.
This section used to open by saying the key still had to be pasted; that was
true once and stopped being true when `streamkey:read` was added. See the
Kick note further up this page for why the key looked unfetchable for so long.

1. <https://kick.com/settings/developer> → create an OAuth application.
2. Redirect URI: `https://YOUR_HOST/api/v1/oauth/kick/callback`
3. Paste the **Client ID** and **Client Secret** into the fields of the same
   names in polyemesis, then **Connect account**. Kick speaks OAuth 2.1, so polyemesis sends a PKCE challenge automatically —
   it is the first provider here that does.
4. Nothing to paste. The ingest URL and stream key are fetched for you over
   the `streamkey:read` scope.

Connecting is worth doing anyway — it pushes your title and category (resolving
categories by name rather than by numeric id), carries chat both ways, and
reports viewer counts.

Scopes requested: `user:read`, `channel:read`, `channel:write`, `chat:write`,
`moderation:chat_message:manage`, `moderation:ban`, `events:subscribe`,
`streamkey:read`.

`moderation:ban` covers banning and timing out a viewer, and lifting either. It
was deliberately omitted at first, on the grounds that nothing here banned
anyone and that asking a restreamer's audience for the power to do so read as
overreach. That was reversed when moderation shipped: automod's action matrix
includes timeout and ban, so the scope is requested up front rather than asked
for silently later. The original argument is kept in `internal/oauth/kick.go`
rather than deleted, because it is what to re-read if the decision is revisited.

Adding a scope later does **not** upgrade an existing connection — it forces
every operator to disconnect and reconnect, and discovering that mid-broadcast
is the worst possible moment. That is why the list is settled in one go rather
than grown as features land.

Kick delivers chat over a webhook rather than a socket, so the chat pane needs a
public HTTPS URL Kick can reach. Without one it says so rather than sitting
silently.

**Setting up Kick chat takes two things, and polyemesis does the second.**

1. In your Kick app (Kick → Settings → Developer → your app) switch on
   **Enable webhooks** and paste the **Webhook URL to paste into your Kick app**
   shown under Settings → Platform credentials → Kick. The URL carries a secret
   derived from the data key, so it is shown only to a signed-in operator or an
   admin token, never to a read-only token, and never in a log or a support
   bundle. With no public HTTPS hostname configured the panel says that instead.
2. polyemesis subscribes the app to `chat.message.sent` itself, with the
   connected account's `events:subscribe` permission: when Kick chat attaches,
   it lists the app's subscriptions for the channel and creates the missing one
   (never a duplicate). Kick disables a subscription after a day of failed
   deliveries, and switching webhooks off drops every subscription, so it
   checks again every 30 minutes (2 minutes after a failed check) and creates
   it again if it has gone. Kick's listing can omit a subscription that exists (KickEngineering/KickDevDocs issue #419), so an empty listing is trusted for 6 hours once polyemesis has created the subscription, and a creation Kick answers as a duplicate counts as success. If Kick refuses, the Kick chat card says so, for
   example "Kick webhook not enabled for your app". Nothing is deleted on
   shutdown: subscriptions persist per app and channel, and deleting one on
   every restart would drop the chat Kick delivers in between.

Kick caps a chat message at 500 user-perceived characters **and** 2048 bytes of
UTF-8, so a run of emoji is refused on bytes long before it reaches 500
characters; polyemesis counts both and refuses with the number that was exceeded.

## Twitch token validation

Twitch requires every app that holds a user's token to validate it at startup
and every hour (`GET https://id.twitch.tv/oauth2/validate`) and to end the
sessions of a token that is no longer valid. polyemesis does this for each
connected Twitch account. On a 401 it spends one refresh to rule out a merely
stale token; if Twitch still refuses (or refuses the refresh itself), the
account's chat session is stopped and the account shows as needing a reconnect
in Settings → Platforms. Reconnecting clears it. A Twitch outage or network
failure is never treated as a revocation.

## Multiple accounts

Connect the same platform more than once to stream to several channels. Each
connected account becomes its own destination with its own routing profile.

Tokens and client secrets are encrypted at rest with NaCl secretbox, keyed by
`secret.key` in the data directory, and refreshed automatically. No stream key,
client secret or token is ever returned by an API or written to a log.

---

## Compliance metadata

YouTube's COPPA self-declaration and privacy status, and Twitch's content
classification labels, are things the platform's own terms require declared
accurately — not stylistic fields. They have been storable and editable in the
destination editor for a while. **This is the first release that sends them
anywhere.**

They ride along on the same metadata push as title and description — the one
the composer's "Push to platforms" button already starts — rather than needing a
separate action. Any destination that has a privacy status, a COPPA
declaration or a content label saved sends it on the very next push after
upgrading, whether or not that push actually changes the title or description.
**Nothing was opt-in about this before, and nothing is opt-in now**: a
destination configured months ago, carrying a setting nobody currently
remembers choosing, starts declaring it the next time anyone pushes metadata
to that account. If what is currently stored on each destination matters to
you, check it before the first push after upgrading.

Coverage by platform:

- **YouTube** — `privacyStatus` and `selfDeclaredMadeForKids`.
- **Twitch** — `content_classification_labels`.
- **Facebook** — the broadcast's audience, through the same read-back-confirmed
  path described in the Facebook section above.
- **Kick has no compliance API at all**, and since 0.2.0 a Kick destination
  cannot hold compliance either. Saving one **clears the setting and says so**,
  because a stored declaration that can never be sent is not merely useless: it
  becomes live again the moment that destination is pointed back at a platform
  that does have a compliance surface — a legal statement transmitted on behalf
  of an operator who last saw it in a form they abandoned.

  A destination carrying compliance from before the upgrade keeps it until its
  next save, and is reported **skipped, with a reason**, on any push until then
  — not silently dropped, and not treated as a failure.

**Compliance is stored per destination; the platform account that receives it
is shared.** A push targets an account, and a connected account can be the
destination for more than one entry in the destination list. If two
destinations sharing one connected account disagree — different privacy,
different COPPA declaration, different labels — the push is refused before
anything is sent, naming both destinations so it is clear which two disagree.
Identical compliance on two destinations sharing an account is not a conflict.
The refusal considers only the accounts the push in question actually
addresses: a disagreement sitting on an account you did not select does not
block the push you asked for, but if the account you did select is one of the
two that disagree, the whole push — every account in it, not just that one —
does not go out.

---

## See also

- [RENDITIONS.md](RENDITIONS.md) — matching your video to what a platform accepts
- [AUDIO-ROUTING.md](AUDIO-ROUTING.md) — giving each platform a different mix
- [TLS.md](TLS.md) — OAuth redirect URIs need a reachable HTTPS host
- [../SECURITY.md](../SECURITY.md) — how credentials are stored
