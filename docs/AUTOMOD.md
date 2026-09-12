# Automatic moderation

**Two tiers. The free one runs here and sees everything; the paid one is off
until you switch it on.**

Set it up under **Settings → Pipeline → Automatic moderation**, directly below
chat retention — the two are the same subject, because retention is the depth
the sequence detectors can see.

## Nothing acts until you say so

The matrix starts empty. Configuring a checker does not arm it: a rule you have
written and a permission you have granted are separate decisions, in that
order, and the card is laid out to match. A checker nobody has configured
cannot be armed at all — the alternative is an operator believing bans are
being issued by a model that was never given an endpoint.

Above everything is one global switch. Off stops every automatic action
everywhere, whatever the cells say. Messages are still flagged for review, so
turning it off costs you enforcement and not visibility.

## The checkers

| Checker | What it sees | Cost |
|---|---|---|
| **Rules** | your regexes, each with its own action | free, in-process |
| **History** | rate, repeats, links, mentions, capitals over a window | free, in-process |
| **Model** | one message, when the other two could not settle it | an API call |

Rules and history run on the messages already arriving for the chat pane.
**Nothing leaves the server to make those decisions**, and they are the ones
that catch what a live chat actually throws at you.

A pattern that does not compile refuses the whole save rather than being
skipped, because a rule silently not running is worse than a rejected form.

## The model tier

Any OpenAI-compatible `/chat/completions` endpoint, including one on the same
machine. **Off by default, and while it is off no message leaves this server.**

| Setting | What it is for |
|---|---|
| Endpoint, model | where to ask, and as what |
| Instruction | your own words for what counts as abuse — sent with every message |
| Confidence floor | a verdict less certain than this is ignored |
| Calls per hour | the spend ceiling, in real money. Zero means none |
| Answer timeout | how long to wait for the model, **not** how long a viewer is silenced |
| Action on a positive verdict | what a confident yes may do |

It is asked only about what the free checkers could not settle, which is what
keeps the bill finite. The per-hour ceiling is the hard stop.

## What a platform can actually do

Availability is **derived from the platform, never stored**. Facebook publishes
no chat ban API, so the ban cells on its row are inert and say why rather than
being absent — a switch that silently does nothing is worse than one that is
missing, and an absent control reads as an oversight.

That is the same table the chat pane uses to decide whether a menu item is
live, so the two cannot disagree.

## Irreversible actions

Deleting a message and banning a viewer **cannot be undone by polyemesis**. A
ban needs a human on the platform to lift it, and a deleted message is gone
from the platform's own record. The card says so beside the cells that arm
them.

Flagging is the reversible one, and it is the default for the model tier for
that reason.

## Through the API

The configuration lives in `/settings` like everything else — the matrix, the
rules and the model options all round-trip through that blob. Two routes exist
on their own:

| Method | Path | Why |
|---|---|---|
| `GET` | `/automod/matrix` | every cell with an `available` flag and a `reason` |
| `GET` | `/automod/stats` | what the checkers have done |
| `PUT` | `/settings/automod-key` | the model key, sealed and never returned |

The key is write-only: `automod.model.hasApiKey` is all `GET /settings`
carries. Sending an empty key clears it. See [the API reference](API.md) for
the envelope.

## See also

- [Platforms](PLATFORMS.md) — what each platform's chat supports
- [Monitoring](MONITORING.md) — automod counters as metrics
