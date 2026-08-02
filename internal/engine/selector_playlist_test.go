package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/playlistmedia"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// The playlist tier is the backup listener with a file where the socket is: a
// supervised process publishing into a hub of ITS OWN, ready long before the
// selector asks for it.
//
// The hub is the point. A file played into the primary's hub puts bytes on it,
// the primary therefore reads live, and failover never switches away from a live
// primary -- so a file on air would have quietly disabled the entire feature.
// These tests are about the tier existing, delivering and being torn down.

// playlistEngine is failoverEngine with a data directory, which the playlist
// needs and nothing else in that helper does.
//
// It seeds the uploads playlistOnSettings names AND their normalised
// derivatives, because since the readiness gate a tier does not start until
// every item has one. An engine with a bare data directory would start nothing,
// and every "the tier came up" assertion below would be passing or failing for
// a reason it does not name.
func playlistEngine(t *testing.T) *Engine {
	t.Helper()
	e := failoverEngine(t)
	e.cfg = config.Config{DataDir: t.TempDir()}
	seedPlaylistUpload(t, e, "loop.mp4", true)
	// The respawn test edits the list to this one, and an item that never
	// became ready would look exactly like a tier that refused to respawn.
	seedPlaylistUpload(t, e, "other.mp4", true)
	return e
}

// seedPlaylistUpload writes the stored upload `name`, and its normalised
// derivative only when `normalised` is true.
//
// An upload WITHOUT a derivative is the state this whole gate exists for: the
// operator's file has landed, the normalisation job has been queued, and it has
// not finished. Nothing else distinguishes the two states on disk.
func seedPlaylistUpload(t *testing.T, e *Engine, name string, normalised bool) {
	t.Helper()

	uploadsDir := filepath.Join(e.cfg.DataDir, uploads.Dir)
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		t.Fatalf("mkdir uploads dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(uploadsDir, name), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed upload %q: %v", name, err)
	}
	if !normalised {
		return
	}

	// Through playlistmedia.DerivativePath rather than a join of our own, so a
	// test cannot go on passing against a directory or extension the package
	// has since moved away from.
	derivative := playlistmedia.DerivativePath(e.cfg.DataDir, name)
	if err := os.MkdirAll(filepath.Dir(derivative), 0o755); err != nil {
		t.Fatalf("mkdir derivative dir: %v", err)
	}
	if err := os.WriteFile(derivative, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed derivative for %q: %v", name, err)
	}
}

// playlistEngineWithItems is playlistEngine, plus the settings that name the
// given uploads as the playlist, in order.
//
// Every name is stored. A name containing "transcoding" is stored WITHOUT its
// derivative, which is how a caller says "this item's normalisation job has not
// finished" without the helper needing a parallel list of booleans nobody could
// read at the call site.
//
// The settings are RETURNED rather than written onto the engine, because that
// is how reconcilePlaylist is reached in production: Reconcile passes the
// document down through reconcileOutputs and reconcileSelector, and the gate
// reads the list it was handed.
func playlistEngineWithItems(t *testing.T, names ...string) (*Engine, db.Settings) {
	t.Helper()
	e := playlistEngine(t)

	s := playlistOnSettings()
	items := make([]db.PlaylistItem, 0, len(names))
	for _, name := range names {
		seedPlaylistUpload(t, e, name, !strings.Contains(name, "transcoding"))
		items = append(items, db.PlaylistItem{Upload: name})
	}
	s.Failover.Playlist.Items = items
	return e, s
}

// playlistOnSettings enables the feature and the tier with a usable file.
func playlistOnSettings() db.Settings {
	s := failoverOnSettings()
	s.Failover.Playlist.Enabled = true
	s.Failover.Playlist.Items = []db.PlaylistItem{{Upload: "loop.mp4"}}
	return s
}

