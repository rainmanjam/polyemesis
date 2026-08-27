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

// playlistEngineWithSourceID is playlistEngine with a chosen sourceID, which is
// what the list filename has to vary on. Two engines from playlistEngine would
// share an id and the test would pass for the wrong reason.
func playlistEngineWithSourceID(t *testing.T, id int64) *Engine {
	t.Helper()
	e := playlistEngine(t)
	e.sourceID = id
	return e
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

// TestAnItemWhoseDerivativeIsMissingStartsNoTier covers the whole of readiness
// through reconcilePlaylist: a name that is a perfectly good upload name and
// has simply never been normalised.
//
// Since B2 this is the ONLY question readiness asks, so there is no second
// branch that could be the reason it passes. Confinement is not asked here and
// is not gone: TestAnUnusablePlaylistItemStopsTheTier covers it where it now
// lives, in playlistSig's PlaylistFileProblem check, and
// playlistmedia.TestADerivativePathCannotEscapeItsDirectory covers the
// base-name reduction that keeps a list entry inside the derivative directory.
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

// TestAnItemWhoseDerivativeIsEmptyStartsNoTier is the same gate against the
// state that LOOKS finished: the derivative exists, under its final name, with
// nothing in it.
//
// A transcode killed between the create and the first write leaves exactly
// this, and os.Stat alone cannot tell it from a completed one. The consequence
// of admitting it is worse than refusing a missing file, because the tier
// STARTS: the concat list names an empty file, FFmpeg exits immediately, the
// supervisor respawns it, and the playlist outranks the slate the whole time.
//
// The three other readers of this path -- api.playlistItemStatus,
// api.enqueuePlaylistNormalisation and playlistmedia.RunNormalise's
// already-normalised skip -- have always required Size() > 0. This is the one
// that decides what goes to air, and for a while it was the one that did not.
//
// The mutation: drop `|| fi.Size() == 0` from playlistItemsReady and this fails.
func TestAnItemWhoseDerivativeIsEmptyStartsNoTier(t *testing.T) {
	e := playlistEngine(t)
	s := playlistOnSettings()
	s.Failover.Playlist.Items = []db.PlaylistItem{{Upload: "truncated.mp4"}}

	// Seeded normally and then emptied, rather than written empty: the file has
	// to arrive at the same path DerivativePath computes, and truncating what
	// the helper wrote is the only way to be sure it is that path and not one a
	// test built for itself.
	seedPlaylistUpload(t, e, "truncated.mp4", true)
	if err := os.Truncate(playlistmedia.DerivativePath(e.cfg.DataDir, "truncated.mp4"), 0); err != nil {
		t.Fatalf("truncate the derivative: %v", err)
	}

	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	if h := e.playlistHub(); h != nil {
		t.Error("a playlist whose derivative is zero-length started a tier: the concat " +
			"list names an empty file, FFmpeg exits at once, and the tier respawn-loops " +
			"on air in preference to the slate")
	}
}

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
// opposite. That test was deleted rather than reworded: a guard that survives
// past the thing it guarded is the failure this project keeps correcting.
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
// TestAConcatEntryIsAbsoluteEvenWhenTheDataDirIsNot is a REGRESSION test for a
// defect the acceptance suite found, not a hypothetical.
//
// The concat demuxer resolves a relative entry against THE LIST FILE'S OWN
// DIRECTORY, and the list lives in <dataDir>/playlist-media -- the very prefix a
// relative DerivativePath already carries. With `--data data` the demuxer
// therefore looked for `data/playlist-media/data/playlist-media/x.ts.v2.ts` and
// the tier respawn-looped on a file that was sitting right there, while
// readiness had already stat'd it and passed, because readiness resolves from
// the process's working directory like every other caller.
//
// It survived because EVERY EXISTING CALLER HAPPENED TO BE ABSOLUTE: deployments
// pass an absolute --data, and this package's own helpers build one with
// t.TempDir(). A path that is only ever exercised as absolute is a path whose
// relative behaviour nothing watches, which is why this test sets a relative
// DataDir on purpose.
//
// The mutation: drop the filepath.Abs in reconcilePlaylist and this fails.
func TestAConcatEntryIsAbsoluteEvenWhenTheDataDirIsNot(t *testing.T) {
	e := playlistEngine(t)
	t.Cleanup(func() { teardownPlaylistTier(e) })

	// A relative DataDir, reached the way an operator would: `--data data` from
	// the directory they launched in. chdir so the relative path still names the
	// seeded files.
	abs := e.cfg.DataDir
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(filepath.Dir(abs)); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	e.cfg.DataDir = filepath.Base(abs)

	s := playlistOnSettings()

	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	e.mu.RLock()
	tier := e.playlist
	e.mu.RUnlock()
	if tier == nil {
		t.Fatal("no tier started for a playlist whose derivative exists")
	}
	body, err := os.ReadFile(tier.listPath)
	if err != nil {
		t.Fatalf("read list: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if !strings.HasPrefix(line, "file ") {
			continue
		}
		p := strings.Trim(strings.TrimPrefix(line, "file "), "'")
		if !filepath.IsAbs(p) {
			t.Errorf("concat entry %q is relative; the demuxer resolves it against the "+
				"list's own directory and would look for it under playlist-media twice", p)
		}
	}
}

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

	// ONE DATA DIRECTORY, SHARED, because that is the only shape in which the
	// collision exists: internal/engine/manager.go runs one engine per source
	// over a single configured data dir, so both tiers write their lists into
	// the same playlist-media directory.
	//
	// playlistEngine hands each engine its own t.TempDir(), and leaving it that
	// way would make this test pass on the directory names alone -- green with
	// the source id dropped from the filename, which is the one regression it
	// exists to catch. Sharing the directory is what leaves the id as the only
	// thing that can tell the two lists apart.
	b.cfg = a.cfg

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

// TestPlaylistItemsReadyRefusesAnUnresolvableName WAS HERE, and it went with
// the uploads.Store.Resolve call it guarded.
//
// It asserted that readiness refuses an item whose name does not resolve to a
// stored upload. B2's readiness resolves no upload at all -- it asks one
// question, about the derivative -- so there is no Resolve left to delete and
// the test could only have been kept by rewriting it into something that cannot
// fail. Deleted for the same reason as
// TestAnItemWhoseUploadWasDeletedStartsNoTier: a guard outliving the thing it
// guarded is worse than no guard, because it reads as coverage.
//
// The confinement it stood for did not go anywhere, and is covered twice over
// by tests that reach production and that can still fail:
//
//   - TestAnUnusablePlaylistItemStopsTheTier drives reconcilePlaylist with
//     "../../etc/shadow" and asserts the tier stops. That is the check that
//     actually runs first now -- playlistSig hashes EMPTY when
//     PlaylistFileProblem fails, so an escaping item never reaches readiness.
//   - playlistmedia.TestADerivativePathCannotEscapeItsDirectory covers
//     DerivativePath's base-name reduction, which is what confines every path in
//     the concat list and is therefore what -safe 0 now rests on.
//
// TestEveryConcatPathIsADerivativePath asserts the consequence directly: nothing
// under the uploads directory reaches the list.

// teardownPlaylistTier stops whatever tier a test left running. The supervised
// child is a binary that does not exist, so it would otherwise sit in the
// restart backoff for the rest of the package's run.
func teardownPlaylistTier(e *Engine) {
	e.mu.RLock()
	tier := e.playlist
	e.mu.RUnlock()
	e.teardownPlaylist(tier)
}

func TestPlaylistFeedLoopsTheListAtWallClockSpeed(t *testing.T) {
	// -stream_loop -1 makes the LIST look like a feed that never ends -- it wraps
	// the whole concatenation, not each entry -- and -re paces it at wall-clock
	// speed. Without -re FFmpeg reads at disk speed and buries the relay in an
	// hour of stream in seconds -- the same reason pullFile carries both flags.
	args := playlistFeedArgs("/data/playlist-1-abc.txt", "udp://127.0.0.1:9000")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-stream_loop -1", "-re", "/data/playlist-1-abc.txt"} {
		if !strings.Contains(joined, want) {
			t.Errorf("playlist argv is missing %q: %v", want, args)
		}
	}
}

