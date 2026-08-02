# Playlist Sequencing (B2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A playlist plays every item, in order, on one continuous timeline, and an operator can build that list.

**Architecture:** The playlist tier stops feeding FFmpeg one original file and feeds it a concat list of normalised derivatives, each entry carrying a measured `duration`. The normaliser gains padding, a measured output duration and a profile version. Runtime readiness switches from asking about uploads to asking about derivatives. A new GET surfaces per-item state so the operator UI can explain why a playlist is not on air.

**Tech Stack:** Go 1.25, FFmpeg 8.1.2 (`-f concat`, `-stream_loop`), React 19 + TypeScript (`ui/`), SQLite via `internal/db`, the job queue in `internal/jobs`.

**Spec:** `docs/superpowers/specs/2026-08-02-playlist-sequencing-design.md`

## Global Constraints

- **Both golden tables under `internal/engine/testdata/` must stay BYTE-UNCHANGED.** Verify with `git status --porcelain internal/engine/testdata/` (must be empty). NEVER run the golden test with `-update`.
- **Every new guard must be proven able to fail.** For each test added, run the named one-line production mutation, quote the failure, restore. A guard that passes either way is worse than none. B1 produced six tests that could not fail.
- **A mutation that fails to COMPILE is not a mutation result.** Re-anchor and re-run.
- **`-safe 0` may only ever be given paths built by `uploads.Store.Resolve` or `playlistmedia.DerivativePath`.** Operator text must never reach a concat list.
- **Pad, never truncate.** `-shortest` must not be introduced on any path handling operator media. It ends output when the shortest stream ends, discarding real content.
- **The canonical item duration is MEASURED FROM THE ENCODED OUTPUT'S PACKETS.** Not from the source, not from `format.duration` taken on trust, and never from `NormaliseParams.DurationMS` (a source-side estimate that is never populated).
- **The tier keeps running continuously.** It is `-c copy`. Do not add stop-when-off-air.
- **B2 still plays the list in order and does not resume at an item boundary.** No duration table, no rotation, no respawn-on-return.
- **`make build` FIRST, before any acceptance suite.** A stale binary already invalidated one task's evidence in B1.
- **Comments explain WHY and must not assert what the code does not do.** Three B1 findings were comments that had outlived their reason.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/ffmpeg/concat.go` (new) | `ConcatList` — the single concat-list renderer, with quoting and `duration` directives. Shared by clipper and playlistmedia. |
| `internal/clipper/args.go` (modify) | Delete the private `concatList`; call `ffmpeg.ConcatList`. |
| `internal/playlistmedia/playlistmedia.go` (modify) | Padding instead of truncation, measured output duration, `ProfileVersion`, versioned `DerivativePath`, publish-time upload re-check. |
| `internal/engine/engine.go` (modify) | `playlistFeedArgs` → concat; derivative-only readiness; list-file lifetime on `playlistTier`. |
| `internal/api/playlist_status.go` (new) | `GET /api/v1/failover/playlist` — per-item readiness. Never on the settings blob. |
| `internal/api/media.go` (modify) | In-use guard, derivative removal, reconcile, serialization with settings PUT. |
| `ui/src/components/PlaylistEditor.tsx` (new) | The operator's list editor. |
| `ui/src/pages/SettingsPage.tsx` (modify) | Mount the editor in the failover section. |
| `ui/src/lib/types.ts` (modify) | `PlaylistSettings`, `PlaylistItem`, `PlaylistStatus`. |
| `internal/db/settings_drift_test.go` (modify) | Remove BOTH playlist skip-list entries. |
| `scripts/acceptance-failover.sh` (modify) | Multi-item sequencing; the mismatched-publisher ratchet. |

---

## Task 1: Measure what FFmpeg actually does

Nothing else in this plan may be built until this task's numbers exist. The spec's timestamp contract is a claim about FFmpeg's behaviour, and this project's standard is a probe rather than a plausible argument.

**Files:**
- Create: `internal/playlistmedia/concat_behaviour_test.go`

**Interfaces:**
- Consumes: `playlistmedia.New`, `RunNormalise`, `DerivativePath` (all existing).
- Produces: measured facts recorded in the test's comments — whether `-stream_loop -1` wraps a concat input cleanly, and whether DTS/PTS stay monotonic across seams and two wraps WITHOUT `duration` directives. Task 3 depends on the answer.

- [ ] **Step 1: Write the measurement test**

Build three real derivatives of different lengths, concatenate with `-stream_loop -1`, capture two full wraps, and assert on packet timestamps.

```go
//go:build integration

package playlistmedia

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// TestConcatTimestampsAreMonotonicAcrossSeamsAndWraps is a MEASUREMENT, not a
// unit test. The spec's timestamp contract asserts things about FFmpeg that
// this project's standard says must be probed rather than argued.
//
// Three items of DIFFERENT lengths, because equal lengths would hide an
// off-by-one-file offset error: with 3x2s a wrong wrap looks identical to a
// right one at t=6s.
func TestConcatTimestampsAreMonotonicAcrossSeamsAndWraps(t *testing.T) {
	dir := t.TempDir()
	items := buildDerivatives(t, dir, []float64{2.0, 3.0, 1.5}) // paths, in order

	list := writeList(t, dir, items, nil) // nil = NO duration directives yet
	// 2 full wraps of a 6.5s list, plus margin.
	out := ffprobePackets(t, list, 14.0)

	assertMonotonic(t, out)
}

