# Chat polls

**One poll, asked in every connected chat at once, counted as one result.**

You ask a question with two to six options. Polyemesis posts it into every
chat it can send to. Viewers vote by typing the option's number, and the result
updates live in the console and, if you add it, on stream.

## Start a poll

Start a poll from any of these:

- the **Polls** panel on the console's **Chat** page;
- a Stream Deck **Chat poll** key (see [STREAM-DECK.md](STREAM-DECK.md));
- an AI assistant over MCP, with `start_poll` (see [MCP.md](MCP.md));
- the API, with `POST /api/v1/polls` (see [API.md](API.md#chat-polls)).

A poll has:

- a **question**, up to 100 characters;
- **2 to 6 options**, up to 30 characters each, all different;
- a **duration**, from 15 seconds to 1 hour.

Only one poll runs at a time, because "type 1" can only mean one thing in a
chat at once. Line breaks and control characters in the question or an option
become spaces before anything is sent.

When the poll opens, polyemesis posts this into every connected chat:

```text
POLL: Next map? — 1) Dust 2) Mirage — type 1-2 or !vote N to vote. 2 min to vote.
```

When it closes, it posts the result:

```text
POLL CLOSED: Next map? — Dust wins with 64% (41 of 64 votes).
```

The same words go to every chat, and they never name a platform. If a long
poll would not fit the strictest chat (YouTube allows 200 characters), the
option labels are shortened in the message. They are never shortened anywhere
else.

A chat that cannot be sent to (Rumble is receive-only), or that refuses the
message, is listed under the poll with the reason. The poll still runs, and
viewers on that platform can still vote.

You can end a poll early with **End now** (`POST /api/v1/polls/{id}/end`).

## How votes are counted

A vote is a chat message that is **exactly** one of these:

- the option's number: `2`
- `!vote 2`, in any upper or lower case

A number inside a sentence (`2 please`, `I vote 2`) is ordinary chat, not a
vote. Only the digits 1 to 6 count, and only up to the number of options.

| Rule | What happens |
|---|---|
| One vote per viewer per platform | A viewer is identified by the platform's own user id, not their display name. The same person on Twitch and on YouTube counts once on each. |
| The last vote counts | A viewer who types `1` and then `3` has voted for option 3. Typing the same number twice is still one vote. |
| Two channels on one platform | A viewer in both of your Twitch channels is one Twitch viewer and has one vote. |
| Automod blocks the message | It is not a vote. |
| Automod's model is still judging the message | The vote counts once the message is cleared. With no verdict within 4 seconds, it is dropped. |
| A moderator deletes or hides the vote message | That vote no longer counts. The viewer may vote again; an earlier vote does not come back. |
| A moderator bans or times out the viewer | Their vote no longer counts, and they are ignored for the rest of the poll on that platform. This holds whether the ban comes from polyemesis, from automod, or from the platform's own dashboard. |
| The chat is cleared | Nothing changes. Clearing the room is housekeeping, not a judgement on anyone's vote. |
| Polyemesis's own messages | Never votes. |

These are the same rules a message has to pass to reach the
[chat overlay](OVERLAY.md): a message automod removes never appears on stream,
and never counts.

When the time is up, no new vote is accepted. Votes that automod is still
judging are waited for, for at most 4 seconds, and then the result is final.
The poll shows **closing** while that happens.

## Results

The **Polls** panel shows, while a poll runs:

- each option's votes, as a bar and a number;
- how many of those votes came from each platform;
- the total, and the time left.

Results are **counts only**. Polyemesis remembers who voted for what for the
length of one poll, so it can apply the rules above, and never writes it down.
No result, event, export or history row names a viewer.

That is why a `read` API token can see results: `GET /api/v1/polls` and the
`poll` events carry the same counts the console shows, and nothing else.
Starting and ending a poll need an `admin` token, because they post into every
chat as you.

## On stream: the poll widget

The poll widget is a separate OBS browser source. It uses the same key and style
as a [chat overlay](OVERLAY.md), so you can place it apart from the chat lines.

1. Open **Settings → Chat overlays** and choose an overlay.
2. Select **Copy poll widget URL**.
3. In OBS, add a **Browser** source with that URL.

The widget shows the question, each option's bar and the total while a poll
is open, and the result for 20 seconds after it closes. Between polls it
draws nothing.

The widget has **no platform logos and no per-platform breakdown**. It shows
the totals only, so it is safe on a Twitch-only scene. It is sent to every
overlay key whatever that overlay's platform filter is, because there is
nothing platform-specific in it. It never shows a voter's name.

## Native platform polls

A poll can also run as the platform's own poll, in the places that have one:

- **Also run as a native Twitch poll** starts a Twitch poll on every connected
  Twitch channel that allows it.
- **Also run as a native YouTube poll** starts a YouTube live poll in every
  connected YouTube chat. It is off by default because it costs API quota.

**While a chat's native poll runs, that chat votes there, not by typing.** Its
viewers are told so when the poll opens:

```text
POLL: Next map? — vote in the Twitch poll above chat. 2 min to vote.
```

A number typed in that chat is not counted, and that platform's share of the
result comes from the native poll alone. A viewer who votes in the Twitch poll
and also types `1` in Twitch chat is counted once. Every other chat votes by
typing as usual.

Polyemesis creates the native polls first and announces the poll afterwards,
so each chat is told the right way to vote. The announcement can therefore
arrive a few seconds after you start the poll.

| What happens | Result |
|---|---|
| The native poll can't be created (see below) | That chat votes by typing, as if you hadn't asked for a native poll. The reason is shown under the poll. |
| The native poll ends early on the platform (a moderator or the streamer closes it there) | Its votes so far stand. From that moment, numbers typed in that chat count again. Numbers typed while it ran stay uncounted, because those viewers were told to vote in the native poll. |
| Your poll ends | The native poll is ended too, and its final count is merged. |
| The native poll's final count can't be read | The native count is dropped, and the numbers that chat's viewers typed while it ran are counted instead, since that is the only count there is. |

Channel-points voting is always off.

### Twitch

A Twitch poll is skipped, with the reason shown under the poll, when:

- the channel is not a Twitch Affiliate or Partner. Twitch runs polls only for
  those, and answers 403 otherwise;
- the Twitch account was connected before polyemesis asked for the
  `channel:manage:polls` permission. Reconnect it once under **Settings →
  Platform credentials**; the account is marked "reconnect needed";
- the poll does not fit Twitch's limits: at most 5 options, a question of at
  most 60 characters, options of at most 25, and 15 seconds to 30 minutes.
  Polyemesis does not shorten the poll to fit, because a Twitch poll that read
  differently from the chat poll would be a different poll.

The running count is read every 5 seconds, so Twitch's share updates while the
poll is open.

### YouTube

A YouTube live poll uses the `youtube` permission polyemesis already has, so
no reconnect is needed. It is skipped when:

- the poll has more than **4 options**, YouTube's limit. YouTube's API sets no
  text lengths; the chat poll's own limits (100 and 30 characters) apply;
- the YouTube chat has no live broadcast open;
- **the API quota cannot cover it.** Creating the poll and closing it cost 20
  units each. Polyemesis creates one only if those 40 units fit in the day's
  quota *without* spending the send reserve, so replying in chat keeps working
  for the rest of the day. Closing a poll is never refused for quota.

**YouTube's count arrives when the poll closes.** YouTube's API cannot read a
running poll's count; the count comes back only in the response to closing it.
While the poll runs, YouTube's share shows nothing, and the panel says the
count is added at close. The final result includes it. This needs the channel
owner's account: YouTube shows the count only to the channel owner.

## History and export

Every poll is kept, newest first, under the **Polls** panel's history and at
`GET /api/v1/polls`. Filter by time with `?since=` and `?until=` (RFC 3339) and
limit with `?limit=`.

`GET /api/v1/polls/export` downloads the history as CSV, one row per option per
platform plus a `total` row per option. Add `?format=json` for JSON, `?id=N` for
one poll, or `?since=`/`?until=` for a range. Cells that a spreadsheet would run
as a formula are prefixed with `'`.

A poll that was open when the server stopped is marked **interrupted**. The
votes it was holding were lost with the process, so it is never shown as a
result.

## Events

Each poll publishes to the [real-time events API](EVENTS.md#polls):

- `poll.started`, `poll.vote` (at most once a second) and `poll.ended`, in the
  `poll` topic, with the per-platform breakdown;
- `poll.display`, the widget's totals-only projection, in the `overlay` topic.

## Known limitations

- **One poll at a time.**
- **The count lives in memory while a poll runs.** A restart mid-poll loses it;
  the poll is marked interrupted.
- **YouTube's share of a native poll is known only at close.** See
  [YouTube](#youtube).
- **A native poll that stops early freezes its count.** A viewer who voted in
  it and then types a number after it stopped is counted twice. The platforms
  don't say who voted in their polls.
- **Kick has no native polls.** Kick's public API has no poll endpoint.
- **The YouTube live poll was built against the API reference and a fake
  server.** It has not been tried against a live YouTube broadcast.
- **Polls are not in a per-stream report yet.** The cross-platform stream report
  (#4) does not exist in this build. Poll history is queryable by time range so
  that report can include it.
- **Predictions and channel points are out of scope.**

## See also

- [OVERLAY.md](OVERLAY.md): the overlay key and styles the widget uses.
- [AUTOMOD.md](AUTOMOD.md): what blocks a message, and so a vote.
- [EVENTS.md](EVENTS.md#polls): the poll events.
- [PLATFORMS.md](PLATFORMS.md): Twitch's `channel:manage:polls` permission.
