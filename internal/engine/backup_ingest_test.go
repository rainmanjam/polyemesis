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
		Facebook: db.FacebookSettings{BackupIngest: true},
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
		{"endpoint without the toggle", func(r *db.Destination) { r.Facebook.BackupIngest = false }, false},
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
	off.Facebook.BackupIngest = false

	if destSpec(on, compiled, "up") != destSpec(off, compiled, "up") {
		t.Error("toggling backup changed the primary's hash; enabling redundancy " +
			"would interrupt the very stream it protects")
	}
	if backupSpecOf(on, compiled, "up") == backupSpecOf(off, compiled, "up") {
		t.Error("toggling backup did not change the backup's hash, so the setting " +
			"would be stored and never take effect")
	}
}
