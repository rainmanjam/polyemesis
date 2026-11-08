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
| **Protection** | the recommended-protection pack: hate terms, doxxing, scams, impersonation, floods, raids | free, in-process |
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

### Hold a match off the overlay

Each rule has a **Hold off the overlay** switch, off by default. A message that
matches a rule with it on is **flagged in the console and kept off every
on-screen chat overlay**, whatever the rule's action is, and nothing else
changes: the console still shows the message, nothing is sent to any platform,
and no moderation event is published. It needs no matrix cell, so it works with
every cell off. Only the two kill switches apply: the global switch, and the
platform's own switch if you have turned it off.

It is a way to be careful about a broadcast without punishing a viewer. A rule
that is right nine times in ten can hold its matches, and the tenth is a click
away: the **Flagged** list on the Chat page shows a **Show on overlay** button
on each held message. The server keeps the last 200 held lines for ten minutes
for this, so a line older than that, or one you have since deleted, hidden or
banned its author over, can't be shown again. A line removed by the rule's own
action (a delete, a timeout, a ban) isn't held, it is removed, and is not
offered back.

Showing a held line is refused while automod's model is still checking that
message (the button answers that it is checking, and works a few seconds later),
and for a message the chat no longer holds: one that has scrolled out of the
last 500 messages, or whose author has been banned or timed out. If the model
removes a line you released before it answered, the line comes straight back
off the overlay. After a release, a retraction, a model removal or the ten
minutes, the Flagged list stops offering the button.

**A held message is never a poll vote**, even after you show it on the overlay.
A viewer's "2" held by a rule is not counted, and releasing it does not count it
afterwards. See [POLLS.md](POLLS.md).

