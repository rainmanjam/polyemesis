# Chat moderation

**Status: SHIPPED**, 2026-07-30 — every item in the plan, plus one the research
did not anticipate. The server side is complete and tested. The chat pane still
exposes only Delete, so everything else is reachable over the API and not yet by
clicking: see [What is not wired to a button](#what-is-not-wired-to-a-button).

**Dependencies:** none in Go. Three platforms needed new OAuth scopes, so
existing connections have to be reconnected — `ScopeVer` reports that in the
account list rather than letting it surface as a 401 mid-broadcast.

Platform APIs were verified against live documentation on 2026-07-30. Nothing
below is inferred: where a fact was not confirmed, it was looked up before the
code depending on it was written.

- [What shipped](#what-shipped)
- [The unit trap](#the-unit-trap)
- [The endpoint that clears a channel](#the-endpoint-that-clears-a-channel)
- [Scopes and re-consent](#scopes-and-re-consent)
- [Per-platform detail](#per-platform-detail)
- [What is not wired to a button](#what-is-not-wired-to-a-button)
- [What is still absent](#what-is-still-absent)

---

## What shipped

Moderation is not a subsystem. It is optional interfaces in
`internal/chat/chat.go`, discovered by type assertion **inside the Hub** and
never at a call site. `Adapter` still requires only `Platform`, `Account`, `Run`.

| Platform | Read | Send | Delete | Hide | Ban / timeout | Chat rules |
|---|---|---|---|---|---|---|
| YouTube | yes | yes | **yes** | — | **yes** | — |
| Twitch | yes | yes | **yes** | — | **yes** | **yes** |
| Kick | yes | yes | yes | — | **yes** | — |
| Facebook | yes (comment poll) | no | **yes** | **yes** | — | — |

All four can delete; before this it was one of four. Two more actions are not
per-platform at all:

**Upstream retraction.** Twitch had been telling us about deletions made on its
own dashboard the whole time, and we dropped them. `twitch.go` already sends
`CAP REQ :twitch.tv/tags twitch.tv/commands`, and that `commands` capability is
exactly what makes `CLEARMSG` (one message deleted) and `CLEARCHAT` (a user timed
out, or the room cleared) arrive. The command switch handled `PRIVMSG` only.

The consequence was specific: polyemesis tells an operator to "use the platform's
dashboard" for anything it cannot do itself — that sentence is the literal error
body — and following that advice desynchronised the pane. The message stayed on
screen, and on any overlay fed from it, until retention aged it out up to two
hours later. Fixed with no new scope: this is Twitch volunteering what it already
decided.

**Local hide.** Removes a message from this server and every connected pane, and
touches nothing on the platform. The one action that works on platforms with no
moderation API at all, because it asks nobody's permission. It is **not**
moderation and the API says so in the response: *"Everyone watching on
&lt;platform&gt; can still see this message."* A control that looked like a delete
and was not would be worse than no control.

## The unit trap

The single most dangerous detail in this work, and the reason the interface takes
a `time.Duration` rather than a number:

| Platform | Field | Unit | Ceiling |
|---|---|---|---|
| YouTube | `banDurationSeconds` | seconds | — |
| Twitch | `duration` | seconds | — |
| Kick | `duration` | **minutes** | 10080 (7 days) |

A unified API taking a bare `600` would mean ten minutes on two platforms and
**seven days** on the third, and nothing anywhere would report an error. The unit
now exists only where the request is built; each adapter converts at the last
moment.

Two rounding rules that are not stylistic:

- **Rounded up, never truncated.** A 30-second timeout truncating to zero minutes
  would be sent as a **permanent ban** — zero means permanent on all three.
- **Zero sends no duration field at all**, rather than `duration: 0`, which the
  platforms read as a timeout of nothing.

Kick refuses beyond seven days rather than quietly converting to a permanent ban.
"Timeout for a very long time" and "ban forever" are different intentions and
guessing between them is not ours to do.

## The endpoint that clears a channel

`DELETE /helix/moderation/chat` documents `message_id` as **optional**, and
omitting it does not fail — it deletes **every message in the channel**. A blank
id reaching that endpoint turns "remove this message" into "wipe the chat", and
returns success while doing it.

So an empty id is refused before the URL is built, the refusal explains the
consequence rather than saying "invalid input", and the test covers `""`, `"   "`
and `"\t"` because the trim is the whole thing standing between the two outcomes.
The same habit is applied to every other adapter even where the API is not known
to behave that way.

## Scopes and re-consent

| Platform | Added | ScopeVer | Re-consent |
|---|---|---|---|
| YouTube | none — `auth/youtube` already covers delete *and* bans | — | **no** |
| Twitch | `moderator:manage:chat_messages`, `moderator:manage:banned_users`, `moderator:manage:chat_settings` | 1 → 4 | yes |
| Kick | `moderation:ban` | 1 → 2 | yes |
| Facebook | MODERATE task permission on the Page | — | app-level |

YouTube being free was worth verifying rather than assuming: "probably the same
scope" is how a feature ships and then 403s on every existing account.

Twitch keeps deletion and banning as separate scopes, and this document describes
them separately for the same reason: removing one message a broadcaster could
already remove by hand is not the same ask as the power to remove a person.

## Per-platform detail

### YouTube

`liveChatMessages.delete` and `liveChatBans.insert` both accept
`https://www.googleapis.com/auth/youtube`, which this app has always requested.
Every connected account can already moderate, with no reconnect.

Quota, not permission, is the real constraint — every write spends from the same
daily Data API budget the chat poll uses. Delete deliberately does **not** refuse
when the budget looks low: a message a moderator decided to remove stays on
stream while we decline to spend 50 units, and the API's own 403 is the authority
on when the quota is actually gone.

`Unban` reports a limitation instead of failing obscurely. YouTube removes a ban
by the **ban's** id, which polyemesis does not store, so it says to use YouTube
Studio.

### Twitch

Moderation is Helix, not IRC — the `/delete` chat command was removed — so that
half of the adapter speaks HTTP and needs three things IRC never carries: a
`Client-Id`, the channel's numeric id, and a token still fresh an hour after
connect.

`POST /helix/moderation/bans` does both ban and timeout: omit `duration` for
permanent, supply seconds for a timeout, with a `reason` up to 1000 characters
(truncated here rather than allowed to fail the whole request).
`PATCH /helix/chat/settings` carries slow mode, follower-only, subscriber-only,
emote-only, unique-chat and the non-moderator delay — all PATCH semantics, so
adjusting one cannot switch off another as a side effect.

Every endpoint takes **`moderator_id` as well as `broadcaster_id`** and answers
**403 when that user does not moderate that channel**. A restreamer moderating
their own chat is the normal case; a delegated operator is a different account
and a different conversation. `BroadcasterID` and `ModeratorID` are separate
fields for exactly that reason, even though the wiring currently sets both from
the same value.

Four failures get four sentences, because each has a different thing to do about
it: 401 the scope was never granted, 403 not a moderator there, 404 usually the
**six-hour limit** on deletion rather than a missing message, 400 Twitch
protecting the broadcaster's own messages and other moderators'.

### Kick

Already had delete. Ban and timeout now work over `moderation:ban`, with the
minutes conversion described above. Kick's ids are integers in JSON, so a quoted
id is rejected locally with a message that names the problem — the API's own
error names neither field.

### Facebook

Live chat is the comment thread on the video, so moderation is comment
moderation: `DELETE /{comment_id}`, and `is_hidden` to take a comment off the
public thread without destroying it. **Facebook is the only platform here with a
reversible primitive**, which is why Hide is a separate interface rather than a
flag on Delete — a moderator choosing between "hide" and "destroy" should have to
say which.

"Hidden" stays Facebook's definition: it remains visible to its author and their
friends. Every string says "off the public thread", never "gone".

Acting on a Page's comments needs the **MODERATE** task permission, which is
separate from being able to read them — an app that happily shows you the thread
can still be refused when you act on it. That is the failure that costs a day, so
the 403 names it.

## What is not wired to a button

The chat pane offers Delete on every message on every platform, and that is
still the only moderation control in the UI. Hide, local hide, ban, timeout,
unban and the Twitch chat rules are complete and tested on the server and
reachable over the API:

```http
DELETE /api/v1/chat/messages?platform=&account=&id=
POST   /api/v1/chat/messages/hide?platform=&account=&id=&scope=local|platform[&hidden=false]
POST   /api/v1/chat/bans?platform=&account=&userId=[&seconds=][&reason=]
DELETE /api/v1/chat/bans?platform=&account=&userId=
PATCH  /api/v1/chat/settings?platform=&account=
```http

`scope` defaults to `local` on the hide route deliberately: a caller that omits
it gets the half that cannot overreach, rather than an unintended write to
somebody's public thread.

The UI work is the remaining piece. It is not a thin wrapper — a timeout needs a
duration control, a ban needs a confirmation proportional to the consequence, and
local hide needs to say plainly that viewers still see the message. Doing it
badly would be worse than the API-only state, because a control that misleads a
moderator about what it did is the failure this whole document is written around.

## What is still absent

**Automated filtering** — word lists, link blocking, rate limits. A category
change rather than a missing feature: it means holding messages before display
and having polyemesis decide, duplicating what every platform already does with
far more signal.

**Fan-out "delete everywhere"** — recommended against. Message ids are
platform-scoped, so matching the "same" message across platforms would need
author-plus-text-plus-time heuristics, and a false positive deletes an innocent
message. The failure mode is worse than the inconvenience it removes.

**Twitch blocked terms and Shield Mode** — real APIs, not built. Both are
channel-wide state like the chat settings that did ship, and would follow the
same shape.

**Role-based gating inside polyemesis.** Messages carry `Moderator`,
`Subscriber` and `Broadcaster`, and the UI renders a shield, but nothing gates on
them. Authority comes from the platform token, not from a role polyemesis
inferred. That is correct and should stay: the platform is the authority.

### On the reversal

Banning and timing out were previously withheld as a product decision, recorded
in `internal/oauth/kick.go`:

> Nothing in polyemesis bans or times out a viewer, and a consent screen asking
> for the power to do so — for a restreamer — reads as overreach and costs trust
> we need for the rest.

The maintainer reversed that, and the scopes are requested now. The old reasoning
is kept in the source rather than deleted, because it is the argument to re-read
if the call is ever revisited. A test in `internal/oauth/kick_test.go` used to
assert that `moderation:ban` was **absent**; it now asserts the opposite, and the
property it guards is unchanged — the consent screen must match what the code
does, in both directions.

## See also

- `internal/chat/chat.go` — the capability interfaces, and the compile-time
  assertions that every adapter really implements what its scope pays for
- `internal/oauth/capabilities.go` — the matrix, kept in step with the UI copy by
  `capabilities_drift_test.go`
- [MONITORING](../MONITORING.md)