// TestAPlaylistWithAnUnnormalisedItemIsNotOffered drives reconcilePlaylist,
// which is what production calls, and asserts on the CONSEQUENCE.
//
// The same rule sub-project A established for a tier that is running but
// delivering nothing: a candidate is offered only when it would actually
// deliver. Offering a playlist whose item is still transcoding would put a
// source on air that cannot play, and it would outrank the slate -- the one
// thing that exists so an operator never sees nothing.
//
// "Not offered" is spelled here as no tier and no hub, deliberately, because
// that is the only way the playlist can be unavailable. There is no readiness
// flag for the selector to read: chooseSource sees one boolean, sampled from
// the playlist hub's byte counter, and a tier that never started has no hub to
// put bytes on. Asserting on a helper's return value instead would prove the
// rule computes and prove nothing about the call site -- which is how this
// project has been bitten twice, once by a test that called reconcilePlaylist
// and sampleSources in an order production never runs, and once by a settings
// field that satisfied a UI guard matching on a leaf name.
func TestAPlaylistWithAnUnnormalisedItemIsNotOffered(t *testing.T) {
	e, s := playlistEngineWithItems(t, "ready.mp4", "still-transcoding.mp4")

	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	if h := e.playlistHub(); h != nil {
		t.Error("a playlist with an unnormalised item was offered: the tier started, so " +
			"its hub carries bytes, playlistRunning goes true, and the selector puts a " +
			"source on air that cannot play in preference to the slate")
	}
	e.mu.RLock()
	tier := e.playlist
	e.mu.RUnlock()
	if tier != nil {
		t.Error("a playlist tier was recorded on the engine for an unnormalised item")
	}
}

// TestAPlaylistWhoseItemsAreAllNormalisedIsOffered is the other direction, and
// it is not decoration: a gate that never opens is a playlist feature that
// never goes on air at all, and the test above cannot tell that apart from the
// gate working.
func TestAPlaylistWhoseItemsAreAllNormalisedIsOffered(t *testing.T) {
	e, s := playlistEngineWithItems(t, "first.mp4", "second.mp4")
	t.Cleanup(func() { teardownPlaylistTier(e) })

	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	if e.playlistHub() == nil {
		t.Error("a playlist whose every item is normalised was refused; nothing would " +
			"ever put the file on air")
	}
}

// TestAPlaylistStartsOnceItsLastDerivativeAppears answers the re-evaluation
// question in code: readiness is re-read on every reconcile and nothing is
// latched, so a normalisation job finishing is picked up by the next one.
// Nothing polls the filesystem, and nothing needs to -- the alternative was a
// watcher whose only job would be to notice a file that the reconcile after it
// would have noticed anyway.
//
// This test proves the NO-LATCH half only, which is all a test in this package
// can prove: it calls reconcilePlaylist twice by hand. What makes the second
// call happen in production is cmd/polyemesis/postprod.go's queue change hook,
// covered by TestAFinishedNormalisationReconcilesTheEngine there. The two are
// deliberately separate -- for a while the hook did not exist and this test
// still passed, which is precisely how a comment comes to describe a mechanism
// nothing implements.
func TestAPlaylistStartsOnceItsLastDerivativeAppears(t *testing.T) {
	e, s := playlistEngineWithItems(t, "ready.mp4", "still-transcoding.mp4")
	t.Cleanup(func() { teardownPlaylistTier(e) })

	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()
	if e.playlistHub() != nil {
		t.Fatal("the tier started while an item was unready, so what follows would prove nothing")
	}

	// The normalisation job finishes. No settings change, no signature change.
	seedPlaylistUpload(t, e, "still-transcoding.mp4", true)

	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()
	if e.playlistHub() == nil {
		t.Error("the playlist never started after its last derivative appeared; a tier " +
			"refused once and never retried is a playlist that silently stays off air")
	}
}

// TestAnItemWhoseDerivativeIsMissingStartsNoTier covers the DERIVATIVE branch
// through reconcilePlaylist: a name that is a perfectly good upload name and
// has simply never been normalised.
//
// It does NOT exercise the confinement check. uploads.Store.Resolve is a SHAPE
// check -- it refuses "", ".", ".." and anything carrying a separator, and
// confines what is left to the uploads directory -- but it never asks whether
// the file exists. "no-such-upload.mp4" therefore RESOLVES happily and is
// refused one line later, at the derivative. That distinction is the whole
// reason TestPlaylistItemsReadyRefusesAnUnresolvableName exists separately: an
// earlier version of this file claimed this test covered Resolve, and the
// confinement check could have been deleted with both tests still green.
func TestAnItemWhoseDerivativeIsMissingStartsNoTier(t *testing.T) {
	e := playlistEngine(t)
	s := playlistOnSettings()
	s.Failover.Playlist.Items = []db.PlaylistItem{{Upload: "no-such-upload.mp4"}}

	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	if h := e.playlistHub(); h != nil {
		t.Error("a playlist naming an upload with no derivative started a tier")
	}
}