// assertMonotonic fails on the FIRST backwards step, quoting both timestamps.
// A summary count would say "17 non-monotonic packets" and leave the reader to
// find which seam.
func assertMonotonic(t *testing.T, pkts []packet) {
	t.Helper()
	var lastDTS, lastPTS float64 = -1, -1
	for i, p := range pkts {
		if p.dts < lastDTS {
			t.Fatalf("packet %d (%s): DTS went backwards, %f after %f", i, p.stream, p.dts, lastDTS)
		}
		if p.pts < lastPTS {
			t.Fatalf("packet %d (%s): PTS went backwards, %f after %f", i, p.stream, p.pts, lastPTS)
		}
		lastDTS, lastPTS = p.dts, p.pts
	}
	if len(pkts) == 0 {
		t.Fatal("no packets: the concat input produced nothing, so nothing was measured")
	}
}

type packet struct {
	stream string
	pts    float64
	dts    float64
}

// ffprobePackets reads packet timestamps from a concat list played through
// -stream_loop -1, stopping after seconds.
func ffprobePackets(t *testing.T, list string, seconds float64) []packet {
	t.Helper()
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-stream_loop", "-1",
		"-f", "concat", "-safe", "0", "-i", list,
		"-read_intervals", "%+"+strconv.FormatFloat(seconds, 'f', 1, 64),
		"-show_entries", "packet=stream_index,pts_time,dts_time",
		"-of", "csv=p=0")
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe: %v\n%s", err, raw)
	}
	var out []packet
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		f := strings.Split(line, ",")
		if len(f) < 3 {
			continue
		}
		pts, err1 := strconv.ParseFloat(f[1], 64)
		dts, err2 := strconv.ParseFloat(f[2], 64)
		if err1 != nil || err2 != nil {
			continue // N/A timestamps are reported separately by the caller
		}
		out = append(out, packet{stream: f[0], pts: pts, dts: dts})
	}
	return out
}
```

Write `buildDerivatives` (runs `RunNormalise` over lavfi-generated sources of the given durations, returns derivative paths in order) and `writeList` (renders `file '<path>'` lines, plus `duration <secs>` lines when durations is non-nil) as helpers in the same file.

- [ ] **Step 2: Run it and RECORD the outcome**

Run: `go test -tags=integration ./internal/playlistmedia/ -run TestConcatTimestampsAreMonotonicAcrossSeamsAndWraps -v`

Both outcomes are valid results. **Record which happened in the test's doc comment, with the actual numbers.**

- PASS → FFmpeg's inferred durations are good enough for these derivatives. Say so, and say that `duration` directives are then belt-and-braces rather than load-bearing.
- FAIL → quote the first backwards step and at which seam. This is the measurement that justifies `duration` directives.

- [ ] **Step 3: Re-run WITH duration directives and compare**

Pass measured durations to `writeList` and run again. Record whether the directives changed the outcome.

- [ ] **Step 4: Commit the measurement**

```bash
git add internal/playlistmedia/concat_behaviour_test.go
git commit -m "test(playlist): measure what concat and -stream_loop actually do

The spec's timestamp contract is a claim about FFmpeg. This is the probe."
```

**Report to the controller:** the recorded numbers, and whether `duration` directives proved necessary. Task 3 needs the answer.

---

## Task 2: The normaliser pads, measures and versions

**Files:**
- Modify: `internal/playlistmedia/playlistmedia.go`
- Test: `internal/playlistmedia/playlistmedia_test.go`

**Interfaces:**
- Produces:
  - `const ProfileVersion = 2` — bumped whenever the encode changes what a derivative contains.
  - `func DerivativePath(dataDir, upload string) string` — UNCHANGED SIGNATURE, but the filename now embeds the version: `<upload>.v2.ts`.
  - `NormaliseResult` gains `DurationMS int64` — MEASURED from the encoded output.
  - `func ProbeOutputDurationMS(ctx, ffprobe, path string) (int64, error)`

- [ ] **Step 1: Write the failing tests**

```go
// A short-audio item must be PADDED, not truncated. -shortest would have ended
// the output when the audio ran out, discarding picture the operator supplied.
//
// The mutation: swap the apad filter for "-shortest" and this fails, because
// the derivative becomes as short as its audio.
func TestAnItemWithShortAudioKeepsAllOfItsVideo(t *testing.T) {
	dir := t.TempDir()
	src := buildSource(t, dir, sourceSpec{videoSecs: 3.0, audioSecs: 1.0})
	p := newTestProcessor(t, dir)

	if err := p.RunNormalise(context.Background(), normaliseJob(t, src), &recorder{}); err != nil {
		t.Fatalf("RunNormalise: %v", err)
	}
	got := probeDurationSecs(t, DerivativePath(dir, src))
	if got < 2.9 {
		t.Errorf("derivative is %.2fs, want ~3.0s — the picture was truncated to "+
			"the audio, which is operator content silently discarded", got)
	}
}

// The mirror, which is the case a fix for the first one usually leaves behind.
//
// The mutation: remove the tpad filter and this fails.
func TestAnItemWithShortVideoKeepsAllOfItsAudio(t *testing.T) {
	dir := t.TempDir()
	src := buildSource(t, dir, sourceSpec{videoSecs: 1.0, audioSecs: 3.0})
	p := newTestProcessor(t, dir)

	if err := p.RunNormalise(context.Background(), normaliseJob(t, src), &recorder{}); err != nil {
		t.Fatalf("RunNormalise: %v", err)
	}
	got := probeDurationSecs(t, DerivativePath(dir, src))
	if got < 2.9 {
		t.Errorf("derivative is %.2fs, want ~3.0s — the audio was truncated to the video", got)
	}
}

