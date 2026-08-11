package engine

import (
	"slices"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/relay"
)

// ----------------------------------------------------- the failed-start backoff
//
// ensureFeed backs a feed off for feedRespawn after a start that produced no
// process, so a slate with an unopenable encoder does not spawn twice a second
// for ever. The backoff is armed by sel.feedAt, and every DELIBERATE teardown in
// the file has to disarm it -- otherwise a teardown the engine asked for is read
// as a start that failed, and the tier sits unfed for two whole seconds with
// nothing wrong.
//
// Three of these tests pass `now` as a real time.Now(). That is not a
// convenience: startFeed stamps feedAt from the wall clock, so a synthetic now
// more than feedRespawn away from it disarms the backoff by accident and the
// test measures nothing.

// feedSubscribed reports whether the selector's copy hop is subscribed to the
// hub it reads. It is the observable for "a feed is running" that does not
// reach into e.sel: the subscription is what actually carries datagrams, and a
// tier with no subscriber is a tier delivering nothing.
func feedSubscribed(h *relay.Hub) bool {
	return h != nil && slices.Contains(h.Subscribers(), selectorSubName)
}

// A teardown the engine ASKED FOR must not arm the failed-start backoff.
//
// detachFeedForSilence stops the primary feed because the silence tier under it
// is being replaced, and reconcileSelector rebuilds it a few lines later. If
// feedAt survived that, ensureFeed would see "no feed, and one was started
// recently" -- the signature of a start that failed -- and refuse to rebuild
// for feedRespawn. Two seconds of no feed on an edit that was supposed to cost
// a pause in datagrams.
//
// Mutation: selector.go:1459, delete `e.sel.feedAt = time.Time{}` from
// detachFeedForSilence.
// Observed to fail with "the sweep after a deliberate teardown started no feed"
// and to be the only failing test in the package.
func TestADeliberateTeardownDoesNotArmTheFailedStartBackoff(t *testing.T) {
	e := failoverEngine(t)
	s := failoverOnSettings()
	setSettings(e, s)
	e.reconcileSelector(s, wantSelector(s), "")
	if e.selectorHub() == nil {
		t.Fatal("the source selector did not start")
	}

	// Positive first: a primary feed exists and is subscribed to the hub it
	// reads. Everything below is about that subscription coming back.
	deliverTS(t, e.hub, 8)
	e.sweepSelector(time.Now())
	if act := e.Failover().Active; act != sourcePrimary {
		t.Fatalf("the primary is delivering and %q is on air; there is no primary feed to tear down", act)
	}
	if !feedSubscribed(e.sourceHub()) {
		t.Fatalf("the primary is on air but nothing is subscribed to its hub (%v): "+
			"no feed was built, so this test cannot see one being rebuilt",
			e.sourceHub().Subscribers())
	}

	e.detachFeedForSilence("silence-signature-moved")
	if feedSubscribed(e.sourceHub()) {
		t.Fatalf("detachFeedForSilence left the feed subscribed to a hub that is about to be "+
			"closed: %v", e.sourceHub().Subscribers())
	}

	e.sweepSelector(time.Now())
	if !feedSubscribed(e.sourceHub()) {
		t.Errorf("the sweep after a deliberate teardown started no feed: the teardown was "+
			"mistaken for a start that failed, and the tier carries no datagrams for a "+
			"whole %s respawn window", feedRespawn)
	}
}

