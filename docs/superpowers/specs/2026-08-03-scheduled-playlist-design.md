# A schedule can turn the playlist on and off

**What:** two new schedule actions, `playlist.start` and `playlist.stop`, that
flip `failover.playlist.enabled` and ask for a reconcile.

**Why:** the playlist plays, sequences and has an operator control, and the only
way to put it on air is to open the settings page and toggle it by hand. A
scheduled broadcast is the case the feature was built for.

This is sub-project **C** of roadmap item 5, and the last of them. A and B
shipped as #64, #68 and #70.

## What already exists

`internal/scheduler` fires on a 20-second sweep and can start or stop
**destinations**. `Action` is `start|stop`; the `Actuator` interface is three
methods — `SetDestinationEnabled`, `ListDestinationIDs`, `Reconcile`. The
`Schedule` row carries `DestinationIDs`, where empty means all of them.

Two pieces of that machinery generalise unchanged and are not touched here: the
**skip-if-missed** rule (an occurrence past its grace window is marked handled
without acting, because the only thing worse than not starting is starting late)
and the **`MarkScheduleRun` monotonic guard** that stops a restart replaying the
morning.

The scheduler package states the invariant this design is bound by:

> The runner deliberately cannot start a process: it writes the same intent a
> human would and asks for a reconcile.

## The finding that shaped this, and shrank it

**`db.Settings` is install-wide.** `GetSettings()` takes no source id, and
`effectiveSettings()` overlays exactly one thing per source —
`settings.Ingest = src.Ingest`. `db.Source` carries no failover fields at all.

So `failover.playlist.enabled` is a **single global setting**, and every engine
in a multi-source install already reads the same playlist configuration.

The roadmap says this sub-project needs "a `source_id` column on `schedules`".
**That line was written against an assumption that does not hold**, and it is
recorded here rather than quietly dropped: there is no per-source playlist for a
`source_id` to select. An early draft of this design asked which sources a
schedule should target before checking, which is the same mistake one layer up.

**What it removes from the work:** no schema change, no `ListSourceIDs`, no
expansion path that can partially fail, no widening of the target concept.

## Scope

**In:** `ActionPlaylistStart` and `ActionPlaylistStop`; the runner acting on
them; validation; the operator control; documentation that says plainly what
"install-wide" means.

**Out:**

- **Per-source playlist settings.** Deferred deliberately — see below.
- **`source.select` / scheduling a selector pin.** The pin is in-memory only
  (`e.sel.pinned`), so there is no stored intent to flip and no way to schedule
  it without breaking the package's invariant. A schedule that called
  `SwitchSource` directly would also leave a pin that vanishes on restart, now
  invisibly.
- **Scheduling anything else in settings.** The door this opens is wide; one
  action pair with a reason is not the same as a general settings scheduler.

## The action

```go
// ActionPlaylistStart enables the failover playlist.
ActionPlaylistStart Action = "playlist.start"
// ActionPlaylistStop disables it.
ActionPlaylistStop Action = "playlist.stop"
```

The runner reads the settings, flips `Failover.Playlist.Enabled`, writes them
back, and lets the existing end-of-sweep `Reconcile()` do the rest — the same
path an operator's save takes. **No new engine call, and no direct spawn.**

**It must set the sweep's `changed` flag.** `Tick` only calls `Reconcile()` when
`changed` is true, and today the only thing that sets it is a successful
destination flip. A playlist flip that forgets it writes the setting and reconciles
nothing, so the playlist starts whenever some unrelated event next triggers a
reconcile — the exact silent-until-something-else-happens failure the readiness
gate was rewritten to avoid in sub-project B1.

`Schedule.Enables()` currently answers `Action == ActionStart` and drives the
destination flip. It must not silently answer `false` for `playlist.stop` and be
read as "disable destinations" — the actions have to branch before that point,
not share a boolean.

**Destination targeting does not apply.** A playlist schedule that also names
destinations is a schedule whose author expected something the product will not
do, so it is refused at validation rather than half-honoured.

## What "install-wide" means, said plainly

A playlist schedule turns the playlist on **for the whole install**, not for one
programme. On a single-source install — which is every acceptance suite in this
repo, and the shape almost every deployment has — that distinction does not
exist. On a two-programme install it is all-or-nothing.