// TestThePlaylistInputFlagsPrecedeTheInput is the half of the argv contract the
// flags test cannot see: -stream_loop, -re, -f and -safe are INPUT options, and
// FFmpeg applies an input option to whatever -i follows it. Placed after -i they
// parse without complaint and apply to nothing, so the list plays once, at disk
// speed, and the tier looks like it worked. -f is the sharper one now: after -i
// it is read as the OUTPUT format, so the list would be probed as though it were
// media rather than demuxed as a playlist.
func TestThePlaylistInputFlagsPrecedeTheInput(t *testing.T) {
	args := playlistFeedArgs("/data/playlist-1-abc.txt", "udp://127.0.0.1:9000")
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
	// idx returns the FIRST match, which for "-f" is the input's "-f concat"
	// rather than the output's "-f mpegts" -- exactly the one that has to come
	// first.
	for _, flag := range []string{"-stream_loop", "-re", "-f", "-safe"} {
		if idx(flag) > input {
			t.Errorf("%s comes after -i, so FFmpeg applies it to no input: %v", flag, args)
		}
	}
}

// TestThePlaylistDemuxerIsNamedAndItsListIsTrusted replaces
// TestThePlaylistPathIsPinnedToTheFileProtocol, which asserted the "file:"
// prefix B1's argv carried on the upload path.
//
// That prefix guarded an operator-derived path: an upload name reaching FFmpeg
// as written, where a colon would have been re-read as a protocol. B2's input is
// a filename this process composed, and the operator's name never appears in it,
// so the prefix guarded nothing that is still there and asserting on it would
// have been asserting on a string.
//
// What has to hold instead is this pair. -f concat, because without it FFmpeg
// probes the list and finds a text file rather than a playlist. -safe 0, because
// the list holds ABSOLUTE paths and the demuxer refuses those by default -- and
// the flag is only defensible because every path in that list was built by
// playlistmedia.DerivativePath, which TestEveryConcatPathIsADerivativePath is
// what proves.
//
// The mutation: drop either flag and this fails.
func TestThePlaylistDemuxerIsNamedAndItsListIsTrusted(t *testing.T) {
	args := playlistFeedArgs("/data/playlist-1-abc.txt", "udp://127.0.0.1:9000")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-f concat", "-safe 0", "-i file:/data/playlist-1-abc.txt"} {
		if !strings.Contains(joined, want) {
			t.Errorf("playlist argv is missing %q: %v", want, args)
		}
	}
}