// TestAnItemWhoseUploadWasDeletedStartsNoTier covers the UPLOAD branch, which
// is a different question from the derivative and became reachable the moment
// uploads got a delete endpoint.
//
// DELETE /media/uploads/{name} removes the upload and leaves the derivative
// standing -- nothing sweeps derivatives yet. reconcilePlaylist plays the
// UPLOAD (the derivative is a readiness token until B2's concat arrives), so a
// gate that asked only about the derivative would open on a file that is gone:
// FFmpeg respawn-loops on a missing input, the hub carries nothing, the slate
// wins on the byte counter, and the operator is left with a crash-looping child
// and no explanation. An earlier version of playlistItemsReady's docstring
// claimed that state was ready ON PURPOSE and that "the tier would still play".
//
// The mutation: delete the os.Stat on the resolved upload in
// playlistItemsReady and this fails, because the derivative below is
// deliberately left in place so nothing else can refuse.
func TestAnItemWhoseUploadWasDeletedStartsNoTier(t *testing.T) {
	e := playlistEngine(t)
	t.Cleanup(func() { teardownPlaylistTier(e) })
	seedPlaylistUpload(t, e, "deleted.mp4", true)

	s := playlistOnSettings()
	s.Failover.Playlist.Items = []db.PlaylistItem{{Upload: "deleted.mp4"}}

	// The operator deletes the upload. Only the upload: handleDeleteMedia
	// touches nothing under playlist-media/.
	if err := os.Remove(filepath.Join(e.cfg.DataDir, uploads.Dir, "deleted.mp4")); err != nil {
		t.Fatalf("remove upload: %v", err)
	}
	if _, err := os.Stat(playlistmedia.DerivativePath(e.cfg.DataDir, "deleted.mp4")); err != nil {
		t.Fatalf("the derivative is not there, so the derivative branch could be the "+
			"reason this test passes: %v", err)
	}

	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	if h := e.playlistHub(); h != nil {
		t.Error("a playlist whose upload was deleted started a tier; the argv names the " +
			"upload, so FFmpeg would respawn-loop on a file that is gone while the " +
			"process reported healthy")
	}
}

// TestAppendingAnUnnormalisedItemLeavesARunningPlaylistOnAir is about the ORDER
// of the readiness gate and the teardown, which is the difference between a
// gate and an outage.
//
// The teardown used to run first. An operator who appended a second item to a
// playlist that was ON AIR moved the signature, which tore the tier down, and
// then the gate refused to bring it back because the new item had not been
// normalised yet -- so the cost of adding an item was dead air, for an item B1
// would not have played anyway, lasting until something unrelated reconciled.
//
// The mutation: move the readiness check back below the teardown block in
// reconcilePlaylist and this fails, because the running tier is gone.
func TestAppendingAnUnnormalisedItemLeavesARunningPlaylistOnAir(t *testing.T) {
	e, s := playlistEngineWithItems(t, "on-air.mp4")
	t.Cleanup(func() { teardownPlaylistTier(e) })

	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	e.mu.RLock()
	before := e.playlist
	e.mu.RUnlock()
	if before == nil {
		t.Fatal("the playlist never started, so what follows would prove nothing")
	}

	// The operator appends a second item. It has landed in uploads but its
	// normalisation job has not finished, which is the ordinary state of an
	// item seconds after it was added.
	seedPlaylistUpload(t, e, "just-uploaded.mp4", false)
	s.Failover.Playlist.Items = append(s.Failover.Playlist.Items,
		db.PlaylistItem{Upload: "just-uploaded.mp4"})

	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	e.mu.RLock()
	after := e.playlist
	e.mu.RUnlock()
	if after == nil {
		t.Fatal("appending an unnormalised item took a running playlist off air; the " +
			"programme that was playing was perfectly playable and is now dead air")
	}
	if after != before {
		t.Error("the running tier was replaced rather than left alone, so the file " +
			"restarted from its first frame for an item that will not be played")
	}

	// And when the transcode lands, the tier moves to the new list: refusing
	// must not latch, or the append would never take effect at all.
	seedPlaylistUpload(t, e, "just-uploaded.mp4", true)
	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	e.mu.RLock()
	third := e.playlist
	e.mu.RUnlock()
	if third == nil || third == before {
		t.Error("the playlist never moved onto the new list after the derivative " +
			"appeared; the refusal latched")
	}
}

