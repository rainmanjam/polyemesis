# A Facebook broadcast that exists before it starts

**What:** roadmap item 10 Part E. When a Facebook destination is on a start
schedule, create its broadcast ahead of time as `SCHEDULED_UNPUBLISHED` with
`event_params`, so there is a Facebook event page people can be notified about
before the stream begins.

**Why:** today a Facebook broadcast is created at the instant the encoder
connects. Nobody can be told about it in advance, which is what a scheduled
show is for. Everything else in polyemesis already knows the start time — the
schedule is stored, evaluated and swept — and Facebook is the only integrated
platform with a pre-announcement surface we do not use.

## What Graph actually wants

Confirmed against the v26.0 reference; polyemesis pins v24.0, supported to
2028-02-18, and neither v25.0 nor v26.0 changed live video.

| | |
|---|---|
| Create | `POST /<ID>/live_videos?status=SCHEDULED_UNPUBLISHED&event_params=<UNIX_TS>` |
| Reschedule | `POST /<LIVE_VIDEO_ID>?event_params=<UNIX_TS>` |
| Bound | **at most seven days ahead**, and not ours to widen |
| Eligibility | account 60+ days old, Page or professional profile with 100+ followers |

`planned_start_time` is gone. Scheduling is a status plus a parameter, not a
field, which is why Part D's estimate looked like one piece of work when it was
two.

## The correction this design rests on

`DESTINATION-SETTINGS.md` used to say a weekly schedule could name a time
Facebook would refuse. **It cannot.** `internal/scheduler` has three kinds, and
the *next* occurrence of a daily schedule is at most a day away, of a weekly one
at most seven days by definition. Only a `once` schedule can be set beyond the
window.

That killed "clamp" — silently moving a broadcast's start time was the worst
option on the table and was only ever needed for a case that does not exist.

## Two decisions, both taken against the roadmap's own text

### It warns; it never refuses

The roadmap says *"refuse a `once` schedule more than seven days out, at save
time"*. This design does not, for two reasons.

**The schedule works either way.** What the seven-day bound limits is the
pre-announced event page — a discovery feature. The destination still goes live
at the scheduled time. Refusing a working configuration because a nice-to-have
cannot happen is the wrong trade.

**And the check cannot be made consistently.** `Schedule.DestinationIDs` is
empty for "every destination", which is what "start the show" usually means. A
save-time refusal therefore cannot see whether a Facebook destination is
involved, and would be silently stricter for schedules that name their targets
than for schedules that do not. Two answers to one question is how a rule
becomes a support request.

So: on save, if a `once` schedule fires beyond the window and names a Facebook
destination, the response carries a warning saying no event page will be
created and that the destination still goes live on time. Same mechanism as the
`warnings` array on destination writes.

### The sweep creates it, not the save

Creating at save time would pre-announce a weekly schedule's next occurrence and
nothing after it — the feature would quietly stop working after one week. The
sweep handles every kind by construction: each occurrence enters the window and
gets a broadcast.

## Where it lives, and where it does not

**Not in `internal/scheduler`.** That package's opening comment is a promise:

> It does not start or stop anything itself. A schedule flips the stored
> "enabled" intent through the same path the API uses and then asks for a
> reconcile [...] That is the whole design: there is exactly one way a
> destination comes up.

A Graph API call inside it would break that. The pre-announce sweep is its own
thing, reading schedules through the helpers `internal/scheduler` already
exports — `scheduler.Next(s, now)` returns the next occurrence for any kind, so
no date arithmetic is reimplemented.

## The flow

Once per sweep, for each enabled `ActionStart` schedule:

1. `scheduler.Next(s, now)` → the next occurrence, or nothing.
2. Skip unless it is inside seven days.
3. Resolve the schedule's destinations — remembering that empty means all.
4. For each that is Facebook, has a connected account, and has not already been
   pre-announced **for this occurrence**: create the broadcast.

### A schedule that names nothing pre-announces everything it would start

Step 3 is not a formality, because `DestinationIDs` is empty for "every
destination" and that is the commonest shape — "start the show" usually names
nothing. So a schedule with no targets and three Facebook destinations publishes
three event pages.

**That is deliberate, and it is not quite the no-op it looks like.** No broadcast
is created that would not have existed anyway; what changes is *when it becomes
visible*. A `LIVE_NOW` broadcast is invisible until bytes arrive.
`SCHEDULED_UNPUBLISHED` is a public event page from the moment it is created —
days early, by design, since being visible in advance is the entire feature.

The alternative considered was pre-announcing only explicitly-named
destinations, on the grounds that publishing something public should be a
deliberate act. Rejected because it would switch the feature off for exactly the
schedules people write.

Creation calls the existing `IngestFor` with a widened `IngestOptions`. That
struct's own comment already anticipates this:

> the create-time surface is going to grow: scheduling (`event_params`) and
> backup ingest both land here

```go
type IngestOptions struct {
    Privacy         db.FacebookPrivacy
    Crosspost       []db.CrosspostTarget
    DonateCharityID string
    // ScheduledFor makes this a SCHEDULED_UNPUBLISHED broadcast at that
    // instant instead of a LIVE_NOW one. Zero means live now, which is what
    // every existing caller passes and what they keep doing.
    ScheduledFor time.Time
}
```

