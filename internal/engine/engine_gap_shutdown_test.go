package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/playlistmedia"
	"github.com/rainmanjam/polyemesis/internal/relay"
)

// ------------------------------------------- reconciling after Stop has run
//
// Stop copies the maps and collects what it finds. Anything published AFTER
// that is an orphan: nothing holds a reference to it, nothing will ever stop
// it, and it keeps whatever the engine handed it. The acceptance suite cannot
// construct this at all -- it drives the daemon over HTTP, and the daemon stops
// serving before it stops -- so the only place these guards can be measured is
// in process.
//
// Two of the four e.stopped guards in selector.go are here, and only two: the
// consequences are DIFFERENT. startFeed leaks a relay port out of a range of
// 500 shared by every source. reconcilePlaylist leaks a file into the data
// directory. reconcileSelector's and reconcileBackupIngest's guards are the
// same bug class with no distinct consequence to name, so copying this shape
// onto them would raise the count and pin nothing new.

// A feed started after shutdown must give its relay port back.
//
// startFeed allocates the port and subscribes BEFORE it builds the process, so
// by the time the guard is reached the port is already spoken for. Skip the
// teardown and it is held for the life of the daemon: there are 500 across
// every source engine, and each shutdown race burns one.
//
// Mutation: selector.go:1548, delete the
// `if e.stopped { e.mu.Unlock(); e.teardownFeed(feed); return nil }` block from
// startFeed.
// Observed to fail on both assertions and to be the only failing test in the
// package.
func TestStartingASourceFeedAfterShutdownLeavesNothingBehind(t *testing.T) {
	e := failoverEngine(t)
	// The span of one is the assertion: a port that was not released cannot be
	// handed out again, so "did the stopped path give it back" is answerable
	// without reaching inside the allocator.
	e.alloc = relay.NewPortAllocator(freeUDPPort(t), 1)
	s := failoverOnSettings()
	setSettings(e, s)
	e.reconcileSelector(s, wantSelector(s), "")
	if e.selectorHub() == nil {
		t.Fatal("the source selector did not start")
	}

	up := e.feedUpstreamSig(s, sourcePrimary, "")

	// Positive first, on a RUNNING engine: a feed takes the one port and
	// subscribes. Without this the assertions below would pass against a
	// startFeed that had quietly stopped doing anything at all.
	e.selMu.Lock()
	live := e.startFeed(s, sourcePrimary, up, "", time.Now())
	e.selMu.Unlock()
	if live == nil {
		t.Fatal("a running engine started no primary feed, so there is no start to compare against")
	}
	if !feedSubscribed(e.sourceHub()) {
		t.Fatalf("a running engine's feed did not subscribe to the primary's hub: %v",
			e.sourceHub().Subscribers())
	}
	if _, err := e.alloc.Allocate(); err == nil {
		t.Fatal("the one-port allocator handed out a second port, so holding a port is not " +
			"observable here and neither is leaking one")
	}
	e.teardownFeed(live)

	e.mu.Lock()
	e.stopped = true
	e.mu.Unlock()

	e.selMu.Lock()
	orphan := e.startFeed(s, sourcePrimary, up, "", time.Now())
	e.selMu.Unlock()
	if orphan != nil {
		// Collected rather than left running, so a failing run does not leave a
		// supervisor goroutine behind for the rest of the package.
		t.Cleanup(func() { e.teardownFeed(orphan) })
	}

	if feedSubscribed(e.sourceHub()) {
		t.Errorf("the hub still forwards to %v after shutdown: Stop has already collected "+
			"the feeds, so this subscription blasts datagrams at whoever is handed the "+
			"port next", e.sourceHub().Subscribers())
	}
	if _, err := e.alloc.Allocate(); err != nil {
		t.Errorf("a feed started after shutdown kept its relay port: %v. There are 500 "+
			"across every source engine and nothing will ever release this one", err)
	}
}

// A playlist reconciled after shutdown must not leave its concat list on disk.
//
// The list is written before the process is spawned, and its name carries the
// signature and the source id so no other tier will ever own it. If the guard
// does not remove it, nothing else can: e.playlist is never set, so
// teardownPlaylist is never called with that path, and the file sits in the
// data directory until somebody notices.
//
// Mutation: selector.go:2320, delete the
// `if e.stopped { e.mu.Unlock(); _ = hub.Close(); _ = os.Remove(listPath); return }`
// block from reconcilePlaylist.
// Observed to fail with "left 1 concat list behind" and to be the only failing
// test in the package.
func TestReconcilingAPlaylistAfterShutdownLeavesNoConcatListBehind(t *testing.T) {
	e := playlistEngine(t)
	s := playlistOnSettings()
	setSettings(e, s)

	lists := func() []string {
		t.Helper()
		got, err := filepath.Glob(filepath.Join(
			playlistmedia.DerivativeDir(e.cfg.DataDir), "playlist-*.txt"))
		if err != nil {
			t.Fatalf("glob the derivative directory: %v", err)
		}
		return got
	}

	// Positive first: a running engine writes exactly one list. This is what
	// makes "none" below mean "removed" rather than "never written".
	e.reconcilePlaylist(s)
	if e.playlistHub() == nil {
		t.Fatal("the playlist tier did not start, so no concat list was ever written")
	}
	if got := lists(); len(got) != 1 {
		t.Fatalf("a running playlist wrote %d concat lists, want exactly 1: %v", len(got), got)
	}

	// Off, which takes the tier and its list with it.
	off := s
	off.Failover.Playlist.Enabled = false
	setSettings(e, off)
	e.reconcilePlaylist(off)
	if got := lists(); len(got) != 0 {
		t.Fatalf("stopping the playlist left %d concat lists behind: %v", len(got), got)
	}

	e.mu.Lock()
	e.stopped = true
	e.mu.Unlock()

	setSettings(e, s)
	e.reconcilePlaylist(s)

	if got := lists(); len(got) != 0 {
		t.Errorf("a playlist reconciled after shutdown left %d concat list(s) behind: %v. "+
			"No tier was recorded, so nothing will ever own that path and nothing will "+
			"ever remove it", len(got), got)
	}
}