// TestPlaylistItemsReadyRefusesAnUnresolvableName is the CONFINEMENT check, and
// it is built so that only that check can be the reason it passes.
//
// The trick is the seeding below. ".." is refused by uploads.Store.Resolve
// outright, but it also has no derivative, so a test that stopped there would
// be satisfied by either branch -- and would still pass with the Resolve call
// deleted, which is exactly the hole this replaces. Writing a real file at the
// derivative path takes the os.Stat branch out of the running and leaves
// Resolve as the only thing that can refuse.
//
// THE MUTATION IS NOW A SUBSTITUTION RATHER THAN A DELETION, and it is the more
// honest one: replace `store.Resolve(upload)` with a naive
// `filepath.Join(e.cfg.DataDir, uploads.Dir, upload)` -- which is what this
// code did before uploads had a store -- and this fails. ".." then joins to the
// data directory itself, which exists, so the upload stat passes, the seeded
// derivative passes, and a traversal is called ready. Simply neutering the
// error branch no longer works as a mutation because Resolve returns an empty
// path with its error and nothing stats "" successfully; a mutation has to be
// the regression that could really happen, and that is the join.
//
// The boundary matters beyond tidiness: resolving through the store rather than
// joining strings is what keeps items upload NAMES rather than paths, and it is
// what makes FFmpeg's -safe 0 defensible when the concat list arrives. A
// deleted confinement check is a directory traversal, and it should not be
// possible for that deletion to leave a green suite.
func TestPlaylistItemsReadyRefusesAnUnresolvableName(t *testing.T) {
	e := playlistEngine(t)

	// DerivativePath takes filepath.Base of the trimmed name, so ".." lands on
	// the real, ordinary file "...ts" inside the derivative directory. Seeding
	// it is legitimate rather than a contrivance: it is precisely the state an
	// attacker-shaped name would be in if the traversal had already written
	// something there.
	derivative := playlistmedia.DerivativePath(e.cfg.DataDir, "..")
	if err := os.MkdirAll(filepath.Dir(derivative), 0o755); err != nil {
		t.Fatalf("mkdir derivative dir: %v", err)
	}
	if err := os.WriteFile(derivative, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed derivative: %v", err)
	}
	if _, err := os.Stat(derivative); err != nil {
		t.Fatalf("the derivative was not seeded, so the stat branch could still be "+
			"the reason this test passes: %v", err)
	}

	if e.playlistItemsReady([]db.PlaylistItem{{Upload: ".."}}) {
		t.Error("an item that does not resolve to a stored upload was called ready; " +
			"its derivative exists, so nothing but uploads.Store.Resolve can refuse it")
	}
}

// teardownPlaylistTier stops whatever tier a test left running. The supervised
// child is a binary that does not exist, so it would otherwise sit in the
// restart backoff for the rest of the package's run.
func teardownPlaylistTier(e *Engine) {
	e.mu.RLock()
	tier := e.playlist
	e.mu.RUnlock()
	e.teardownPlaylist(tier)
}

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

// TestThePlaylistInputFlagsPrecedeTheInput is the half of the argv contract the
// flags test cannot see: -stream_loop and -re are INPUT options, and FFmpeg
// applies an input option to whatever -i follows it. Placed after -i they parse
// without complaint and apply to nothing, so the file plays once, at disk speed,
// and the tier looks like it worked.
func TestThePlaylistInputFlagsPrecedeTheInput(t *testing.T) {
	args := playlistFeedArgs("/data/loop.mp4", "udp://127.0.0.1:9000")
	idx := func(flag string) int {
		for i, a := range args {
			if a == flag {
				return i
			}
		}
		t.Fatalf("playlist argv has no %q: %v", flag, args)
		return -1
	}
	input := idx("-i")
	for _, flag := range []string{"-stream_loop", "-re"} {
		if idx(flag) > input {
			t.Errorf("%s comes after -i, so FFmpeg applies it to no input: %v", flag, args)
		}
	}
}

