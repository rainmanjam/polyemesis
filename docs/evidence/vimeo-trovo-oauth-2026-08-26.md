# Vimeo and Trovo OAuth and live APIs, checked 2026-08-26

Researched because both appear in the destination preset catalogue
(`internal/db/platforms.go`) and `docs/PLATFORMS.md` records them as
*unverified* — "not built, and the platform's API not confirmed either way".
This file confirms them either way. It does not propose building either.

Same standard as `platform-lifecycle-apis-2026-08-16.md`: every claim traces to
the platform's own reference page, read on the date in the title, with the
operative sentence quoted verbatim. A capability with no dated source is not
recorded.

## The fetch trap, again, in a new shape

The 2026-08-16 pass recorded two hosts that answer **HTTP 200 with a
"page not found" body**. A third shape was hit here and is worth adding:

* `developer.vimeo.com` answers **HTTP 200 with a 0-byte body containing only
  the word "Vimeo"** to a non-browser fetch. The documentation is rendered
  client-side. A fetcher that trusted the status code would have concluded the
  page was empty, and a fetcher that trusted its own memory would have filled
  the gap from training data — which is the failure this file format exists to
  prevent. Every Vimeo claim below was read from a **rendered** page.

`developer.trovo.live` serves static HTML and needed no browser.

---

## The finding that decides Vimeo

