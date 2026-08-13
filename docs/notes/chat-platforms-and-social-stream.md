# Chat sources: where we are, what Social Stream Ninja does, and what to take

Written 2026-08-13, before building the live chat acceptance suite.

## What polyemesis has today

Four adapters in `internal/chat`: Twitch, YouTube, Kick, Facebook. All four use
**official APIs** — Twitch IRC over TLS, Kick webhooks with signature
verification against `api.kick.com`'s published public key, the YouTube Data
API with a quota pacer, Facebook's graph.

10,063 lines of adapter code, 4,494 lines of tests, and **no test that opens a
socket to any of them**.

## Social Stream Ninja

<https://github.com/steveseguin/social_stream> — browser extension and desktop
app, MIT-ish, very active, built on VDO.Ninja's peer-to-peer data transport.
**124 sites listed in the README**, and it claims 120+.

### How it actually works, and why that matters to us

> **Zero authentication**: No logins, API keys, or permissions required for most
> platforms

It reads chat by **scraping the DOM of a chat page you have open in a
logged-in browser**. That is the whole trick, and it explains both the
platform count and the trade-offs.

Their own README:

> Using this application may potentially violate the Terms of Service of some
> social media platforms.

The two approaches have opposite failure modes:

| | polyemesis | Social Stream |
|---|---|---|
| Source | official APIs | DOM of an open page |
| Platforms | 4 | 124 |
| Breaks when | the API changes (rare, announced, versioned) | the site restyles (often, silent) |
| Needs | a token | a logged-in browser, open, on a desktop |
| Runs headless | yes | no |
| ToS | within it | "may potentially violate" |

**The decisive one is headless.** polyemesis is a single server binary that
runs unattended — the OVH box has no display and nobody logged in. A Chrome
extension is not something that architecture can host. Xvfb plus a real browser
plus a session that expires is a large, fragile dependency for a product whose
whole shape is "one binary, no runtime deps".

### So: do not copy it. Bridge to it.

They publish a WebSocket API:

    wss://io.socialstream.ninja/join/SESSIONID/CHANNELIN/CHANNELOUT

**One adapter** in `internal/chat` speaking that protocol would give
polyemesis the long tail of 120+ platforms for the cost of a single
integration — and the fragile scraping stays on the streamer's desktop, where a
browser already exists, and stays their problem to maintain. Our four official
adapters stay first-class for the platforms we actually broadcast to.

That is a genuinely good split: **official APIs for what we stream to, a bridge
for everything else.**

Caveats to design against, not reasons to refuse:

- **It is a third-party relay.** Chat would transit `io.socialstream.ninja`. For
  a self-hosted product that is a real posture change, and it should be opt-in
  and clearly labelled. Their server component can be self-hosted, and our
  adapter should take the host as configuration rather than hardcoding theirs.
- **The session ID is the credential.** Anyone holding it can read the chat and
  inject messages. That is exactly the class of thing this session spent its
  time fixing (#310, #312): it needs `SecretSet`, `scrub`, never in argv, never
  in a log.
- **It times out about every 60 seconds** and requires a rejoin — their README
  says so outright. Reconnect logic has to be built for that from the start, not
  retrofitted.

## A finding worth acting on: the chat graveyard

Social Stream maintains a list of platforms that have died. Two of them are
platforms **polyemesis still ships destination presets for**:

- **DLive** — listed RIP April 2026. Verified today: `dlive.tv` serves the text
  **"DLive Service Discontinued"**. It is gone.
- **Trovo** — listed RIP April 2026. **Not confirmed.** `trovo.live` still
  serves `robots.txt` and its SPA shells, but the page is JavaScript-rendered so
  nothing is visible from outside, and `open-api.trovo.live/openplatform` 404s.
  Needs a look from a browser before acting.

Our own DLive preset already says:

> DLive's developer portal at dev.dlive.tv no longer resolves, so there is
> nothing published to integrate against.

So the developer portal disappearing was noticed and written down, and the
preset shipped anyway. The catalogue has a `Checked:` date field — the Trovo
entry reads `Checked: "2026-08-06"` — so the mechanism for freshness exists and
nothing acts on it.

**Suggested**: a test that fails when a preset's `Checked` date is older than
some window, and removal of DLive now.

## Where to expand chat, ranked

Ranked by (has an official API — no scraping) × (a polyemesis user would want
it) × (we already stream there).

### Tier 1 — official API, high value, no scraping

1. **Discord** — first-class bot API and websocket gateway. This is the largest
   genuine gap: it is where a streamer's community actually lives between
   streams, and it needs no scraping at all.
2. **Owncast** and **PeerTube** — we already ship destination presets for both,
   both are open source with documented APIs, and both are self-hosted, which
   matches this product's posture exactly.
3. **Telegram** — official Bot API, small integration.

### Tier 2 — official API, moderate effort

4. **Mastodon / Bluesky** — open APIs, growing streamer presence.
5. **Rumble**, **Odysee** — we already ship destination presets.
6. **TikTok LIVE**, **Instagram Live** — we ship destination presets, but chat
   access is restricted and the unofficial routes are exactly what gets accounts
   banned. Bridge territory, not native.

### Tier 3 — leave to the bridge

Zoom, Teams, WhatsApp, Steam, bilibili, chzzk, sooplive, Instagram comments,
Substack, Patreon. All scraping-dependent. One Social Stream adapter covers the
lot without polyemesis owning any of the fragility.

## Recommendation

1. Build the live chat acceptance suite first (the original #1) — it tests what
   already exists, and nothing above is safe to build on an untested base.
2. Remove the DLive preset; check Trovo.
3. Then **Discord** as the next native adapter, and a **Social Stream bridge**
   adapter for the long tail.