// TestThePlaylistPathIsPinnedToTheFileProtocol covers a path FFmpeg would
// otherwise re-read as a protocol name: "data/2026:01.ts" names no protocol we
// meant, and the failure is an unopenable input rather than anything obvious.
func TestThePlaylistPathIsPinnedToTheFileProtocol(t *testing.T) {
	args := playlistFeedArgs("/data/2026:01.ts", "udp://127.0.0.1:9000")
	for i, a := range args {
		if a == "-i" && args[i+1] != "file:/data/2026:01.ts" {
			t.Errorf("playlist input is %q, want it pinned to the file protocol", args[i+1])
		}
	}
}

// TestAPlaylistUploadWithSurroundingWhitespaceStillResolves guards
// playlistItemUpload as the single trim point: playlistSig hashes a trimmed
// name and reconcilePlaylist resolves through uploads.Store.Resolve, which
// does NOT reject a stray space (it is not a separator) -- it just includes
// it literally in the path. Before playlistItemUpload existed, resolution had
// lost the trim that the code it replaced carried, so a leading space
// validated, hashed as though trimmed, respawned looking like it should
// work, and then resolved to a file that was never the one an operator
// meant. This proves the item still resolves to the REAL, trimmed path.
func TestAPlaylistUploadWithSurroundingWhitespaceStillResolves(t *testing.T) {
	e := playlistEngine(t)
	t.Cleanup(func() { teardownPlaylistTier(e) })

	// The upload must actually exist for the assertion below to mean
	// anything: Resolve succeeding is not the same as resolving to the right
	// file, and only checking the tier "started" cannot tell those apart.
	uploadsDir := filepath.Join(e.cfg.DataDir, uploads.Dir)
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		t.Fatalf("mkdir uploads dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(uploadsDir, "loop.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed upload file: %v", err)
	}

	s := playlistOnSettings()
	s.Failover.Playlist.Items = []db.PlaylistItem{{Upload: " loop.mp4"}}

	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	if e.playlistHub() == nil {
		t.Fatal("the playlist tier did not start")
	}
	e.mu.RLock()
	tier := e.playlist
	e.mu.RUnlock()
	if tier == nil {
		t.Fatal("no playlist tier recorded after reconcile")
	}

	want := "file:" + filepath.Join(uploadsDir, "loop.mp4")
	args := tier.proc.Args()
	found := false
	for i, a := range args {
		if a != "-i" || i+1 >= len(args) {
			continue
		}
		found = true
		if args[i+1] != want {
			t.Errorf("playlist input = %q, want %q -- a leading space in the "+
				"upload name must not reach the resolved path", args[i+1], want)
		}
	}
	if !found {
		t.Fatal("playlist argv has no -i flag")
	}
}

func TestPlaylistHubIsNilWhenNoPlaylistRuns(t *testing.T) {
	e := playlistEngine(t)
	// The tier is off by default, and every caller asks before knowing whether
	// it is running. A panic here would be a nil check moved into each of them.
	if h := e.playlistHub(); h != nil {
		t.Errorf("playlistHub() = %v with no playlist tier, want nil", h)
	}
}

func TestThePlaylistStartsWithItsOwnHubAndStopsWhenDisabled(t *testing.T) {
	e := playlistEngine(t)
	s := playlistOnSettings()

	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	hub := e.playlistHub()
	if hub == nil {
		t.Fatal("the playlist tier did not start")
	}
	// Its OWN hub, never the ingest's: bytes on the primary's relay are what
	// make the primary read live, and a file on air must not do that.
	if hub == e.hub {
		t.Error("the playlist published into the PRIMARY's hub, which would make " +
			"the primary read live and silently disable failover")
	}

	s.Failover.Playlist.Enabled = false
	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	if h := e.playlistHub(); h != nil {
		t.Error("the playlist tier is still running after being disabled")
	}
	e.mu.RLock()
	tier := e.playlist
	e.mu.RUnlock()
	if tier != nil {
		t.Error("the playlist tier was left on the engine after being torn down")
	}
}