// TestThePlaylistListPathIsPinnedToTheFileProtocol is the successor to B1's
// TestThePlaylistPathIsPinnedToTheFileProtocol. That test guarded the prefix on
// the UPLOAD path, an operator-supplied name; this one guards it on the LIST
// path, which this process composes.
//
// WHAT THE PREFIX PROTECTS WAS MEASURED, not assumed, against FFmpeg 8.1.2:
// FFmpeg infers a protocol from the characters before the first ":" only while
// no "/" has appeared. So a RELATIVE data directory whose first segment carries
// a colon -- "2026:01/data" -- fails with "Protocol not found" unprefixed and
// opens with the prefix, and that is the case this buys. An ABSOLUTE data
// directory, "/mnt/2026:01/data", opens either way, because the leading "/"
// ends protocol detection before the colon is reached.
//
// The assertion is therefore deliberately about the ARGV rather than about
// FFmpeg opening anything: for the ordinary absolute-path deployment there is
// no behaviour to observe, because unprefixed already works. What is worth
// holding is that every file input this package builds is spelled the same way,
// so the guarantee does not rest on the reader re-deriving that argument.
//
// The mutation: drop the "file:" prefix from playlistFeedArgs and this fails.
func TestThePlaylistListPathIsPinnedToTheFileProtocol(t *testing.T) {
	// A colon in the FIRST segment, which is the only shape FFmpeg misreads.
	args := playlistFeedArgs("2026:01/data/playlist-media/playlist-1-abc.txt",
		"udp://127.0.0.1:9000")
	want := "file:2026:01/data/playlist-media/playlist-1-abc.txt"
	found := false
	for i, a := range args {
		if a != "-i" || i+1 >= len(args) {
			continue
		}
		found = true
		if args[i+1] != want {
			t.Errorf("playlist input = %q, want %q -- an unprefixed relative path "+
				"whose first segment carries a colon is read as a protocol name, and "+
				"FFmpeg answers it with \"Protocol not found\"", args[i+1], want)
		}
	}
	if !found {
		t.Fatal("playlist argv has no -i flag")
	}
}

