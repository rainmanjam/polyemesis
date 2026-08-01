# The playlist gets its own hub — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** a playlist plays into its own relay hub and its own supervised feed, so a file on air no longer makes the primary read live and silently disable failover.

**Architecture:** the playlist becomes a tier shaped exactly like the backup ingest — a supervised process publishing into its own `relay.Hub`, which the selector then copies into the programme hub when it chooses it. The *decision* already exists from roadmap item 4; this makes it reachable and changes no ranking.

**Tech Stack:** Go 1.26, no new dependencies. FFmpeg argv reuses `-stream_loop -1 -re`, which `pullFile` already emits.

**Spec:** `docs/superpowers/specs/2026-08-01-playlist-own-hub-design.md`

## Global Constraints

- **The ranking must not change.** The playlist is below both ingests and above the slate, and a live encoder pre-empts it immediately with no stability window. Any task that moves a decision is a regression, not a feature.
- **`internal/engine/testdata/selector_golden_no_playout.txt` must stay byte-identical**, and its hardcoded SHA-256 assertion must keep passing. It proves no pre-playlist decision moved. Never regenerate it.
- **The additive-invariance test must keep reporting 0 moved decisions** across the original 1024.
- **`chooseSource` stays pure and cheap.** No clock, no I/O, no engine state — the golden table's claim to being exhaustive depends on its inputs being enumerable.
- **Nothing may put a candidate list into `selector.spec`** — that would close the hub and restart every destination on it.
- **Path confinement to `DataDir`** for the operator-supplied file, exactly as `SlateSettings.ImagePath` and `file://` pull sources do.
- **Every new settings field needs a reload rule.** `TestEverySettingsFieldHasAReloadRule` (`internal/engine/reload_test.go:23`) is a reflection walk that fails the build otherwise.
- CI gates, in CI's order: `gofmt -l ./cmd ./internal` prints nothing; `go build ./...`; `go vet ./...`; `go test -race -timeout 15m ./...`.
- British spelling. Comments explain *why*, and name the failure that motivated the decision.

## File Structure

| File | Responsibility |
|---|---|
| `internal/engine/engine.go` | the rename, the playlist tier, its hub accessor, the three feed cases, `playlistRunning` sampling, the `SwitchSource` pin |
| `internal/db/settings.go` | `PlaylistSettings` and its validation |
| `internal/engine/reload.go` | reload classes for the two new settings fields |
| `internal/engine/testdata/selector_golden.txt` | regenerated twice — once for the rename, once when the rows become reachable |
| `internal/engine/selector_playlist_test.go` | new: the tier's unit tests |
| `scripts/acceptance-failover.sh` | the case that is the whole point |

---

### Task 1: The rename

**Files:**
- Modify: `internal/engine/engine.go`
- Modify: `internal/engine/testdata/selector_golden.txt` (regenerated)

**Interfaces:**
- Produces: `sourcePlaylist sourceKind = "playlist"`, replacing `sourcePlayout sourceKind = "playout"`. Every later task uses the new name.

Do this first and alone. It touches 740 rows of the golden table, and mixing it with behaviour would make the diff unreadable.

- [ ] **Step 1: Confirm the rename is still free**

The spec verified this, but verify again rather than trust a document — if it has become false, this is a migration and the plan is wrong.

```bash
grep -rn "sourceKind" internal/db/*.go | grep -v _test
grep -rn '"playout"' internal/ --include=*.go | grep -v _test
```

Expected: `sourceKind` appears in no `internal/db` type, and `"playout"` appears only in `internal/engine`. If a database column or migration mentions it, STOP and report.

- [ ] **Step 2: Rename the constant and every use**

`sourcePlayout` → `sourcePlaylist`, and its value `"playout"` → `"playlist"`.

Leave the five reason strings alone — they already say "playlist" and are a contract reaching the API as `Failover.Reason`.

Also update the comment at the `playoutRunning` field and the `errNoFeedShape` message, which both spell the old name. Rename the field `playoutRunning` → `playlistRunning` at the same time; it is the same rename and splitting it would leave the struct half-converted.

- [ ] **Step 3: Regenerate the golden table**

