package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
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
func playlistEngine(t *testing.T) *Engine {
	t.Helper()
	e := failoverEngine(t)
	e.cfg = config.Config{DataDir: t.TempDir()}
	return e
}

// playlistOnSettings enables the feature and the tier with a usable file.
func playlistOnSettings() db.Settings {
	s := failoverOnSettings()
	s.Failover.Playlist.Enabled = true
	s.Failover.Playlist.Items = []db.PlaylistItem{{Upload: "loop.mp4"}}
	return s
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

// TestAnUnusablePlaylistFileStartsNothing leans on PlaylistFileProblem rather
// than a check of its own: the confinement that keeps an operator-supplied path
// inside the data directory is written once, and a second copy here would be a
// second thing to keep in step with SECURITY.md.
func TestAnUnusablePlaylistFileStartsNothing(t *testing.T) {
	e := playlistEngine(t)
	s := playlistOnSettings()
	s.Failover.Playlist.Items = []db.PlaylistItem{{Upload: "../../etc/shadow"}}

	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	if h := e.playlistHub(); h != nil {
		t.Error("a playlist path that escapes the data directory started a process; " +
			"the confinement check is decorative if the argv is built anyway")
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