// TestAPlaylistUploadWithSurroundingWhitespaceIsTheSamePlaylist guards
// playlistItemUpload as engine.go's single trim point.
//
// It used to assert on the RESOLVED PATH in the argv, because a leading space
// once validated, hashed as though trimmed, respawned looking like it should
// work, and then resolved to a file that was never the one an operator meant.
// That assertion no longer proves the trim: the path in the list is built by
// playlistmedia.DerivativePath, which trims through db.PlaylistUploadName
// itself, so it would come out identical with engine.go's trim deleted. Keeping
// it would have been a guard that cannot fail.
//
// What is still engine.go's own is the SIGNATURE. It decides whether a save
// respawns the tier, and it is the only thing left here that a lost trim would
// change: " loop.mp4" would hash differently from "loop.mp4", so re-saving a
// playlist an operator had merely retyped would tear a PLAYING programme down
// and start it again from its first frame.
//
// The mutation: drop db.PlaylistUploadName from playlistItemUpload and this
// fails on the signature.
func TestAPlaylistUploadWithSurroundingWhitespaceIsTheSamePlaylist(t *testing.T) {
	e := playlistEngine(t)
	t.Cleanup(func() { teardownPlaylistTier(e) })

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

	trimmed := playlistOnSettings()
	trimmed.Failover.Playlist.Items = []db.PlaylistItem{{Upload: "loop.mp4"}}
	if got, want := tier.sig, playlistSig(trimmed); got != want {
		t.Errorf("signature = %q for %q, want %q -- surrounding whitespace makes a "+
			"playlist look like a different one, so re-saving it respawns a tier "+
			"that was playing perfectly well", got, " loop.mp4", want)
	}

	// And the file it plays is still the real, trimmed one: a tier that started
	// on the right hash but the wrong path is the same failure wearing a
	// different hat.
	body, err := os.ReadFile(tier.listPath)
	if err != nil {
		t.Fatalf("read list: %v", err)
	}
	want := playlistmedia.DerivativePath(e.cfg.DataDir, "loop.mp4")
	if !strings.Contains(string(body), "file '"+want+"'") {
		t.Errorf("list does not name %s:\n%s", want, body)
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

// TestAScheduledPlaylistStartWillNotStoreInvalidSettings exercises the REAL
// actuator against a REAL store, which is the only way this defect is visible:
// every runner test in internal/scheduler uses a fake, and a fake happily
// accepts a document the database would too.
//
// PutSettings does not validate -- it marshals and inserts -- while
// handlePutSettings calls Settings.Validate first. So a scheduled write is the
// one path that can store what the API layer would refuse, and the DEFAULT
// INSTALL is exactly that case: the playlist ships disabled with no items, and
// "enabled with no items" is a state Validate rejects by name.
//
// Unvalidated, an overnight playlist.start stored that document and every later
// PUT /settings answered 400 for a reason the operator did not cause and could
// not see.
//
// The mutation: delete the Validate() call in db.UpdateSettings -- which is
// where SetPlaylistEnabled's validation now lives -- and this fails, both
// halves, because the flip then both returns nil and writes.
func TestAScheduledPlaylistStartWillNotStoreInvalidSettings(t *testing.T) {
	m, store := managerFixture(t)
	act := scheduleActuator{m: m}

	// The shipped default: failover playlist off, no items. Enabling it is
	// precisely what Settings.Validate refuses.
	if err := act.SetPlaylistEnabled(true); err == nil {
		t.Fatal("enabling a playlist with no items was accepted; every later " +
			"PUT /settings would then 400 on a document the operator never wrote")
	}

	// And nothing was written. An error that still stored the document would be
	// the same lockout with a log line attached.
	got, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got.Failover.Playlist.Enabled {
		t.Error("the settings were stored despite the error")
	}
	if err := got.Validate(); err != nil {
		t.Errorf("stored settings are invalid after a refused schedule: %v", err)
	}
}

// The other direction, so the guard above cannot pass by refusing everything: a
// playlist that HAS items enables normally.
//
// The mutation: make SetPlaylistEnabled always return an error and this fails.
func TestAScheduledPlaylistStartEnablesAValidPlaylist(t *testing.T) {
	m, store := managerFixture(t)
	act := scheduleActuator{m: m}

	s, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	s.Failover.Enabled = true
	s.Failover.Playlist.Items = []db.PlaylistItem{{Upload: "loop.mp4"}}
	if err := store.PutSettings(s); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}

	if err := act.SetPlaylistEnabled(true); err != nil {
		t.Fatalf("SetPlaylistEnabled on a playlist with items: %v", err)
	}
	got, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if !got.Failover.Playlist.Enabled {
		t.Error("a valid playlist was not enabled")
	}
}

// The spec's row "firing when already enabled changes nothing and respawns
// nothing", which was the one row of eight with no test.
//
// Both tests above start from a DISABLED playlist and call SetPlaylistEnabled
// once, so the no-op branch in that function could be deleted with the whole
// suite green -- the branch existed and nothing depended on it.
//
// The observable that makes it fail is not "was a write skipped": a redundant
// write stores a byte-identical document, so nothing downstream can see it.
// What is visible is that going through the write path VALIDATES, and the
// document being validated is not the one this schedule is asking to change.
// So the fixture stores a playlist that is enabled and empty -- invalid, and
// reachable in the field: it is exactly what the lockout above used to leave
// behind, and what a hand-edited or restored database can hold.
//
// A schedule asking for a state the install is already in must not fail because
// of that. Without the branch it does: Validate refuses the untouched document,
// the runner leaves the occurrence unhandled, and it retries every sweep until
// the grace window closes -- an overlapping schedule turning into a stream of
// warnings about a change nobody asked for.
//
// The mutation: delete the three-line `if s.Failover.Playlist.Enabled ==
// enabled` branch in scheduleActuator.SetPlaylistEnabled.
func TestAScheduledPlaylistStartOnAnAlreadyEnabledPlaylistIsANoOp(t *testing.T) {
	m, store := managerFixture(t)
	act := scheduleActuator{m: m}

	s, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	s.Failover.Playlist.Enabled = true
	// PutSettings, not UpdateSettings: this is the raw door precisely because
	// the fixture has to store a document the validating door would refuse.
	if err := store.PutSettings(s); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}

	if err := act.SetPlaylistEnabled(true); err != nil {
		t.Fatalf("a schedule asking for the state the playlist is already in reported %v; "+
			"the occurrence then stays unhandled and retries every sweep", err)
	}

	// And the document is untouched -- neither repaired nor damaged by a flip
	// that had nothing to do.
	got, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if !got.Failover.Playlist.Enabled || len(got.Failover.Playlist.Items) != 0 {
		t.Errorf("the stored playlist changed on a no-op: enabled=%v items=%d, want enabled=true items=0",
			got.Failover.Playlist.Enabled, len(got.Failover.Playlist.Items))
	}
}
