# Chat moderation

**Status:** researched, one item shipped (Kick delete). **Nothing here is blocked
on data plumbing.**
**Dependencies:** none in Go. Two items need new OAuth scopes and therefore
re-consent; one needs none at all.

Platform APIs verified against live documentation on 2026-07-30. Where a claim
was not confirmed it says so rather than guessing — the pattern this repository
keeps relearning is that an unverified "no" becomes a capability nobody builds.

- [What ships today](#what-ships-today)
- [The identifiers are already there](#the-identifiers-are-already-there)
- [Action matrix](#action-matrix)
- [Per-platform detail](#per-platform-detail)
- [The four ways this could work](#the-four-ways-this-could-work)
- [Recommended order](#recommended-order)
- [What is deliberately absent](#what-is-deliberately-absent)

---

## What ships today

Moderation is not a subsystem. It is optional interfaces in
`internal/chat/chat.go`, discovered by type assertion **inside the Hub** and
never at a call site — `Sender`, `Deleter`, `Healther`, and `Purger` on the
Store. `Adapter` itself requires only `Platform`, `Account`, `Run`.

| Platform | Read | Send | Delete |
|---|---|---|---|
| YouTube | yes | yes | **no** |
| Twitch | yes | yes | **no** |
| Kick | yes | yes | **yes** |
| Facebook | yes (comment poll) | no | **no** |

One platform of four can delete. Three mechanisms surround that:

1. **`Hub.Delete` is per-platform, never fan-out.** A message id only means
   something on the platform that issued it.
2. **The refusal is a designed answer, not an error.** A non-`Deleter` adapter
   returns a sentence — *"polyemesis cannot delete twitch messages; use the
   twitch dashboard"* — and the API passes it through as the 400 body. The UI
   offers Delete on every message on every platform on purpose: hiding it on a
   guess *"would silently remove a moderator's only tool the day a platform
   gains the capability."*
3. **Ordering is the safety property.** The platform is asked first; the local
   copy is removed only once it agreed. The other order leaves a message deleted
   in polyemesis and still visible to every viewer, which is the exact failure
   the button exists to prevent.

## The identifiers are already there

This is the finding that changes the effort estimates. Every adapter already
captures both the message id and the author id, because dedupe and reply-to
needed them:

| Platform | Message id | Author id |
|---|---|---|
| Twitch | `Tags["id"]` (twitch.go:352) | `Tags["user-id"]` (twitch.go:359) |
| YouTube | `it.ID` (youtube.go:422) | `a.ChannelID` (youtube.go:429) |
| Facebook | `c.ID` (facebook.go:254) | `c.From.ID` (facebook.go:261) |
| Kick | yes | yes |

`Message.ID` and `Author.ID` are both in the unified model. **No moderation
action on any platform is blocked on plumbing.** Every remaining cost is an
OAuth scope or a product decision.

## Action matrix

`S` = needs a new scope and therefore re-consent for every connected account.

| Action | YouTube | Twitch | Kick | Facebook |
|---|---|---|---|---|
| Delete one message | possible, **no new scope** | possible `S` | **shipped** | possible `S` |
| Hide (reversible) | — | — | — | possible, `is_hidden` `S` |
| Timeout / temp ban | possible `S`? | possible `S` | scope exists, refused | — |
| Permanent ban | possible `S`? | possible `S` | scope exists, refused | — |
| Un-ban | possible `S`? | possible `S` | unverified | — |
| Clear all chat | — | possible `S` | unverified | — |
| Slow / follower / delay mode | — | possible `S` | unverified | — |
| Blocked-term list | — | possible `S` | unverified | — |
| Shield mode | — | possible `S` | — | — |
| Manage moderator list | possible `S`? | possible `S` | unverified | — |
| **React to UPSTREAM deletion** | not handled | **not handled, events already arriving** | not handled | n/a (poll) |

## Per-platform detail

### YouTube — the cheap one

`liveChatMessages.delete` is documented as accepting
**`https://www.googleapis.com/auth/youtube`**, which `internal/oauth/youtube.go`
**already requests**. So message deletion needs no new scope and no re-consent.
That makes YouTube the cheapest platform to add, not the hardest — the opposite
of the assumption in the capability matrix, which records it as `unknown`.

Also available: `liveChatBans.insert` (permanent when `banDurationSeconds` is
unset, temporary otherwise, defaulting to 300s), `liveChatBans.delete`, and
`liveChatModerators`. **Unverified:** whether the bans endpoints accept the same
`auth/youtube` scope or insist on `youtube.force-ssl`. Confirm before estimating,
because the answer decides whether bans are also re-consent-free.

Quota is the real YouTube constraint, not permission. Chat read is already paced
against the daily Data API quota; every write spends from the same budget.

### Twitch — the richest surface, all of it gated

`POST /helix/moderation/bans` does **both** ban and timeout: omit `duration` for
permanent, supply seconds for a timeout, with an optional `reason` up to 1000
characters. `DELETE /helix/moderation/bans` reverses either. Scope
**`moderator:manage:banned_users`**.

Beyond bans: `PATCH /helix/chat/settings` for slow mode, follower-only mode and
non-moderator message delay (scope `moderator:manage:chat_settings`), plus
delete-all-chat, blocked terms, and Shield Mode.

Two traps worth knowing before designing anything:

- Every endpoint takes **`moderator_id` as well as `broadcaster_id`**, and
  returns **403 when that user is not a moderator of that channel**. A
  restreamer moderating their own channel is fine; a delegated operator is a
  different account and a different conversation.
- polyemesis currently requests `chat:read`, `chat:edit` and
  `channel:manage:broadcast` — **no moderation scope at all**.

**Unverified:** the single-message delete endpoint and its scope. The docs
surface "Delete All Chat Messages" plainly; the single-message variant needs
confirming rather than assuming.

### Kick — shipped, and deliberately stopped short

`Delete` is implemented over `moderation:chat_message:manage`, with an error path
that recognises a pre-chat token and tells the operator to reconnect. Kick's
`moderation:ban` scope exists and is **deliberately not requested** — see
[What is deliberately absent](#what-is-deliberately-absent). **Unverified:**
Kick's ban/timeout endpoint shape and the rest of its moderation surface, since
nothing here calls it.

### Facebook — a different model

Live chat is the comment thread on the live video, so moderation is comment
moderation: **`DELETE /{comment_id}`**, and an **`is_hidden`** field that hides a
comment reversibly — the only native hide-rather-than-destroy primitive across
all four platforms.

Two constraints: accessing comment ids on a Page post requires the **MODERATE**
task permission for apps under Page Public Content Access (Graph API v11.0+), and
polyemesis has no Facebook `Sender` at all today, so this would be the first
write to a Facebook comment.

## The four ways this could work

**1. Consume upstream deletions.** No new scope on any platform, and it fixes an
incoherence rather than adding a feature. `twitch.go:162` already sends
`CAP REQ :twitch.tv/tags twitch.tv/commands`, and that `commands` capability is
what makes Twitch deliver **`CLEARMSG`** (one message deleted) and **`CLEARCHAT`**
(chat cleared, or a user timed out). The command switch at `twitch.go:247`
handles **`PRIVMSG` only**, so those events are received and dropped.

The consequence is specific: a moderator does exactly what the product tells them
to do — deletes on Twitch's own dashboard — and the message stays on screen in
the unified pane until retention ages it out, up to two hours later. Any
on-stream overlay fed from that pane keeps showing viewers a message that no
longer exists. `forgetMessage` already exists to do the removal.

**2. Extend `Deleter` to the other three.** YouTube first, because it needs no
re-consent. Turns one platform of four into three or four.

**3. Local-only hide.** Removes a message from the polyemesis pane without
touching the platform. No scope anywhere. Only honest if labelled as such —
viewers still see it — and genuinely useful when the pane feeds an overlay.
Facebook's `is_hidden` is the one place this maps onto a real platform primitive.

**4. Fan-out "delete everywhere".** Recommended **against**. Message ids are
platform-scoped, so matching the "same" message across platforms would need
author-plus-text-plus-time heuristics, and a false positive deletes an innocent
message. The failure mode is worse than the inconvenience it removes.

Not on this list: automated filtering (words, links, rate limits). That is a
category change — it means holding messages before display and having polyemesis
decide, duplicating what every platform already does with far more signal.

## Recommended order

| # | Item | Scope cost | Why here |
|---|---|---|---|
| 1 | Upstream deletion events (Twitch `CLEARMSG`/`CLEARCHAT`) | none | Capability already negotiated, events already arriving, fixes a live incoherence |
| 2 | YouTube `Deleter` | **none** | `auth/youtube` is already granted |
| 3 | Local-only hide | none | No platform dependency at all |
| 4 | Facebook delete / `is_hidden` | MODERATE | First Facebook write; `is_hidden` is uniquely reversible |
| 5 | Twitch `Deleter` | `S` | Re-consent, and the `moderator_id` 403 needs a UI answer |
| 6 | Chat-settings controls (slow, follower-only) | `S` | Channel-wide state, not per-message; a different UI |
| 7 | Ban / timeout | `S` | Blocked on the stance below, not on effort |

Items 1–3 need no consent screen changes between them.

## What is deliberately absent

**Ban and timeout.** This is a scope decision, not missing code. Kick's
`moderation:ban` is omitted and Twitch is asked for no moderation scope at all.
The reasoning, from `internal/oauth/kick.go`:

> Nothing in polyemesis bans or times out a viewer, and a consent screen asking
> for the power to do so — for a restreamer — reads as overreach and costs trust
> we need for the rest.

Revisiting it means re-consent on every connected account, which is what
`ScopeVer` exists for. It is a product decision and belongs to whoever owns the
consent screen.

**Role-based gating inside polyemesis.** Messages carry `Moderator`,
`Subscriber` and `Broadcaster`, and the UI renders a shield, but nothing gates on
them. Delete authority comes from the platform token, not from a role polyemesis
inferred. That is correct and should stay: the platform is the authority.

## See also

- `internal/chat/chat.go` — the capability interfaces
- `internal/oauth/capabilities.go` — the matrix, which records YouTube, Twitch and
  Facebook moderation as `unknown`; item 2 above makes at least YouTube a `yes`
- [MONITORING](../MONITORING.md)
