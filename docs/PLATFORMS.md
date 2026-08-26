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

| Platform | Sign in | Stream key | Metadata | Chat read | Chat send | Moderation | Viewers  Start / end |
|---|---|---|---|---|---|---|---|---|
| **YouTube Live** | Works | Works | Works | Works | Works | Works | Works | Works |
| **Twitch** | Works | Works | Works | Works | Works | Works | Works | Not possible |
| **Facebook Live** | Works | Works | Works | Works | Not possible | Works | Unverified | Works |
| **Kick** | Works | Works | Works | Works | Works | Works | Works | Not possible |
| **Vimeo Livestream** | Works | By hand | Unverified | Unverified | Unverified | Unverified | Unverified | Unverified |
| **X (Twitter) Live** | Unverified | By hand | Unverified | Unverified | Unverified | Unverified | Unverified | Unverified |
| **Rumble** | Not possible | By hand | By hand | Works | Not possible | Not possible | Unverified | Not possible |
| **DLive** | Unverified | By hand | Unverified | Unverified | Unverified | Unverified | Unverified | Unverified |
| **Trovo** | Unverified | By hand | Unverified | Unverified | Unverified | Unverified | Unverified | Unverified |
| **Odysee** | Unverified | By hand | Unverified | Unverified | Unverified | Unverified | Unverified | Unverified |
| **Dailymotion** | Unverified | By hand | Unverified | Unverified | Unverified | Unverified | Unverified | Unverified |
| **TikTok LIVE** | Unverified | By hand | Unverified | Unverified | Unverified | Unverified | Unverified | Unverified |
| **LinkedIn Live** | Unverified | By hand | Unverified | Unverified | Unverified | Unverified | Unverified | Unverified |
| **Instagram Live** | Not possible | Not possible | Not possible | Not possible | Not possible | Not possible | Not possible | Not possible |
| *Everything else* | — | By hand | — | — | — | — | — |

| Term | Means |
|---|---|
| **Works** | polyemesis does this for you today |
| **By hand** | Supported, with one step you do yourself, usually pasting a key. A pasted key is a fully supported destination, not a degraded one |
| **Unverified** | Not built, and the platform's API not confirmed either way. Never a refusal — nothing stops you trying |
| **Not possible** | Somebody read the platform's published API and the thing is not in it. No amount of setup will produce it |

*Everything else* is the other nineteen entries in the destination preset
catalogue — PeerTube, Owncast, Cloudflare Stream, Mux, AWS IVS, LinkedIn, Trovo,
Odysee, Dailymotion and the rest. They stream perfectly over RTMP, RTMPS
or SRT with a pasted URL and key; we simply have not researched their APIs, and
"unverified" is the honest thing to say about an API nobody here has read.

## The platforms that need a sentence each

**Facebook — read this before you start.** Full support, and Meta requires
**App Review** first. Your own account works immediately as a developer or
tester of your own app, which is all a single-operator setup needs. Publishing
on anyone else's behalf needs Advanced Access to `publish_video` (profiles) or
`pages_manage_posts` plus `pages_read_engagement` (Pages). That review is Meta's
process and yours to complete, not something polyemesis can shorten, and it is
measured in days. Start it before you need it. Facebook also issues a fresh
ingest and key per broadcast, so connecting the account is what creates the
broadcast — there is no permanent key to reuse.

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
scope, which Kick's own Get Channels page does not list among its required
scopes. Invisible twice over.

**An account connected before this landed must be disconnected and reconnected
once.** Granting a scope never upgrades a token that has already been issued —
and Settings → Platforms flags exactly this, so it does not have to be
remembered from a page of documentation.

**X (Twitter) — paste your key, there is no API.** X's developer platform covers
posts, users, media and the post firehose. "Streaming" in its documentation
means streaming posts, not ingesting video, and there is no documented
third-party live-video ingest endpoint; access to what *is* documented is
credit-based and paid. Create the source in X's own producer tooling and copy
the URL and key across.

**Instagram — polyemesis cannot AUTOMATE it, but it will push to it.** The
distinction matters and this page used to collapse it. Instagram's platform
covers messaging, content publishing and comments: there is no Live broadcast
API, so nothing here can create a broadcast, fetch a key, read chat or report
viewers. That is what "unsupported" means, and it is why the preset exists.

What it does NOT mean is that the bytes are refused. `TierUnsupported` feeds
`UnsupportedPresets()`, which only tells the destination picker to mark the row;
nothing in the streaming path consults it. If your account is one of the few
that still has Live Producer, take its server URL and key, create a **Generic
RTMPS** destination, and it streams like any other — with its own per-destination
audio mix. The key changes every broadcast, so it is manual each time. It is listed, and
marked unsupported in the destination picker, rather than shipped as a preset
that quietly never connects — a destination that fails silently looks exactly
like a bug in polyemesis, and there is nothing to fix. If your account is one of
the exceptions that still has Live Producer RTMP, add a **Generic RTMPS**
destination and paste what Meta gives you. Check that you have it before you
build a show around it.

**TikTok LIVE** and **LinkedIn Live** are *unverified* for a different reason
than DLive: their APIs demonstrably exist and answer — a request to
`open.tiktokapis.com` or `api.linkedin.com` comes back with a structured
authentication error, not a 404 — but the live surfaces sit behind partner
programmes rather than open developer registration. Partner-gated is not the
same as absent, and this project has not applied, so *unverified* is the honest
word. Somebody inside either programme may well find every column is a yes.

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

### YouTube (Google)

1. <https://console.cloud.google.com/apis/credentials> — create or pick a project.
2. **APIs & Services → Library** → enable **YouTube Data API v3**.
3. **OAuth consent screen** → External; add your own Google account under
   *Test users*. You do not need to publish the app.
4. **Credentials → Create Credentials → OAuth client ID → Web application.**
5. Add the redirect URI shown on the credentials page, exactly:
   `https://YOUR_HOST/api/v1/oauth/youtube/callback`
6. Paste the client ID and secret into polyemesis, then **Connect account**.

Scope requested: `https://www.googleapis.com/auth/youtube`. Write access is
needed because polyemesis creates a reusable ingest stream if your channel has
none.

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
4. **Manage → New Secret**, then paste both values into polyemesis.

Scopes requested, all seven: `channel:read:stream_key` (the key),
`channel:manage:broadcast` (title and category at go-live), `chat:read` and
`chat:edit` (the unified chat pane), and the three that make the Moderation row
above read *Works* — `moderator:manage:chat_messages` (delete a message),
`moderator:manage:banned_users` (ban, timeout, and lift either) and
`moderator:manage:chat_settings`. The moderation three only work in a channel
the connected account already moderates; Twitch answers 403 otherwise.

Granting a scope does not upgrade a token you already hold — if you connected
Twitch before chat landed, disconnect and reconnect once.

polyemesis now says so itself. Each platform carries a scope version that is
stored with the account, and **Settings → Platforms marks an account
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
3. **App settings → Basic** — the App ID is the client ID and the App Secret is
   the client secret. Paste both into polyemesis.
4. **Connect account**, then pick your profile or a Page.

Facebook issues a fresh ingest URL and stream key for every broadcast, so
connecting is what creates the broadcast and there is no permanent key to reuse.
"Refresh key" on a Facebook destination therefore starts a new live video rather
than re-reading an existing one.

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
3. Paste the client ID and secret into polyemesis, then **Connect account**.
   Kick speaks OAuth 2.1, so polyemesis sends a PKCE challenge automatically —
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
the composer's "Push metadata" button already starts — rather than needing a
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
