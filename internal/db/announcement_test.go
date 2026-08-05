package db

import (
	"errors"
	"testing"
	"time"
)

func announcedDestination(t *testing.T, d *DB) *Destination {
	t.Helper()
	created, err := d.CreateDestination(&Destination{
		Name: "fb", Kind: "rtmp", Platform: PlatformFacebook,
		URL: "rtmps://live.example/rtmp", StreamKey: "original-key",
		AudioBitrate: 128,
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	return created
}

// The pre-announce sweep holds a destination across a Graph call that can take
// thirty seconds, and it used to write the whole row back afterwards -- so a
// rename, an enable, or a routing change made in that window was silently
// reverted. This write owns four columns and no others.
//
// Mutation: in destinations.go, add `name=?` to UpdateAnnouncement's UPDATE and
// pass cur.Name. That is not enough on its own to revert anything, because the
// row is re-read here -- which is the point, and the reason the API-side guard
// TestAnOperatorEditDuringTheGraphCallSurvives exists as well. Mutation that
// this one catches: drop `stream_key=?` (and its argument) from the same
// statement. Observed: red -- the pre-created key never reaches the encoder and
// the announced event page stays empty beside a live stream.
func TestUpdateAnnouncementWritesOnlyWhatThePreannounceSweepOwns(t *testing.T) {
	d := testDB(t)
	created := announcedDestination(t, d)
	at := time.Date(2026, 8, 9, 20, 0, 0, 0, time.UTC)

	got, err := d.UpdateAnnouncement(created.ID, func(cur *Destination) bool {
		cur.StreamKey = "key-from-the-broadcast"
		cur.BackupURL, cur.BackupStreamKey = "rtmps://backup.example/rtmp", "backup-key"
		cur.Facebook.Announce(7, at, "777", at.Add(-time.Hour))
		// Everything below is outside the four columns this write owns, and is
		// dropped. A caller that could write any column would be
		// UpdateDestination, which is the thing this exists not to be.
		cur.Name = "renamed by the sweep"
		cur.Enabled = true
		cur.AudioBitrate = 320
		return true
	})
	if err != nil {
		t.Fatalf("UpdateAnnouncement: %v", err)
	}

	if got.StreamKey != "key-from-the-broadcast" {
		t.Errorf("StreamKey = %q, want the pre-created broadcast's key", got.StreamKey)
	}
	if got.BackupURL != "rtmps://backup.example/rtmp" || got.BackupStreamKey != "backup-key" {
		t.Errorf("backup endpoint = %q / %q, want it stored", got.BackupURL, got.BackupStreamKey)
	}
	if !got.Facebook.AnnouncedFor(at) {
		t.Error("the marker was not recorded, so the next sweep announces the show again")
	}
	if got.Name != "fb" {
		t.Errorf("Name = %q -- this write reaches a column it does not own, so an "+
			"operator edit made during the Graph call is reverted", got.Name)
	}
	if got.Enabled {
		t.Error("the enable switch was written by the pre-announce sweep")
	}
	if got.AudioBitrate != 128 {
		t.Errorf("AudioBitrate = %d, want 128 untouched", got.AudioBitrate)
	}
}

// The row the callback is shown is the row as it stands NOW, not whatever the
// caller loaded before it went off to Facebook. Everything else in the Facebook
// blob -- crossposting, the donate charity, the backup toggle -- rides the same
// column the markers do, so writing that column from a stale copy would revert
// those too.
//
// Mutation: in destinations.go, change UpdateAnnouncement's
// `scanDestination(tx.QueryRow(destByIDQuery, id))` to read nothing and start
// from a zero Destination. Observed: red -- the donate charity set after the
// sweep's snapshot is gone.
func TestUpdateAnnouncementAppliesToTheRowAsItStandsNow(t *testing.T) {
	d := testDB(t)
	created := announcedDestination(t, d)

	// The operator edit that lands while the sweep is talking to Facebook.
	created.Facebook.DonateCharityID = "999"
	created.Name = "renamed by the operator"
	if _, err := d.UpdateDestination(created); err != nil {
		t.Fatalf("operator edit: %v", err)
	}

	var sawCharity, sawName string
	got, err := d.UpdateAnnouncement(created.ID, func(cur *Destination) bool {
		sawCharity, sawName = cur.Facebook.DonateCharityID, cur.Name
		cur.Facebook.Announce(7, time.Now().Add(time.Hour), "777", time.Now())
		return true
	})
	if err != nil {
		t.Fatalf("UpdateAnnouncement: %v", err)
	}
	if sawCharity != "999" || sawName != "renamed by the operator" {
		t.Fatalf("the callback was shown a stale row (%q / %q)", sawName, sawCharity)
	}
	if got.Facebook.DonateCharityID != "999" {
		t.Error("the donate charity set during the sweep was reverted by the " +
			"announcement write")
	}
}

// The callback can refuse the row it is shown, which is how the sweep declines
// to rewrite the stream key of a destination that went live while Facebook was
// being asked for one. Refusing must write NOTHING.
//
// Mutation: in destinations.go, ignore apply's return value (call `apply(cur)`
// as a statement). Observed: red -- the stream key of a live destination is
// replaced, which cycles the running FFmpeg at a moment nobody chose.
func TestUpdateAnnouncementCanRefuseTheRowItIsShown(t *testing.T) {
	d := testDB(t)
	created := announcedDestination(t, d)

	_, err := d.UpdateAnnouncement(created.ID, func(cur *Destination) bool {
		cur.StreamKey = "key-from-the-broadcast"
		return false
	})
	if !errors.Is(err, ErrAnnouncementSkipped) {
		t.Fatalf("err = %v, want ErrAnnouncementSkipped", err)
	}
	got, _ := d.GetDestination(created.ID)
	if got.StreamKey != "original-key" {
		t.Errorf("StreamKey = %q after a refused write, want it untouched", got.StreamKey)
	}
}

func TestUpdateAnnouncementOnAMissingDestination(t *testing.T) {
	d := testDB(t)
	if _, err := d.UpdateAnnouncement(404, func(*Destination) bool { return true }); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