`IngestFor` currently hardcodes `status: LIVE_NOW`. It becomes the zero-value
branch.

## The marker, and why it is a time rather than a flag

A sweep runs every 20 seconds. Without a record of what has been created, it
would create a broadcast every 20 seconds.

A boolean cannot express it: a weekly schedule needs a new broadcast every week,
so "already done" has to mean "already done **for this occurrence**". So the
destination stores `facebookScheduledFor` — the occurrence a broadcast was last
created for — and the sweep skips when it equals the occurrence in hand. A new
occurrence differs, and creation happens again.

Stored on `db.Destination` in the `facebook` column added by Part D, so there is
no migration.

## What the pre-created broadcast does to go-live

**This is the part with a real hazard in it.** Creating the broadcast returns a
stream key, and that key is what the encoder must publish to — the pre-announced
broadcast has to BE the broadcast that goes live, or the event page people were
notified about stays empty while the stream appears somewhere else.

So the sweep writes the returned key to the destination, and the existing
go-live path finds a key already present and uses it. Facebook flips
`SCHEDULED_UNPUBLISHED` to live when bytes arrive; nothing has to tell it to.

Two consequences worth stating rather than discovering:

- **A key written ahead of time is a key that can go stale.** If the operator
  deletes the scheduled video on Facebook, the stored key points at nothing.
  The go-live path treats a rejected pre-created key as "create a fresh one" and
  **says so** — the stream goes live, and the operator is told that the page
  people were notified about is gone.

  Failing loudly instead was considered and rejected: it would let a deleted
  Facebook post take a stream off the air, which turns an optional discovery
  feature into a single point of failure for going live. Recreating *silently*
  was rejected for the opposite reason — the operator would never learn that
  what was announced and what is live had diverged.
- **Rescheduling is a real case.** Moving a schedule changes the occurrence, so
  the marker no longer matches and the sweep would create a *second* broadcast.
  When a destination already holds a pre-created broadcast for a different
  occurrence, the sweep reschedules it — `POST /<LIVE_VIDEO_ID>?event_params=` —
  rather than creating another.

## Failure behaviour

**Never fails a schedule.** The pre-announce is best-effort by construction: it
runs ahead of the stream and the stream does not depend on it. A Graph error is
logged and retried on the next sweep, and the destination goes live on time with
a broadcast created the old way.

**The eligibility gate is not an error.** An account under 60 days old, or a
Page under 100 followers, cannot schedule. Graph refuses, and that refusal is a
fact about the account rather than a fault in the run — reported once, not
retried every 20 seconds.

## Testing

Every guard proven able to fail by a named one-line mutation.

| Case | Why it matters |
|---|---|
| A Facebook destination on a start schedule within the window gets a broadcast, asserted on the REQUEST carrying `status=SCHEDULED_UNPUBLISHED` and `event_params` | The feature. Asserted on the wire, not on a call being made |
| A second sweep for the same occurrence creates nothing | Without the marker this fires every 20 seconds |
| A NEW occurrence does create another | The marker must not be a flag; this is the case a boolean gets wrong |
| A moved schedule reschedules rather than creating a second broadcast | Otherwise every edit leaves an orphan event page |
| The pre-created key is what the encoder publishes to | The whole point — a key that does not match means an empty event page beside a live stream |
| A rejected pre-created key falls back to creating a fresh one | A cancelled event page must not break the stream |
| A non-Facebook destination on the same schedule is untouched | The rule must not widen |
| A `once` schedule beyond seven days warns and still saves | The decision this design took against the roadmap |
| A Graph failure leaves the schedule and the go-live path unaffected | Best-effort has to be provably best-effort |
| A schedule with NO explicit destinations pre-announces every Facebook one it would start | Empty means all, and it is the commonest shape; a rule that quietly skipped it would switch the feature off for most installs |
| A rejected pre-created key produces a live stream AND a warning | Both halves. Live-but-silent and warned-but-dead are each half a pass |
| The destination card links to the scheduled broadcast when one exists | Watches the RENDERED link, not the stored field. A field that exists and is never rendered is the defect this repo has shipped twice |

The last row and the fifth are the two to write first: they are the ones whose
absence would let this feature break streaming rather than merely fail to
pre-announce.

## Out

- **Distant `once` schedules getting an event page when they enter the window.**
  The sweep already checks a seven-day horizon, so a one-off 23 days out simply
  starts being eligible on day 16 — this falls out for free and needs no code.
  What is out is any attempt to pre-announce *beyond* the bound.
- **`enable_backup_ingest`, `stop_on_delete_stream`, spatial audio, 360.** Part
  F, ingest-shaped.

## The event page has to be findable

Creating a public page on an operator's behalf and giving them no way to reach
it is half a feature. The destination card carries a link to the scheduled
broadcast whenever one exists.

No new API work: the create response already returns the id, and
`FacebookLiveVideoID` already recovers it from the stored key. This is a stored
field and an anchor.

It also makes the stale-key case legible. When the link 404s, the operator can
see for themselves what the warning is talking about.
