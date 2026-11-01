# Planned rollover before platform caps

A platform stops a live broadcast when it reaches its own limit. YouTube
stops keeping the replay of a stream longer than 12 hours, Twitch ends a
broadcast at 48 hours, and Facebook at 8 hours. Planned rollover replaces a
broadcast before that point, so a long show keeps going.

> **Status.** Built and tested against a fake channel and fake platform
> calls. Nothing here has been run against a live platform account yet.
> Items marked PROVISIONAL are the ones to check on a test account first.

## What each platform does

| Platform | Limit | Source | What a rollover does |
|---|---|---|---|
| YouTube | 12 h archive | [YouTube Help, archive](https://support.google.com/youtube/answer/6247592?hl=en) (documented) | Creates a replacement broadcast, binds it to the same stream, takes it live, then ends the old one. |
| Twitch | 48 h broadcast | [Broadcasting guidelines](https://help.twitch.tv/s/article/broadcasting-guidelines?language=en_US) (reported, not read in the check) | Reconnects the destination. PROVISIONAL: whether Twitch resets its count on a reconnect is unverified. |
| Facebook | 8 h broadcast | [Live video API reference](https://developers.facebook.com/docs/live-video-api/reference) (documented) | Countdown only. No rollover action. |
| Kick | not verified | none confirmed | No countdown and no action. |

The cap for each platform lives in `internal/oauth/capabilities_limits.go`. A
limit with no confirmed source has no number, and so no countdown.

## When a rollover runs

A destination must be delivering. Then any one of these triggers a rollover:

- **Safety margin.** The platform's cap less the margin you set (5 to 120
  minutes, default 30). For YouTube the session counts from the broadcast's
  own actual start, so a daemon that began watching late still counts from the
  real start.
- **Quiet window.** A daily window in a time zone you choose, such as 03:00 to
  05:00. A session must have run at least an hour before a window can roll it
  over, and a window that has already rolled over stays spent until the next
  day.
- **OBS scene.** A live switch to the scene you name, during the session. A
  reconnect snapshot from OBS does not count.

If several are due, the earliest one is recorded. A rollover is never repeated
within ten minutes of the last one, so the three triggers cannot fire together.

## What is never touched

A failed rollover leaves the old broadcast on air. The failure is recorded and
`rollover.failed` is sent. No ingest is stopped, ever. A YouTube replacement
that will not start is removed, and if it cannot be removed the error names it
so it can be deleted in YouTube Studio. When the start answers with an error
that is not a refusal, the replacement's state is read again first: one that
went live anyway is kept and the rollover carries on, and one whose state
cannot be read is kept and named rather than deleted.

The replacement is scheduled two minutes after the rollover starts, because
YouTube wants a start time in the future. PROVISIONAL.

If the channel will not carry two live broadcasts at once, the old one ends
first and the replacement goes live after it. That creates a gap, which is
reported. PROVISIONAL.

## Settings and API

| Method and path | Purpose |
|---|---|
| `GET /api/v1/rollover/limits` | The platform caps, with their sources and how well each is established. |
| `GET /api/v1/destinations/{id}/rollover` | One destination's settings, its countdown and what the planner would do now. |
| `PUT /api/v1/destinations/{id}/rollover` | Save the settings: `enabled`, `marginMinutes`, `quietStart`, `quietEnd`, `quietZone`, `obsScene`. A bad value is refused with the reason. |
| `POST /api/v1/destinations/{id}/rollover/run` | Roll over now. Refused with a 409 if the destination is not delivering, the platform is countdown only, or a rollover ran in the last ten minutes. If the platform refuses the rollover, answers 502 with the redacted reason; the timeline and webhook record the failure too. |

## Events

`rollover.started`, `rollover.completed` and `rollover.failed` go to the
events stream, to webhooks and to the operator timeline. The timeline row is
keyed to its destination, and a failure is recorded at warning severity. See
[EVENTS.md](EVENTS.md) and [HOOKS.md](HOOKS.md). `reason` names the trigger,
and on a failure says what happened to the old broadcast.

A countdown for a YouTube destination counts from the start YouTube records for
the broadcast. Until YouTube records one, no countdown runs and no rollover is
planned, so a late look at the stream never shortens the margin.

## The console

Each destination whose platform has a cap shows a planned-rollover panel on its
card. It shows the cap in hours with its source link. A cap that is reported
rather than documented is labelled as reported. Unverified caps show no
countdown at all. While the destination is delivering and the cutoff is known,
the panel counts down to it, refreshing about once a minute.

- **Settings** are the margin, the quiet window (start, end and time zone), the
  OBS scene trigger and the chat notice.
- **Roll over now** asks first. The result is reported as it came back: done
  (200), refused because the destination is not in a state to roll over (409),
  refused by the platform (502), or a plain failure.

## Chat notice

The chat notice is off by default. When on, one fixed line goes to the chat of
the rolled-over destination's own account, just before the platform call. A
second account connected on the same platform is a different channel, and it
does not receive the notice. A notice that fails is logged and the rollover
goes ahead.

## Twitch countdown start

The Twitch countdown counts from the stream's own start, read from Helix
`GET /helix/streams` (`started_at`). If that read fails, the first moment
polyemesis sees the destination on air is used instead, which was the behaviour
before. That fallback means a restart mid-stream can reset the count when the
read is unavailable.

After a reconnect the session is cleared, so the next sweep reads `started_at`
again. If Twitch still reports the old start, Twitch kept its count across the
reconnect. The countdown then counts from the reconnect, so the margin does not
come due again as soon as the ten-minute wait ends, and the destination is not
restarted every ten minutes.

## Countdown-only platforms

A countdown-only destination (Facebook, for example) records its session the
first time it is seen on air, and shows a countdown from that moment. Nothing
here can read the platform's own start time for these platforms.

## A replacement that would not start

If the old YouTube broadcast has ended and the replacement exists but would not
go live, the destination is pointed at the replacement with a fresh retry
budget, and the broadcast lifecycle takes it live once the ingest arrives there.
The rollover is still recorded as failed. While the old broadcast is still live,
a failed rollover leaves the destination on the old broadcast, as before.

With a recurring template, the replacement's privacy is the stricter of the
template's and the destination's own compliance privacy, so a private show is
never replaced by a public broadcast. The replacement also gets the template's
category and thumbnail. These are applied best effort: if the platform refuses either one,
the rollover still succeeds and the refusal is recorded as a warning.

## Not in this build

- Twitch replacement by a second session, and Facebook or Kick rollover.
- The YouTube ordering correction (create and bind the new broadcast before
  ending the old one) is not yet in this build; the rollover still ends first.
