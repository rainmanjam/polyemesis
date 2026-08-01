# Selector Generalisation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the selector's fixed three-value enum and hardcoded ladder with an **ordered list of candidate sources**, without changing what the selector decides for any input it can currently see.

**Architecture:** `chooseSource` becomes a ranking over a `[]candidate` slice instead of a switch over `sourceKind`. The three existing sources become three entries in that list, built in the order the current ladder implies. Everything above the function — `sourceKind` as a stored value, the feed machinery, `reconcileSelector`, the API and the UI — is untouched in tasks 1–3 and only widened in task 4.

**Tech Stack:** Go 1.26, no new dependencies. No UI work until task 5, and none at all if the roadmap defers items 5 and 7 again.

---

## Why this is worth doing before playlist or compositing

Both want the same thing and neither can have it today. Playlist needs the
selector to yield to a live encoder, which means "the playlist is a candidate
that ranks below a real ingest". Compositing wants per-input failover, which
means "each input has its own ordered candidate list". Both are the same
generalisation, and doing it twice would mean doing it twice **in the codebase's
most safety-critical pure function** — the one that decides what every viewer
sees.

`docs/roadmap/PLAYLIST-AND-COMPOSITING.md` says this in prose. This plan is the
part it does not have: how to do it without breaking what works.

## The safety mechanism, which is the whole plan

`chooseSource` is a **pure function with a small input space**. Every field it
branches on is an enum or a boolean:

| Field | Distinct values |
|---|---|
| `cur` | 4 — none, primary, backup, slate |
| `pinned` | 4 |
| `primary.alive` | 2 |
| `backup.alive` | 2 |
| `backupEnabled` | 2 |
| `slateEnabled` | 2 |
| `autoReturn` | 2 |
| `primary.stableFor >= returnStable` | 2 |

**1024 combinations.** That is not a number that needs sampling — it is a number
you enumerate.

So the refactor is not "rewrite it and hope the 25 table cases catch a
regression". It is:

1. Freeze the current behaviour as data, by running the existing function over
   all 1024 inputs and recording every `(kind, reason)` pair.
2. Write the new implementation beside the old one.
3. Prove they agree on all 1024, **including the reason strings**, which are
   operator-visible and appear in the UI.
4. Only then delete the old one.

A refactor of this function that is not exhaustively equivalence-tested is not
worth doing, because the failure mode is a viewer seeing the wrong thing during
an outage, which is exactly when nobody is watching the logs.

## Global Constraints

- **Nothing here re-encodes video.** No task adds, removes or alters an FFmpeg
  argument. The selector decides which feed writes into a hub; the hub and every
  consumer below it are untouched. Grep-provable: task 6.
- **The hub must not close.** `selector.spec` is deliberately constant while the
  tier is enabled, because anything that changed it would close the hub and
  restart every destination on it — which is the failure the tier exists to
  prevent. No task may put a candidate list into that signature.
- **The reason strings are a contract.** Verified: they reach the API as
  `Failover.Reason` (`engine.go:4851`) and are therefore visible to any operator
  or script reading status. Whether the current UI renders that field was NOT
  confirmed while writing this — check before assuming a reword is invisible.
  Task 2 preserves them exactly; task 4 may add new ones but may not reword an
  existing one.
- **`sourceKind` stays a string enum on the wire.** It is stored in settings and
  rendered by the UI. The candidate list is an internal shape; widening the wire
  format is task 5 and is explicitly optional.
- **Every guard must be mutation-tested.** Each task ends with a step that
  breaks the implementation, watches the named test fail, and restores it.
- CI gates, in CI's order: `gofmt -l ./cmd ./internal` prints nothing;
  `go build ./...`; `go vet ./...`; `go test -race -timeout 15m ./...`.
- British spelling. Comments explain *why*, and name the failure that motivated
  the decision.

---

### Task 1: Freeze the current behaviour as an exhaustive golden table

**Files:**
- Create: `internal/engine/selector_golden_test.go`

**Interfaces:**
- Consumes: `chooseSource`, `sourceChoice`, `liveness` as they are today.
- Produces: `allSourceChoices() []sourceChoice`; `TestChooseSourceGoldenIsExhaustive`.

- [ ] **Step 1: Enumerate the input space**

Write `allSourceChoices()` returning every combination of the eight dimensions
above. Use two fixed `liveness` values per source — one alive, one not — and two
`primary` values that differ only in whether `stableFor(now) >= returnStable`.

The enumeration must be **deterministic and ordered**, so a golden file diffs
cleanly.

