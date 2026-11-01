# Recurring broadcast events

A start schedule can carry a **broadcast template**. The template says what
each show's platform event looks like: its title, description, privacy,
category and thumbnail. Polyemesis then keeps the next few occurrences of that
schedule created as upcoming events, so viewers can find and follow the show
before it starts.

> **Status.** The templates, the look-ahead and the single-occurrence skip and
> retitle are built and tested against fakes. Only YouTube creates events
> today. Twitch and Kick take the template's title and category at go-live. The calendar screen is not in this build yet; the same data is served
> over the API described below. Items marked PROVISIONAL have not been checked
> against a live platform account.

## What a template holds

| Field | Meaning |
|---|---|
| `titlePattern` | The event title. `{date}` becomes `YYYY-MM-DD` and `{time}` becomes `HH:MM`, both read in the schedule's own time zone. Any other `{...}` is refused by name. The rendered title is limited to 100 characters, YouTube's limit. |
| `description` | The event description, the same for every occurrence. Up to 5,000 characters. |
| `privacy` | `public`, `unlisted` or `private`. Empty means `public`. A private event announces the show to nobody. |
| `category` | The platform category, applied to the event's video where the platform allows it. A refusal is reported as a warning and does not stop the event. |
| `lookahead` | How many upcoming occurrences are kept created, from 1 to 10. Default 2. |
| thumbnail | A PNG or JPEG of up to 2 MB. Stored once and sent with each event. It is never returned by the read API, only as `hasThumbnail`. |

A template belongs to one start schedule. A stop schedule cannot carry one.

## How the look-ahead behaves

Every five minutes, and once when the server starts, the look-ahead:

1. Walks forward through the occurrences of each enabled start schedule that
   has a template, and counts the first `lookahead` of them that are **not
   skipped**. A skipped occurrence does not use up a slot: the occurrence after
   the window takes its place, so the look-ahead always keeps that many shows
   created.
2. For each destination that schedule targets, creates any counted occurrence
   that does not yet have an event. Each event is created scheduled, titled from
   the template, given the thumbnail, and then recorded. If the title write
   fails, the new event is cancelled, so no event goes public with a placeholder
   name. If the title write fails and the cancel is refused too, the event's id
   is kept as **stranded**: it is not listed as a show and does not count
   toward the look-ahead, and the cancel is retried on every pass until it
   succeeds. Its title is empty, which is the truth about it, and it is not
   presented as an upcoming show.
3. Points the destination's go-live at the **first occurrence that is not
   skipped**. When the show starts, its stream key and that event are the ones
   used. The stream does not depend on the look-ahead: if an event could not be
   created, the show still goes live on time.

The look-ahead never changes a destination that is currently live, because a
new event can issue a new stream key. Such a destination is skipped until it
is off air.

Platforms that cannot title a scheduled event are not given template events.
Facebook is in this group until its Page API is checked (PROVISIONAL). X has no
scheduled-event create in this build.

## Twitch and Kick at go-live

Twitch and Kick have no scheduled event, so the template reaches them when the
show starts instead:

- Every 30 seconds the server looks for a template-bearing start schedule whose
  show began in the last 30 minutes, and that targets an enabled Twitch or Kick
  destination.
- It writes the rendered title and the template's category to that channel,
  through the same metadata write the go-live composer uses. It does this **once
  per show**: the attempt is recorded before the write, so a restart does not
  write it again over an operator's own edit.
- A destination turned on by hand long after its show started is not written.
  The template title is not sent to a destination the operator started, after
  the show's window has closed.
- The template's description is not sent. Twitch has no stream description, and
  Kick's live broadcast has none either.
- A refused write, or a category the platform cannot find, is logged as a
  warning with the platform's own words. It is not retried for the same show.
  Set the title and category in the composer if one of them did not land.

## Skipping, retitling and deleting

- **Skip an occurrence** (`POST /api/v1/recurring/occurrences/{id}/skip`)
  deletes that occurrence's platform event and records it as skipped, so the
  look-ahead does not create it again. The look-ahead then creates the occurrence
  after the window, so the count of created shows stays at `lookahead`. The
  schedule still starts the destinations at that time. To stop the show outright,
  switch the schedule off.
  A skip is **refused with 409** for a show whose start has passed, and for a
  destination that is on air now. Nothing is cancelled in that case. A show that
  has started may be the live broadcast, or the page of a show that aired.
- **Retitle one occurrence** (`POST /api/v1/recurring/occurrences/{id}/title`)
  changes the title of that event only. The template still applies to every
  other occurrence.
- **Delete the template** (`DELETE /api/v1/schedules/{id}/template`) removes it.
  The schedule keeps running. Upcoming events for that schedule are cancelled
  at the next look-ahead pass.
- **Delete the schedule** has the same effect: its upcoming events are
  cancelled at the next pass.

An event that cannot be cancelled keeps its record, so the next pass tries
again. If its destination has been deleted too, the event cannot be reached
from here, and the error names it so it can be removed in the platform's own
studio.

A template or schedule that disappears cancels only the events of shows that
have not started, on a destination that is not on air. For a show that has
started, or whose destination is on air, the record is dropped and the platform
event is left alone: that event may be the live broadcast or the page of a show
that aired, and removing it is the operator's decision.

On YouTube, a template event is deleted only while YouTube reports it as
`created` or `ready`. A broadcast that has gone live is refused and kept.

## API

All routes sit beside `/api/v1/schedules` and use the same session and CSRF
rules.

| Method and path | Purpose |
|---|---|
| `GET /api/v1/schedules/{id}/template` | Read the template. |
| `PUT /api/v1/schedules/{id}/template` | Create or replace the template. Refused with the reason if it is invalid. |
| `DELETE /api/v1/schedules/{id}/template` | Remove the template. |
| `PUT /api/v1/schedules/{id}/template/thumbnail` | Upload the thumbnail as a raw PNG or JPEG body. The bytes must match the type. |
| `GET /api/v1/recurring/occurrences?from=&to=` | List occurrences in a window, at most 90 days. Defaults to the next two weeks. |
| `POST /api/v1/recurring/occurrences/{id}/skip` | Skip one occurrence. |
| `POST /api/v1/recurring/occurrences/{id}/title` | Retitle one occurrence. Body: `{"title": "..."}`. |

## Platform notes

- **YouTube.** Events are scheduled broadcasts with the template's title,
  description, privacy and thumbnail. The thumbnail upload before go-live is
  PROVISIONAL: it is documented for a video id, and a broadcast's id is its
  video id, but it has not been checked on a live channel.
- **Facebook.** PROVISIONAL. Not given template events until its Page API
  accepts a titled scheduled video.
- **Twitch.** Title and category written at go-live, once per show. No
  description. Live verification is pending: no Twitch account was used.
- **Kick.** Title and category written at go-live, once per show. No description,
  because Kick's live broadcast has none. A category Kick cannot match is a
  warning, and the title still lands.
- **X.** Not yet. X has no scheduled-event create in this build, and it can
  join the look-ahead once it implements the same editor capability YouTube
  does.