The same applies to the message on a viewer-activity alert (a Super Chat's
words, a tip's note): a held message is left off the alert, which still shows
without it. See [OVERLAY.md](OVERLAY.md#alerts-overlay).

A rule can also carry a **category** (`category` in the API), one of the closed
set of reasons the model uses, such as `hate_speech` or `spam`. It is what is
recorded against a match instead of the generic "matched a chat filter", and it
is the only text that can reach a platform. Leave it empty and nothing changes.
There is no control for it in the rule editor yet.

Existing rules are unaffected: a rule stored without these two fields behaves
exactly as before.

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
| `windowSeconds` | **30** | how far back every detector below looks, in seconds |
| `maxMessages` | **8** | messages in the window before it counts as flooding |
| `maxRepeats` | **3** | repeats of the same *normalised* text in the window |
| `maxLinks` | **3** | links in the window |
| `maxMentionsPerMessage` | **5** | mentions in one message before it is mention spam |
| `minLengthForCaps` | **12** | below this length a shouty message is just a short one — "OK" and "WHAT" are not shouting |
| `maxCapsRatio` | **0.8** | proportion of capitals before it counts as shouting, 0..1 |
| `action` | **timeout** | flooding is usually somebody carried away, and a timeout expires on its own where a ban needs a human |
| `timeoutSeconds` | **60** | duration for that action |
| `retainPerAuthor` | **24** | messages kept per author |
| `idleEvictionSeconds` | **600** | how long an author is kept after their last message, in seconds |

The last two are memory bounds rather than policy. A raid is thousands of new
authors in a minute, so the ring has to forget — otherwise the defence becomes
the denial of service. Idle eviction alone is not a bound while a raid is still
going, because nobody is idle, so there is also a **fixed ceiling of 20,000
tracked authors**. It is not a setting.

The fields sit under `automod.history` in `PUT /settings`. A field the table
does not name — a typo, or `window` for `windowSeconds` — is refused with a 400,
the same as in every other section, rather than accepted and ignored.

Because `maxRepeats` compares the **normalised** text, a spammer varying case,
padding or doubled letters still trips it.

Two limits to know. The history checker is **per author and per platform**: one
person with an account on Twitch and another on YouTube is two authors, because
there is no reliable link between the accounts, so a spammer who alternates
platforms is counted on each separately. And `maxRepeats` counts exact repeats
only. Near-copies ("buy now 1", "buy now 2") and the same text from many
different accounts are the job of the protection pack, below.

## The recommended-protection pack

A compiled term list and a set of detectors, in `internal/automod/pack`, that
run as their own checker, so that arming an action for the pack never arms your
own regex rules. A hit is **flagged in the console and can be held off every
overlay**; it asks for no platform action by itself, and the matrix is still the
only thing that can permit one.

### Who has it on

A **fresh install** has the pack on, with every platform action off. An
**existing install is never switched on silently**: its settings carry no
answer yet (`automod.protection.state` is empty), nothing new runs, and
`GET /settings` reports `automod.protection.offer: true` so the console can ask.
Answering writes `on` or `off`, a recorded answer rather than a dismissed
notice. A matrix cell for the `protection` checker is refused on save
("recommended protection is not on") unless the pack is on in the same save.

### In the console

Settings, Pipeline, **Recommended protection** is the first automod card. An
install that has not answered yet sees a banner there (and above the flagged list
on the Chat page) with **Turn on** and **Not now**; both record an answer, and
**Not now** is final. Once the pack is on the card shows:

- **One row per category** (hate and slurs, doxxing, scams, spam shapes,
  impersonation, raids, and profanity, which is off). Each has a switch, a chip
  saying whether its hits are held off the overlays or only shown in the console,
  how often it is wrong, and the highest ladder rung it may reach. **Details**
  opens a switch and a "hold off overlays" checkbox for each detector, the box to
  add a word or allow a pack word (hate and profanity), the domain allow-list
  (scams and doxxing) and the extra names to protect (impersonation). The
  street-address detector is off until you switch it on.
- **The action ladder**, a row per platform with **Delete, Time out, Ban** as
  three pills. Nothing is armed until you click a pill, and delete and ban ask
  first because polyemesis cannot take either back. A pill the platform cannot
  perform is inert and gives the reason. Turning the pack off disarms its pills in
  the same save. The Twitch row repeats a note worth knowing: Twitch's own AutoMod
  and blocked terms only see Twitch chat, so a YouTube or Kick message shown on
  your Twitch stream is kept off it by an overlay hold, not by Twitch.
- **A test box** that calls `POST /automod/explain`. It shows which detectors
  fire, whether the line would be held, and what each would ask a platform to do.
  It uses the settings you have **saved**, and it never stores, logs or sends the
  text.
- **Advanced**: the ladder's timeout length, the strike window and each
  category's highest rung. Self-harm has no control: it is hold only.

The protection column is not in the switch matrix below the cards, because the
pack is armed one rung at a time on its own card.

The settings are also reachable through the API. What you change is stored as a **difference over the
pack**, never as a copy of it, so a release that updates the pack reaches every
install that is on and nothing you chose is overwritten:

| Key under `automod.protection` | Meaning |
|---|---|
| `state` | `""` (undecided), `"on"` or `"off"` |
| `pack`, `packVersion` | the pack id (`en`) and the version you last accepted |
| `categories` | `{category: {enabled, detectors: {name: bool}}}`. Absent means the pack's default. Categories: `hate_speech`, `profanity` (off by default), `doxxing`, `scam`, `flood`, `impersonation`, `raid`. A detector is named by what follows the slash (`address` for `doxxing/address`) |
| `ceilings` | `{category: "hold" \| "delete" \| "timeout" \| "ban"}`, how high a category may climb the ladder |
| `holdDetectors` | `{detector: bool}`, whether a detector also holds the message off the overlay |
| `addTerms`, `allowTerms`, `allowDomains` | your terms (2 to 100 characters, hate tier `severe` or `contextual`, profanity tier `mild` or `strong`), pack terms to ignore, and domains that are never a shortener or lookalike |
| `impersonation` | `protect`: extra names to guard, `allowIds`: author ids that are never an impostor |
| `strikeWindowSeconds`, `ladderTimeoutSeconds` | how long a strike counts, and the timeout rung's length (both 600 by default; never zero, because a zero timeout is a permanent ban) |

A sent `categories`, `ceilings`, `holdDetectors`, `addTerms`, `allowTerms`,
`allowDomains` or `impersonation` list **replaces** the stored one, so removing
an entry saves and takes effect. A misspelt key is a 400, and so is a category,
detector or ceiling the pack does not have.

### The action ladder

Each platform has up to three rungs for the pack: **delete**, **timeout** and
**ban**. You arm one by switching on its matrix cell, `platform/rung/protection`
(for example `twitch/ban/protection`). None is armed by default, so on a fresh
install a hit only flags the message and holds it off the overlay.

Every pack hit is a strike against its author on that platform, counted once
per message however many detectors fired, inside `strikeWindowSeconds`. Strike 1
asks for a delete, strike 2 for a timeout and strike 3 and later for a ban,
**capped by the category's ceiling** and then lowered to the highest rung you
have actually armed at or below it. If none is armed the hit stays a flag. So
with only Ban armed, the first two hits flag and the third bans.

| Category | Highest rung by default |
|---|---|
| Hate terms, severe | ban |
| Scams | ban |
| Doxxing | timeout |
| Floods (only a detector that holds) | timeout |
| Raid | delete |
| Profanity | delete |
| Impersonation, contextual hate terms, self-harm | hold only |

Only a detector that **holds** is on the ladder. Emote walls, shouting and
near-duplicate messages ship flag-only (see the table below), so they stay a
flag however many rungs you arm; turning `holdDetectors` on for one of them
opts it in. Where another checker (the flood history checker's timeout, a rule)
already acts on the same message, the ladder adds a second action only if its
rung is more severe, and then the ban is performed first.

Two properties are deliberate:

- **Arming starts the count again.** Strikes belong to an arming epoch for the
  platform, and any change to the set of rungs armed there starts a new one. An
  install that ran unarmed for a week cannot ban someone for their third message
  the moment Ban is armed: arming affects only what happens afterwards.
- **A settings save in the middle of a raid forgets nothing.** Strike counts,
  the raid window and the names learned from moderators belong to the server,
  not to the rebuilt engine.

Strikes are kept in memory only, are bounded (idle authors are dropped, and at
most 20,000 are tracked), and are not carried across a restart. The model
checker has its own category table and is not part of the ladder.

| Detector | What it catches | Held off the overlay |
|---|---|---|
| Hate terms, severe | slurs whose meaning does not depend on context. Whole-token match on a normalised form (confusables, leetspeak, zero-width characters, letter-spacing, stretched letters) | yes |
| Hate terms, contextual | reclaimable or ambiguous terms; flagged only | no |
| Doxxing | phone numbers, email addresses (also `name [at] host [dot] com`), IPv4 and IPv6 addresses. A street address detector exists and ships off. Private, loopback and documentation address ranges are never reported, and neither are version strings, scores, dates and times | yes |
| Scams | giveaway and crypto lures, check-my-bio pitches, fake platform support, URL shorteners, lookalike domains | yes |
| Impersonation | an author name that folds onto the broadcaster's or a moderator's (look-alike letters, `_official`, `tv`, a digit suffix), or is one edit away while the message carries a link or handle | yes |
| Floods | zalgo text, one character repeated, mass mentions, too many links | yes |
| Floods, flag only | emote walls, shouting, near-duplicate repeats | no |
| Raid | five or more different accounts, on any platform, posting the same or near-same text (at least 12 characters) within 20 seconds | yes |

A finding names its detector and never the matched value, because a finding's
reason is written to the server log and the value of a doxxing hit is the thing
being kept out of it.

Near-duplicate repeats count a message as a repeat of an earlier one by the same
author when most of their words match (a Jaccard overlap of 0.8) or when they
are the same once digits are removed. More than three of them in 30 seconds
counts, the same numbers as the exact-repeat detector's defaults; this detector
does not yet follow your `maxRepeats` and `windowSeconds`.
Raids are counted by text, not by author, and a chant such as `F` or `GG`, an
emote-only line, a command like `!play` and anything a moderator or the
broadcaster says never counts. The tracker is bounded (a fixed number of
distinct texts and of accounts per text), so a raid cannot grow it without
limit, and it survives a settings save.

The test box is `POST /automod/explain` (see below). It runs the pack and your
rules on text you type and leaves no trace. It uses the pack as you have
configured it (a category off, a term added, a domain allowed), and it never
counts a strike.

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

**Off by default, and while it is off no message leaves this server.**
polyemesis ships with no API key and no account anywhere: you bring your own
key for whichever provider you choose.

There are **two model checkers** behind the one *Model* column of the matrix:

- **Primary.** Asked first. It defaults to a typed-decision moderation model
  on the **Jev** protocol: TypeSafe's hosted Jev (pinned to `jev-1.14.0`), or
  a local Jev-compatible model such as `laya` on Ollama, which serves the same
  API.
- **Fallback (optional).** Asked about a message **only when the primary could
  not answer it**: it timed out, returned an error or a non-200, replied in a
  shape that is not a verdict, named a category this build does not offer, ran
  out of its hourly budget, or was paused by a 429/529. The same message goes
  to the fallback. It defaults to an OpenAI-compatible provider.

A message passes unchecked (fails open) only when **both** checkers fail. A
clean answer from the primary is an answer: the fallback is not asked for a
second opinion.

The model is asked only about messages the free checkers did not already
**remove**. A message a rule deleted is not sent anywhere; a message a rule
only flagged still is.

### Setting up a checker

Each checker has its own protocol, provider, endpoint, model, key, budget,
timeout, Test button and failure counter.

| Setting | What it is for |
|---|---|
| Protocol | **Jev** (typed decisions) or **OpenAI-compatible** |
| Provider | a preset that fills in the address and model, and carries the JSON mode, the reasoning setting and the privacy class |
| Endpoint, model | where to ask, and as what |
| Confidence floor | for Jev, the probability that the message breaks your rule; for OpenAI-compatible, the model's self-reported confidence |
| Calls per hour | this checker's spend ceiling, in real money. Zero means none |
| Answer timeout | how long to wait for this model, **not** how long a viewer is silenced. 4 s by default |
| API key | sealed at rest, never returned, never logged, never in an event |

Both checkers share the policy on the card: the **Instruction** (your own
words for what counts as abuse, sent with every message) and the **actions**.

The presets:

| Protocol | Provider | JSON mode | Reasoning | Where messages go |
|---|---|---|---|---|
| Jev | TypeSafe Jev | typed by construction | — | hosted, US |
| Jev | Ollama (laya) | typed; compact two-question form for laya's 512-token window | — | this machine |
| Jev | Custom address | typed | — | wherever the address points |
| OpenAI-compatible | OpenAI | strict `json_schema` | — | hosted, US |
| OpenAI-compatible | Cerebras | strict `json_schema` | `reasoning_effort: "none"` | hosted, US |
| OpenAI-compatible | Groq | `json_object` | — | hosted, US |
| OpenAI-compatible | OpenRouter | `json_object` | — | a relay that forwards to a model host it chooses |
| OpenAI-compatible | Ollama | `json_object` | — | this machine |
| OpenAI-compatible | Custom address | `json_object` | — | wherever the address points |

The preset decides the JSON mode, the reasoning setting and the flag-only lock
on the server, by its ID. A client cannot send its own.

### Test before enabling

A checker that cannot answer lets every message through, and from the outside
it looks exactly like a model that approves of everything. So a checker
**cannot be switched on until its Test button has passed** for the connection
on screen (protocol, provider, endpoint, model and key). The server enforces
this: a `PUT /settings` that turns a checker on, or points an enabled one
somewhere new, without a matching pass is refused with a 400.

Test sends three made-up messages, one ordinary, one abusive and one spam.
**Nothing from your chat is sent.** It passes when every message gets a usable
answer, the ordinary one is not flagged, and the abusive one is. The spam line
is reported but does not decide the result, because the default instruction
does not mention spam. A new key, or a cleared one, needs a new Test.

### Keys go only where they were given

Each sealed key is **bound to the endpoint it was entered for**:

- The server sends a stored key only to that endpoint, compared after
  normalising scheme, host, port and path. A connector holding a key for one
  address and configured for another refuses the call. The message fails open,
  and the key goes nowhere.
- To test a **different** address, type its key into the form before pressing
  Test. A key that passes for an address you have not saved yet is held until
  you save exactly that address. Then it becomes the checker's key.
- Saving a new address **clears the stored key** unless a key was tested for
  that address, in the same database transaction as the save. Re-enter the key.
- Neither connector follows a redirect, so an endpoint that answers `307`
  cannot pass your key on.
- The Test button needs a signed-in session. No API token can run it.

Keys stored before this binding existed are bound, once, to the endpoint that
was saved when you upgraded.

### Jev's confidence floor and the flag band

Jev answers typed questions with probabilities, so the floor gates something
calibrated: **the probability that the message breaks your rule**. It does not
gate the category answer's `confidence` field, which is a normalised margin
over seven options and reads much lower.

- At or above the floor (0.8 by default), the finding asks for its category's
  action.
- **Between 0.5 and the floor, the message is only flagged** for a person to
  look at, whatever the category's action is.
- Under 0.5, nothing.

### Action by category

Each category the model can answer with gets its own action: harassment, hate
speech, threats, sexual content, spam, and other rule breaking. The choices are
flag, hide (local), hide, delete, time out or ban. **Every category starts on
the default action, which is flag.** The card shows suggested values and can
apply them for you: spam → delete; harassment, hate speech and threats →
time out.

The matrix still decides what may happen on each platform. A category set to
*time out* does nothing on a platform whose *Time out / Model* cell is off, or
whose platform cannot time anyone out.

An install upgraded from before this table keeps its single action for every
category until you change it, so an upgrade never turns your timeouts into
flags.

### Which checker decided

Every model finding records `decidedBy` (`primary` or `secondary`) and the
provider preset. They appear in the moderation log, in the review queue, and
in `chat.moderation` events as `decidedBy` (see [EVENTS.md](EVENTS.md)). The
card shows each checker's spend, answers and failures, how often the fallback
answered for the primary, how many messages neither could answer, and which
checker made the last decision. `GET /automod/stats` carries the same numbers
as `primary` and `secondary` objects beside the old top-level fields, which
are still the primary's.

### Privacy

- **Local** presets: nothing leaves this machine or your network.
- **Hosted** presets: each message the free checkers do not remove is sent to
  the provider, in the US. For a streamer in the EU this is an international
  transfer. TypeSafe does not train on input; zero data retention is
  enterprise-only. Read your provider's terms before switching one on.
- **OpenRouter** passes messages on to a model host it chooses.
- The card shows where messages go, next to the provider.

### A local model

Local presets (Ollama, and laya over Ollama's Jev API) are **flag-only**. A
local model puts a message in front of a person and never deletes, times out
or bans, whatever the category table says. No local model in our testing was
accurate enough to act on its own.

**A computer without a GPU will probably miss the 4 s answer timeout**, and
every message it misses passes unchecked. The card warns about this. On four
CPU cores, no model we tried answered in under 4.6 s. You can raise the
timeout, because the model never holds up chat: a late verdict still
retracts the message.

Ollama in Docker with a CPU limit needs `num_thread`, because llama.cpp starts
one thread per *host* core and thrashes inside a `--cpus`-limited container.
In one measurement a 1B model went from 36–75 s per call to 1.7 s once it was
set. Set it to the number of CPUs the container has, either in a Modelfile:

```
FROM llama3.2:3b
PARAMETER num_thread 4
```

or per model with `ollama create`. `--cpuset-cpus` alone does not fix it.
Thinking models (qwen3, deepseek-r1) also need their reasoning switched off,
or they think past the timeout.

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
| `GET` | `/automod/matrix` | every cell with an `available` flag and a `reason`, plus the provider `presets` and `modelCategories` |
| `GET` | `/automod/stats` | what the checkers have done |
| `PUT` | `/settings/automod-key` | a model key, sealed and never returned. `{"key": "…"}` is the primary; add `"checker": "secondary"` for the fallback |
| `POST` | `/automod/explain` | the pack's test box: `{text, authorName?, platform?, protect?, emoteCount?}` returns which detectors fire, whether each holds the message, and whether the current matrix would let each act. Session only. `protection` says whether the pack is on in the live engine, `held` is what chat would do and `wouldHoldIfOn` what it would do with the pack on; a detection with `live: false` is the pack's. Offline, stateless and unlogged: it never records the message in any window and never calls a provider |
| `POST` | `/automod/test` | the Test button: `{checker, protocol, preset, endpoint, model, timeoutSeconds, minConfidence, instruction, key?}`. Session only |

Keys are write-only: `automod.model.hasApiKey` and
`automod.secondary.hasApiKey` are all `GET /settings` carries, and
`automod.*.tested` says whether the current connection has passed Test. Both
are derived, and a value you send is ignored. Sending an empty key clears it.
See [the API reference](API.md) for the envelope.

The primary is `automod.model` (its name predates there being two) and the
fallback is `automod.secondary`. Both have `protocol` (`jev` or `openai`),
`preset`, `endpoint`, `model`, `timeoutSeconds`, `maxCallsPerHour` and
`minConfidence`. The shared policy is on `automod.model`: `instruction`,
`action` (the default), `timeoutForBan`, and `categoryActions`, a map from a
category to an action. A model block saved before protocols existed, with no
`protocol` field, is read as `openai`.

A `PUT /settings` that switches **on** a cell whose checker is not configured
is refused with a 400 naming the cell: no enabled rule for a `rules` cell, the
model switched off or with no endpoint for a `model` cell. Configure the
checker in the same save and the cell arms. A cell that was already on when its
checker was later switched off is kept, so switching the checker back on
restores it, but it cannot fire in the meantime, and `/automod/matrix` does not
count it in `summary`.

## See also

- [Platforms](PLATFORMS.md) — what each platform's chat supports
- [Monitoring](MONITORING.md) — automod counters as metrics