- [ ] **Step 2: Record the current answers**

```go
// The golden table is the whole safety net for this refactor. chooseSource is
// pure and its input space is 1024 wide, so "does the new one behave like the
// old one" is a question that can be ANSWERED rather than sampled.
//
// Regenerate with: go test ./internal/engine/ -run Golden -update
func TestChooseSourceGoldenIsExhaustive(t *testing.T) { ... }
```

Follow whatever `-update` convention the repo already uses; if there is none,
add the flag here and say so in the comment.

- [ ] **Step 3: Assert the table covers what it claims**

The golden file must contain exactly 1024 rows. A test that silently enumerated
900 would be a safety net with a hole in the middle, so assert the count.

- [ ] **Step 4: Verify**

Run: `go test ./internal/engine/ -run Golden -count=1`
Expected: PASS, and `git status` shows a new golden file with 1024 rows.

- [ ] **Step 5: Mutation-test the net itself**

Temporarily invert one branch of `chooseSource` — make the `sourceSlate` pinned
case return `sourcePrimary`.
Expected: the golden test FAILS and names the rows that moved.
Restore, re-run, confirm PASS.

**This step is not optional.** A golden test that does not fail when the
behaviour changes is worse than none: it is a green light with nothing behind
it.

- [ ] **Step 6: Commit**

Commit the golden file and its test alone, before any refactor. The commit
message should say plainly that this changes no behaviour and exists so the next
commit can be proven not to either.

---

### Task 2: The candidate list, behind the same signature

**Files:**
- Modify: `internal/engine/engine.go`
- Create: `internal/engine/selector_candidates_test.go`

**Interfaces:**
- Produces: `type candidate struct{ kind sourceKind; available bool; rank int }`;
  `candidatesFor(sourceChoice) []candidate`; `chooseFrom([]candidate, sourceChoice) (sourceKind, string)`.
- `chooseSource` keeps its exact signature and becomes a two-line wrapper.

- [ ] **Step 1: Write the candidate model**

```go
// candidate is one source the selector may choose, and whether it can be
// chosen right now.
//
// available is not "is the process running" — it is "would this deliver bytes
// if we switched to it". A slate is always available when enabled because it
// synthesises its own picture; an ingest is available only when it is actually
// delivering, which is the distinction the liveness type already draws and the
// reason the selector switches on delivery rather than on process state.
type candidate struct {
	kind      sourceKind
	available bool
	// rank orders the list. Lower is preferred. It is a field rather than the
	// slice position so a future caller can build the list in any order and
	// still get a stable decision.
	rank int
}
```

- [ ] **Step 2: Build the list in the order the ladder implies**

`candidatesFor` returns primary, backup, slate — in that order — with
`available` computed exactly as the current function computes
`primaryLive`/`backupLive`/`slateEnabled`.

Do **not** try to improve the ordering here. The golden table will reject it,
and that is the point: this task is a shape change with no behaviour change.

- [ ] **Step 3: Port the ladder to `chooseFrom`**

Translate each branch of the current switch into a rule over the list. The
hysteresis rules — the flapping guard, "a slate is a holding pattern, never a
destination", "stay parked on the primary rather than switching to nothing" —
are the hard part and must be carried across verbatim in behaviour, including
their reason strings.

- [ ] **Step 4: Make `chooseSource` a wrapper**

```go
func chooseSource(c sourceChoice) (sourceKind, string) {
	return chooseFrom(candidatesFor(c), c)
}
```

- [ ] **Step 5: Verify equivalence**

Run: `go test ./internal/engine/ -run Golden -count=1`
Expected: PASS, **with the golden file unchanged**. If it changed, the refactor
altered behaviour and the diff names exactly which of the 1024 inputs moved.

Run the whole package: `go test ./internal/engine/ -race -count=1`.

- [ ] **Step 6: Mutation-test the port**

Reorder `candidatesFor` to put the slate before the backup.
Expected: the golden test FAILS on the rows where a backup was available.
Restore, re-run, confirm PASS.

- [ ] **Step 7: Commit**

---

### Task 3: Delete the old ladder

**Files:**
- Modify: `internal/engine/engine.go`

- [ ] **Step 1:** Remove any code the wrapper made unreachable.
- [ ] **Step 2:** Run the golden test and the acceptance suite:
  `go test ./internal/engine/ -race -count=1 && ./scripts/acceptance-failover.sh`
- [ ] **Step 3:** Commit.

The acceptance suite matters here specifically. The golden table proves the pure
function is unchanged; only the suite proves the tier still rides a real switch
without restarting a destination.