Run: `go test ./internal/engine/ -run Golden -update -count=1`

Then confirm ONLY the kind column changed:

```bash
git diff --stat internal/engine/testdata/
git diff internal/engine/testdata/selector_golden.txt | grep -c "^[-+].*=> " 
```

Expected: `selector_golden.txt` changed; `selector_golden_no_playout.txt` **NOT** changed. If the frozen file moved, you regenerated the wrong thing — restore it with `git checkout --`.

- [ ] **Step 4: Verify no decision moved**

Run: `go test ./internal/engine/ -race -count=1`

Expected: PASS, including `TestAdmittingThePlaylistMovedNoDecisionThatDidNotInvolveIt` and the frozen file's SHA assertion. A rename changes a stored string, not a decision.

- [ ] **Step 5: Commit**

```bash
git add internal/engine/engine.go internal/engine/testdata/selector_golden.txt
git commit -m "refactor(engine): the fourth source kind is a playlist, not a playout

internal/playout is the viewer-facing HLS origin. Storing the playlist
source as \"playout\" gave two unrelated features one name, while every
operator-facing reason string already said \"playlist\".

Free now and a migration later: sourceKind appears in no database type,
sel.pinned is set only in memory and never written, and SwitchSource
rejects the value today, so nobody can have stored it.

No decision moved. The frozen no-playout table is byte-identical and its
SHA assertion still passes."
```

---

### Task 2: Settings, validation and reload rules

**Files:**
- Modify: `internal/db/settings.go`
- Modify: `internal/engine/reload.go`
- Test: `internal/db/settings_test.go`

**Interfaces:**
- Produces: `db.PlaylistSettings{Enabled bool, FilePath string}`, reached as `s.Failover.Playlist`. Task 3 and Task 4 read it.

It lives under `Failover` because the playlist is a failover candidate, which is what puts it beside `Slate` — the block it is modelled on.

- [ ] **Step 1: Write the failing validation test**

```go
func TestPlaylistFilePathIsConfinedToTheDataDir(t *testing.T) {
	// The same guarantee SlateSettings.ImagePath and file:// pull sources
	// already carry. An operator-supplied path is exactly the shape
	// SECURITY.md's path confinement section exists to defend.
	for _, bad := range []string{"../etc/passwd", "/etc/passwd", "a/../../b"} {
		s := DefaultSettings()
		s.Failover.Playlist.Enabled = true
		s.Failover.Playlist.FilePath = bad
		if err := s.Validate(); err == nil {
			t.Errorf("path %q was accepted; it escapes the data directory", bad)
		}
	}
}

func TestPlaylistNeedsAFileWhenEnabled(t *testing.T) {
	s := DefaultSettings()
	s.Failover.Playlist.Enabled = true
	s.Failover.Playlist.FilePath = ""
	if err := s.Validate(); err == nil {
		t.Error("an enabled playlist with no file was accepted; it would " +
			"start a feed that can never deliver, and the selector would " +
			"offer a candidate that always loses")
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/db/ -run TestPlaylist -count=1`
Expected: FAIL — `s.Failover.Playlist` does not compile.

- [ ] **Step 3: Add the settings block**

```go
// PlaylistSettings is a file the selector can put on air when no encoder is
// delivering.
//
// Deliberately smaller than SlateSettings. The slate carries encoder, preset,
// colour and bitrate because it SYNTHESISES a picture; a playlist plays a file
// that already has its own encoding, so it needs none of them.
type PlaylistSettings struct {
	Enabled bool `json:"enabled"`
	// FilePath is relative to the data directory and confined there exactly as
	// SlateSettings.ImagePath and a file:// pull source are. An operator-supplied
	// path is the shape SECURITY.md's path confinement section exists to defend.
	FilePath string `json:"filePath"`
}
```

Add `Playlist PlaylistSettings \`json:"playlist"\`` to the failover settings struct beside `Slate`, and validate it the same way the slate's image path is validated.

- [ ] **Step 4: Add the reload rules**

`TestEverySettingsFieldHasAReloadRule` fails the build without these. Model them on the slate's, which are the closest existing pair:

```go
"failover.playlist.enabled":  {ClassLive, "applySourceChoice", "whether the playlist is an eligible choice is re-read every 500ms; the playlist feed's own argv is not"},
"failover.playlist.filePath": {ClassRespawn, "feedUpstreamSig", "the input file in the playlist feed's argv"},
```

The classes are not a guess: `enabled` decides eligibility, which `applySourceChoice` re-reads every sweep; `filePath` reaches an FFmpeg argv, and anything that reaches an argv requires a respawn.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/db/ ./internal/engine/ -count=1`
Expected: PASS, including `TestEverySettingsFieldHasAReloadRule`.

- [ ] **Step 6: Mutation-test the reload guard**

Delete the `failover.playlist.filePath` rule. Run `go test ./internal/engine/ -run ReloadRule -count=1`.
Expected: FAIL naming the missing field. Restore, confirm PASS.

A drift guard that cannot fail is a green light with nothing behind it.

- [ ] **Step 7: Commit**

```bash
git add internal/db/settings.go internal/db/settings_test.go internal/engine/reload.go
git commit -m "feat(settings): a playlist file, confined to the data directory

Modelled on SlateSettings and deliberately smaller: the slate synthesises
a picture and needs an encoder, preset, colour and bitrate; a playlist
plays a file that already has its own encoding.