// The duration written into a concat list must come from the ENCODED OUTPUT.
// A source-side estimate describes a file that no longer exists.
//
// The mutation: report params.DurationMS instead of the probed value and this
// fails, because the job carries zero.
func TestTheResultCarriesTheMeasuredOutputDuration(t *testing.T) {
	dir := t.TempDir()
	src := buildSource(t, dir, sourceSpec{videoSecs: 2.0, audioSecs: 2.0})
	p := newTestProcessor(t, dir)
	rep := &recorder{}

	if err := p.RunNormalise(context.Background(), normaliseJob(t, src), rep); err != nil {
		t.Fatalf("RunNormalise: %v", err)
	}
	res, ok := rep.result.(NormaliseResult)
	if !ok {
		t.Fatalf("result = %+v, want NormaliseResult", rep.result)
	}
	if res.DurationMS < 1900 || res.DurationMS > 2100 {
		t.Errorf("DurationMS = %d, want ~2000 measured from the encoded output", res.DurationMS)
	}
}

// A derivative from an older profile must NOT satisfy readiness, or B2 plays
// B1's unpadded files forever: DerivativePath is keyed on the upload's name and
// the enqueue path skips anything that already exists.
//
// The mutation: drop the version from DerivativePath and this fails, because
// the v1 file is found at the v2 path.
func TestAnOlderProfileVersionIsNotTheCurrentDerivative(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(DerivativeDir(dir), "show-1a2b.mp4.v1.ts")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("a v1 derivative"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DerivativePath(dir, "show-1a2b.mp4"); got == old {
		t.Fatal("the current derivative path collides with a v1 file")
	}
	if _, err := os.Stat(DerivativePath(dir, "show-1a2b.mp4")); !os.IsNotExist(err) {
		t.Error("a v1 derivative satisfied the current path; B2 would concatenate " +
			"unpadded B1 files while reporting the item ready")
	}
}

// An upload deleted while its normalisation is in flight must not have a
// derivative published afterwards -- that recreates the orphan the delete rule
// exists to remove, with no upload left to explain it.
//
// The mutation: remove the pre-publish re-check and this fails.
func TestAnUploadDeletedMidNormalisationPublishesNothing(t *testing.T) {
	dir := t.TempDir()
	src := buildSource(t, dir, sourceSpec{videoSecs: 1.0, audioSecs: 1.0})
	p := newTestProcessor(t, dir)
	// Delete the upload after the encode starts but before publication, which
	// is exactly the window an operator's DELETE lands in.
	p.beforePublish = func() { os.Remove(filepath.Join(dir, uploads.Dir, src)) }

	err := p.RunNormalise(context.Background(), normaliseJob(t, src), &recorder{})
	if err == nil {
		t.Fatal("publishing succeeded after the upload was deleted")
	}
	if !jobs.IsPermanent(err) {
		t.Errorf("err = %v, want permanent: an upload that is gone can never "+
			"be normalised, and a retryable error burns every queue attempt", err)
	}
	if _, statErr := os.Stat(DerivativePath(dir, src)); !os.IsNotExist(statErr) {
		t.Error("a derivative was published for an upload that no longer exists")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/playlistmedia/ -run 'ShortAudio|ShortVideo|MeasuredOutput|OlderProfile|DeletedMidNormalisation' -v`
Expected: all FAIL — `ProfileVersion` undefined, `DurationMS` not a field, no `beforePublish`.

- [ ] **Step 3: Add the profile version and versioned path**

```go
// ProfileVersion identifies what a derivative CONTAINS, and it is part of the
// derivative's filename rather than a sidecar.
//
// DerivativePath is keyed on the upload's name, and the enqueue path skips any
// upload whose derivative already exists. So without a version in the path, a
// change to the encode is invisible: every derivative written by the previous
// profile is silently reused while readiness reports the item ready. B2's
// padding and measured duration are exactly such a change, and B1's files have
// neither.
//
// Bump this whenever the encode changes what the output contains. Re-normalising
// is the cost; concatenating a file that predates the contract is the
// alternative.
const ProfileVersion = 2

func DerivativePath(dataDir, upload string) string {
	name := filepath.Base(db.PlaylistUploadName(upload))
	return filepath.Join(dataDir, Dir,
		fmt.Sprintf("%s.v%d%s", name, ProfileVersion, NormalisedExt))
}

// DerivativeGlob matches every version of one upload's derivative, which is what
// deletion has to remove: a version bump can leave more than one on disk.
func DerivativeGlob(dataDir, upload string) string {
	name := filepath.Base(db.PlaylistUploadName(upload))
	return filepath.Join(dataDir, Dir, name+".v*"+NormalisedExt)
}
```

- [ ] **Step 4: Replace truncation with padding**

In `normaliseArgs`, add filters so both streams reach the same length WITHOUT `-shortest`:

```go
// apad extends audio with silence, tpad holds the final video frame. Together
// they mean the output is as long as the LONGER stream.
//
// NOT -shortest, which was this design's first answer and is wrong: it ends the
// output when the SHORTEST stream ends, so a video-short item loses its trailing
// audio and an audio-short item loses picture. That is operator media discarded
// to tidy arithmetic.
//
// There is no common end instant to aim for -- a 30fps video frame is 33.3ms and
// an AAC frame at 48kHz is 21.3ms -- so the goal is not equality. It is that one
// duration describes the file, which the measured probe below supplies.
args = append(args, "-af", "apad", "-vf", "tpad=stop_mode=clone:stop_duration=0")
```

Keep `-shortest` ONLY on the synthesised-silence path, where the audio is generated to match the picture and there is no operator audio to lose. Add a comment saying so, or a later reader will delete it as an inconsistency.

- [ ] **Step 5: Measure the output duration and re-check before publishing**

```go
// ProbeOutputDurationMS reads the duration of a file this process just wrote.
//
// From the OUTPUT, never the source: the derivative has been re-encoded, padded
// and remuxed, and the source's duration describes a file that is no longer what
// plays. NormaliseParams.DurationMS is the source-side estimate and is not this.
func ProbeOutputDurationMS(ctx context.Context, ffprobe, path string) (int64, error) {
	// implementation: ffprobe -v error -show_entries format=duration
	// -of default=nw=1:nk=1 <path>, parsed to ms, rejecting <= 0.
}
```

In `RunNormalise`, immediately before `publish(partial, final)`:

```go
// The upload may have been deleted while this job ran. Publishing now would
// recreate the orphan that DELETE /media/{name} exists to prevent, with no
// upload left on disk to explain where the derivative came from.
//
// The check is HERE, at the last atomic step, rather than at dequeue: it closes
// the window without the queue needing to support cancellation.
if p.beforePublish != nil {
	p.beforePublish() // test seam only
}
if _, err := store.Resolve(params.Upload); err != nil {
	p.discard(partial)
	return jobs.Permanent(fmt.Errorf("upload %s was deleted while it was being "+
		"normalised; nothing was published", params.Upload))
}
```

Then set `res.DurationMS` from `ProbeOutputDurationMS`.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/playlistmedia/ -v`
Expected: PASS.

- [ ] **Step 7: Run every mutation and quote the output**

Five mutations, one per test, named in the test comments above. Each must fail. Restore after each and confirm `git diff --stat` is empty.

- [ ] **Step 8: Commit**

```bash
git add internal/playlistmedia/
git commit -m "feat(playlist): pad rather than truncate, measure, and version the profile"
```

---

## Task 3: One concat-list writer, with durations

**Files:**
- Create: `internal/ffmpeg/concat.go`, `internal/ffmpeg/concat_test.go`
- Modify: `internal/clipper/args.go` (delete the private `concatList`)

**Interfaces:**
- Produces: `func ConcatList(entries []ConcatEntry) string` and `type ConcatEntry struct { Path string; DurationMS int64 }`. A zero `DurationMS` omits the directive.
- Consumes: nothing.

- [ ] **Step 1: Write the failing test**

```go
// The quoting rule is the demuxer's, not the shell's: single quotes, and a path
// containing one must close, escape and reopen, because there is no backslash
// escape inside quotes. A list that gets this wrong parses as something else
// entirely.
//
// The mutation: drop the ReplaceAll and this fails.
func TestAPathContainingAQuoteStaysOnePath(t *testing.T) {
	got := ConcatList([]ConcatEntry{{Path: "/media/Tom's stream/a.ts"}})
	want := "file '/media/Tom'\\''s stream/a.ts'\n"
	if got != want {
		t.Errorf("ConcatList = %q, want %q", got, want)
	}
}

// A duration directive follows its file line, and is omitted when unknown --
// concat infers one in that case, and an inaccurate directive is worse than
// none.
//
// The mutation: emit "duration 0" for a zero and this fails.
func TestADurationIsWrittenAfterItsFileAndOmittedWhenUnknown(t *testing.T) {
	got := ConcatList([]ConcatEntry{
		{Path: "/a.ts", DurationMS: 2500},
		{Path: "/b.ts"},
	})
	want := "file '/a.ts'\nduration 2.500\nfile '/b.ts'\n"
	if got != want {
		t.Errorf("ConcatList = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/ffmpeg/ -run Concat -v`
Expected: FAIL, `undefined: ConcatList`.

- [ ] **Step 3: Implement, and delete the copy in clipper**

```go
// ConcatEntry is one line of a concat demuxer list.
type ConcatEntry struct {
	Path string
	// DurationMS overrides what the demuxer would infer. Zero omits the
	// directive, which is correct when nothing has measured the file:
	// FFmpeg's own estimate beats a number somebody guessed.
	DurationMS int64
}

// ConcatList renders the concat demuxer's list file.
//
// ONE implementation, in internal/ffmpeg, because internal/clipper and
// internal/playlistmedia both need it and neither can import the other. A
// private copy in each is how the four TrimSpace sites collapsed into
// db.PlaylistUploadName got there in the first place.
//
// Single quotes are the demuxer's own quoting, and a path containing one has to
// close, escape and reopen: there is no backslash escape inside quotes.
func ConcatList(entries []ConcatEntry) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString("file '")
		b.WriteString(strings.ReplaceAll(e.Path, "'", `'\''`))
		b.WriteString("'\n")
		if e.DurationMS > 0 {
			fmt.Fprintf(&b, "duration %.3f\n", float64(e.DurationMS)/1000)
		}
	}
	return b.String()
}
```

In `internal/clipper/args.go`, delete `concatList` and call `ffmpeg.ConcatList` with `DurationMS` zero for every entry — the clipper has never supplied durations and this task does not change that.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/ffmpeg/ ./internal/clipper/ -v`
Expected: PASS, including clipper's existing tests unchanged.

- [ ] **Step 5: Run both mutations, quote the failures, restore**

- [ ] **Step 6: Commit**

```bash
git add internal/ffmpeg/ internal/clipper/
git commit -m "refactor(ffmpeg): one concat-list writer, and it can carry durations"
```

---

## Task 4: The tier plays the whole list

**Files:**
- Modify: `internal/engine/engine.go`
- Test: `internal/engine/selector_playlist_test.go`

**Interfaces:**
- Consumes: `ffmpeg.ConcatList`, `ffmpeg.ConcatEntry`, `playlistmedia.DerivativePath`, `playlistmedia.ProfileVersion`.
- Produces: `playlistTier` gains `listPath string`.

**Existing test helpers you will use, with their real signatures:**

```go
func playlistEngine(t *testing.T) *Engine                                   // selector_playlist_test.go:33
func seedPlaylistUpload(t *testing.T, e *Engine, name string, normalised bool) // :50
func playlistEngineWithItems(t *testing.T, names ...string) (*Engine, db.Settings) // :88
```

`playlistEngine` calls `failoverEngine(t)` then replaces `e.cfg` with a fresh
`config.Config{DataDir: t.TempDir()}` and seeds `loop.mp4` and `other.mp4`.

**You must ADD one helper**, because no existing one sets the source id and the
two-source test below needs two engines that differ only in it:

```go
// playlistEngineWithSourceID is playlistEngine with a chosen sourceID, which is
// what the list filename has to vary on. Two engines from playlistEngine would
// share an id and the test would pass for the wrong reason.
func playlistEngineWithSourceID(t *testing.T, id int64) *Engine {
	t.Helper()
	e := playlistEngine(t)
	e.sourceID = id
	return e
}
```

Both engines in that test must ALSO get their own `DataDir`, or they share a
`playlist-media` directory and the filenames collide for a reason the test is not
about.

- [ ] **Step 1: Write the failing tests**

```go
// Readiness asks about the DERIVATIVE and no longer about the upload.
//
// B1 required both, because the argv named the upload and a deleted upload meant
// a respawn loop. The argv names the derivative now, so that reason has expired:
// playback is unaffected by the original's absence, and stopping a working
// playlist because a source file was tidied away punishes the operator for
// something the broadcast does not depend on. A missing upload is reported by
// the readiness endpoint instead.
//
// This REPLACES TestAnItemWhoseUploadWasDeletedStartsNoTier, which asserted the
// opposite. Delete that test; do not leave it passing with a new comment.
//
// The mutation: re-add an os.Stat on the resolved upload in playlistItemsReady
// and this fails.
func TestAPlaylistPlaysOnWhenOnlyTheOriginalUploadIsGone(t *testing.T) {
	e := playlistEngine(t)
	t.Cleanup(func() { teardownPlaylistTier(e) })
	seedPlaylistUpload(t, e, "kept.mp4", true)
	if err := os.Remove(filepath.Join(e.cfg.DataDir, uploads.Dir, "kept.mp4")); err != nil {
		t.Fatal(err)
	}

	s := playlistOnSettings()
	s.Failover.Playlist.Items = []db.PlaylistItem{{Upload: "kept.mp4"}}

	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	if h := e.playlistHub(); h == nil {
		t.Error("the playlist stopped because its ORIGINAL was deleted, though the " +
			"derivative it actually plays is still there")
	}
}

// A derivative written by an older profile is not ready.
//
// The mutation: compare only the base name and this fails.
func TestAStaleProfileDerivativeStartsNoTier(t *testing.T) {
	e := playlistEngine(t)
	t.Cleanup(func() { teardownPlaylistTier(e) })
	seedPlaylistUpload(t, e, "old.mp4", false) // upload only, no derivative
	stale := filepath.Join(playlistmedia.DerivativeDir(e.cfg.DataDir), "old.mp4.v1.ts")
	if err := os.WriteFile(stale, []byte("a v1 derivative"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := playlistOnSettings()
	s.Failover.Playlist.Items = []db.PlaylistItem{{Upload: "old.mp4"}}

	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	if h := e.playlistHub(); h != nil {
		t.Error("a derivative from an older profile started a tier; B2 would " +
			"concatenate an unpadded file with no measured duration")
	}
}

// Every path handed to -safe 0 must be one this process built. This is the
// boundary that makes the flag defensible, and until B2 there was no list for it
// to protect.
//
// The mutation: build the list from item.Upload joined to the data dir and this
// fails.
func TestEveryConcatPathIsADerivativePath(t *testing.T) {
	e := playlistEngine(t)
	t.Cleanup(func() { teardownPlaylistTier(e) })
	for _, n := range []string{"a.mp4", "b.mp4"} {
		seedPlaylistUpload(t, e, n, true)
	}

	s := playlistOnSettings()
	s.Failover.Playlist.Items = []db.PlaylistItem{{Upload: "a.mp4"}, {Upload: " b.mp4 "}}

	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	e.mu.RLock()
	tier := e.playlist
	e.mu.RUnlock()
	if tier == nil {
		t.Fatal("no tier started")
	}
	body, err := os.ReadFile(tier.listPath)
	if err != nil {
		t.Fatalf("read list: %v", err)
	}
	for _, want := range []string{
		playlistmedia.DerivativePath(e.cfg.DataDir, "a.mp4"),
		playlistmedia.DerivativePath(e.cfg.DataDir, "b.mp4"),
	} {
		if !strings.Contains(string(body), "file '"+want+"'") {
			t.Errorf("list does not name %s:\n%s", want, body)
		}
	}
	if strings.Contains(string(body), uploads.Dir) {
		t.Errorf("the list names something under the uploads directory, so -safe 0 "+
			"was given a path this process did not build:\n%s", body)
	}
}

// Two sources with IDENTICAL playlists hash the same, so the signature alone
// cannot own a filename: one tier stopping would sweep a list the other is still
// re-reading at its next wrap.
//
// The mutation: drop sourceID from the list filename and this fails.
func TestTwoSourcesWithTheSamePlaylistOwnDifferentLists(t *testing.T) {
	a := playlistEngineWithSourceID(t, 1)
	b := playlistEngineWithSourceID(t, 2)
	t.Cleanup(func() { teardownPlaylistTier(a); teardownPlaylistTier(b) })
	for _, e := range []*Engine{a, b} {
		seedPlaylistUpload(t, e, "same.mp4", true)
		s := playlistOnSettings()
		s.Failover.Playlist.Items = []db.PlaylistItem{{Upload: "same.mp4"}}
		e.selMu.Lock()
		e.reconcilePlaylist(s)
		e.selMu.Unlock()
	}
	if a.playlist.listPath == b.playlist.listPath {
		t.Fatalf("both sources own %s; stopping one deletes the other's list", a.playlist.listPath)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/engine/ -run 'OnlyTheOriginal|StaleProfile|ConcatPath|TwoSources' -v`
Expected: FAIL — `listPath` undefined, readiness still stats the upload.

- [ ] **Step 3: Change readiness to ask about derivatives**

In `playlistItemsReady`, replace the two-stat rule:

```go
// Runtime readiness asks ONE question: is there a derivative this profile
// produced? It no longer asks whether the upload survives.
//
// B1 required the upload because the argv NAMED it, so a deleted upload meant
// FFmpeg respawn-looping on a missing file. B2's argv names the derivative, and
// that reason expired with it. A missing upload is a configuration problem the
// readiness endpoint reports; it is not a reason to take a playing programme off
// air. Validation governs what may be SAVED, readiness governs what may go to
// AIR, and this is where they stop swapping jobs.
for i := range items {
	upload := db.PlaylistUploadName(items[i].Upload)
	if _, err := os.Stat(playlistmedia.DerivativePath(e.cfg.DataDir, upload)); err != nil {
		return false
	}
}
return true
```

Delete `TestAnItemWhoseUploadWasDeletedStartsNoTier`.

- [ ] **Step 4: Build the list and feed it**

Replace `playlistFeedArgs(path, outURL)` with `playlistFeedArgs(listPath, outURL)`:

```go
// -f concat over every item's derivative, looped as a whole.
//
// The list names DERIVATIVES, which is the point of B1: every entry shares
// codec, timebase, geometry and channel layout by construction, so -c copy
// across a seam is a copy and not a codec change.
//
// -safe 0 because the list holds absolute paths. They are paths this process
// built through playlistmedia.DerivativePath from a name uploads.SafeName
// already sanitised -- never operator text -- which is the whole reason items
// reference uploads rather than paths.
//
// Always concat, even for one item: a single-file special case would mean two
// argv shapes, two sets of seam behaviour, and a branch that is wrong in a way
// nobody notices.
func playlistFeedArgs(listPath, outURL string) []string {
	return []string{
		"-hide_banner", "-nostdin", "-loglevel", "warning",
		"-nostats", "-progress", "pipe:1",
		"-stream_loop", "-1",
		"-re",
		"-fflags", "+genpts",
		"-f", "concat", "-safe", "0", "-i", listPath,
		"-map", "0", "-c", "copy",
		"-f", "mpegts", "-flush_packets", "1",
		ffmpeg.RelayOutputURL(outURL),
	}
}
```

Write the list before spawning, at a path carrying BOTH the signature and the source:

```go
// The filename carries the signature AND this engine's source id.
//
// Signature alone fixes the different-list overwrite but not the identical-list
// case: two sources configured with the same playlist hash the same, so one
// tier stopping would delete a file the other is still re-reading at its next
// wrap. A tier deletes only the path it holds, and only once its process is
// gone.
listPath := filepath.Join(playlistmedia.DerivativeDir(e.cfg.DataDir),
	fmt.Sprintf("playlist-%d-%s.txt", e.sourceID, want))
```

Record `listPath` on `playlistTier`, and in the teardown block remove exactly that path after `proc.Stop` returns.

Populate `ConcatEntry.DurationMS` only if Task 1 measured that the directives are needed; otherwise pass zero and say why in a comment referencing Task 1's recorded numbers.

- [ ] **Step 5: Delete the `BUG.` comment**

It says the gate insists on a derivative that nothing reads. Something reads it now. Replace it with a note that the derivative IS the input and the original is no longer opened.

- [ ] **Step 6: Run the tests, and prove the goldens did not move**

Run: `go test ./internal/engine/ -v`
Run: `git status --porcelain internal/engine/testdata/`
Expected: tests PASS; testdata output EMPTY.

- [ ] **Step 7: Run all four mutations, quote each failure, restore**

- [ ] **Step 8: Commit**

```bash
git add internal/engine/
git commit -m "feat(engine): the playlist plays every item, from its derivatives"
```

---

## Task 5: Deletion cannot orphan or strand

**Files:**
- Modify: `internal/api/media.go`, `internal/api/playlist_normalise.go`
- Test: `internal/api/media_test.go`

**Interfaces:**
- Consumes: `playlistmedia.DerivativeGlob`.
- Produces: `func (s *Server) uploadIsReferenced(name string) (bool, int, error)` — reports whether a stored playlist item names it, and which index.

- [ ] **Step 1: Write the failing tests**

```go
// The in-use guard B1 deferred. Defensible now in a way it was not then: B1's
// lockout came from punishing an operator for state they could not edit, and B2
// gives them the control.
//
// The mutation: delete the uploadIsReferenced check and this returns 204.
func TestDeletingAnUploadAPlaylistNamesIsRefused(t *testing.T) {
	// save a playlist naming "used.ts", then DELETE /media/used.ts
	// want 409, body naming the item index
}

// A permitted deletion removes EVERY derivative version, not just the current
// profile's: a version bump can leave more than one on disk, and deleting the
// upload while orphaning them is the leak B1 carried.
//
// The mutation: remove only DerivativePath's exact name and the v1 file remains.
func TestAPermittedDeletionRemovesEveryDerivativeVersion(t *testing.T) {
	// seed unused.ts plus unused.ts.v1.ts and unused.ts.v2.ts
	// DELETE /media/unused.ts -> 204, and neither derivative remains
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/api/ -run 'DeletingAnUpload|EveryDerivativeVersion' -v`
Expected: FAIL — deletion currently succeeds and leaves derivatives.

- [ ] **Step 3: Implement the guard, the sweep and the reconcile**

`handleDeleteMedia` gains, in order: the reference check (409 naming the item), removal of `DerivativeGlob` matches, removal of the upload, then a reconcile.

**The reference check and the settings write must not interleave.** Take the same lock `handlePutSettings` holds across its validate-and-store, so a PUT cannot create the reference between this check and this delete. Add a comment saying that is what the lock is for — a lock whose purpose is not written down gets removed.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/api/ -v`
Expected: PASS.

- [ ] **Step 5: Run both mutations, quote, restore**

- [ ] **Step 6: Commit**

```bash
git add internal/api/
git commit -m "fix(api): deleting media cannot orphan a derivative or strand a playlist"
```

---

## Task 6: Per-item readiness, on its own endpoint

**Files:**
- Create: `internal/api/playlist_status.go`, `internal/api/playlist_status_test.go`
- Modify: `internal/api/api.go` (route)

**Interfaces:**
- Produces:

```go
type PlaylistItemStatus struct {
	Upload string `json:"upload"`
	State  string `json:"state"`  // "ready" | "transcoding" | "attention"
	Detail string `json:"detail,omitempty"`
}
type PlaylistStatus struct {
	Ready bool                 `json:"ready"`
	Items []PlaylistItemStatus `json:"items"`
}
```

Route: `r.Get("/failover/playlist", s.handlePlaylistStatus)`, beside the existing `r.Post("/failover/source", ...)`.

- [ ] **Step 1: Write the failing test**

```go
// Readiness is OBSERVED state and must not ride the settings blob.
//
// The settings document travels outward on every read and SettingsPage PUTs
// back what it GOT, so a derived read-only field in that payload is something
// the UI sends back as configuration -- which is how B1's lockout happened. Its
// own GET, for the same reason handlePutMQTTPassword has its own PUT.
//
// The mutation: add Items to settingsResponse and this fails.
func TestReadinessIsNotOnTheSettingsBlob(t *testing.T) {
	body := getJSON(t, srv, "/api/v1/settings")
	if strings.Contains(body, `"state"`) {
		t.Error("per-item readiness is on the settings payload, which the UI PUTs " +
			"back verbatim; it belongs on GET /failover/playlist")
	}
}

// A failed item must be NAMED. "The playlist is not on air" with eleven items
// and no indication which one is the silent-never-starts failure the spec calls
// the worst outcome, moved one layer up.
//
// The mutation: return a bare boolean and this fails.
func TestAnItemNeedingAttentionIsNamedWithAReason(t *testing.T) {
	// seed one ready item and one whose upload is gone
	// want items[1].State == "attention" and Detail mentioning the upload
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/api/ -run 'NotOnTheSettingsBlob|NeedingAttention' -v`
Expected: FAIL, route 404.

- [ ] **Step 3: Implement**

State per item: derivative present for the current profile → `ready`; an active `KindNormalise` job for that upload → `transcoding`; otherwise `attention`, with `Detail` naming the cause (upload missing, job failed, or not yet queued).

- [ ] **Step 4: Run, mutate, restore, commit**

```bash
git add internal/api/
git commit -m "feat(api): say which playlist item is not ready, and why"
```

---

## Task 7: The operator can build a playlist

**Files:**
- Create: `ui/src/components/PlaylistEditor.tsx`
- Modify: `ui/src/pages/SettingsPage.tsx`, `ui/src/lib/types.ts`, `internal/db/settings_drift_test.go`
- Test: `ui/tests/playlist-editor.spec.ts`

**Interfaces:**
- Consumes: `GET /api/v1/failover/playlist` → `PlaylistStatus`; `GET /api/v1/media` for the pickable uploads.
- Produces: `PlaylistSettings` and `PlaylistItem` in `types.ts`.

- [ ] **Step 1: Remove both skip-list entries and watch the guard fail**

Delete `"failover.playlist.enabled"` and `"failover.playlist.items.upload"` from the `skip` map in `internal/db/settings_drift_test.go`.

Run: `go test ./internal/db/ -run TestUITypesCanNameEverySettingsField -v`
Expected: FAIL, naming `failover.playlist.items.upload`.

**This failing guard is the task's specification.** The promise that the control lands in B2 has been recorded twice and corrected once; the guard passing with no exemption is the acceptance criterion.

- [ ] **Step 2: Add the types**

```ts
export interface PlaylistItem {
  upload: string;
}
export interface PlaylistSettings {
  enabled: boolean;
  items: PlaylistItem[];
}
```

Add `playlist?: PlaylistSettings` to `FailoverSettings`.

- [ ] **Step 3: Re-run the drift guard**

Run: `go test ./internal/db/ -run TestUITypesCanNameEverySettingsField -v`
Expected: PASS, with no skip entries.

- [ ] **Step 4: Build the editor**

`PlaylistEditor` renders the ordered list with each item's state from `GET /failover/playlist`, an add control listing uploads, remove, and reorder. Mount it inside the `draft.failover?.enabled &&` block in `SettingsPage`, beside the slate controls.

Follow the file's existing `setDraft({ ...draft, failover: { ...draft.failover, ... } })` shape exactly.

- [ ] **Step 5: Write the Playwright test**

```ts
// The operator must be able to CLEAR an item, which is what makes the deletion
// guard's 409 actionable rather than a dead end.
test("an operator can add, reorder and remove playlist items", async ({ page }) => {
  // add two uploads, assert order, drag to reorder, assert, remove one, save,
  // reload and assert it persisted
});

// An item that is not ready must SAY SO. Use toContainText carefully: it matches
// textContent and reads through display:none. Pass { useInnerText: true } when
// asserting on what is actually visible.
test("an item whose upload is missing is shown as needing attention", async ({ page }) => {
  // seed the broken state, assert the row names the reason
});
```

- [ ] **Step 6: Run everything**

Run: `cd ui && npx tsc --noEmit && npx oxlint && npm run build`
Run: `npx playwright test playlist-editor`
Run: `go test ./internal/db/`
Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add ui/ internal/db/settings_drift_test.go
git commit -m "feat(ui): an operator can build a playlist, and see why one is not ready"
```

---

## Task 8: Acceptance and verification

**Files:**
- Modify: `scripts/acceptance-failover.sh`, `scripts/acceptance_failover_driver.go`
- Modify: `docs/roadmap/PLAYLIST-AND-COMPOSITING.md`, `docs/SCHEDULED-BROADCAST.md`, `docs/ARCHITECTURE.md`

- [ ] **Step 1: Sequence real items, and stop hand-copying the derivative**

The suite currently does `cp data/uploads/filler.ts data/playlist-media/filler.ts.ts`, standing in for the transcode job. That stand-in is how B1's unwired job survived a green 18/18.

Build THREE filler clips of different lengths, enable a playlist naming all three, and assert the tier plays past the first item's duration — which is what proves sequencing rather than B1's play-item-0.

- [ ] **Step 2: Add the mismatched-publisher ratchet**

```bash
# The failure path B2 does NOT fix: items are normalised to match each other,
# never the operator's encoder. A 720p60 publisher against 1080p30 filler is a
# mid-stream codec change at every live<->playlist cut.
#
# The existing suite hides this by building filler that MATCHES the publisher on
# purpose. This case does the opposite, and pins the result: the count is not
# zero and B2 does not claim it will be. What is guaranteed is that it cannot
# silently get worse.
EXPECTED_MISMATCH_RESTARTS=<measured>
```

Fail if the observed count EXCEEDS the pinned value. Record the measured number in the commit message.

- [ ] **Step 3: Correct the documents this design makes false**

- `PLAYLIST-AND-COMPOSITING.md`: record that item-boundary resume was considered and rejected, with the reasoning, so the old text does not reintroduce it.
- `SCHEDULED-BROADCAST.md`: it describes a single-file playlist.
- `ARCHITECTURE.md`: add `playlistmedia/` to the package tree.

- [ ] **Step 4: Run every gate, in CI's order**

```bash
gofmt -l ./cmd ./internal
go build ./...
go vet ./...
go test -race -timeout 15m ./...
git status --porcelain internal/engine/testdata/   # MUST be empty
make build                                          # BEFORE the suites
./scripts/acceptance-failover.sh
./scripts/acceptance-synth.sh
```

- [ ] **Step 5: Commit**

```bash
git add scripts/ docs/
git commit -m "test(acceptance): sequence real items, and pin the mismatch we do not fix"
```

---

## What is NOT covered

**Item-boundary resume.** Decided against in the spec, with reasoning. An edit therefore restarts the playlist from item 1.

**The operator toggle** OBS and vMix ship. Out of scope; it needs both behaviours plus a settings key, a control and its own acceptance case.

**The ingest mismatch.** Measured, not fixed. Fixing it means constraining the ingest or re-encoding at the selector, and the latter reverses a decision made throughout the engine.

**`NormaliseParams.DurationMS`** stays unpopulated. B2 measures the OUTPUT duration, which is a different quantity.

## Self-Review

**Spec coverage.** Sequencing → Task 4. The concat writer and durations → Task 3. Padding, measured duration, profile version, in-flight deletion → Task 2. `-safe 0` → Task 4 Step 1. Deletion rules → Task 5. Readiness endpoint → Task 6. UI and the skip list → Task 7. Timestamp contract → Task 1 measures it, Task 3 acts on it. Ratchet and docs → Task 8.

**Placeholders.** Tasks 5, 6 and 7 carry test intent plus exact assertions rather than full bodies for the HTTP and React work, because those depend on helpers already in `internal/api` and `ui/tests`. Every code step that introduces a NEW rule carries its actual code.

**Type consistency.** `ProfileVersion`, `DerivativePath`, `DerivativeGlob`, `ProbeOutputDurationMS`, `ConcatEntry{Path,DurationMS}`, `ConcatList`, `playlistTier.listPath`, `PlaylistStatus`/`PlaylistItemStatus` are spelled identically everywhere they appear.

**Ordering.** Task 1 gates Task 3's duration decision. Task 2 must precede Task 4, which reads the versioned path. Task 6 precedes Task 7, which consumes its payload.
