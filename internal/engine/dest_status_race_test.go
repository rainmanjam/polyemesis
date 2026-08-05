package engine

import (
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// B4. startDestinations publishes &next into e.dests, drops the lock, and THEN
// calls reconcileBackup, which wrote d.backup, d.backupPort, d.backupSub,
// d.backupSpec and d.backupErr through the pointer it had just published.
//
// The code states the violated invariant itself, in the copy-on-publish comment
// directly above the publication: "Replaced wholesale rather than mutated in
// place: Status hands out these pointers and then reads their fields after
// dropping the lock, which is only safe while a published destination never
// changes again."
//
// WHY THERE ARE TWO READERS, and it is the interesting part of this test.
//
// The Status() loop is the dashboard, and on its own it does NOT trip the race
// detector -- measured, not assumed: 120 reconciles against 24 concurrent
// Status() goroutines, with the destination torn down and rebuilt on every one
// of them, stayed green for a hundred seconds. Status copies the pointers under
// e.mu and then acquires e.mu five more times (SourceInfo, Renditions,
// Loudness, ClipBuffer, Silence, Failover) before it reads the backup fields at
// the end. The reconciler releases e.mu within microseconds of writing them, so
// one of those later acquisitions almost always establishes the release-acquire
// edge that hides the write from the detector.
//
// The second reader is the same contract with nothing in between: copy the
// pointers under the lock, drop it, read their fields -- exactly what the
// comment above says a holder of one of these pointers is entitled to do, and
// what Status itself would be doing if any of those five calls were moved. It
// reports the race within the first reconciles.
//
// So the pair is deliberate. The Status loop is the real caller; the tight
// reader is what makes the detector able to SEE what the real caller is already
// doing. Dropping either weakens the test in a different direction.
//
// Mutation: none needed. Against the tree as it was before this commit,
// `go test -race -run TestStatusDoesNotRaceTheBackupFields` reports
// WARNING: DATA RACE between startBackup's write of d.backup and this test's
// read of it. Observed.
func TestStatusDoesNotRaceTheBackupFields(t *testing.T) {
	e, store := storeEngine(t)

	created, err := store.CreateDestination(&db.Destination{
		Name: "fb", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmp://127.0.0.1:1/rtmp", StreamKey: "key", Enabled: true,
		BackupURL: "rtmp://127.0.0.1:2/rtmp", BackupStreamKey: "backup-key",
		AudioBitrate: 128, Profile: routing.DefaultProfile(),
		BackupIngestWanted: true,
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	// A second destination with the toggle and no endpoint: that is the real
	// state between enabling the setting and the next broadcast being created,
	// and it is the case where reconcileBackup writes backupErr on EVERY pass
	// rather than only on the one that brings a backup up.
	if _, err := store.CreateDestination(&db.Destination{
		Name: "fb awaiting an endpoint", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmp://127.0.0.1:3/rtmp", StreamKey: "key", Enabled: true,
		AudioBitrate: 128, Profile: routing.DefaultProfile(),
		BackupIngestWanted: true,
	}); err != nil {
		t.Fatalf("CreateDestination(pending): %v", err)
	}

	done := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			for _, d := range e.Status().Destinations {
				_, _ = d.BackupError, d.BackupProcess
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			e.mu.RLock()
			live := make([]*destination, 0, len(e.dests))
			for _, d := range e.dests {
				live = append(live, d)
			}
			e.mu.RUnlock()
			if d := e.destByID(live, created.ID); d != nil {
				_, _, _ = d.backup, d.backupErr, d.backupSpec
			}
		}
	}()

	for i := range 40 {
		if err := e.Reconcile(); err != nil {
			t.Errorf("Reconcile %d: %v", i, err)
			break
		}
	}
	close(done)
	wg.Wait()
}

// The deterministic half of B4. The race test proves the fields were being
// written after publication; this proves the shape that stops it, without
// depending on the scheduler.
//
// It is the same assertion TestRefreshingARunningDestinationPublishesAReplacementRatherThanMutatingIt
// makes about the row refresh, applied to the backup fields -- the other writer
// on the same published pointer, and the one that was missed.
//
// Mutation: in reconcileBackup, change `e.buildBackup(&next, compiled, want)`
// to `e.buildBackup(prev, compiled, want)`. That is exactly the code this
// commit replaced. Observed to fail -- the destination the dashboard was
// holding grew a backup under it.
func TestReconcilingTheBackupPublishesAReplacementRatherThanMutatingIt(t *testing.T) {
	e, _ := storeEngine(t)
	prev := &destination{row: backupRow(), hub: e.hub, spec: "unchanged"}
	e.dests[prev.row.ID] = prev

	e.reconcileBackup(prev.row.ID, prev, routing.Result{FilterComplex: "anull", OutLabel: "a"}, "up")

	next := e.dests[prev.row.ID]
	if next == prev {
		t.Fatal("the backup was reconciled into the same pointer Status is already " +
			"holding; every field it writes is a read of unsynchronised memory")
	}
	// Ordered before the fixture check on purpose. Under the mutation the
	// backup lands on prev instead of next, so a `next.backup == nil` fatal
	// first would report this as a broken fixture rather than as the write
	// through a published pointer that it is.
	// Spelled out one field at a time rather than through a table of `any`: a
	// nil *supervisor.Process in an interface is not a nil interface, so a
	// table would have compared the one field that matters most against the
	// wrong thing and passed.
	if prev.backup != nil {
		t.Error("the published destination grew a backup process under the dashboard")
	}
	if prev.backupSub != "" {
		t.Errorf("the published destination's backupSub changed to %q", prev.backupSub)
	}
	if prev.backupPort != 0 {
		t.Errorf("the published destination's backupPort changed to %d", prev.backupPort)
	}
	if prev.backupSpec != "" {
		t.Errorf("the published destination's backupSpec changed to %q", prev.backupSpec)
	}
	if next.backup == nil && prev.backup == nil {
		t.Fatal("no backup was started anywhere; the fixture is wrong, not the code")
	}
}
