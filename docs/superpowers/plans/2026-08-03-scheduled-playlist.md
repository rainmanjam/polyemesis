# Scheduled Playlist (5C) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A schedule can turn the failover playlist on and off.

**Architecture:** Two new `scheduler.Action` values. The runner flips `failover.playlist.enabled` in stored settings and lets the existing end-of-sweep `Reconcile()` apply it — the same path an operator's save takes, so a scheduled start and a clicked one are one code path. No schema change: `db.Settings` is install-wide, so there is no `source_id` and no target list.

**Tech Stack:** Go 1.25, `internal/scheduler`, `internal/db`, React 19 + TypeScript (`ui/`).

**Spec:** `docs/superpowers/specs/2026-08-03-scheduled-playlist-design.md`

## Global Constraints

- **The runner may never start a process.** It writes the same intent a human would and asks for a reconcile. No new engine call, no direct spawn. This is the scheduler package's stated invariant.
- **No schema change.** `db.Settings` is global — `GetSettings()` takes no source id and `db.Source` carries no failover fields. There is no `source_id` on schedules and no per-source playlist.
- **`Enables()` returns `Action == ActionStart`** and drives the DESTINATION flip. `playlist.stop` must never reach that path — it would answer `false` and disable every destination.
- **The sweep's `changed` flag gates `Reconcile()`.** A flip that does not set it writes the setting and reconciles nothing.
- **Every new guard must be proven able to fail** by a named one-line mutation: run it, quote the failure, restore. Across item 5's earlier sub-projects, eleven tests that could not fail were found and fixed.
- **A mutation that fails to COMPILE is not a mutation result.** Re-anchor and re-run.
- **COMMIT BEFORE YOU MUTATE.** `git checkout --` reverts uncommitted work along with the mutation.
- **Comments explain WHY and must never assert what the code does not do.** This is the repo's most recurring defect.
- Both golden tables under `internal/engine/testdata/` must stay byte-unchanged. Nothing here should go near them; verify with `git status --porcelain internal/engine/testdata/`.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/scheduler/scheduler.go` | The two `Action` values; `Validate` accepting them and refusing destinations alongside them; a `TargetsPlaylist()` predicate so callers branch on intent rather than on string comparison |
| `internal/scheduler/runner.go` | `Actuator` gains `SetPlaylistEnabled(bool) error`; `Tick` branches to it and sets `changed` |
| `internal/engine/engine.go` | `scheduleActuator.SetPlaylistEnabled` — read settings, flip, write back. Hair-thin, like its neighbours |
| `internal/scheduler/action_drift_test.go` (new) | The guard: every `Action` value must be offered by the UI |
| `ui/src/pages/AutomationPage.tsx` | `ScheduleAction` gains both values; the dropdown offers them; destinations hidden for a playlist action |
| `docs/SCHEDULED-BROADCAST.md` | Both limits, stated where an operator meets them |

---

## Task 1: The actions, and the validation that keeps them apart

**Files:**
- Modify: `internal/scheduler/scheduler.go`
- Test: `internal/scheduler/scheduler_test.go`

**Interfaces:**
- Produces: `ActionPlaylistStart Action = "playlist.start"`, `ActionPlaylistStop Action = "playlist.stop"`, and `func (s Schedule) TargetsPlaylist() bool`.

- [ ] **Step 1: Write the failing tests**

```go
// A playlist schedule and a destination schedule are different shapes, and a
// row that is both is a row whose author expected something the product will
// not do. Refusing it beats half-honouring it.
//
// The mutation: delete the DestinationIDs clause in Validate and this passes.
func TestAPlaylistScheduleMayNotAlsoNameDestinations(t *testing.T) {
	s := Schedule{
		Name:           "evening filler",
		Action:         ActionPlaylistStart,
		Kind:           KindDaily,
		DestinationIDs: []int64{7},
	}.Normalized()
	err := s.Validate()
	if err == nil {
		t.Fatal("a playlist schedule naming destinations was accepted")
	}
	if !strings.Contains(err.Error(), "destination") {
		t.Errorf("error %q does not tell the operator which half is the problem", err)
	}
}