// TestASaveThatDoesNotTouchThePlaylistDoesNotRespawnIt is the no-op reconcile,
// and it is the reason reconcilePlaylist compares signatures at all.
//
// A respawn is VISIBLE: the hub goes quiet while FFmpeg reopens the file, and
// the loop restarts from the top, so an operator saving an unrelated setting
// sees the programme jump back to its first frame. The hub pointer changing is
// worse still -- everything the selector subscribes to a playlist with would be
// reading a relay that had closed.
func TestASaveThatDoesNotTouchThePlaylistDoesNotRespawnIt(t *testing.T) {
	e := playlistEngine(t)
	t.Cleanup(func() { teardownPlaylistTier(e) })
	s := playlistOnSettings()

	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	e.mu.RLock()
	first := e.playlist
	e.mu.RUnlock()
	if first == nil {
		t.Fatal("the playlist tier did not start")
	}

	// A save that changes something else entirely, which is what nearly every
	// save is.
	s.Recording.Enabled = !s.Recording.Enabled
	s.Failover.GraceSeconds = 9
	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	e.mu.RLock()
	second := e.playlist
	e.mu.RUnlock()
	if second != first {
		t.Fatal("an unrelated settings save respawned the playlist; the programme " +
			"would jump back to its first frame every time anything is edited")
	}
	if second.proc != first.proc || second.hub != first.hub {
		t.Error("the playlist tier kept its slot but replaced its process or hub")
	}

	// The file, on the other hand, IS in the argv, so changing it must respawn.
	s.Failover.Playlist.Items = []db.PlaylistItem{{Upload: "other.mp4"}}
	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	e.mu.RLock()
	third := e.playlist
	e.mu.RUnlock()
	if third == nil {
		t.Fatal("changing the file stopped the playlist instead of respawning it")
	}
	if third.hub == first.hub {
		t.Error("changing the playlist file left the old process running; the file " +
			"reaches an argv, so it cannot be applied to a running child")
	}
}

// TestAnUnusablePlaylistItemStopsTheTier guards playlistSig's
// PlaylistFileProblem check, and it is written against a RUNNING tier because
// that is the only state in which that line has an observable of its own.
//
// This test used to start from cold and assert no tier appeared, which proved
// nothing: readiness refuses "../../etc/shadow" as well, so deleting the
// PlaylistFileProblem check from playlistSig left it green. It was the fourth
// guard in this sub-project that passed either way.
//
// The signature is where the difference lives. An unusable list hashes EMPTY,
// and an empty signature means STOP -- teardown, unconditionally, ahead of the
// readiness gate. A non-empty signature for the same list would instead reach
// the readiness gate, which refuses without tearing anything down (see
// TestAppendingAnUnnormalisedItemLeavesARunningPlaylistOnAir for why it must),
// and the tier would go on playing under a configuration the operator has
// replaced with one that is not allowed to run at all.
//
// The mutation: delete the `if p.PlaylistFileProblem() != nil { return "" }`
// block from playlistSig and this fails with the tier still on air.
func TestAnUnusablePlaylistItemStopsTheTier(t *testing.T) {
	e, s := playlistEngineWithItems(t, "on-air.mp4")
	t.Cleanup(func() { teardownPlaylistTier(e) })

	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()
	if e.playlistHub() == nil {
		t.Fatal("the playlist never started, so what follows would prove nothing")
	}

	// A path where an upload name belongs. Unreachable through the API, which
	// validates it away -- this is the defence behind that, and the reason
	// items are names rather than paths at all.
	s.Failover.Playlist.Items = []db.PlaylistItem{{Upload: "../../etc/shadow"}}
	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	if h := e.playlistHub(); h != nil {
		t.Error("a playlist item that escapes the uploads directory left the tier " +
			"running; the confinement check is decorative if an unusable list is " +
			"treated as merely not-ready-yet")
	}
	e.mu.RLock()
	tier := e.playlist
	e.mu.RUnlock()
	if tier != nil {
		t.Error("a playlist tier was still recorded for an unusable item")
	}
}

// TestARunningPlaylistThatHasDeliveredNothingIsNotPutOnAir is why
// playlistRunning is sampled from BYTES rather than from "the tier is up".
//
// PlaylistFileProblem checks that an operator's path is confined to the data
// directory; it does not check that the path names a file. A confined path that
// names nothing therefore passes validation, playlistSig is non-empty, the tier
// starts, and the process fails to open its input and backs off. Here the tier
// is up for the whole test and its hub never carries a byte -- which is the same
// state -- and the selector must not offer it. Ranking a candidate above the
// slate is a promise that it would deliver; the slate exists so that an operator
// never sees nothing, and a playlist that cannot deliver must not displace it.
func TestARunningPlaylistThatHasDeliveredNothingIsNotPutOnAir(t *testing.T) {
	e := playlistEngine(t)
	s := playlistOnSettings()

	// Not under selMu: reconcileSelector takes it itself, which is the whole
	// difference between it and reconcilePlaylist.
	e.reconcileSelector(s, wantSelector(s), "")
	t.Cleanup(func() {
		e.selMu.Lock()
		defer e.selMu.Unlock()
		if e.sel != nil {
			e.teardownFeed(e.sel.feed)
			if e.sel.hub != nil {
				_ = e.sel.hub.Close()
			}
		}
		e.teardownPlaylist(e.playlist)
		e.teardownBackup(e.backup)
	})

	if e.playlistHub() == nil {
		t.Fatal("reconcileSelector did not bring up the playlist tier")
	}
	e.mu.RLock()
	active := e.sel.active
	e.mu.RUnlock()
	if active == sourcePlaylist {
		t.Error("the selector put a playlist on air that has never delivered a byte; " +
			"sampling the tier's existence rather than its hub offers a candidate " +
			"that can never carry the broadcast")
	}
}

