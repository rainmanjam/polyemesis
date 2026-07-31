# Poka-Yoke review

A mistake-proofing walkthrough of polyemesis, code and UI, done 2026-07-28.

> **Status, re-checked 2026-07-30.** All three of the "what I would do first"
> findings have since been built. The review is left as written, because the
> reasoning is the reusable part — but read findings 1–3 as history rather than
> as a to-do list.
>
> | Finding | Now |
> |---|---|
> | 1 — Settings can drop a live broadcast silently | **Done.** `SettingsPage.tsx` imports `useIngestLive` from `useLiveData`, and the port fields commit on an explicit save rather than on blur |
> | 2 — `window.confirm` and inconsistent guards | **Done.** `components/ConfirmDestructive.tsx` exists and is used across nine call sites. The only remaining `window.confirm` in the tree is inside that file's own comment, describing what it replaced |
> | 3 — Numeric inputs do not constrain at the widget | **Done, by a better mechanism than recommended.** Bounds live in `ui/src/lib/limits.ts`; 50 of 55 `type="number"` inputs carry `min` and 41 carry `max`. Rather than serving them from the settings-meta endpoint at runtime, `internal/db/limits_drift_test.go` fails the build when the TypeScript and the Go validator disagree — the drift is caught at test time instead of being avoided by a network round trip |
>
> Findings 4–8 have not been re-verified.

Shingo's distinction is the whole of this document: a **control** makes the
error impossible, a **warning** tells you about it. Warnings are what you build
when a control is genuinely unavailable — they are not a cheaper substitute for
one. The second idea that matters here is **source inspection**: catch the
defect where it is created, not downstream where it is expensive. In this
product "downstream" means *live, in front of an audience*, so the distance
between a config-time refusal and a stream-time failure is the whole game.

The findings are ordered by consequence, not by effort.

## The product already has a reference implementation

Expert mode (`internal/api/expert.go`) is the best-mistake-proofed surface here,
and it is the standard the rest of this document measures against. It does all
three things properly:

- **Hard refusal for the impossible.** `-i` in the input args is an error, not
  a warning, because a second input renumbers every stream the routing graph
  refers to. Nothing acknowledges it away.
- **Acknowledgement for the guarantee-breaking.** `-c:v` anything-but-`copy`
  raises a guard naming the promise being broken, and the acknowledgement is
  *stored on the row* rather than treated as a one-shot click — so a later edit
  that keeps the override does not lose the record of who agreed.
- **Dry run before commit.** The arguments are put to FFmpeg and its rejection
  is surfaced before anything goes live.

Everywhere below that falls short, the fix is "do what expert mode already
does".

---

## 1. Settings can drop a live broadcast with no warning at all

**Severity: highest. This is the one that costs someone an audience.**

`ui/src/pages/SettingsPage.tsx` does not import `useLiveData`. It has no idea
whether a stream is running. Changing the SRT port, passphrase or latency mid-
broadcast reconciles the ingest, restarts the FFmpeg child, and drops every
viewer — and the UI gives no indication that this is what is about to happen.

The same applies to the Sources page I added: its port fields commit on blur,
each commit restarts that source's ingest, and the only acknowledgement of this
is a code comment.

**Poka-Yoke:** this is a *warning* problem being solved by nothing at all. It
does not need a control — an operator sometimes genuinely must change the port
mid-stream — it needs the consequence stated at the moment of decision.

- Read live state on both pages. When a source is publishing, the save button
  for anything in its ingest block reads **"Save and drop the live stream"**,
  not "Save".
- Group ingest fields behind an explicit "editing live ingest" state, so
  restarting is a deliberate act rather than a side effect of tabbing out of a
  field.
- The port fields should commit on an explicit save, not on blur. Blur-commit
  is fine for a name; for a value that tears down a broadcast it turns an
  accidental tab-press into an outage.

## 2. Destructive actions are guarded inconsistently, and mostly by `window.confirm`

Nine delete paths in the UI. Seven have no destructive-variant button. The
guards that do exist are `window.confirm`:

| Action | Guard today | Irreversible? |
|---|---|---|
| Delete recording | `window.confirm` | **yes — the file is gone** |
| Delete platform credentials | `window.confirm` | yes (must re-create the OAuth app) |
| Disconnect account | `window.confirm` | recoverable (re-auth) |
| Delete job | **none** | recoverable |
| Delete source | Dialog | **yes — cascades to destinations and renditions** |
| Delete rendition | Dialog | recoverable (destinations fall to passthrough) |
| Delete clip / transcript / session | varies | **clip is often the only artifact** |

Three problems, in Poka-Yoke terms:

1. **`window.confirm` is a weak control.** It is one keystroke from OK, it is
   suppressible by the browser ("prevent this page from creating additional
   dialogs"), and it looks identical for "delete a job" and "delete a
   recording forever". Friction should be proportional to consequence, and
   here it is constant.
2. **Inconsistency is itself the defect.** An operator learns "deletes ask
   first", then meets the job delete that does not, and the learned caution is
   now wrong.
3. **No fixed-value check.** Deleting a source cascades. The dialog says so in
   prose; it does not say *how many* destinations and renditions are about to
   go with it.

**Poka-Yoke:**

- One `<ConfirmDestructive>` component, used everywhere. No `window.confirm`.
- Consequence-proportional friction: recoverable actions get a click;
  **irreversible ones require typing the name**. Typing "Vertical" to delete
  the Vertical source is the contact method — the shape of the input only fits
  the intended target.
- Show the blast radius as a *count*, fetched before the dialog opens: "this
  deletes 3 destinations and 1 rendition". That is Shingo's fixed-value method:
  the operator confirms a number, not a vibe.

## 3. Numeric inputs do not constrain at the widget

33 `type="number"` inputs; 25 have `min`, 20 have `max`. So 8 accept anything
downward and 13 accept anything upward — including the SRT and RTMP port fields
I wrote, which have neither.

The server validates all of this properly (`IngestSettings.problems()` and
`Rendition.Validate` are thorough and report every problem at once). But that is
**detection**, and it happens a round trip later.

**Poka-Yoke:** the widget should not accept 70000 into a port field at all.
`min`/`max`/`step` on every numeric input, derived from the same constants the
Go validator uses rather than typed twice — otherwise the two drift and the
form starts accepting what the server refuses. Export the bounds through the
existing settings-meta endpoint and have the inputs read them.

## 4. Nullable foreign keys encode states that should be unreachable

`Destination.SourceID *int64`, `Rendition.SourceID *int64`. `nil` is documented
as "the source was deleted, which CASCADE makes unreachable in practice".

That comment is doing the work a type should do. The state is representable, so
every reader has to handle it, and the handling is invisible: a destination with
a `nil` source is created successfully, listed successfully, and never started
by any reconciler — which presents to the operator as a destination that simply
does nothing.

**Poka-Yoke — make illegal states unrepresentable:**

- The store already defaults `SourceID` on create. Go further: have the *scan*
  refuse a `nil` source_id rather than propagating it, so a row that should not
  exist fails loudly at the boundary instead of flowing inward.
- Longer term, the honest fix is `NOT NULL` on the column. SQLite would not
  accept that via `ALTER TABLE ADD COLUMN` while foreign keys are on — which is
  why it is nullable — but a table rebuild during a future migration could
  correct it, and the nullability is then a migration artefact rather than a
  permanent part of the model.

## 5. A silent stream is caught late

`no track is enabled with non-zero gain` is a validation error, so a profile
that would produce silence is refused. Good — that is source inspection.

But the *neighbouring* failure is not covered: a destination whose enabled
tracks are all excluded by `ExcludeRoles`, or whose selected tracks do not exist
on the current ingest, compiles to a valid graph that carries nothing. The
routing page shows an excluded track as "not sent", which is a warning at the
right moment — but nothing stops saving a destination that, against the ingest
currently arriving, is silent.

**Poka-Yoke:** at save time, compile the profile against the *probed* ingest and
refuse — or at minimum require an acknowledgement, the expert-mode pattern —
when the result carries no audio. Streaming silence to a platform is exactly
the defect this product exists to prevent, and it is the one an operator cannot
hear from the dashboard.

## 6. `window.confirm`'s cousin: errors swallowed into nothing

Eight `.catch(() => {})` sites across `useLiveData`, `Dashboard`,
`MonitoringPage`. Polling failures are deliberately silent, which is right — a
transient poll failure should not raise a toast every two seconds.

But the same swallow is used for one-shot loads, where a failure means the panel
is showing stale or empty data with no indication that it is not current.

**Poka-Yoke:** distinguish the two. A *poll* that fails may be silent, but the
component should show a stale-data marker after N consecutive failures. A
*load* that fails must say so. The rule: silence is acceptable when the next
attempt is seconds away, never when there is no next attempt.

## 7. A 200 that changed nothing

Fixed during this session, recorded because it is the archetype: `PUT /settings`
reconciled the default *engine* rather than the *manager*, so enabling one-port
ingest returned `200 OK` and bound no listener.

**Poka-Yoke:** a success response should be evidence that the effect happened,
not that the write happened. Where a handler's job is "change the running
system", it should confirm the system changed — the sources API now returns
`running` and `tokenEnforced` read back from the live manager rather than from
the setting, which is the right shape. Apply it wherever a mutation implies a
runtime effect.

## 8. Smaller contact-method wins

- **Stream keys and tokens are `type="text"`.** They are pasted in front of
  whoever is watching a screen share. `type="password"` with a reveal toggle
  costs nothing.
- **The rotate-token button sits next to copy with identical weight.** One is
  idempotent, the other invalidates a credential. Different weight, or a
  confirm.
- **`enabled` defaulted to false on source create** — fixed this session, worth
  keeping as a rule: a thing the operator just created should be in the state
  they created it *for*.
- **Destination URL fields accept any scheme** and validate server-side.
  A `<select>` for the scheme plus a host field cannot produce `htp://`.

---

## What I would do first

1. **Finding 1** — live-stream awareness on Settings and Sources. Highest
   consequence, and it is a day's work.
2. **Finding 2** — one confirm component, typed confirmation for irreversible
   actions, blast-radius counts.
3. **Finding 3** — bounds on every numeric input, read from the server's own
   constants.

4–8 are worth doing and none of them will cost anyone a broadcast.

## What I deliberately did not recommend

**More confirmations everywhere.** Poka-Yoke is not "add a dialog". A
confirmation an operator clicks through fifty times a day has been trained into
a reflex and stops being a control at all — it becomes a warning that also
wastes time. Every recommendation above either removes the possibility of the
error (3, 4, 8) or attaches friction *specifically* to the irreversible cases
(1, 2, 5). The recoverable ones should get faster, not slower.