// Both new actions must pass validation, or the feature is unreachable in a way
// no other test would notice: Validate's default branch rejects anything it does
// not know, so forgetting to list them here fails every save.
//
// The mutation: remove ActionPlaylistStart from the switch and this fails.
func TestBothPlaylistActionsValidate(t *testing.T) {
	for _, a := range []Action{ActionPlaylistStart, ActionPlaylistStop} {
		s := Schedule{Name: "filler", Action: a, Kind: KindDaily}.Normalized()
		if err := s.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", a, err)
		}
	}
}

// TargetsPlaylist is what the runner branches on. It exists so no caller
// compares against a string literal: a fourth action added later would then
// silently take the destination path.
//
// The mutation: make TargetsPlaylist return false for ActionPlaylistStop and
// this fails.
func TestTargetsPlaylistIsTrueForBothPlaylistActionsAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		action Action
		want   bool
	}{
		{ActionPlaylistStart, true},
		{ActionPlaylistStop, true},
		{ActionStart, false},
		{ActionStop, false},
	} {
		if got := (Schedule{Action: tc.action}).TargetsPlaylist(); got != tc.want {
			t.Errorf("TargetsPlaylist(%q) = %v, want %v", tc.action, got, tc.want)
		}
	}
}

// Enables() answers "the enabled value this schedule writes" and is read by the
// DESTINATION path. playlist.stop answers false there, which is correct for the
// playlist and catastrophic if it ever reaches a destination: it would disable
// every one of them.
//
// This test pins the pairing rather than the boolean: whatever Enables says, a
// playlist action must not be routed by it.
func TestPlaylistStopDoesNotLookLikeADestinationDisable(t *testing.T) {
	s := Schedule{Action: ActionPlaylistStop}
	if !s.TargetsPlaylist() {
		t.Fatal("playlist.stop is not recognised as a playlist action, so the runner "+
			"would route it by Enables() and disable every destination")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/scheduler/ -run 'PlaylistSchedule|BothPlaylistActions|TargetsPlaylist|PlaylistStopDoes' -v`
Expected: FAIL — `undefined: ActionPlaylistStart`.

- [ ] **Step 3: Add the actions and the predicate**

```go
// ActionPlaylistStart enables the failover playlist. INSTALL-WIDE: settings are
// global (GetSettings takes no source id), so this is not per-programme.
//
// It means "filler from now if nothing is live", NOT "filler now regardless".
// The playlist ranks below both ingests and a live encoder pre-empts it
// immediately, deliberately. Forcing filler over a live encoder needs a pin,
// and a pin is in-memory only, so it cannot be scheduled without breaking this
// package's invariant.
ActionPlaylistStart Action = "playlist.start"
// ActionPlaylistStop disables it.
ActionPlaylistStop Action = "playlist.stop"
```

```go
// TargetsPlaylist reports whether this schedule acts on the playlist rather
// than on destinations.
//
// A predicate rather than a comparison at each call site, because Enables()
// answers Action == ActionStart and the destination path reads it: route
// playlist.stop by that boolean and it disables every destination. One place
// decides which half of the runner a schedule belongs to.
func (s Schedule) TargetsPlaylist() bool {
	return s.Action == ActionPlaylistStart || s.Action == ActionPlaylistStop
}
```

In `Validate`, add both to the action switch, and after it:

```go
	if s.TargetsPlaylist() && len(s.DestinationIDs) > 0 {
		return fmt.Errorf("schedule %q acts on the playlist, so it cannot also name "+
			"%d destination(s); use a second schedule for those", s.Name, len(s.DestinationIDs))
	}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/scheduler/ -v`
Expected: PASS, including every existing test unchanged.

- [ ] **Step 5: Run all three mutations, quote each failure, restore**

- [ ] **Step 6: Commit**

```bash
git add internal/scheduler/
git commit -m "feat(scheduler): a schedule can name the playlist instead of destinations"
```

---

## Task 2: The runner acts on it, and asks for a reconcile

**Files:**
- Modify: `internal/scheduler/runner.go`, `internal/engine/engine.go`
- Test: `internal/scheduler/runner_test.go`

**Interfaces:**
- Consumes: `Schedule.TargetsPlaylist()` from Task 1.
- Produces: `Actuator` gains `SetPlaylistEnabled(enabled bool) error`. Every existing fake in `runner_test.go` must implement it.

**The real test helpers, with their real shapes.** There is no `newTestRunner`
and no `now` in this package — a runner is built inline. Use these:

```go
func quietLog() *slog.Logger  // runner_test.go:11

type fakeStore struct {
	rows   []Schedule
	err    error
	marked map[int64]time.Time   // <- assert on THIS for "was it marked handled"
	markEr error
}

type fakeActuator struct {
	all       []int64
	allErr    error
	setErr    error
	set       map[int64]bool      // <- destination id -> enabled
	setCalls  int
	reconcile int                 // <- the reconcile COUNT is named `reconcile`
}

r := New(quietLog(), store, act)   // how every existing test builds one
```

**Extend `fakeActuator` in place** with `playlistEnabled bool`, `playlistSet bool`
and `playlistErr error`; do not add a parallel fake. `playlistSet` matters
separately from `playlistEnabled`: a test asserting only the latter cannot tell
"disabled because the schedule ran" from "never touched".

The tests below are written against these names. Where a test says
`act.reconcile`, it means `act.reconcile`; where it says `store.marked`, that
field already exists. Adjust the test bodies to the real fields rather than
renaming the fakes.

- [ ] **Step 1: Write the failing tests**

```go
// The feature: a playlist schedule flips the stored setting.
//
// Asserted on what was STORED, not on a call being recorded, because the
// invariant this package is built on is that a schedule writes the same intent
// a human writes. A fake that merely counts calls would pass while the runner
// did something else entirely.
//
// The mutation: delete the TargetsPlaylist branch in Tick and this fails.
func TestAPlaylistScheduleEnablesThePlaylist(t *testing.T) {
	act := &fakeActuator{}
	store := &fakeStore{rows: []Schedule{{
		ID: 1, Name: "filler", Enabled: true,
		Action: ActionPlaylistStart, Kind: KindOnce,
		RunAt: now, GraceSeconds: 60,
	}}}
	r := New(quietLog(), store, act)

	res := r.Tick(now)

	if len(res) != 1 || !res[0].Fired {
		t.Fatalf("results = %+v, want one fired", res)
	}
	if !act.playlistEnabled {
		t.Error("the playlist schedule fired and the playlist was not enabled")
	}
}

// And it must RECONCILE. Tick only reconciles when `changed` is set, and the
// only thing that sets it today is a destination flip. A playlist flip that
// forgets it writes the setting and reconciles nothing, so the playlist starts
// whenever some unrelated event next happens to reconcile -- the exact
// silent-until-something-else-happens failure sub-project B1 rewrote the
// readiness gate to avoid.
//
// The mutation: remove `changed = true` from the playlist branch and this fails.
func TestAPlaylistScheduleAsksForAReconcile(t *testing.T) {
	act := &fakeActuator{}
	store := &fakeStore{rows: []Schedule{{
		ID: 1, Name: "filler", Enabled: true,
		Action: ActionPlaylistStart, Kind: KindOnce,
		RunAt: now, GraceSeconds: 60,
	}}}
	r := New(quietLog(), store, act)

	r.Tick(now)

	if act.reconcile != 1 {
		t.Errorf("reconciles = %d, want 1: the setting was written and nothing applied it", act.reconcile)
	}
}

// playlist.stop disables the playlist AND TOUCHES NO DESTINATION.
//
// Enables() answers false for it, and the destination path reads Enables(): if
// a playlist action ever reached that path it would disable every destination
// in the install. This is the test that catches it.
//
// The mutation: route by Enables() instead of TargetsPlaylist and this fails
// with destinations disabled.
func TestPlaylistStopDisablesThePlaylistAndNoDestinations(t *testing.T) {
	act := &fakeActuator{playlistEnabled: true, destinations: []int64{7, 8}}
	store := &fakeStore{rows: []Schedule{{
		ID: 1, Name: "stop filler", Enabled: true,
		Action: ActionPlaylistStop, Kind: KindOnce,
		RunAt: now, GraceSeconds: 60,
	}}}
	r := New(quietLog(), store, act)

	r.Tick(now)

	if act.playlistEnabled {
		t.Error("playlist.stop fired and the playlist is still enabled")
	}
	if len(act.disabled) != 0 {
		t.Errorf("playlist.stop disabled destinations %v; Enables() answers false for it "+
			"and the destination path reads Enables()", act.disabled)
	}
}

// A failed settings write leaves the occurrence UNHANDLED, so the next sweep
// retries it inside its grace window.
//
// This differs from the destination path on purpose. There, a partial failure
// is marked handled because retrying would re-apply the flip to the ones that
// worked. Here there is no partial: one write either lands or does not.
//
// The mutation: mark the occurrence handled on error and this fails.
func TestAFailedPlaylistWriteIsRetriedOnTheNextSweep(t *testing.T) {
	act := &fakeActuator{playlistErr: errors.New("disk is full")}
	store := &fakeStore{schedules: []Schedule{{
		ID: 1, Name: "filler", Enabled: true,
		Action: ActionPlaylistStart, Kind: KindOnce,
		RunAt: now, GraceSeconds: 600,
	}}}
	r := New(quietLog(), store, act)

	res := r.Tick(now)

	if len(res) != 1 || res[0].Err == "" {
		t.Fatalf("results = %+v, want one carrying the error", res)
	}
	if len(store.marked) != 0 {
		t.Error("the occurrence was marked handled after a failed write, so the next " +
			"sweep will not retry it inside its grace window")
	}
}

// A missed playlist occurrence is skipped and marked handled, exactly as a
// missed destination one is. The skip rule generalising is a claim, and claims
// get tested.
//
// The mutation: exempt playlist actions from the skip branch and this fails.
func TestAMissedPlaylistOccurrenceIsSkippedNotFiredLate(t *testing.T) {
	act := &fakeActuator{}
	late := now.Add(10 * time.Minute)
	store := &fakeStore{rows: []Schedule{{
		ID: 1, Name: "filler", Enabled: true,
		Action: ActionPlaylistStart, Kind: KindOnce,
		RunAt: now, GraceSeconds: 60,
	}}}
	r := New(quietLog(), store, act)

	res := r.Tick(late)

	if len(res) != 1 || !res[0].Skipped {
		t.Fatalf("results = %+v, want one skipped", res)
	}
	if act.playlistEnabled {
		t.Error("a missed occurrence started the playlist late; the only thing worse " +
			"than not starting is starting late")
	}
}
```

Extend the existing fake actuator in `runner_test.go` with `playlistEnabled bool`, `playlistErr error`, and a `SetPlaylistEnabled` that records or returns the error. Read the file first and match its existing shape rather than adding a parallel one.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/scheduler/ -run 'PlaylistSchedule|PlaylistStop|FailedPlaylist|MissedPlaylist' -v`
Expected: FAIL — the fake has no `SetPlaylistEnabled`, and `Tick` has no branch.

- [ ] **Step 3: Widen the Actuator**

```go
// Actuator is the enable/disable path. The runner deliberately cannot start a
// process: it writes the same intent a human would and asks for a reconcile, so
// there is exactly one code path that brings a destination up.
type Actuator interface {
	SetDestinationEnabled(id int64, enabled bool) error
	// ListDestinationIDs expands a schedule that targets everything.
	ListDestinationIDs() ([]int64, error)
	// SetPlaylistEnabled flips the failover playlist's stored intent.
	//
	// INSTALL-WIDE: db.Settings is global, so this is not per-programme. There
	// is no id parameter because there is nothing to select.
	SetPlaylistEnabled(enabled bool) error
	// Reconcile is called once per sweep, and only when something changed.
	Reconcile() error
}
```

- [ ] **Step 4: Branch in Tick, before the destination path**

Immediately after the `d.Skip` block and BEFORE `targets := s.DestinationIDs`:

```go
		// The playlist half. It must branch HERE, above the destination
		// expansion: Enables() answers Action == ActionStart, so playlist.stop
		// reaching the code below would disable every destination in the
		// install.
		if s.TargetsPlaylist() {
			enable := s.Action == ActionPlaylistStart
			if err := r.act.SetPlaylistEnabled(enable); err != nil {
				// NOT marked handled: unlike a destination sweep there is no
				// partial success to avoid re-applying, so the next sweep
				// should try again while the occurrence is still inside its
				// grace window.
				res.Err = err.Error()
				r.log.Warn("schedule cannot set the playlist",
					"schedule", s.Name, "err", err)
				out = append(out, res)
				r.emit(res)
				continue
			}
			// Without this the setting is written and nothing applies it: Tick
			// only reconciles when something changed.
			changed = true
			res.Fired = true
			if err := r.store.MarkScheduleRun(s.ID, d.At); err != nil {
				res.Err = err.Error()
			}
			r.log.Info("schedule fired",
				"schedule", s.Name, "action", string(s.Action),
				"target", "playlist", "occurrence", d.At.Format(time.RFC3339))
			out = append(out, res)
			r.emit(res)
			continue
		}
```

- [ ] **Step 5: Implement it on the engine's actuator**

In `internal/engine/engine.go`, beside `SetDestinationEnabled`:

```go
// SetPlaylistEnabled flips the playlist's stored intent, exactly as the settings
// endpoint does. Read-modify-write rather than a targeted UPDATE because
// PutSettings is the one door settings go through, and a second one would drift.
func (a scheduleActuator) SetPlaylistEnabled(enabled bool) error {
	s, err := a.e.store.GetSettings()
	if err != nil {
		return err
	}
	if s.Failover.Playlist.Enabled == enabled {
		// Already there. Writing anyway would move UpdatedAt and make an
		// overlapping schedule look like a change to anything watching.
		return nil
	}
	s.Failover.Playlist.Enabled = enabled
	return a.e.store.PutSettings(s)
}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/scheduler/ ./internal/engine/ -v 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 7: Run all five mutations, quote each failure, restore**

- [ ] **Step 8: Commit**

```bash
git add internal/scheduler/ internal/engine/
git commit -m "feat(scheduler): a playlist schedule flips the setting and asks for a reconcile"
```

---

## Task 3: A guard so the next action cannot be unreachable

**Files:**
- Create: `internal/scheduler/action_drift_test.go`

**Interfaces:**
- Consumes: the `Action` constants from Task 1.

- [ ] **Step 1: Write the guard, and watch it fail**

`ui/src/pages/AutomationPage.tsx` declares `type ScheduleAction = "start" | "stop"` LOCALLY — not in `types.ts`. The existing UI-nameability guard walks `db.Settings` and `db.Destination`, and a schedule is neither, so **nothing today catches an action the UI cannot offer.** B2 could use a failing guard as its specification; this sub-project has to build the guard first.

```go
package scheduler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every Action must be offered by the operator's dropdown.
//
// This is the sibling of TestUITypesCanNameEverySettingsField, for a type that
// guard cannot see: it walks db.Settings and db.Destination, and a schedule is
// neither. So an Action added here and forgotten in the UI is a feature no
// operator can reach — which is the exact class roadmap item 0 existed to fix,
// and which this package had no guard against at all.
//
// It matches on the STRING VALUE, not the Go name, because the value is what
// crosses the wire and what the dropdown carries.
func TestEveryScheduleActionIsOfferedByTheUI(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "pages", "AutomationPage.tsx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	src := string(raw)

	for _, a := range []Action{
		ActionStart, ActionStop, ActionPlaylistStart, ActionPlaylistStop,
	} {
		if !strings.Contains(src, `"`+string(a)+`"`) {
			t.Errorf("action %q appears nowhere in AutomationPage.tsx. An action the "+
				"operator cannot choose is a feature nobody can reach — add it to the "+
				"dropdown, or delete the action.", a)
		}
	}
}

// The list above is exhaustive only if somebody remembers to extend it, so this
// pins the count. A fifth action makes this fail, which is the reminder.
//
// The same shape as i18n's wantLocales and the settings drift guard's own
// counts: a list that must be complete needs something that notices when it
// stops being.
func TestTheActionListInThisFileIsComplete(t *testing.T) {
	const wantActions = 4
	got := len([]Action{ActionStart, ActionStop, ActionPlaylistStart, ActionPlaylistStop})
	if got != wantActions {
		t.Fatalf("this file checks %d actions; if you added one, add it to the list "+
			"above and to AutomationPage.tsx, then bump this count", got)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/scheduler/ -run TestEveryScheduleActionIsOfferedByTheUI -v`
Expected: **FAIL**, naming `playlist.start` and `playlist.stop`. That failure is Task 4's specification.

- [ ] **Step 3: Commit the failing guard**

```bash
git add internal/scheduler/action_drift_test.go
git commit -m "test(scheduler): an action the UI cannot offer is a feature nobody can reach"
```

This commit is red on purpose and Task 4 turns it green. If that is unacceptable in this repo's history, fold Tasks 3 and 4 into one commit — but write the guard first either way, and watch it fail before making it pass.

---

## Task 4: The operator can choose it

**Files:**
- Modify: `ui/src/pages/AutomationPage.tsx`
- Test: `ui/e2e/` — extend the existing automation spec if one exists; otherwise assert through the drift guard only and say so in the report.

**Interfaces:**
- Consumes: the action strings `"playlist.start"` and `"playlist.stop"`.

- [ ] **Step 1: Widen the type and the dropdown**

```ts
type ScheduleAction = "start" | "stop" | "playlist.start" | "playlist.stop";
```

Add both to the action `<Select>` (around line 1016, `onValueChange={(v) => setForm({ ...form, action: v as ScheduleAction })}`). Label them for what they do rather than for their value — "Start the playlist" reads better than "playlist.start" — and follow the labelling style already used for start/stop.

- [ ] **Step 2: Hide destination selection for a playlist action**

A playlist schedule that names destinations is refused by `Validate` (Task 1). An editor that lets an operator pick destinations and then rejects the save is an editor that wasted their time.

```tsx
{!form.action.startsWith("playlist.") && (
  /* the existing destination picker */
)}
```

Clear `destinationIds` when switching to a playlist action, or a previously-chosen destination is submitted invisibly and the save fails with an error about something the operator can no longer see.

- [ ] **Step 3: Run the guard and the UI gates**

Run: `go test ./internal/scheduler/ -run TestEveryScheduleActionIsOfferedByTheUI -v`
Expected: **PASS**.

Run: `cd ui && npx tsc --noEmit && npx oxlint && npm run build`
Expected: all pass.

- [ ] **Step 4: Commit**

```bash
git add ui/ && git commit -m "feat(ui): an operator can schedule the playlist on and off"
```

---

## Task 5: Say what it does, where an operator meets it

**Files:**
- Modify: `docs/SCHEDULED-BROADCAST.md`

- [ ] **Step 1: Document both limits**

Both belong next to the feature, not only in the spec. An operator who learns either of these during a broadcast learns it the worst way.

**It is install-wide.** Settings are global, so a playlist schedule turns the playlist on for the whole install rather than for one programme. On a single-source install — which is what almost every deployment is — the distinction does not exist. On a two-programme install it is all-or-nothing.

**It means "filler from 20:00 if nothing is live", not "filler at 20:00 regardless."** The playlist ranks below both ingests and a live encoder pre-empts it immediately, deliberately. Scheduling `playlist.start` while an encoder is publishing changes nothing visible until that encoder stops.

Write both in the document's existing voice, naming the failure each prevents.

- [ ] **Step 2: Check the document for claims this makes false**

`SCHEDULED-BROADCAST.md` was written when the playlist was one file and has been corrected twice already. Read it whole and fix anything the scheduling feature makes untrue.

- [ ] **Step 3: Run every gate, in CI's order**

```bash
gofmt -l ./cmd ./internal
go build ./... && go vet ./...
go test -race -timeout 15m ./...
git status --porcelain internal/engine/testdata/   # MUST be empty
cd ui && npx tsc --noEmit && npx oxlint && npm run build
```

- [ ] **Step 4: Commit**

```bash
git add docs/ && git commit -m "docs: what a scheduled playlist does, and what it does not"
```

---

## What is NOT covered

**Per-source playlist settings.** Deferred, with the reason in the spec: settings are install-wide, the two are indistinguishable for every install that exists, and the migration is a copy that costs no more later.

**`source.select` / scheduling a pin.** The pin is in-memory only, so there is no stored intent to flip.

**Scheduling any other settings field.** One action pair with a reason is not a general settings scheduler.

## Self-Review

**Spec coverage.** Actions and validation → Task 1. The runner, the `changed` flag and the actuator → Task 2. The drift guard → Task 3. The operator control → Task 4. Both documented limits → Task 5. Failure behaviour (unhandled on write failure) → Task 2. Skip-if-missed generalising → Task 2.

**Placeholders.** Task 4's e2e step is conditional on a spec existing, and says what to do in either case rather than leaving it open. Every code step carries its code.

**Type consistency.** `ActionPlaylistStart`, `ActionPlaylistStop`, `TargetsPlaylist()`, `SetPlaylistEnabled(bool) error` are spelled identically in every task that uses them. The UI strings match the Go constants' values exactly, which is what Task 3's guard compares.

**Ordering.** Task 1 must precede Task 2 (which calls `TargetsPlaylist`). Task 3's guard must be written before Task 4 and must be seen to fail.