---

### Task 4: Admit a fourth candidate

**Files:**
- Modify: `internal/engine/engine.go`
- Modify: `internal/engine/selector_golden_test.go`

This is the task the whole refactor exists for, and it is where behaviour
changes on purpose.

- [ ] **Step 1:** Add a `sourcePlayout` kind that is available when a playlist
  feed is running, ranked **below** both ingests and **above** the slate.
- [ ] **Step 2:** Extend the enumeration to 4096 rows and regenerate the golden
  file. **Review the diff row by row.** Every row that changed must be one where
  the new candidate is available; any other change is a regression the earlier
  tasks were supposed to make impossible.
- [ ] **Step 3:** Mutation-test the ranking: put the playlist above the primary
  ingest and confirm the golden test fails on rows where both are available.
- [ ] **Step 4:** Commit.

**Ordering decision, stated rather than assumed:** a playlist ranks below a live
encoder because a scheduled broadcast is a fallback for "nobody is streaming",
not a pre-emption of somebody who is. An operator who wants the playlist to win
pins it, which the `pinned` path already supports for free.

---

### Task 5 (OPTIONAL): Widen the wire format

Only if items 5 or 7 are actually scheduled. Until then the internal shape is
general and the wire format is not, which is the correct place to stop.

- [ ] Settings gain an ordered candidate list per source.
- [ ] The UI renders it as a reorderable list.
- [ ] `settingsReload` gains rules for the new fields, and the drift guard in
      `internal/engine/reload_test.go` will fail until it does.

---

### Task 6: Verification

- [ ] **Step 1:** Every gate CI runs, in CI's order.
- [ ] **Step 2:** Prove no argv moved:
  ```bash
  git diff main --stat -- internal/ffmpeg internal/playout
  ```
  Expected: **empty**. This refactor decides which feed writes into a hub. It
  does not build a command line.
- [ ] **Step 3:** Prove the hub signature did not gain a candidate list:
  ```bash
  grep -n "spec" internal/engine/engine.go | grep -i "candidate\|rank"
  ```
  Expected: no hits. A candidate list in `selector.spec` would close the hub on
  every reorder and restart every destination — the exact failure the tier
  exists to prevent.
- [ ] **Step 4:** Run `./scripts/acceptance-failover.sh` and
  `./scripts/acceptance-synth.sh` in full.

---

## What is NOT covered, and what could go wrong

**The golden table freezes behaviour, including behaviour that is wrong.** If
the current ladder has a bug, this plan preserves it exactly and calls that
success. That is deliberate — a refactor and a bug fix in the same commit are
indistinguishable in a bisect — but it means "the golden test passes" is not
"the selector is correct". Any behaviour change belongs in its own commit with
its own reasoning, after task 3.

**1024 is exhaustive over the fields the function branches on, not over
reality.** `liveness` carries timestamps, and the enumeration collapses them to
alive/not-alive plus stable/not-stable. A bug that depends on a specific
duration — an off-by-one at exactly `returnStable` — is outside the net. The
existing table test covers the boundary cases; keep it rather than replacing it
with the golden table.

**Reason strings are asserted verbatim, which makes them brittle on purpose.**
Rewording one requires regenerating the golden file, which shows up in review as
a diff touching many rows. That is the intended cost: these strings are what an
operator reads to understand why their stream switched.

**Task 4 changes what viewers see.** Everything before it is provably
behaviour-preserving; task 4 is not, and its golden diff is the review artifact.
Do not merge tasks 1–3 and 4 as one change.

**Per-input failover for compositing is still not solved.** This makes one
selector general over N candidates. Compositing wants N selectors, one per
input, which is a different problem this plan only makes tractable rather than
solves.

---

## Self-Review

**Is the golden table over-engineering for a 3–5 day task?** No, and the
arithmetic is why: writing it is under an hour because the input space is
enumerable, and it converts "we think the refactor is safe" into "the refactor
is provably equivalent on every input the function can see". The alternative is
25 hand-written cases guarding the function that decides what every viewer sees
during an outage.

**Why not go straight to the candidate list and skip the wrapper?** Because
then task 2 changes both the shape and the call sites at once, and a golden
failure could be either. The wrapper makes the shape change independently
verifiable, and it costs two lines that task 3 deletes.

**Is `rank` as a field rather than slice position premature?** Arguably. It is
kept because compositing wants to build candidate lists per input, and a caller
assembling one from configuration should not have to sort before calling. If
task 4 lands and nothing needs it, delete it then — a field with one caller is
easier to remove than to add.
