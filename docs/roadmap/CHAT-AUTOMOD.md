# Automatic chat moderation

**Status: SHIPPED**, 2026-07-30. This document is the design that was agreed
before implementation, kept as written — the two corrections it took during
review are recorded in place rather than tidied away, because the wrong version
of a call is often more instructive than the right one.

Moderation today is entirely manual: you watch chat, you act. The four adapters
already expose `Delete`, `Hide`, `HideLocally`, `Ban` and `Unban`, and upstream
retraction works — so the *acting* half is finished. What is missing is anything
that decides.

- [The three-layer design](#the-three-layer-design)
- [Why history is the hard part](#why-history-is-the-hard-part)
- [The switch matrix](#the-switch-matrix)
- [The model connector](#the-model-connector)
- [What it must never do](#what-it-must-never-do)
- [Testing](#testing)

---

## The three-layer design

Not "an LLM moderates chat". Three layers, cheapest first, each able to stop the
message before the next one costs anything.

| Layer | Decides on | Cost | Latency |
|---|---|---|---|
| **1. Rules** | One message, in isolation | Free | Microseconds |
| **2. History** | A *sequence* from one author | Free | Microseconds |
| **3. Model** | One message plus context | Per call | 300 ms – 2 s |

Layer 1 is regex and simple predicates. Go's `regexp` is RE2 — linear time, no
backtracking — so a crafted message cannot turn a filter into a denial of
service. That is a genuine reason to spend rules freely here where in most
languages it would not be.

Layer 3 only ever sees what layers 1 and 2 could not settle. Chat is
high-volume; sending every message to an API is unaffordable in both money and
time, and unnecessary because most abuse is not subtle.

## Why history is the hard part

**Rate and repetition are properties of a sequence, not a message.** No
per-message classifier — regex or model — can see them. Ten identical messages
are individually innocuous and collectively the most common form of chat abuse
there is.

This is the layer that catches what operators actually complain about:

| Pattern | Detected by |
|---|---|
| Fast-posting flood | N messages within a sliding window |
| Same phrase repeated | Normalised text repeated ≥ N times |
| Near-duplicate copypasta | Repetition after whitespace/case/homoglyph folding |
| Wall of text or emote | Length and emote-density thresholds |
| Shouting | Sustained upper-case ratio |
| Link spam | Link count per window, weighted by account age where a platform gives it |
| @-mention spam | Mentions per message and per window |

The data already exists. `internal/db/chat.go` carries
`WHERE platform = ? AND author_id = ?`, indexed on `(author_id, at_ms)` — built
for the moderator user card, and exactly the query a history detector needs.
Hot-path checks run against a small in-memory ring per author; anything needing
depth falls back to that query.

Two properties this layer must have:

- **Per-author, per-platform.** The same display name on two platforms is not
  the same person, and `author_id` is the stable identifier on all four.
- **Bounded memory.** A raid is thousands of new authors in a minute. The ring
  is fixed-size with an idle eviction, so the defence cannot become the
  denial of service.

## The switch matrix

Three dimensions, not one: **action × platform × checker**.

```
                    ┌─ rules ─┬─ history ─┬─ model ─┐
  YouTube   delete  │    ☐    │     ☐     │    ☐    │
            timeout │    ☐    │     ☐     │    ☐    │
            ban     │    ☐    │     ☐     │    ☐    │
  Twitch    delete  │    ☐    │     ☐     │    ☐    │
            ...
```

Each dimension earns its place for a different reason:

**Checker**, because the same action deserves different trust depending on the
evidence. A regex hit is deterministic, reproducible and explainable after the
fact; a model verdict is none of those. Auto-deleting on the first is
defensible, on the second it is a judgement the operator should make knowingly.

**Platform**, because they are not equivalent and an operator's exposure differs
per channel. Facebook's hide is reversible upstream; everywhere else it is
local-only. Kick counts timeouts in minutes where the others count seconds. Most
of all, somebody may happily automate their small second-language stream and
want nothing automatic on the channel their income depends on.

**Action**, because consequence varies from "flagged for review" to
"permanently removed with no undo".

### Cells are gated by what the platform can actually do

A switch offering an action a platform does not support is a promise the product
cannot keep, and it fails silently — the operator ticks it and nothing ever
happens.

`ui/src/lib/capabilities.ts` already holds the per-platform capability matrix,
including a `moderation` key, and `internal/oauth`'s drift guards already pin
that TypeScript against the Go source of truth. The automod matrix must derive
its cells from the same data, so each is one of three states rather than two:

| State | Meaning |
|---|---|
| **unavailable** | This platform has no such action. Rendered inert with the reason, never as an unticked box |
| **off** | Available and not automatic. The checker still flags for review |
| **on** | Available and automatic |

A guard test belongs here in the same shape as the existing ones: **the automod
matrix may not offer a cell the capability matrix says is impossible.** Without
it the two drift, and the failure is an operator believing a channel is
protected when nothing is wired to it.

### Defaults

Everything **off** except *flag for review*, which is on everywhere it is
possible. Automod that does something on first install is automod that surprises
somebody during a broadcast.

Auto-ban is **offered on every platform that supports it, and defaults off for
every checker including the model.** An earlier draft of this document said a
model may never auto-ban at all; that was the wrong call. Refusing to offer a
capability is not a safety feature, it is a decision taken away from the person
who knows their channel — and an operator running a raid-prone stream at 3 a.m.
may want exactly that. What the product owes them is that it is off until they
turn it on, that the consequence is stated plainly at the moment they do, and
that turning it on is per platform rather than everywhere at once.

### Making sixty switches usable

Five actions × four platforms × three checkers is sixty cells. That is a table
nobody reads, so:

- **Row and column bulk toggles** — "nothing automatic on Twitch", "no model
  actions anywhere" — because the common operations are whole rows and columns.
- **A per-platform master switch**, so automod can be killed on one channel
  instantly. Mid-incident, unticking fifteen boxes is not a thing anyone should
  have to do.
- **A global kill switch**, for the same reason at larger scale.
- **The matrix collapses by default** to a summary line per platform — "Twitch:
  3 automatic actions" — and expands on demand.

## The model connector

An HTTP call. No SDK, no new module — consistent with driving FFmpeg through
`os/exec`: a stable, inspectable interface beats a binding.

- **Credentials** go in the existing NaCl secretbox alongside OAuth tokens.
- **Asynchronous, never blocking.** The message displays immediately; a verdict
  may retract it afterwards. Blocking chat on a 300 ms–2 s round trip makes the
  product feel broken, and the retraction path already exists.
- **Fail open.** A timeout, a 500, a rate-limit or an expired key means the
  message passes and is flagged for a human. This mirrors the principle the
  codebase already holds for hardware detection: *detection that could not run
  must never be the thing that stops your stream.* A moderation outage must not
  silence your chat.
- **Bounded spend.** A per-hour call ceiling, and the operator sees the count.
  An unbounded per-message API call is a surprise invoice.
- **The prompt and the verdict are both logged** against the message, so a
  disputed action can be explained. A moderation decision nobody can account
  for is worse than none.

## What it must never do

- **Never act on a message it has not stored.** The audit trail is the point.
- **Never escalate on its own.** Three flags do not become a ban unless a human
  configured exactly that, on that platform, for that checker.
- **Never apply a duration without conversion.** Kick counts timeouts in
  minutes where YouTube and Twitch count seconds. That trap already bit once and
  is handled in the adapters; automod must route through the same path rather
  than computing its own.
- **Never treat an empty rule set as "allow everything quietly".** A misconfigured
  automod that silently does nothing is indistinguishable from a working one.

## Testing

The suite that would make this trustworthy:

- Rate detector fires at N in the window and **not** at N−1.
- Repeat detector fires on the same phrase ×3 and **not** on three different
  messages — the negative case is what proves it is not simply always firing.
- **With a cell off, the engine produces a suggestion and takes no action.**
  This is the test that stops an accidental auto-ban, and it is the most
  important one here. It needs running per action, per platform, per checker --
  a single case passing proves only that one cell is wired.
- **A cell the platform cannot support is never automatic**, whatever the stored
  setting says. A config written before a capability changed must not become an
  action nobody can explain.
- **The matrix offers no cell the capability matrix calls impossible**, in the
  shape of the existing drift guards.
- Model timeout, 500 and rate-limit each let the message through and flag it.
- A raid of 5,000 distinct authors does not grow memory without bound.
- Kick durations convert to minutes and round **up**: truncating 30s to zero
  minutes would reach Kick as a permanent ban.
- An action taken by automod is attributable in the audit record to the rule or
  verdict that caused it.

---

## See also

- [CHAT-MODERATION.md](CHAT-MODERATION.md) — the manual actions this builds on
- [../REVIEW-POKA-YOKE.md](../REVIEW-POKA-YOKE.md) — friction proportional to consequence
- [../../SECURITY.md](../../SECURITY.md) — where the API credential is stored