> "Please note that our live API is available only to Vimeo Enterprise
> customers."
>
> — [Vimeo API Reference: Live](https://developer.vimeo.com/api/reference/live),
> read 2026-08-26, rendered

**Vimeo's OAuth is open to any app; Vimeo's LIVE API is not.** The distinction
matters more than the presence of a token endpoint: an operator can authenticate
and still be unable to create an event. For a self-hosted product whose users
are individuals and small teams, that is the same practical wall as LinkedIn
Live's partner gate — the flow works and the capability is unreachable.

Also recorded, because it changes what anyone building this should target:

> "One-time live events are being deprecated. We recommend that you avoid using
> the methods for these."
>
> — same page, read 2026-08-26

### Vimeo OAuth, which is genuinely open

| item | value | source |
|---|---|---|
| authorize | `https://api.vimeo.com/oauth/authorize?response_type=code&client_id={client_id}&redirect_uri={redirect_uri}&state={state}&scope={scope_list}` | [Working with Authentication](https://developer.vimeo.com/api/authentication), read 2026-08-26 |
| token exchange | `https://api.vimeo.com/oauth/access_token` | same |
| device flow | `https://api.vimeo.com/oauth/device`, `https://api.vimeo.com/oauth/device/authorize` | same |
| grant types | client credentials, authorization code, implicit, **device code** | same, Table 2 |
| scopes | `public`, `private`, `purchased`, `create`, `edit`, and others in Table 1 | same, Table 1 |

Device code is present, which is the flow polyemesis already implements in
`internal/oauth/device.go`. Had the live API been reachable, this would have been
the cheapest of the two to add.

**PKCE: not established.** The authentication page's grant-type table does not
mention it and no PKCE parameter was seen. Recorded as *unknown*, not as absent.

### Vimeo live capabilities, for the record

Behind the Enterprise gate, the surface is complete: create / update / delete an
event, **activate an event**, **end an event**, get ingest status, event
destinations (including RTMP), M3U8 playback, thumbnails, speakers, audio-track
settings and viewer analytics export. So the lifecycle polyemesis models —
create, go live, end — maps cleanly. The obstacle is commercial, not technical.

---

## Trovo: fully documented and open

Every row below is from
[Trovo APIs & OAuth Developer Doc](https://developer.trovo.live/docs/APIs.html),
read 2026-08-26.

| capability | verdict | endpoint | scope |
|---|---|---|---|
| authorize (code flow) | documented | `https://open.trovo.live/page/login.html?client_id=…&response_type=code&scope=…&redirect_uri=…&state=…` | n/a |
| authorize (implicit) | documented | same URL, `response_type=token` | n/a |
| validate token | documented | `GET https://open-api.trovo.live/openplatform/validate` | **None** |
| revoke token | documented | §4.2 | — |
| refresh token | documented | `POST https://open-api.trovo.live/openplatform/refreshtoken` | — |
| **stream key** | documented | `GET https://open-api.trovo.live/openplatform/channel` | `channel_details_self` |
| metadata (title, category, language, audience) | documented | `POST https://open-api.trovo.live/openplatform/channels/update` | `channel_update_self` |
| chat read | documented | websocket, see [Chat Service](https://developer.trovo.live/docs/Chat%20Service.html) | — |
| chat send | documented | `POST https://open-api.trovo.live/openplatform/chat/send` | `chat_send_self` + `send_to_my_channel` |
| moderation (ban, mod, delete) | documented | `POST https://open-api.trovo.live/openplatform/channels/command` | `manage_messages` |
| viewer count | documented | `POST https://open-api.trovo.live/openplatform/channels/{channel_id}/viewers` | **None**, and no access token |
| broadcast start / end | **not possible** | no such endpoint exists in the reference | — |

### The full scope list, verbatim

| Scope | Description |
|---|---|
| `user_details_self` | View your email address and user profiles. |
| `channel_details_self` | View your channel details. Including Stream Key. |
| `channel_update_self` | Update your channel settings |
| `channel_subscriptions` | Get your subscribers list. |
| `chat_send_self` | Send chat messages on behalf of myself. |
| `send_to_my_channel` | Send chat messages to my channel. |
| `manage_messages` | Perform chat commands and delete chat messages. |

### Refresh, which is the half that matters at hour four

> "A refresh token can hold a maximum of 50 access tokens at the same time. If
> exceeded, you should wait for the old access tokens to expire before you can
> refresh again."

> "the new segment "refresh_token" is added in the response. This means you can
> always get a new refresh_token with 30 days lifetime from your old effective
> refresh_token before it's going to expire."

Access tokens expire in `14400` seconds — four hours, per the response sample.
Refresh tokens last 30 days and are rotated on use, and the old one keeps working
until it expires. **The fifty-token ceiling is the trap**: a client that refreshes
on a timer rather than on expiry would exhaust it, and the failure arrives as a
refused refresh rather than as a rejected request.

### Rate limit — a documented number, which is unusual

> "When an application is first registered, your application will get a rate
> limit of 1200 requests per minute."

Headers `x-ratelimit-limit`, `x-ratelimit-remaining`, `x-ratelimit-reset`, and
status `11706` with message "API rate limit exceeded" when exhausted.

The 2026-08-16 file's rule was that a numeric limit the docs refuse to state must
stay unstated in code. Trovo states one, so it may be relied on — but the header
is still the authority, because the doc also says a higher limit can be granted
per client id.

---

## What is NOT recorded here

* **Trovo's code→token exchange endpoint.** §3.2 describes the authorize step and
  the reference documents validate, revoke and refresh. The exchange call itself
  was not captured verbatim in this pass. Anyone implementing must read §3.2 step
  2 and record it before writing the call — do not infer it from the refresh
  endpoint's shape.
* **Whether Trovo supports PKCE.** Not mentioned. The documented code flow uses a
  `client_secret`, which for polyemesis means a confidential client.
* **Vimeo Enterprise pricing or whether a trial exposes the live API.** Not a
  documentation question.

## What this changes today

Nothing. `docs/PLATFORMS.md` records both as *unverified*, and on this evidence:

* **Trovo** would move to buildable — seven capabilities documented with scopes,
  and it maps onto polyemesis's existing matrix better than any unbuilt platform
  examined so far. Start/end is genuinely *not possible*, which matches Twitch
  and Kick.
* **Vimeo** should be recorded as gated rather than unverified. The API exists,
  is complete, and is unreachable without an Enterprise contract.

Updating `PLATFORMS.md` is deliberately left to whoever decides to act on this;
a matrix cell that says "Works" must mean polyemesis does it today.