Both fields carry reload classes. enabled is live -- eligibility is
re-read every sweep. filePath is respawn, because it reaches an FFmpeg
argv, and this repo's rule is that anything reaching an argv does."
```

---

### Task 3: The playlist tier

**Files:**
- Modify: `internal/engine/engine.go`
- Test: `internal/engine/selector_playlist_test.go` (create)

**Interfaces:**
- Consumes: `db.PlaylistSettings` from Task 2.
- Produces: `type playlistTier struct{ proc *supervisor.Process; hub *relay.Hub; sig string }`, `(e *Engine) playlistHub() *relay.Hub`, `(e *Engine) reconcilePlaylist(s db.Settings)`. Task 4 reads the hub.

The tier is shaped exactly like `backupIngest` (`engine.go:2754`), because the backup is the existing proof that a supervised process can publish into its own hub and be a selector candidate. The difference is only what the process reads: the backup dials an ingest, the playlist reads a file.

- [ ] **Step 1: Write the failing test**

```go
func TestPlaylistFeedLoopsTheFileAtWallClockSpeed(t *testing.T) {
	// -stream_loop -1 makes the file look like a feed that never ends, and -re
	// paces it at wall-clock speed. Without -re FFmpeg reads at disk speed and
	// buries the relay in an hour of stream in seconds -- the same reason
	// pullFile carries both flags.
	args := playlistFeedArgs("/data/loop.mp4", "udp://127.0.0.1:9000")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-stream_loop -1", "-re", "/data/loop.mp4"} {
		if !strings.Contains(joined, want) {
			t.Errorf("playlist argv is missing %q: %v", want, args)
		}
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/engine/ -run TestPlaylistFeed -count=1`
Expected: FAIL — `playlistFeedArgs` is undefined.

- [ ] **Step 3: Add the tier**

Add the struct beside `backupIngest`, a `playlistHub()` accessor mirroring `backupHub()` (`engine.go:2860`) including its `RLock` and nil guard, `playlistFeedArgs`, and `reconcilePlaylist(s db.Settings)` mirroring `reconcileBackupIngest` (`engine.go:3992`).

`reconcilePlaylist` starts the process when `s.Failover.Playlist.Enabled` and a confined path resolves, stops it when not, and is a no-op when the signature is unchanged — the same shape as the backup, so a settings save that does not touch the playlist does not restart it.

- [ ] **Step 4: Run the test**

Run: `go test ./internal/engine/ -run TestPlaylist -count=1`
Expected: PASS.

- [ ] **Step 5: Wire the reconcile and the teardown**

Call `reconcilePlaylist` from the same place `reconcileBackupIngest` is called, and close the playlist hub wherever the backup's is closed.

**The teardown is not optional.** Two hubs where there was one means a second thing that can leak; the sweeper that bounds the segment directory exists because a previous tier forgot.

- [ ] **Step 6: Run the package**

Run: `go test ./internal/engine/ -race -count=1`
Expected: PASS. The golden table is untouched by this task — the tier exists but `playlistRunning` is still hardcoded, so no decision is reachable yet.

- [ ] **Step 7: Commit**

```bash
git add internal/engine/engine.go internal/engine/selector_playlist_test.go
git commit -m "feat(engine): the playlist publishes into its own hub

Shaped like backupIngest, because the backup is the existing proof that a
supervised process can publish into its own hub and be a selector
candidate. The only difference is what the process reads: the backup
dials an ingest, this loops a file.

Its own hub is the whole point. A file playing into the PRIMARY hub makes
the primary read live, so failover would never switch to a backup or a
slate -- a file on air would silently disable the entire feature.

No decision is reachable yet: playlistRunning is still hardcoded false."
```

---

### Task 4: Make the decision reachable

**Files:**
- Modify: `internal/engine/engine.go`
- Modify: `internal/engine/testdata/selector_golden.txt` (regenerated)

**Interfaces:**
- Consumes: `playlistHub()` from Task 3.

This is the task that turns 740 frozen rows into behaviour.

- [ ] **Step 1: Sample the hub**

Replace the hardcoded `playlistRunning: false` (search for it; Task 1 renamed it from `playoutRunning`) with a sample of the playlist hub's byte counter, taken the same way the primary's is — under the same lock, in the same place, so `chooseSource` still receives a plain boolean and stays pure.

- [ ] **Step 2: Add the three feed cases**

`feedUpstreamSig`, `startFeed` and `downstreamFeedInput` each currently refuse `sourcePlaylist` via `errNoFeedShape`. Give each an explicit `case sourcePlaylist:`:

| function | what it returns |
|---|---|
| `feedUpstreamSig` | a signature over the playlist's file path, so a changed file respawns the feed |
| `startFeed` | the copy hop from the playlist hub, exactly as the backup's |
| `downstreamFeedInput` | `e.playlistHub()` |

Keep `errNoFeedShape` and its positive allow-list. A fifth kind must still fail loudly.

- [ ] **Step 3: Accept the pin**

`SwitchSource`'s allow-list (search for `case sourceSlate:` inside it — it is the only allow-list in the repo) gains `case sourcePlaylist:`. The pin path was built in item 4 and has never been reachable.

- [ ] **Step 4: Regenerate and review the golden diff**

Run: `go test ./internal/engine/ -run Golden -update -count=1`

**Expected: NO CHANGE.** Making a row reachable does not change what it decides — the decision was already frozen. If the table moves, the sampling changed a decision and that is a regression.

Then run: `go test ./internal/engine/ -race -count=1`
Expected: PASS, including the additive-invariance test at 0 moved and the frozen file's SHA.

- [ ] **Step 5: Mutation-test the sampling**

Force `playlistRunning` to `true` unconditionally. Run the golden test.
Expected: FAIL, on rows where a playlist that is not running was being offered. Restore, confirm PASS.

This proves the sampling is load-bearing rather than decorative.

- [ ] **Step 6: Commit**

```bash
git add internal/engine/engine.go internal/engine/testdata/selector_golden.txt
git commit -m "feat(engine): the selector can actually put the playlist on air

playlistRunning is sampled from the playlist hub instead of hardcoded
false, and the three feed functions that refused the kind now build it.
SwitchSource accepts the pin item 4 taught pinReason to honour.

The golden table does not move. Making a row reachable does not change
what it decides -- 740 rows were frozen and reviewed when the candidate
was added, and this is the commit that turns them into behaviour."
```

---

### Task 5: The acceptance case

**Files:**
- Modify: `scripts/acceptance-failover.sh`

This is the case the whole sub-project exists for, and it is impossible today.

- [ ] **Step 1: Add the check**

Extend `acceptance-failover.sh` with: a playlist enabled and playing, then the primary publisher stops, and assert the selector switches away from the primary — which today it cannot, because the file keeps the primary hub fed.

Use the suite's existing helpers. `poly_poll_until` and `poly_poll_field` are there so a timed-out wait explains itself, and `poly__grace_check` distinguishes "never happened" from "happened one sample late". Do not write a bare `for _ in $(seq ...)` loop.

Raise the suite's `EXPECTED_CHECKS` by the number of checks added, or the guard stops meaning anything.

- [ ] **Step 2: Run it**

Run: `./scripts/acceptance-failover.sh`
Expected: all checks pass, including the new one. It was 13/13 with 0 destination restarts before this plan; that number must not fall.

- [ ] **Step 3: Mutation-test it**

Disable the playlist tier's own hub — point `downstreamFeedInput(sourcePlaylist)` at `e.sourceHub()` instead, reproducing the bug this sub-project fixes.
Expected: the new check FAILS, because the primary now reads live and the switch never happens. Restore, confirm PASS.

**This is the most important mutation in the plan.** It proves the acceptance case actually tests the failover regression rather than merely passing alongside it.

- [ ] **Step 4: Commit**

```bash
git add scripts/acceptance-failover.sh
git commit -m "test(acceptance): filler playing, encoder drops, failover still works

Impossible before this sub-project: a file played through the primary
hub, so the primary read live and the selector would never switch to a
backup or a slate.

Mutation-tested by pointing the playlist feed back at the primary hub,
which reproduces the old bug and fails this check."
```

---

### Task 6: Verification

- [ ] **Step 1: Every gate CI runs, in CI's order**

```bash
gofmt -l ./cmd ./internal
go build ./...
go vet ./...
go test -race -timeout 15m ./...
```

- [ ] **Step 2: Prove no pre-playlist decision moved**

```bash
git diff main --stat -- internal/engine/testdata/selector_golden_no_playout.txt
```

Expected: **empty**. This file is the proof that the refactor and this feature changed nothing that predates them, and its SHA is asserted in the test.

- [ ] **Step 3: Prove the hub signature did not gain a candidate list**

```bash
grep -n "spec" internal/engine/engine.go | grep -iE "candidate|rank"
```

Expected: no hits. Use `-E`, not `\|` — BSD grep does not reliably treat `\|` as alternation in a basic regex, and this repo has already been bitten by that difference producing a false negative.

- [ ] **Step 4: Prove no argv moved outside the playlist**

```bash
git diff main --stat -- internal/ffmpeg
```

Expected: **empty**. This sub-project adds a feed; it does not change how anything else is encoded.

- [ ] **Step 5: Run both acceptance suites in full**

```bash
./scripts/acceptance-failover.sh
./scripts/acceptance-synth.sh
```

Expected: failover at its raised count with 0 destination restarts; synth 10/10.

---

## What is NOT covered, and what could go wrong

**One file, looped.** Sequencing is sub-project B. This ships the same single-file behaviour the pull source has today, moved off the primary hub.

**No item-boundary resume.** It needs items, which do not exist until B. Writing a boundary rule here would be writing against a concept the code cannot express.

**`acceptance-failover` is the suite most at risk.** This adds a source to the suite whose whole job is switching between sources. It has been measured at 13/13 with zero destination restarts; a regression there matters more than a slow test.

**740 rows become reachable at once.** They were frozen and reviewed when the candidate was added, but frozen is not exercised. Task 5's acceptance case is what turns the most important of them into observed behaviour.

**The rename is free only until it ships.** Task 1 re-verifies rather than trusting the spec, because if a database column has appeared since the spec was written, this is a migration and the plan is wrong.

## Self-Review

**Spec coverage.** Rename → Task 1. Settings and confinement → Task 2. Own hub and supervised feed → Task 3. `playlistRunning`, the three feed cases, the pin → Task 4. The acceptance case → Task 5. Golden-table invariants → Tasks 1, 4 and 6. Every spec section maps to a task.

**Placeholders.** None: every code step carries the code, every command carries its expected output, and every mutation names what must fail.

**Type consistency.** `sourcePlaylist`, `playlistRunning`, `playlistTier`, `playlistHub()`, `reconcilePlaylist`, `playlistFeedArgs`, `db.PlaylistSettings` and `s.Failover.Playlist` are spelled identically in every task that uses them. Task 1 renames both the constant and the field together so no later task sees a half-converted struct.
