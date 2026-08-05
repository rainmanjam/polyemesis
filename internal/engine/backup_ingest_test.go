package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

func backupRow() *db.Destination {
	return &db.Destination{
		ID: 7, Name: "fb", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmps://live.example/rtmp", StreamKey: "primary-key",
		BackupURL: "rtmps://backup.example/rtmp", BackupStreamKey: "backup-key",
		BackupIngestWanted: true,
	}
}

// Intent alone is not a backup. Between enabling the toggle and the next
// broadcast being created there is no endpoint, and publishing to an empty
// target would be a second FFmpeg failing forever for a reason nobody could
// read off the card.
func TestABackupNeedsBothTheToggleAndAnEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*db.Destination)
		want bool
	}{
		{"toggle and endpoint", func(*db.Destination) {}, true},
		{"toggle without an endpoint", func(r *db.Destination) { r.BackupURL = "" }, false},
		{"endpoint without the toggle", func(r *db.Destination) { r.BackupIngestWanted = false }, false},
		{"not an rtmp destination", func(r *db.Destination) { r.Kind = db.DestFile }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := backupRow()
			tc.mut(row)
			if got := wantsBackup(row); got != tc.want {
				t.Errorf("wantsBackup = %v, want %v", got, tc.want)
			}
		})
	}
}

// THE CLAIM THE WHOLE CHANGE IS FOR. A destination that is not Facebook, with
// an endpoint and the intent set, gets a redundant feed.
//
// This was unreachable rather than merely untested. wantsBackup read
// row.Facebook.BackupIngest, which is false on a Twitch row however the row was
// configured -- so the engine's gate on BackupURL and BackupStreamKey, two
// columns whose own comment says the engine must not have to know which
// platform a destination is, went through a Facebook-named struct. Nothing but
// Facebook populates the endpoint today; the point is that the engine no longer
// has an opinion about that, so the next platform to fill it in needs no engine
// change at all.
//
// Mutation, run against a committed tree: in wantsBackup,
//
//	return row.BackupIngestWanted && row.BackupURL != "" && row.Kind == db.DestRTMP
//
// ->
//
//	return row.Platform == db.PlatformFacebook && row.BackupIngestWanted && row.BackupURL != "" && row.Kind == db.DestRTMP
//
// which restores the defect exactly -- the engine deciding redundancy by
// platform. Observed FAIL here, with TestABackupNeedsBothTheToggleAndAnEndpoint
// still green above it, which is why that one alone was never enough.
func TestABackupDoesNotRequireTheDestinationToBeFacebook(t *testing.T) {
	twitch := &db.Destination{
		ID: 9, Name: "twitch", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
		URL: "rtmp://live.example/app", StreamKey: "primary-key",
		BackupURL: "rtmp://backup.example/app", BackupStreamKey: "backup-key",
		BackupIngestWanted: true,
	}
	if !wantsBackup(twitch) {
		t.Error("a non-Facebook RTMP destination with the intent set and an " +
			"endpoint stored does not get a redundant feed, so the engine is " +
			"still deciding redundancy by platform")
	}
	if got := backupTarget(twitch); got != "rtmp://backup.example/app/backup-key" {
		t.Errorf("backupTarget = %q, want the backup URL and key joined", got)
	}
}

// The backup publishes to the BACKUP endpoint. Two feeds to one endpoint is
// not redundancy, and a count of processes cannot tell the difference.
func TestTheBackupTargetsTheBackupEndpointNotThePrimary(t *testing.T) {
	row := backupRow()
	got, primary := backupTarget(row), row.Target()
	if got == primary {
		t.Fatalf("the backup publishes to the primary's target %q", got)
	}
	if got != "rtmps://backup.example/rtmp/backup-key" {
		t.Errorf("backupTarget = %q, want the backup URL and key joined", got)
	}
}

// The two hashes must not move together. A rotated BACKUP key has to restart
// the backup and leave the primary alone; if backupSpecOf tracked destSpec,
// enabling or rotating redundancy would drop the operator's live connection --
// which is the whole reason the toggle is absent from destSpec.
func TestTheBackupsHashMovesIndependentlyOfThePrimarys(t *testing.T) {
	compiled := routing.Result{FilterComplex: "anull", OutLabel: "a"}

	before := backupRow()
	after := backupRow()
	after.BackupStreamKey = "rotated"

	if destSpec(before, compiled, "up") != destSpec(after, compiled, "up") {
		t.Error("rotating the BACKUP key changed the PRIMARY's hash, so the " +
			"primary would restart to deliver redundancy it already had")
	}
	if backupSpecOf(before, compiled, "up") == backupSpecOf(after, compiled, "up") {
		t.Error("rotating the backup key did not change the backup's hash, so " +
			"the backup would keep publishing to an endpoint that no longer exists")
	}
}

// Turning the toggle off must be visible to the backup's reconciler and
// invisible to the primary's.
func TestTogglingBackupDoesNotMoveThePrimarysHash(t *testing.T) {
	compiled := routing.Result{FilterComplex: "anull", OutLabel: "a"}

	on := backupRow()
	off := backupRow()
	off.BackupIngestWanted = false

	if destSpec(on, compiled, "up") != destSpec(off, compiled, "up") {
		t.Error("toggling backup changed the primary's hash; enabling redundancy " +
			"would interrupt the very stream it protects")
	}
	if backupSpecOf(on, compiled, "up") == backupSpecOf(off, compiled, "up") {
		t.Error("toggling backup did not change the backup's hash, so the setting " +
			"would be stored and never take effect")
	}
}
