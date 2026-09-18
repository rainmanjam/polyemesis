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

## Writing a rule

Patterns are **Go regular expressions (RE2)**, compiled with `(?i)` already
prepended, and matched as a **substring** — `spam` fires on "a spammy sentence".
You never need to write `(?i)` yourself, and there is no way to make a rule
case-sensitive.

### Each pattern is tried three ways

A rule fires if it matches **any** of three forms of the message, which is why
these examples catch evasions the pattern does not mention:

| form | what it defeats |
|---|---|
| the message as sent | nothing — for patterns targeting punctuation the other forms remove |
| **normalised** | case, padding, doubled letters, homoglyphs, zero-width characters |
| **despaced** | letter-spacing, and only when the spacing looks deliberate |

Normalising collapses runs of the same character, so `ssssbadword` reduces
toward `badword`. Despacing is not applied to ordinary text — "a bad wordsmith"
would otherwise match `badword`, which is the Scunthorpe problem arriving by a
different road.

### Examples that work

Each was run against the real checker; the right-hand column is what it actually
caught.

| Pattern | Catches |
|---|---|
| `free\s*robux` | "FREE ROBUX now", and "f r e e   r o b u x" |
| `badword` | "b a d w o r d", and "ssssbadword" |
| `bit\.ly/\S+` | "check bit.ly/abc" |
| `(?:discord\|t)\.me/\S+` | "join discord.me/xyz" |
| `\b(?:buy\|cheap)\s+followers\b` | "buy cheap followers here" |
| `^\s*!(?:so\|shoutout)\b` | "!so @someone" — anchored, so only at the start |

### What RE2 refuses

RE2 has no backtracking, so three habits from PCRE, Perl and JavaScript do not
compile. The save is refused with the compiler's own message:

| Pattern | Error |
|---|---|
| `(?!free)robux` | `invalid or unsupported Perl syntax: (?!` |
| `(?<=@)\w+` | `invalid named capture` |
| `(\w+)\s+\1` | `invalid escape sequence: \1` |
| `(.)\1{9,}` | `invalid escape sequence: \1` |

The last one is worth calling out because it is the natural way to write "the
same character ten times over" — reach for the **history** checker's repeat
detection instead, which is what that job belongs to.

A pattern that does not compile refuses the whole save rather than being
skipped, and the console renders the error against the offending rule.

## The history checker's settings

The rules checker needs patterns from you; this one ships working defaults and
is deliberately forgiving. Every field below is what the console writes and what
the API accepts.

| Field | Default | What it means |
|---|---|---|
| `window` | **30s** | how far back every detector below looks |
| `maxMessages` | **8** | messages in the window before it counts as flooding |
| `maxRepeats` | **3** | repeats of the same *normalised* text in the window |
| `maxLinks` | **3** | links in the window |
| `maxMentionsPerMessage` | **5** | mentions in one message before it is mention spam |
| `minLengthForCaps` | **12** | below this length a shouty message is just a short one — "OK" and "WHAT" are not shouting |
| `maxCapsRatio` | **0.8** | proportion of capitals before it counts as shouting, 0..1 |
| `action` | **timeout** | flooding is usually somebody carried away, and a timeout expires on its own where a ban needs a human |
| `timeoutSeconds` | **60** | duration for that action |
| `retain` | **24** | messages kept per author |
| `idleEviction` | **10m** | how long an author is kept after their last message |
| `maxAuthors` | **20000** | ceiling on tracked authors |

The last three are memory bounds rather than policy. A raid is thousands of new
authors in a minute, so the ring has to forget — otherwise the defence becomes
the denial of service.

Because `maxRepeats` compares the **normalised** text, a spammer varying case,
padding or doubled letters still trips it.

### A timeout of zero is a permanent ban

On every platform. The server refuses to save a timeout action carrying no
duration rather than accepting it and surprising you later:

```
rule "…" asks for a timeout but carries no duration; a timeout of zero seconds
is a permanent ban on every platform, so set timeoutSeconds, or use the ban
action if that is what you meant
```

Worth knowing when writing rules through the API, where it is easier to omit a
field than it is in the console.

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
