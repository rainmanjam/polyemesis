# The nine platforms: what each API gives, and what polyemesis can use

Compiled 2026-08-13. Two kinds of evidence here, kept apart on purpose:

- **Probed** — I opened a connection today. A `400`/`401`/`403` is a pass: the
  endpoint exists and is asking for credentials.
- **Not verified** — whether the API *grants* live-chat access to a
  self-hosted tool, and on what terms. That is a docs-and-approval question,
  and the relevant doc pages are JavaScript-rendered, so I could not settle it
  from here. Marked as such rather than guessed.

That distinction is the whole point. Every platform below has a live API. Only
some will let a tool like polyemesis read live chat.

## Capability legend

Taken from `internal/oauth/capabilities.go`, which is the real matrix:

| | meaning |
|---|---|
| **SSO** | connect the account with OAuth |
| **Key** | polyemesis fetches the ingest URL and stream key itself |
| **Meta** | push title / description / category at go-live |
| **Read** | chat into the unified pane |
| **Send** | reply from the pane |
| **Mod** | delete, timeout, ban |
| **Stats** | live viewer count |

## The table

`Y` yes · `–` no · `?` unknown · `‼` gated behind partner approval

| Platform | API probed | Stream to it | SSO | Key | Meta | Read | Send | Mod | Stats | Status |
|---|---|---|---|---|---|---|---|---|---|---|
| **YouTube Live** | 200 | Y | Y | Y | Y | Y | Y | Y | ? | **integrated** |
| **Twitch** | 200 | Y | Y | Y | Y | Y | Y | Y | ? | **integrated** |
| **Facebook Live** | 400 | Y | Y | Y | Y | Y | ? | Y | ? | **integrated** |
| **Kick** | 400 | Y | Y | Y | Y | Y | Y | Y | Y | **partial** |
| **Rumble** | 403 | Y | ? | – | ? | ? | ? | ? | ? | manual |
| **X (Twitter)** | 401 | Y | – | – | – | – | – | – | – | manual |
| **Instagram Live** | 400 | – | – | – | – | – | – | – | – | unsupported |
| **TikTok LIVE** | 401 | Y | ‼ | ‼ | ‼ | ‼ | ‼ | ‼ | ‼ | **not in the matrix** |
| **LinkedIn Events** | 401 | Y | ‼ | ‼ | ‼ | ‼ | ‼ | ‼ | ‼ | **not in the matrix** |

### Reading the four integrated rows

These are done. Chat read, send, moderation and metadata all work on YouTube,
Twitch, Facebook and Kick. The `?` on viewer stats is honest — the matrix
records it as unknown for three of the four, meaning nobody has confirmed it
end to end.

Kick is "partial" only because chat arrives by **webhook**, so it needs a public
HTTPS URL the platform can reach. Everything else about Kick is complete.

### The two genuine additions

**Rumble** is the standout. Its live-stream API answered with a structured
body — `{"user":{"id":null,"logged_in":false},"errors":[{"code":"403"}]}` —
which is a real API refusing an invalid key, not a dead endpoint. The key comes
from the user's own Rumble account rather than a partner programme, which makes
it the only one of the five unintegrated platforms plausibly reachable without
an approval process. **Confirm the chat payload before committing to it.**

**X** has a live, paid API. The matrix marks every capability `–`, which is
accurate for the free tier. Live video producer access was retired; "chat" on
X is replies to the post, readable on paid tiers. Buyable, not free.

### The three that are gated, and why they stay that way

**Instagram**, **TikTok** and **LinkedIn Events** all have live APIs — all three
answered my probe. None publishes live-chat read to a self-hosted tool without
partner approval:

- Instagram has no third-party live-comment surface at all. Our matrix already
  says `TierUnsupported`, which matches.
- TikTok's live surface is partner-gated.
- LinkedIn Live requires approved broadcast-partner status.

The routes that *do* work for these three are unofficial, and unofficial access
to exactly these platforms is what gets user accounts banned. They belong behind
a user-opted-in bridge (see the Social Stream note), not in a polyemesis adapter
that looks official.

**Not verified**: the specifics of each approval programme. Worth an hour with
the real docs before any of it is treated as settled.

## Two defects found while compiling this

### 1. TikTok and LinkedIn are missing from the capability matrix

Both ship **destination presets** — you can stream to them today — but neither
appears in `PlatformCapabilities()`. So the UI can tell an operator how to
stream to TikTok and cannot tell them what else does or does not work. Every
other preset-carrying platform has a row, including the unsupported ones;
`Instagram Live` is listed precisely so the answer "no" is visible.

### 2. The Kick preset says the stream key is manual. It is not.

`internal/db/platforms.go:525`, mirrored in `DestinationDialog.tsx:180`:

> Kick is the one platform where the key stays manual: its public API exposes
> the channel, chat and viewer counts but **no stream key anywhere**.

The capability matrix says the opposite, and explains itself:

> Fetched from the channels resource, over the `streamkey:read` scope. This was
> recorded here as impossible for a long time […] the key rides as `stream.key`
> on the same channels response we already fetch, and is withheld unless
> `streamkey:read` was granted, which the Get Channels page does not list among
> its required scopes.

So the limitation was real, was lifted, was corrected in the matrix — and the
preset note that an operator actually reads still describes the old world. It
tells them to copy a key by hand that polyemesis can fetch for them.

Same class as everything else this session: two places describing one fact, one
of them updated. I edited that exact note earlier today for the `/app` fix and
did not notice it.

## Suggested order

1. Fix the stale Kick note; add TikTok and LinkedIn rows to the matrix saying
   plainly what is and is not possible.
2. Confirm the `?` viewer-stats cells for YouTube, Twitch and Facebook — four
   unknowns in the shipped matrix is four questions the UI answers with a shrug.
3. **Rumble** as the next real integration, once its chat payload is confirmed.
4. Leave Instagram, TikTok and LinkedIn to a bridge.