// TestDisablingAPlaylistThatIsOnAirHandsTheSlateTheStreamImmediately is the
// operator action the tier is most likely to meet: untick "playlist" while the
// file is what viewers are watching.
//
// It goes through reconcileSelector, and THAT IS THE TEST. An earlier version of
// this test called reconcilePlaylist and then sampleSources by hand and asserted
// on the liveness, and it passed against code that produced two seconds of dead
// air on exactly this action -- because production never runs that pair in that
// order. reconcileSelector reconciles the tier and then DECIDES, with no sample
// between the two, so the decision reads whatever the teardown left behind. A
// test that inserts the missing sample itself proves a sequence that does not
// occur, and it is the second guard on this branch to pass that way: the first
// was a settings field that satisfied a UI-nameability check because the check
// matched on a leaf name rather than the path an operator actually sees. Both
// have the same shape -- the assertion was true, and it was not the claim. Drive
// the production entry point, assert on what is ON AIR, and the guard can only
// pass for the reason it names.
//
// What must happen: the tier goes, the playlist stops being a candidate in the
// same breath, and the slate takes the stream. What must NOT happen: the
// selector holds a playlist whose hub it has just closed, startFeed answers with
// "the playlist source has no relay to read", and nothing is on air until the
// failed-start backoff expires.
func TestDisablingAPlaylistThatIsOnAirHandsTheSlateTheStreamImmediately(t *testing.T) {
	e := playlistEngine(t)
	s := playlistOnSettings()

	e.reconcileSelector(s, wantSelector(s), "")
	t.Cleanup(func() {
		e.selMu.Lock()
		defer e.selMu.Unlock()
		if e.sel != nil {
			e.teardownFeed(e.sel.feed)
			if e.sel.hub != nil {
				_ = e.sel.hub.Close()
			}
		}
		e.teardownPlaylist(e.playlist)
		e.teardownBackup(e.backup)
	})

	// No encoder has ever connected and the backup is off, so the playlist
	// delivering is all it takes to put it on air -- the same route a real
	// deployment takes when a file covers an outage.
	now := time.Now()
	e.deliver(sourcePlaylist, now)
	e.step(s, now)

	e.mu.RLock()
	active := e.sel.active
	e.mu.RUnlock()
	if active != sourcePlaylist {
		t.Fatalf("the playlist is not on air (active=%s), so disabling it below would prove nothing", active)
	}

	// The operator unticks it. reconcileSelector is what a settings save calls,
	// and the signature has not moved -- failover is still enabled -- so this
	// takes the "already running" window: reconcile the tier, then decide.
	off := s
	off.Failover.Playlist.Enabled = false
	e.reconcileSelector(off, wantSelector(off), "")

	if h := e.playlistHub(); h != nil {
		t.Fatal("the playlist tier survived being disabled")
	}

	e.mu.RLock()
	active, feed := e.sel.active, e.sel.feed
	e.mu.RUnlock()

	if active == sourcePlaylist {
		t.Error("the selector held the playlist through the save that tore its tier down; " +
			"it is now feeding from a hub that has been closed, and every destination " +
			"is on dead air until the failed-start backoff lets the slate in")
	}
	if active != sourceSlate {
		t.Errorf("active = %s after the playlist was disabled, want %s: the slate is what "+
			"holds the stream when nothing else can deliver", active, sourceSlate)
	}
	if feed == nil {
		t.Error("nothing is on air after the playlist was disabled; a source that cannot " +
			"be fed must be given up in the same decision that tears it down, not a sweep later")
	}
}
