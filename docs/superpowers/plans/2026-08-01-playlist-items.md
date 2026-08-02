# A playlist is a list of normalised items — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** a playlist becomes an ordered list of uploads, and each upload gains a normalised derivative built to one fixed profile, so concat's compatibility requirement holds by construction.

**Architecture:** `PlaylistSettings.Items` replaces `FilePath`. A new `KindPlaylistItem` job normalises each referenced upload to a fixed profile, following `media.KindProxy`. The playlist is available only when every item's derivative exists, which the selector's existing ranking already handles.

**Tech Stack:** Go 1.26, no new dependencies. Reuses `internal/jobs`, `internal/uploads` and the FFmpeg argv builders already in the tree.

**Spec:** `docs/superpowers/specs/2026-08-01-playlist-items-design.md` (merged #66)

## Global Constraints

- **The ranking must not change.** The playlist stays below both ingests and above the slate, pre-empted immediately by a live encoder.
- **`internal/engine/testdata/selector_golden_no_playout.txt` stays byte-identical** with its hardcoded SHA-256 passing. Never regenerate it.
- **The additive-invariance test must keep reporting 0 moved decisions.**
- **A candidate is offered only when it would actually deliver.** Sub-project A established this; an item still transcoding must make the playlist unavailable, not on-air-and-broken.
- **`-safe 0` may only ever see server-chosen paths.** Items reference uploads, resolved through `uploads.Store.Resolve`, never a free-text path.
- **Every new settings field needs a reload rule.** `TestEverySettingsFieldHasAReloadRule` fails the build otherwise.
- CI gates, in CI's order: `gofmt -l ./cmd ./internal`; `go build ./...`; `go vet ./...`; `go test -race -timeout 15m ./...`.
- British spelling. Comments explain *why*, and name the failure that motivated the decision.

## A correction to the spec, carried into every task

The spec writes the item field as `UploadID string \`json:"uploadId"\``. **`internal/uploads` has no ID.** It keys by the stored filename: `Store.Resolve(name string) (string, error)` takes it, and `Store.List()` returns it as `File.Name`. Calling it an ID would name an identifier that does not exist.

The field is therefore:

```go
Upload string `json:"upload"`
```

and its value is the stored upload name. Every task below uses that spelling.

## File Structure

| File | Responsibility |
|---|---|
| `internal/db/settings.go` | `PlaylistItem`, `Items`, validation, the `FilePath` migration |
| `internal/engine/reload.go` | the reload rule for the new field |
| `internal/playlistmedia/` (new) | the normalise job: kind, profile, worker |
| `internal/engine/engine.go` | resolving items to a playable path, availability |

A new package rather than adding to `internal/media`: that package is about recordings and their proxies, and its `Processor` carries recording-shaped config. A playlist item is a different input with a different profile and a different consumer.

---

### Task 1: The item model and the migration

**Files:**
- Modify: `internal/db/settings.go`
- Modify: `internal/engine/reload.go`
- Test: `internal/db/settings_test.go`

**Interfaces:**
- Produces: `db.PlaylistItem{Upload string}`; `PlaylistSettings.Items []PlaylistItem`. Tasks 2–4 read them.

- [ ] **Step 1: Write the failing tests**

```go
func TestAPlaylistItemMustNameAKnownUpload(t *testing.T) {
	// Items reference uploads, never paths. That is the security boundary
	// this whole design rests on: the concat demuxer's -safe 0 is only
	// defensible because every path it sees was chosen by this process, and
	// uploads.SafeName is what guarantees that.
	s := DefaultSettings()
	s.Failover.Playlist.Enabled = true
	s.Failover.Playlist.Items = []PlaylistItem{{Upload: ""}}
	if err := s.Validate(); err == nil {
		t.Error("an item naming no upload was accepted")
	}
}

func TestAPlaylistItemRejectsAnythingPathShaped(t *testing.T) {
	for _, bad := range []string{"../escape.mp4", "/etc/passwd", "sub/dir.mp4"} {
		s := DefaultSettings()
		s.Failover.Playlist.Enabled = true
		s.Failover.Playlist.Items = []PlaylistItem{{Upload: bad}}
		if err := s.Validate(); err == nil {
			t.Errorf("item %q was accepted; an upload name is a bare filename, "+
				"and anything path-shaped means the caller is trying to reach "+
				"outside the uploads directory", bad)
		}
	}
}

func TestAnEnabledPlaylistNeedsAtLeastOneItem(t *testing.T) {
	s := DefaultSettings()
	s.Failover.Playlist.Enabled = true
	s.Failover.Playlist.Items = nil
	if err := s.Validate(); err == nil {
		t.Error("an enabled playlist with no items was accepted; it would " +
			"offer a candidate that can never deliver")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/db/ -run TestAPlaylist -count=1`
Expected: FAIL — `PlaylistItem` and `Items` do not compile.

- [ ] **Step 3: Add the model**

```go
// PlaylistItem is one entry in the playlist, in play order.
type PlaylistItem struct {
	// Upload is the STORED name of an upload -- what Store.List reports as
	// File.Name and what Store.Resolve accepts. Not a path, and not an id:
	// internal/uploads has no identifier other than the name it chose.
	//
	// The distinction is a security boundary rather than a convenience. The
	// concat demuxer needs -safe 0 to accept absolute paths, which disables
	// its own check; that is only defensible while every path it sees was
	// chosen by this process, which uploads.SafeName is what guarantees.
	Upload string `json:"upload"`
}
```

Replace `FilePath string` with `Items []PlaylistItem` on `PlaylistSettings`. Validate that an enabled playlist has at least one item, and that every `Upload` is a bare filename — reuse the same rejection `PlaylistFileProblem` already applies rather than writing new path logic.

- [ ] **Step 4: Migrate `FilePath`**

A deployment that set `FilePath` under sub-project A has a value that must not vanish. On load, a non-empty legacy `FilePath` becomes a single-item list.

**Do not drop it silently.** Write the migration with a comment naming what would otherwise happen: an operator's configured filler disappears on upgrade and the playlist stops going on air with nothing saying why.

- [ ] **Step 5: Update the reload rule**

`failover.playlist.filePath` no longer exists. Replace it with a rule for the items list, keeping the class the old one had — the item list reaches the tier's argv, so it is `ClassRespawn` via `playlistSig`, exactly as the path was.

Also update the UI-nameability skip list: the key changed, and the reason must still say sub-project B2 rather than naming a task that does not exist.

- [ ] **Step 6: Run the gates**

Run: `go test ./internal/db/ ./internal/engine/ -count=1`
Expected: PASS, including `TestEverySettingsFieldHasAReloadRule`.

- [ ] **Step 7: Mutation-test the migration**

Set a legacy `FilePath`, load, and assert one item results. Then delete the migration and confirm the test FAILS. Restore.

A migration that silently does nothing is indistinguishable from one that works until an upgrade loses somebody's configuration.

- [ ] **Step 8: Commit**

```bash
git add internal/db/settings.go internal/db/settings_test.go internal/engine/reload.go
git commit -m "feat(settings): a playlist is an ordered list of uploads

Items reference uploads by their stored name, never a path. That is the
security boundary the sequencing work rests on: the concat demuxer needs
-safe 0 to accept absolute paths, which disables its own check, and that
is only defensible while every path it sees was chosen by this process.

Not an id, because internal/uploads has none -- Store.Resolve takes the
stored name and Store.List reports it, so calling it an id would name an
identifier that does not exist.

A legacy FilePath migrates to a single-item list rather than being
dropped, so an upgrade does not quietly lose somebody's filler."
```

---

### Task 2: The normalisation job

**Files:**
- Create: `internal/playlistmedia/playlistmedia.go`
- Create: `internal/playlistmedia/playlistmedia_test.go`

**Interfaces:**
- Produces: `playlistmedia.KindNormalise jobs.Kind = "playlist.normalise"`; `NormaliseLimit = 1`; `Processor` with `Register(r Registry) error` and `RunNormalise(ctx, job, rep) error`; `DerivativePath(dataDir, upload string) string`.

- [ ] **Step 1: Write the failing test for the profile**

```go
func TestTheNormalisedProfileIsFixedAndComplete(t *testing.T) {
	// Every item must agree on codec, timebase, resolution and channel layout
	// or the concat demuxer errors or produces garbage. Fixed, not derived:
	// matching the live encoder would move the target on every settings change
	// and leave every existing derivative stale with nothing saying so.
	args := normaliseArgs("/in.mov", "/out.ts")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-r", "30", "-c:a", "aac", "-ac", "2", "-ar", "48000",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("normalise argv is missing %q: %v", want, args)
		}
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/playlistmedia/ -count=1`
Expected: FAIL — the package does not exist.

- [ ] **Step 3: Build the package**

Model it on `internal/media`: a `Processor` with a `Register` that wires one kind at one concurrency limit, exactly as `media.Register` wires three.

`NormaliseLimit = 1`, and the hardware probe is deliberately NOT consulted — copy `media`'s reasoning into a comment in your own words: a normalisation that raced the live encoders for a GPU would trade a stream for a file.

The derivative is keyed on the upload, not on the playlist entry, so the same upload used twice normalises once.

- [ ] **Step 4: Run the test**

Run: `go test ./internal/playlistmedia/ -count=1`
Expected: PASS.

- [ ] **Step 5: Mutation-test the profile assertion**

Remove `-ac 2` from the argv builder. Expected: the test FAILS naming it. Restore, confirm PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/playlistmedia/
git commit -m "feat(playlist): normalise an upload to one fixed profile

The concat demuxer requires every item to share codec, timebase,
resolution and channel layout, or it errors or produces garbage.
Normalising on import makes that hold by construction instead of asking
the operator to produce matching files by hand, which is the wall this
feature exists to remove.

Fixed rather than derived from the live encoder: that target moves on
every encoder settings change, and every existing derivative would go
stale with nothing saying so.

Its own package rather than internal/media, which is about recordings and
carries recording-shaped config. Same shape though, down to a limit of
one and not consulting the hardware probe -- a transcode racing the live
encoders for a GPU trades a stream for a file."
```

---

### Task 3: Availability and resolution

**Files:**
- Modify: `internal/engine/engine.go`
- Test: `internal/engine/selector_playlist_test.go`

**Interfaces:**
- Consumes: `db.PlaylistItem` (Task 1), `playlistmedia.DerivativePath` (Task 2).

- [ ] **Step 1: Write the failing test**

```go
func TestAPlaylistWithAnUnnormalisedItemIsNotOffered(t *testing.T) {
	// The same rule sub-project A established for a tier that is running but
	// delivering nothing: a candidate is offered only when it would actually
	// deliver. Offering a playlist whose item is still transcoding would put a
	// source on air that cannot play, and it would outrank the slate -- the one
	// thing that exists so an operator never sees nothing.
	e := playlistEngineWithItems(t, "ready.mp4", "still-transcoding.mp4")
	if e.playlistReady() {
		t.Error("a playlist with an unnormalised item was offered")
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/engine/ -run TestAPlaylistWith -count=1`
Expected: FAIL — `playlistReady` is undefined.

- [ ] **Step 3: Implement**

Resolve each item through `uploads.Store.Resolve` — never by joining strings — and require every derivative to exist before the tier is started at all. An item that cannot be resolved, or whose derivative is missing, makes the playlist unavailable.

Wire the readiness into `reconcilePlaylist`: a playlist that is not ready does not start a tier, so `playlistRunning` stays false through the existing byte-counter path and the slate wins. **Do not add a second availability mechanism** — sub-project A's rule is that availability means bytes on the hub, and this task decides whether the hub gets fed at all.

- [ ] **Step 4: Run the package**

Run: `go test ./internal/engine/ -race -count=1`
Expected: PASS. Both golden tables unchanged — this task changes when a tier starts, not what the selector decides.

- [ ] **Step 5: Mutation-test the readiness gate**

Make `playlistReady` always return true. Expected: the Step 1 test FAILS. Restore.

- [ ] **Step 6: Commit**

```bash
git add internal/engine/engine.go internal/engine/selector_playlist_test.go
git commit -m "feat(engine): a playlist is not offered until every item is ready

Normalisation is asynchronous, so an item can be referenced before its
derivative exists. Starting the tier then would put a source on air that
cannot play, ranked above the slate -- the one thing that exists so an
operator never sees nothing.

Readiness gates whether the tier STARTS, rather than adding a second
notion of availability beside the byte counter. Sub-project A settled
that a candidate is available when its hub is delivering; this decides
whether the hub gets fed at all."
```

---

### Task 4: Verification

- [ ] **Step 1: Every gate CI runs, in CI's order**

```bash
gofmt -l ./cmd ./internal
go build ./...
go vet ./...
go test -race -timeout 15m ./...
```

- [ ] **Step 2: Prove no decision moved**

```bash
git diff main --stat -- internal/engine/testdata/
```

Expected: **empty**. This sub-project changes what feeds the playlist tier and when it starts. It changes no decision, so neither table may move.

- [ ] **Step 3: Prove `-safe 0` never sees an operator path**

```bash
grep -rn "safe" internal/engine internal/playlistmedia --include=*.go | grep -v _test
```

Expected: no `-safe 0` outside a path built from `uploads.Store.Resolve`. Use `-E` for alternation if you need it — BSD grep does not reliably treat `\|` as alternation in a basic regex, and this repo has already been bitten by that producing a false negative.

- [ ] **Step 4: Run the acceptance suites**

```bash
./scripts/acceptance-failover.sh
./scripts/acceptance-synth.sh
```

Expected: failover at its current count with **0 destination restarts**; synth passing.

---

## What is NOT covered, and what could go wrong

**No sequencing.** B1 still plays the first item. Concat is B2, and this exists to make B2's items compatible by construction.

**No item-boundary resume.** It needs boundaries, which need sequencing.

**No UI.** Both settings keys stay in the UI-nameability skip list, with the reason naming sub-project B2 — not a task that does not exist, which this repo has already had to correct once.

**The profile is a guess.** 1080p30 / libx264 / AAC stereo is defensible and unmeasured. It is in one place so it can be revised once real files have been through it.

**Disk cost is real.** Derivatives are additional copies of operator media. The uploads store already reports free bytes; a normalisation that would exhaust the disk must fail cleanly rather than filling it.

**Normalisation competes with live encoding.** A limit of one and the existing governor are the mitigation. A box at its limit will still feel a transcode.

## Self-Review

**Spec coverage.** Item model and migration → Task 1. The job and its profile → Task 2. Readiness and resolution → Task 3. Invariants → Task 4. The spec's `-safe 0` boundary is enforced in Task 1 (validation) and asserted in Task 4 (grep).

**Placeholders.** None: every code step carries code, every command carries its expected output, every mutation names what must fail.

**Type consistency.** `PlaylistItem.Upload`, `PlaylistSettings.Items`, `playlistmedia.KindNormalise`, `NormaliseLimit`, `DerivativePath`, `normaliseArgs`, `playlistReady` are spelled identically in every task that uses them. The spec's `UploadID` is corrected to `Upload` once, at the top, and used consistently thereafter.
