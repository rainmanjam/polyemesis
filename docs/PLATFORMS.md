# Streaming platforms

Two separate questions, often confused:

1. **Can polyemesis stream there?** Almost always yes. Anything that accepts
   RTMP, RTMPS or SRT works with a pasted URL and key.
2. **Can polyemesis automate the setup?** That depends entirely on what the
   platform's published API offers, and it varies enormously.

This page answers both, then gives the OAuth app setup for each platform that
supports sign-in.

- [Capability matrix](#capability-matrix)
- [The four that need a sentence each](#the-four-that-need-a-sentence-each)
- [Connecting an account](#connecting-an-account)
- [Multiple accounts](#multiple-accounts)

---

## Capability matrix

Streaming itself works the same everywhere: per-destination audio routing,
renditions, reconnect, meters and recording do not depend on a single column
below. The table is what each platform's **published API** allows today.

The same matrix is rendered in `Settings → Platform credentials` and served from
`GET /api/v1/platforms/capabilities`.

| Platform | Sign in | Stream key | Metadata | Chat read | Chat send | Moderation | Viewers |
|---|---|---|---|---|---|---|---|
| **YouTube Live** | Works | Works | Works | Works | Works | Unverified | Unverified |
| **Twitch** | Works | Works | Works | Works | Works | Unverified | Unverified |
| **Facebook Live** | Works | Works | Works | Works | Unverified | Unverified | Unverified |
| **Kick** | Works | Works | Works | Works | Works | Works | Works |
| **X (Twitter)** | Not possible | By hand | Not possible | Not possible | Not possible | Not possible | Not possible |
| **Rumble** | Unverified | By hand | Unverified | Unverified | Unverified | Unverified | Unverified |
| **DLive** | Unverified | By hand | Unverified | Unverified | Unverified | Unverified | Unverified |
| **Instagram Live** | Not possible | **Not possible** | Not possible | Not possible | Not possible | Not possible | Not possible |
| *Everything else* | — | By hand | — | — | — | — | — |

| Term | Means |
|---|---|
| **Works** | polyemesis does this for you today |
| **By hand** | Supported, with one step you do yourself, usually pasting a key. A pasted key is a fully supported destination, not a degraded one |
| **Unverified** | Not built, and the platform's API not confirmed either way. Never a refusal — nothing stops you trying |
| **Not possible** | Somebody read the platform's published API and the thing is not in it. No amount of setup will produce it |

*Everything else* is the other twenty-five entries in the destination preset
catalogue — PeerTube, Owncast, Cloudflare Stream, Mux, AWS IVS, LinkedIn, Trovo,
Odysee, Vimeo, Dailymotion and the rest. They stream perfectly over RTMP, RTMPS
or SRT with a pasted URL and key; we simply have not researched their APIs, and
"unverified" is the honest thing to say about an API nobody here has read.

## The four that need a sentence each

**Facebook — read this before you start.** Full support, and Meta requires
**App Review** first. Your own account works immediately as a developer or
tester of your own app, which is all a single-operator setup needs. Publishing
on anyone else's behalf needs Advanced Access to `publish_video` (profiles) or
`pages_manage_posts` plus `pages_read_engagement` (Pages). That review is Meta's
process and yours to complete, not something polyemesis can shorten, and it is
measured in days. Start it before you need it. Facebook also issues a fresh
ingest and key per broadcast, so connecting the account is what creates the
broadcast — there is no permanent key to reuse.

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
once.** Granting a scope never upgrades a token that has already been issued.

**X (Twitter) — paste your key, there is no API.** X's developer platform covers
posts, users, media and the post firehose. "Streaming" in its documentation
means streaming posts, not ingesting video, and there is no documented
third-party live-video ingest endpoint; access to what *is* documented is
credit-based and paid. Create the source in X's own producer tooling and copy
the URL and key across.

**Instagram — polyemesis cannot stream here.** Instagram's platform covers
messaging, content publishing and comments. There is no Live broadcast API, and
Live Producer's RTMP path was removed for most accounts. It is listed, and
marked unsupported in the destination picker, rather than shipped as a preset
that quietly never connects — a destination that fails silently looks exactly
like a bug in polyemesis, and there is nothing to fix. If your account is one of
the exceptions that still has Live Producer RTMP, add a **Generic RTMPS**
destination and paste what Meta gives you. Check that you have it before you
build a show around it.

**Rumble** and **DLive** are marked *unverified* rather than *unsupported* on
purpose. Rumble's API page at `rumble.com/account/api` sits behind a login and
publishes nothing, and DLive's developer portal at `dev.dlive.tv` no longer
resolves in DNS. Neither fact tells us what those APIs can do, so we make no
claim — undocumented is not the same as absent. Streaming to both works today
with a pasted URL and key.

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

Scopes requested: `channel:read:stream_key` (the key), `channel:manage:broadcast`
(title and category at go-live), `chat:read` and `chat:edit` (the unified chat
pane).

Granting a scope does not upgrade a token you already hold — if you connected
Twitch before chat landed, disconnect and reconnect once.

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

### Kick

Kick signs in, and the stream key still gets pasted. Both halves are real at
once, and neither is a workaround for the other.

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
`moderation:chat_message:manage`, `events:subscribe`, `streamkey:read`. `moderation:ban` is
deliberately not requested: nothing in polyemesis bans or times out a viewer.

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

## See also

- [RENDITIONS.md](RENDITIONS.md) — matching your video to what a platform accepts
- [AUDIO-ROUTING.md](AUDIO-ROUTING.md) — giving each platform a different mix
- [TLS.md](TLS.md) — OAuth redirect URIs need a reachable HTTPS host
- [../SECURITY.md](../SECURITY.md) — how credentials are stored
