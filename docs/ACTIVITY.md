# Viewer activity: follows, subs, tips and raids

**One event shape for the things viewers do besides chat: they follow, subscribe,
gift, tip, raid and redeem. It reaches a console feed, an alerts overlay for
OBS, the [events stream](EVENTS.md) and opt-in [webhooks](HOOKS.md), the same
way for every platform.**

Find it on the **Chat** page, under the **Activity** tab. The OBS side is the
[alerts overlay](OVERLAY.md#alerts-overlay).

> **Status of this release.** This is the shared core: the model, the
> ingestion point, the fan-out, the console feed, the overlay, the settings and
> **Send test alert**. Each platform's own events are added one platform at a
> time (see [Platforms](#platforms)); until a platform's source is in your build,
> nothing real arrives from it, and **Send test alert** is how you see and style
> everything. The console says so on the empty feed rather than showing a blank.

## What "activity" means, and what it does not

The word is **activity** on every surface (the `activity` event type and topic,
the `activity` hook trigger, `/api/v1/activity`) so that **alert** keeps its
existing meaning: an *operator alert* is polyemesis telling you a destination
went down ([MONITORING.md](MONITORING.md)). A viewer following you is activity.
The OBS source that draws it may be called the *alerts overlay*, because that is
what streamers call it, but the wire name does not follow the label.

| | Chat | Activity | Operator alert |
|---|---|---|---|
| Is | what a viewer said | what a viewer or the platform did | polyemesis telling *you* something broke |
| Stored | yes, for the retention you set | **no, memory only** | rules and history |
| Event type | `chat.message` | `activity` | `alert` |

## The kinds

| `kind` | What happened | Key fields | On the overlay |
|---|---|---|---|
| `follow` | someone followed | `actor` | yes |
| `subscribe` | a first subscription or membership | `actor`, `tier`, `product`, `amount?` | yes |
| `resub` | a renewal or a milestone | `actor`, `tier`, `months`, `streak`, `message` | yes |
| `gift` | someone gifted subscriptions | `actor` (the gifter), `count`, `tier`, `recipients` | yes |
| `tip` | bits, a Super Chat or Sticker, KICKs, a rant | `actor`, `amount`, `message` | yes (above your minimum) |
| `raid` | a channel sent its viewers, or you sent yours | `other`, `direction`, `viewers` | only `direction: "in"` |
| `redemption` | a channel-points reward was redeemed | `actor`, `title`, `status`, `message` | yes |
| `hypetrain` | a hype train began, moved or ended | `level`, `progress`, `phase` | yes |
| `shoutout` | a shoutout was given or received | `other`, `direction`, `viewers` | yes |
| `poll` | a platform-native poll | `title`, `progress`, `phase` | never |
| `prediction` | a platform-native prediction | `title`, `progress`, `phase` | never |
| `stream` | the **platform's** report that the stream went online, offline or changed title | `title`, `phase` | never |
| `moderation` | a ban, timeout or unban done **outside** polyemesis | `actor` (the moderator), `other` (the target), `message` (the reason), `action` | never |
| `reaction` | a tally of reactions (Facebook), with no actor | `count`, `breakdown` | never |

Two choices worth knowing:

- **One `tip` kind, not five.** Bits, Super Chats, Super Stickers, KICKs and
  rants all answer the same question (who paid, how much, what did they say), so
  an overlay, a threshold and a hook handle them once. The platform's own name
  for it survives in `source.type` (`channel.cheer`, `superChatEvent`,
  `kicks.gifted`, `rant`).
- **Memberships are subs.** A YouTube membership, a Rumble paid subscription
  and an X Premium subscription are `subscribe` and `resub`, with `product`
  naming the platform's word (`sub`, `membership`, `premium`, `paid_sub`). A
  member milestone is a `resub` with `months`.

`stream` is the platform's own word that the stream went live. It is **not**
`broadcast.live`, which is polyemesis moving a broadcast on air. `moderation` is
**not** `chat.moderation`, which stays the record of what polyemesis itself did.

## The event

```json
{
  "id": "act_3f9a0c1d",
  "kind": "tip",
  "platform": "youtube",
  "account": "UCabc",
  "at": "2026-11-07T20:14:03.100Z",
  "actor": { "id": "UCxyz", "name": "Mara" },
  "amount": { "currency": "USD", "minor": 500, "exponent": 2, "display": "$5.00" },
  "message": "great stream!",
  "tier": "2",
  "source": { "type": "superChatEvent", "id": "LCC.Cg8K" }
}
```

| Field | Meaning |
|---|---|
| `id` | opaque, per process; do not store it |
| `kind` | one of the kinds above |
| `platform`, `account` | the platform, and the connected account it arrived on |
| `at` | the platform's timestamp if it sent one, otherwise when polyemesis received it |
| `actor` | `{id?, login?, name, anonymous?}`. `name` is empty only when `anonymous` |
| `other` | the other channel, or the target of a moderation action |
| `amount` | see [Amounts](#amounts) |
| `message` | **written by the viewer** |
| `tier`, `product`, `months`, `streak` | as the platform sent them (`tier` is `"1000"`, `"2000"`, `"3000"` or a level name) |
| `count`, `recipients` | a gift: how many, and who got them when the platform says |
| `direction`, `viewers` | a raid or shoutout: `in` or `out`, and how many viewers came |
| `title`, `status` | a redemption, poll, prediction or stream title |
| `phase` | `begin`, `progress`, `end` (and `lock` for a prediction); `online`, `offline`, `update` for a stream |
| `action` | moderation: `ban`, `timeout`, `unban`, `automod_hold` |
| `level`, `progress`, `breakdown` | hype train level and bars; poll and prediction options; reaction counts |
| `source` | `{type, id}`: the platform's own name for the event and its id, which is what de-duplicates it |
| `test` | `true` for an event made by **Send test alert** |

Every text field is trimmed and bounded (names 100 characters, messages 500,
titles 200), and an event the system cannot make sense of (a tip with no amount,
a raid with no other channel, an unknown kind) is refused and counted under
`invalid` on `GET /api/v1/activity/sources`; its text is not logged.

### Amounts

An amount is **either money or a platform unit**, never both, and it is **never
converted**.

```json
{ "currency": "USD", "minor": 500, "exponent": 2, "display": "$5.00" }
{ "unit": "bits",   "units": 100, "display": "100 bits" }
```

- Money: `currency` (ISO 4217), `minor` (500 with `exponent` 2 is 5.00) and
  `exponent`, carried so nothing needs a currency table (JPY has 0, BHD 3).
- A unit: `unit` (`bits`, `kicks`) and `units`.
- `display` is the platform's own string when it sends one (a Super Chat's
  `amountDisplayString`), otherwise built.

A bit is a US cent on Twitch, but there is no published rate for a KICK and
exchange rates move daily, so a rate in the contract would be wrong somewhere.
That is why a [minimum](#settings) is one row **per currency or unit**.

## Send test alert

**Activity → Settings → Send test alert**, and beside **Copy OBS URL** on the
overlay editor. `POST /api/v1/activity/test` with an admin session or token,
limited to two a second:

```json
{ "kind": "tip", "platform": "twitch",
  "overrides": { "name": "Mara", "message": "hi", "amount": { "unit": "bits", "units": 500 } } }
```

It builds a canned event of that kind with `"test": true` and
`source.type: "polyemesis.test"`, and sends it **through the same path as a real
one**: your kind and minimum settings, the overlay's automod check, the console
feed, the events stream, the `/ws` feed and any hook that subscribes to
`activity`. It needs no account, no Affiliate status and no monetisation, so you
can style the overlay and check the platform filter before you ever go live.

The answer says what happened:

```json
{ "accepted": true, "event": { "id": "act_…", "kind": "tip", "test": true } }
{ "accepted": false, "reason": "disabled", "detail": "tip is switched off for twitch in settings", "event": { … } }
```

A refused request is a `400` (unknown kind or platform, or overrides that make
an invalid event), and a limit hit is a `429` with `Retry-After: 1`.
`overrides` may set `name`, `message`, `title`, `amount` (tips only), `count`
(gifts and reactions), `viewers` (raids and shoutouts), `months` (resubs) and
`tier`. It cannot forge a source or an id.

The reaction tally is **off by default**, so a `reaction` test alert is refused
until you switch **Reactions** on.

## Settings

Under **Activity → Settings** (stored in the ordinary settings, as
`settings.activity`; no new table):

| Setting | Does |
|---|---|
| **Activity** (`enabled`) | the master switch. Off, nothing is delivered anywhere: console, events, hooks or overlay |
| **Kinds per platform** (`kinds`) | which kinds are on for each platform. A platform with no entry uses the defaults: every kind except the reaction tally. An entry is exactly the list, and `[]` switches that platform's activity off |
| **Minimum tips** (`minTip`) | an overlay-only floor, one row per currency or unit (`{currency, minor}` or `{unit, units}`). A tip in a currency or unit with no row is never below a minimum. The console, the events stream and hooks always see every tip |
| **Messages on the overlay** (`overlayMessages`) | on by default. Off, an alert shows the name and amount only. Messages are always judged by automod first |
| **Reactions** (`reactions`) | Facebook's reaction tally. Off by default |

Where a platform reports that a kind is not available to your account, the row
is greyed with the reason rather than hidden: "needs Affiliate", "reconnect to
grant", "needs App Review".

## Where it goes

```
 chat adapters (YouTube, Rumble, Kick, X, Facebook)      platforms with their own
   emitActivity(sink, event)                              transport (Twitch EventSub)
            \____________________  Hub.Submit  ____________________/
            normalise -> validate -> dedupe -> settings filter -> ring -> fan-out
                 |                |                  |                  |
        bus "activity"    alerts overlay       opt-in hooks        GET /api/v1/activity
        (/ws, then SSE)   (automod, then       (trigger            (the console feed)
                           activity.display)    "activity")
```

- **Console feed.** `GET /api/v1/activity?limit=&platform=&kind=` seeds it
  (newest first, up to 500), then the `/ws` socket delivers each new event.
  `POST /api/v1/activity/{id}/replay` ("Replay on overlay") draws a remembered
  event again.
- **Events stream.** `GET /api/v1/events?topics=activity` delivers
  [`activity`](EVENTS.md#activity). An overlay key is delivered
  [`activity.display`](EVENTS.md#activity), a different, stripped payload.
- **Webhooks.** A hook receives the [`activity` trigger](HOOKS.md#the-activity-trigger-opt-in)
  only if it names it.
- **Not stored.** The console feed is a 500-event ring in memory and a restart
  empties it. The events stream keeps its usual 5-minute replay. Durable history
  would need a database table, which is a deliberate step not taken here.
- **De-duplicated.** The same event reported twice (a platform that redelivers,
  two sockets overlapping during a reconnect) is delivered once, on
  `(platform, account, source.type, source.id)`. A bounded memory of 4096 keys.
  A test alert is never a duplicate.

### Who can see what

An activity names viewers and carries words they wrote, so it is treated like
chat:

| | Admin | Read token |
|---|---|---|
| `GET /api/v1/activity`, `/activity/sources` | yes | **no (403)** |
| `activity` on `/api/v1/events` | every kind | only `stream`, which names nobody |
| `activity` on `/ws` | yes | no |
| `POST /api/v1/activity/test`, `/activity/{id}/replay` | yes | no |
| `activity.display` | with an overlay key, for that overlay's platforms only | no |

An overlay key reads `activity.display` and nothing else of activity: not the
full event, not the feed.

### What goes on air

Whatever is on the overlay is on air, and a viewer's name and words are written
by a stranger. So, on the overlay only:

- the **message** is held until automod has passed it. If automod removes it, or
  does not answer within four seconds, **the alert is shown without the
  message**. An error from the model checker is treated the same way, which is
  stricter than chat (a chat line is already on your console; an alert loses
  nothing by showing no words);
- the **name** (and a single gift recipient) is checked against your blocked
  terms, and a name that fails is shown as "Someone";
- the projection has **no ids** (no actor id, account, source id or recipient
  id), and a **raid you sent out** is never drawn;
- the text is drawn as plain text, never as markup.

A Super Chat or a rant stays a chat line **and** becomes an activity; the two
are separate, and automod judges each.

## Platforms

Each platform's source is added by its own change and lists its kinds, how it
gets them and what it needs here. **Nothing below is connected in this release.**

| Platform | How the events arrive | Status |
|---|---|---|
| Twitch | EventSub over a WebSocket (its own transport) | not yet |
| Kick | webhook events on the existing chat webhook | not yet |
| X | Activity API events on the existing webhook | not yet |
| YouTube | Super Chat, Sticker, membership and milestone items in the chat poll | not yet |
| Rumble | the follower, subscriber, gift and rant blocks in the existing poll | not yet |
| Facebook | a reaction tally from the existing comments loop (off by default) | not yet |

Off-stream events are accepted as missed: a platform whose events arrive on the
chat connection (Kick and X webhooks) delivers activity only while that chat is
attached, so a follow while you are offline is not reported later.

## Limitations

- **Memory only.** A restart empties the console feed; the events stream keeps
  its usual 5-minute replay. There is no history and no export.
- **No currency conversion.** A minimum is per currency or per unit.
- **No MQTT topic, no Stream Deck key** for activity yet. Both are planned
  follow-ups; neither is wired.
- **A platform's live-status webhook for Facebook** is not built. Reactions are
  read by polling when that source is added.
- **The overlay is tested in headless Chromium**, as the chat overlay is, not in
  OBS itself.

## For platform authors

A platform adds its events in its own files and does not edit the core.

1. **Say it with `emitActivity`.** A chat adapter calls
   `emitActivity(sink, activity.Event{...})` (`internal/chat/activity.go`) on
   the `chat.Sink` it was handed; the runner stamps `Platform` and `Account`.
   Fill `Kind`, the actor or other channel, the `Amount` (money or a unit, never
   converted), `Source.Type` and a `Source.ID` that is stable across redelivery
   (the platform's message id, or a hash of the fields that identify the event).
   A decode failure logs the type and length, never the body.
2. **A platform with its own transport implements `activityhub.Source`**
   (`internal/activityhub/source.go`): `Platform()`, `Account()` and
   `Run(ctx, emit)`. Register a builder for the platform in
   `activitySourceBuilders` in `internal/api/activity_wiring.go`. The server
   starts it beside the account's chat and stops it when the chat is detached;
   the supervisor owns the goroutine, the jittered backoff and the panic
   recovery. Optional `Health()` and `Kinds()` report in plain words.
3. **Report each kind** with `activity.KindStatus{Kind, State, Detail}`: `on`,
   `off`, `unavailable` (the platform will not send it to this account; **not**
   a failure), `reconnect` (the token predates the scope) or `unknown`. A chat
   adapter does it by implementing `ActivityKinds() []activity.KindStatus`.
4. **Never add a catalogue row, bus type or overlay case.** The two rows
   (`activity`, `activity.display`) cover every kind.
5. **Polled lists need a silent baseline.** If the platform returns a recent
   list, the first poll after start or reconnect seeds the dedupe and emits
   nothing.
6. **Prove it with a test** that decodes a recorded payload into the event,
   including the redelivery case.