// The converse, and it is what stops the test above being "fixed" by hoisting
// the feedAt clear into teardownFeed: a start that GENUINELY failed must back
// off. Clearing feedAt on every teardown would clear it here too, and the tier
// would retry an unstartable feed on every 500ms sweep for ever.
//
// The failure is real rather than simulated: the allocator is drained, so
// startFeed's `e.alloc.Allocate()` fails, fail() records it on the tier and
// returns no feed at all.
//
// Mutation: selector.go:1136, delete the
// `case cur == nil && !lastAt.IsZero() && now.Sub(lastAt) < feedRespawn:` arm
// from ensureFeed's switch.
// Observed to fail on both assertions and to be the only failing test in the
// package.
func TestAStartThatFailedIsNotRetriedOnTheNextSweep(t *testing.T) {
	e := failoverEngine(t)
	// One port, so draining it is a single call and holding it is the whole of
	// "there is no port for a copy hop".
	e.alloc = relay.NewPortAllocator(freeUDPPort(t), 1)
	s := failoverOnSettings()
	setSettings(e, s)
	e.reconcileSelector(s, wantSelector(s), "")
	if e.selectorHub() == nil {
		t.Fatal("the source selector did not start")
	}

	held, err := e.alloc.Allocate()
	if err != nil {
		t.Fatalf("could not drain the one-port allocator: %v", err)
	}

	deliverTS(t, e.hub, 8)
	first := time.Now()
	e.sweepSelector(first)

	// Positive first: the start was ATTEMPTED and failed, and the tier says so.
	// Without this a test that never reached startFeed at all would pass the
	// assertions below for the wrong reason.
	if problem := e.Failover().Error; problem == "" {
		t.Fatalf("the primary is delivering, there is no port for its copy hop and the tier " +
			"reports no problem at all: the start was never attempted, so there is no " +
			"failed start to back off from")
	}
	if feedSubscribed(e.sourceHub()) {
		t.Fatalf("a feed subscribed despite the allocator being empty: %v", e.sourceHub().Subscribers())
	}

	// The port comes back -- an unrelated consumer went away -- so the ONLY
	// thing that can hold the retry off is the backoff.
	e.alloc.Release(held)

	e.sweepSelector(first.Add(300 * time.Millisecond))

	if feedSubscribed(e.sourceHub()) {
		t.Errorf("a feed that failed to start was retried %s later, well inside the %s "+
			"respawn window: an unstartable feed now respawns on every sweep, which is "+
			"twice a second for as long as the fault lasts", 300*time.Millisecond, feedRespawn)
	}
	if p, err := e.alloc.Allocate(); err != nil {
		t.Errorf("the retried start took the relay port back: %v", err)
	} else {
		e.alloc.Release(p)
	}
}

// reconcileBackupIngest tears the backup's hub down and its feed with it, for
// the same reason detachFeedForSilence does, and it has to disarm the same
// backoff. An operator editing the standby's SRT latency would otherwise leave
// the tier unable to start ANY feed -- including the primary's -- for two
// seconds after the edit.
//
// The route to a backup feed here is the operator's own pin rather than a
// timed-out primary, so every instant in this test is a real time.Now() and
// there is no window in which a slow machine changes the answer.
//
// Mutation: selector.go:1768, delete `e.sel.feedAt = time.Time{}` from
// reconcileBackupIngest.
// Observed to fail with "no feed was started after the backup tier was
// rebuilt" and to be the only failing test in the package.
func TestRebuildingTheBackupTierDoesNotArmTheFailedStartBackoff(t *testing.T) {
	e := failoverEngine(t)
	s := failoverOnSettings()
	s.Failover.Backup.Enabled = true
	s.Failover.Backup.Mode = db.IngestSRT
	s.Failover.Backup.SRT.LatencyMS = 120
	setSettings(e, s)
	e.reconcileSelector(s, wantSelector(s), "")
	if e.selectorHub() == nil {
		t.Fatal("the source selector did not start")
	}
	if e.backupHub() == nil {
		t.Fatal("the backup ingest did not start, so there is no backup feed to rebuild")
	}

	// Both ingests are delivering, so the pin below is honoured and the return
	// to the primary at the end is not waiting on anything.
	deliverTS(t, e.hub, 8)
	deliverTS(t, e.backupHub(), 8)
	e.sweepSelector(time.Now())
	if act := e.Failover().Active; act != sourcePrimary {
		t.Fatalf("both ingests are delivering and %q is on air rather than the primary", act)
	}

	if err := e.SwitchSource(string(sourceBackup)); err != nil {
		t.Fatalf("SwitchSource(backup): %v", err)
	}
	if act := e.Failover().Active; act != sourceBackup {
		t.Fatalf("the operator pinned the backup and %q is on air", act)
	}
	if !feedSubscribed(e.backupHub()) {
		t.Fatalf("the backup is on air but nothing is subscribed to its hub (%v)",
			e.backupHub().Subscribers())
	}

	// The operator edits the standby's latency. The signature moves, so the
	// tier is rebuilt and the feed reading its hub goes with it.
	s2 := s
	s2.Failover.Backup.SRT.LatencyMS = 900
	setSettings(e, s2)
	e.reconcileBackupIngest(s2)
	if feedSubscribed(e.backupHub()) {
		t.Fatalf("the backup feed survived its hub being replaced: %v", e.backupHub().Subscribers())
	}

	if err := e.SwitchSource(string(sourcePrimary)); err != nil {
		t.Fatalf("SwitchSource(primary): %v", err)
	}
	if !feedSubscribed(e.sourceHub()) {
		t.Errorf("no feed was started after the backup tier was rebuilt: the deliberate "+
			"teardown armed the failed-start backoff, so an edit to the STANDBY's latency "+
			"leaves the PRIMARY unfed for a whole %s window", feedRespawn)
	}
}
