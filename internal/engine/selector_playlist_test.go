package engine

import (
	"strings"
	"testing"

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
// These tests are about the tier existing and being torn down; nothing here
// decides anything, because playlistRunning is still hardcoded false.

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
	s.Failover.Playlist.FilePath = "loop.mp4"
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
	s.Failover.Playlist.FilePath = "other.mp4"
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
	s.Failover.Playlist.FilePath = "../../etc/shadow"

	e.selMu.Lock()
	e.reconcilePlaylist(s)
	e.selMu.Unlock()

	if h := e.playlistHub(); h != nil {
		t.Error("a playlist path that escapes the data directory started a process; " +
			"the confinement check is decorative if the argv is built anyway")
	}
}

// TestARunningPlaylistStillDecidesNothing is the boundary of this task written
// down. The tier exists and the process is up, and the selector must still not
// be able to choose it: playlistRunning is hardcoded false until the feed layer
// knows how to build a playlist feed, and until then a decision of "playlist"
// would reach three functions that refuse it.
func TestARunningPlaylistStillDecidesNothing(t *testing.T) {
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
		t.Error("the selector put the playlist on air; no feed can run one yet, " +
			"so the process on air would be reading somebody else's bytes")
	}
}
