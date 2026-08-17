package db

import (
	"errors"
	"testing"
)

func lifecycleDestination(t *testing.T, d *DB) *Destination {
	t.Helper()
	created, err := d.CreateDestination(&Destination{
		Name: "yt", Kind: "rtmp", Platform: PlatformYouTube,
		URL: "rtmp://a.rtmp.youtube.com/live2", StreamKey: "original-key",
		AudioBitrate: 128, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	return created
}

// THE BLAST RADIUS OF THE LIFECYCLE COORDINATOR, ASSERTED RATHER THAN PROMISED.
//
// internal/api/lifecycle.go claims it cannot start, stop or reconfigure an
// output however wrong its logic becomes, and one of the three reasons it gives
// is this function: the only writer it holds persists a single column and
// discards everything else. A callback that sets Enabled, StreamKey or URL is
// writing into a copy that is thrown away.
//
// Mutation observed red: add `enabled=?` to UpdateLifecycle's UPDATE and pass
// cur.Enabled. The coordinator can then flip a destination off, which is the
// exact power the whole design is built to deny it.
func TestUpdateLifecycleWritesOnlyTheLifecycleColumn(t *testing.T) {
	d := testDB(t)
	created := lifecycleDestination(t, d)

	got, err := d.UpdateLifecycle(created.ID, func(cur *Destination) bool {
		cur.Lifecycle = BroadcastControl{BroadcastID: "bid-1", Phase: "live", Attempts: 2, Fault: "x"}
		// Everything below is outside the one column this write owns. All of it
		// is dropped -- and the first two are dropped for a reason worth more
		// than tidiness: a coordinator that could write Enabled could stop a
		// show, and one that could write StreamKey could cycle a live process.
		cur.Enabled = false
		cur.StreamKey = "key-the-coordinator-invented"
		cur.URL = "rtmp://somewhere.else/live"
		cur.Name = "renamed by the sweep"
		cur.AudioBitrate = 320
		return true
	})
	if err != nil {
		t.Fatalf("UpdateLifecycle: %v", err)
	}

	if got.Lifecycle.BroadcastID != "bid-1" || got.Lifecycle.Phase != "live" ||
		got.Lifecycle.Attempts != 2 || got.Lifecycle.Fault != "x" {
		t.Errorf("lifecycle block = %+v, want it stored whole", got.Lifecycle)
	}
	if !got.Enabled {
		t.Error("the enable switch was written through UpdateLifecycle: the broadcast " +
			"lifecycle coordinator can now stop a destination that is on air")
	}
	if got.StreamKey != "original-key" {
		t.Errorf("StreamKey = %q -- a lifecycle write reached the one column that is inside "+
			"the engine's restart hash, so recording a phase would cycle a live process",
			got.StreamKey)
	}
	if got.URL != "rtmp://a.rtmp.youtube.com/live2" {
		t.Errorf("URL = %q, want it untouched", got.URL)
	}
	if got.Name != "yt" || got.AudioBitrate != 128 {
		t.Errorf("name/bitrate = %q/%d, want them untouched", got.Name, got.AudioBitrate)
	}
}

// The compare-and-set that makes an END claim safe.
//
// The coordinator reads the row, spends up to twenty seconds asking a platform
// two questions, and only then decides to end a broadcast. If the operator
// re-enabled the destination in that window the end must not be sent -- and the
// only place that decision can be made correctly is inside the transaction that
// writes it, against a row read there rather than against the caller's snapshot.
//
// Mutation observed red: make UpdateLifecycle ignore apply's return value. The
// declining callback then commits, the caller sees no error and sends `complete`
// to a broadcast the operator has just put back on air.
func TestUpdateLifecycleRefusesWhenTheCallbackDeclines(t *testing.T) {
	d := testDB(t)
	created := lifecycleDestination(t, d)

	_, err := d.UpdateLifecycle(created.ID, func(cur *Destination) bool {
		// What the END path's callback does: refuse the moment the row says the
		// operator wants this destination running.
		if cur.Enabled {
			return false
		}
		cur.Lifecycle.Phase = "live"
		return true
	})
	if !errors.Is(err, ErrLifecycleSkipped) {
		t.Fatalf("UpdateLifecycle err = %v, want ErrLifecycleSkipped", err)
	}
	after, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if !after.Lifecycle.Empty() {
		t.Errorf("a declined write still committed: %+v", after.Lifecycle)
	}
}

// The callback sees the row AS IT STANDS NOW, not the caller's stale copy.
//
// Same failure UpdateAnnouncement's equivalent guards, arriving through a
// different door: the coordinator holds a *Destination across two platform calls
// and everything it decides afterwards has to be decided against the database.
func TestUpdateLifecycleShowsTheCallbackTheCurrentRow(t *testing.T) {
	d := testDB(t)
	created := lifecycleDestination(t, d)

	// The world moves while the coordinator is talking to the platform.
	if err := d.SetDestinationEnabled(created.ID, false); err != nil {
		t.Fatalf("SetDestinationEnabled: %v", err)
	}

	var sawEnabled bool
	if _, err := d.UpdateLifecycle(created.ID, func(cur *Destination) bool {
		sawEnabled = cur.Enabled
		return true
	}); err != nil {
		t.Fatalf("UpdateLifecycle: %v", err)
	}
	if sawEnabled {
		t.Fatal("the callback was shown a stale row: an END claim would be decided against " +
			"the state the destination was in before the platform was asked")
	}
}

// A row written before this column existed decodes to a zero value rather than
// failing the scan -- and the zero value is what keeps an upgrade quiet: a
// destination with no recorded phase is one the coordinator declines to end.
func TestADestinationWithNoLifecycleColumnReadsAsUntracked(t *testing.T) {
	d := testDB(t)
	created := lifecycleDestination(t, d)

	if _, err := d.SQL().Exec(`UPDATE destinations SET lifecycle='' WHERE id=?`, created.ID); err != nil {
		t.Fatalf("blank the column: %v", err)
	}
	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination on a row with no lifecycle JSON: %v", err)
	}
	if !got.Lifecycle.Empty() {
		t.Errorf("lifecycle = %+v, want the zero value", got.Lifecycle)
	}
}

// The lifecycle block is NOT reachable through the ordinary destination write.
//
// This is what removes a whole class of bug rather than guarding against it: an
// operator's edit decodes the request body over the stored row and writes the
// whole thing back, so a UI that round-tripped a stale lifecycle block would
// silently revert a phase the coordinator recorded a moment earlier. Neither
// CreateDestination nor UpdateDestination mentions the column, so there is
// nothing to revert with.
func TestTheOrdinaryDestinationWriteCannotTouchTheLifecycleBlock(t *testing.T) {
	d := testDB(t)
	created := lifecycleDestination(t, d)

	if _, err := d.UpdateLifecycle(created.ID, func(cur *Destination) bool {
		cur.Lifecycle = BroadcastControl{BroadcastID: "bid-1", Phase: "live"}
		return true
	}); err != nil {
		t.Fatalf("UpdateLifecycle: %v", err)
	}

	row, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	// Exactly what handleUpdateDestination produces: the stored row with a
	// client's body decoded over it, lifecycle block and all.
	row.Name = "renamed by an operator"
	row.Lifecycle = BroadcastControl{BroadcastID: "stale", Phase: "created"}
	if _, err := d.UpdateDestination(row); err != nil {
		t.Fatalf("UpdateDestination: %v", err)
	}

	after, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if after.Name != "renamed by an operator" {
		t.Errorf("the ordinary edit did not land: Name = %q", after.Name)
	}
	if after.Lifecycle.BroadcastID != "bid-1" || after.Lifecycle.Phase != "live" {
		t.Errorf("lifecycle = %+v, want the coordinator's own record: a rename has just "+
			"reverted the platform state, and the next sweep would act on a phase that is "+
			"one edit out of date", after.Lifecycle)
	}
}