This is a real limitation and it belongs in `docs/SCHEDULED-BROADCAST.md` next
to the feature, not only in this spec. An operator who reads "start the playlist
at 20:00" and has two programmes must not discover the scope by watching the
wrong one go to filler.

## What it does NOT do, and why that is not a bug

The playlist ranks **below both ingests**. Enabling it puts filler on air when
nothing better is delivering; it does nothing visible while an encoder is live,
because a live encoder pre-empts the playlist immediately and deliberately.

So `playlist.start` at 20:00 means *"filler from 20:00 if nothing is live"*, not
*"filler at 20:00 regardless"*. The second needs a pin, and the pin is not
schedulable — see Out of scope. **The documentation must say which of the two
this is**, because an operator expecting the second and getting the first
discovers it during a broadcast.

## Failure behaviour

**A failed settings write leaves the occurrence unhandled**, so the next sweep
retries it while it is still inside its grace window — matching what the runner
already does when it cannot list destinations, and differing from the
destination-flip path, which marks handled even on partial failure because
re-applying would re-flip the ones that worked. There is no partial failure
here: one settings write either lands or does not.

**A schedule that fires while the playlist is already in the target state is a
no-op**, not an error. Two overlapping schedules, or a restart mid-window, must
not produce a spurious respawn — and they will not, because `reconcilePlaylist`
compares signatures and an unchanged signature does nothing.

**A playlist that is enabled but not ready still does not go on air.** The
readiness gate is unchanged and remains the only thing that decides. A schedule
turning the playlist on while an item is still transcoding results in the slate,
and the readiness endpoint says which item is the reason.

## The operator control

`ui/src/pages/AutomationPage.tsx` already has a schedule editor with an action
dropdown, typed **locally** as `type ScheduleAction = "start" | "stop"`.

**Nothing automated will catch a new action that never reaches that dropdown.**
The UI-nameability drift guard walks `db.Settings` and `db.Destination`;
schedules are neither, so `internal/scheduler`'s `Action` type has no guard at
all. B2 could lean on a failing test as its specification; this cannot.

That asymmetry is worth fixing rather than remembering: a guard over
`scheduler.Action` in the same shape as the existing ones would make the next
action value fail the build until it is reachable. It is a small test and it
closes the class.

## Testing

| Case | Why it matters |
|---|---|
| `playlist.start` fires and the playlist is enabled in stored settings | The whole feature, asserted on what was STORED rather than on a call being made |
| It reconciles afterwards | Without it the flip sits in the database until something else happens to reconcile |
| `playlist.stop` disables it, and does NOT disable any destination | `Enables()` returns false for it; a shared boolean would silently reach the destination path |
| A missed playlist occurrence is skipped and marked handled | The skip rule generalising is a claim; claims get tested |
| A playlist schedule naming destinations is refused at validation | Half-honouring it is worse than refusing |
| Firing when already enabled changes nothing and respawns nothing | Overlapping schedules and restarts must not cost a gap |
| A failed settings write leaves the occurrence unhandled | So the next sweep retries inside the grace window |
| The UI dropdown offers both new actions | The only thing standing between this and an unreachable feature |

Every guard must be shown to fail against a named one-line mutation. Across item
5's three sub-projects, eleven tests that could not fail were found and fixed,
and five of those had their mutation named by a plan rather than an implementer.

## What could go wrong

**This opens a door.** Once a schedule can flip one settings field, every field
is a candidate, and the scheduler becomes a general settings automation surface
by accretion rather than by decision. The playlist earns it because it is the
thing scheduled broadcasts are made of; the next request should have to argue
for itself.

**"Start" now means two things.** `ActionStart` starts destinations;
`ActionPlaylistStart` starts the playlist. The names are distinguishable and the
UI shows both, but a run log skimmed quickly could mislead. The log line should
name what was acted on, not only the action.

**Per-source playlist is deferred, not rejected.** Playlist settings are
install-wide, so a scheduled enable is all-or-nothing. Moving the playlist block
onto `db.Source` — mirroring `Source.Ingest`, whose comment already records the
pattern — would make it per-programme and make a `source_id` on schedules mean
something. It was deferred because for every install that exists today the two
are indistinguishable, because it is a migration on the air path bought for a
capability nobody has asked for, and because the migration is a copy that costs
no more later than now. It should be built when someone runs two programmes and
wants different filler on each, and shaped by that use case rather than guessed.
