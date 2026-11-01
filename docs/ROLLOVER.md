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
so it can be deleted in YouTube Studio.

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

## Not in this build

- **Deviation from #19, not built in this PR:** the countdown card and the
  settings control in the console. The API above serves the same data.
- The chat notice before a rollover. A send path exists (`chat.Hub.Send`), but
  no notice is built in this release.
- Twitch replacement by a second session, and Facebook or Kick rollover.
