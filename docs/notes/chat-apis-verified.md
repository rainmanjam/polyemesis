# Platforms with an official chat API — probed, not remembered

Probed 2026-08-13 from a live network. A `401`/`400`/`403` is a **pass**: it
means the endpoint exists, is serving, and is asking for credentials. `404`,
`502` and DNS failure are the failures.

This is evidence that an API *exists and answers*. It is not evidence that its
terms permit what we want, nor that its chat surface is complete — both need
reading before any adapter is written.

## Already in polyemesis

| Platform | Mechanism |
|---|---|
| Twitch | IRC over TLS + Helix |
| YouTube | Data API `liveChatMessages`, quota-paced |
| Kick | webhooks, signature-verified against their published key |
| Facebook | graph |

## Verified live, not yet integrated

| Platform | Endpoint probed | Status | Notes |
|---|---|---|---|
| **Discord** | `/api/v10/gateway` | **200** | Bot + websocket gateway. Fully documented, no scraping, generous terms. |
| **Telegram** | `api.telegram.org` Bot API | **401** | Bot API. Small integration, long-stable. |
| **Slack** | `apps.connections.open` | **200** | Socket Mode. Real events API. |
| **Mastodon** | `/api/v1/instance` | **200** | Per-instance streaming API. Open. |
| **Bluesky** | `describeServer` + Jetstream | **200** | Jetstream answers `Welcome to Jetstream`. Firehose is public. |
| **Matrix** | `/_matrix/client/versions` | **200** | Open standard, self-hostable. |
| **Owncast** | `/api/chat` | **400** | *"Query argument accessToken is required"* — that is the chat API itself, answering. We already ship an Owncast destination preset. |
| **PeerTube** | `/api/v1/config` | **200** | Core API live. **Chat is a plugin** (`peertube-plugin-livechat`, XMPP-backed), not core — so support is per-instance, not universal. |
| **Picarto** | `api.picarto.tv/api/v1/online` | **200** | Public API. |
| **Rumble** | `-livestream-api/get-data` | **400** | Needs an API key; endpoint is real. **Now integrated** — 400 with no key, 403 with a bad one. Chat rides in `livestreams[].chat.recent_messages`. See `internal/chat/rumble.go`. |
| **Twitcasting** | `verify_credentials` | **401** | Official API. |
| **Vimeo** | `api.vimeo.com` | **401** | Live event chat exists on paid tiers. |
| **Dailymotion** | `api.dailymotion.com` | **200** | API live; live-chat surface needs checking. |
| **Zoom** | `api.zoom.us/v2` | **401** | Official, but webinar/meeting chat, not broadcast chat. |

## Correction: Trovo is not dead

I reported earlier, from Social Stream's "chat graveyard", that Trovo died in
April 2026. **That looks wrong.**

    POST open-api.trovo.live/openplatform/chat/channel-token/1
    400 {"status":11701,"error":"invalidHeader","message":"Invalid header."}

That is a chat API parsing a request and rejecting it for a missing auth
header — a live, functioning service. `trovo.live` also still serves
`robots.txt` and its app shells.

So: their graveyard is a useful signal, not a source of truth. **Do not remove
the Trovo preset on its say-so.**

DLive is a different matter and remains confirmed dead — `dlive.tv` serves the
literal text *"DLive Service Discontinued"*.

## Failed the probe

| Platform | Result |
|---|---|
| Odysee / LBRY comment server | `502` on both `comments.odysee.com` and `comments.lbry.com` |
| Nimo TV | DNS failure |

Neither is proof of death — a 502 is an outage — but neither is something to
build against today without a second look.

## Restricted, and deliberately excluded

**TikTok**, **Instagram**, **X**, **LinkedIn**. All have APIs; none grant
practical live-chat read access to a self-hosted tool. The routes that do work
are unofficial, and unofficial access to these four is precisely what gets user
accounts banned. If they are wanted, they belong behind a Social Stream–style
bridge that the user opts into on their own desktop, not in a polyemesis
adapter that looks official.

## Ranking, for a polyemesis user

1. **Discord** — biggest genuine gap. Official gateway, no scraping, and it is
   where a streamer's community lives between broadcasts.
2. **Owncast** — we already stream to it, the chat API is confirmed answering,
   and it is self-hosted, which matches this product exactly.
3. **Telegram**, **Matrix**, **Mastodon**, **Bluesky** — open protocols, small
   adapters, no rate-limit politics.
4. **Trovo**, **Rumble**, **Picarto**, **Twitcasting** — real APIs, smaller
   audiences; worth doing when someone asks.
5. **PeerTube** — wanted, but chat is a per-instance plugin, so it is a weaker
   promise than the others.
