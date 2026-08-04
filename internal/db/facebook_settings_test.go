package db

import (
	"testing"
	"time"
)

// The marker is a TIME, and this is the case that decides it. A boolean
// "already announced" is true forever after the first week, so every occurrence
// after that would get no event page -- a weekly show would be announced once,
// ever, and nobody would notice because the first week worked.
func TestTheAnnouncedMarkerDistinguishesOneOccurrenceFromTheNext(t *testing.T) {
	week1 := time.Date(2026, 8, 9, 20, 0, 0, 0, time.UTC)
	week2 := week1.AddDate(0, 0, 7)

	f := FacebookSettings{ScheduledFor: week1, BroadcastID: "777"}
	if !f.AnnouncedFor(week1) {
		t.Error("the occurrence it was announced for reads as not announced")
	}
	if f.AnnouncedFor(week2) {
		t.Error("next week's occurrence reads as already announced; a weekly " +
			"show would get one event page ever")
	}
}

// A recorded time with no broadcast behind it is not an announcement. This is
// the state a half-written marker would leave, and treating it as done would
// suppress every later attempt for that occurrence.
func TestATimeWithNoBroadcastIsNotAnAnnouncement(t *testing.T) {
	at := time.Date(2026, 8, 9, 20, 0, 0, 0, time.UTC)
	f := FacebookSettings{ScheduledFor: at}
	if f.AnnouncedFor(at) {
		t.Error("a marker with no broadcast id reads as announced, so nothing " +
			"would ever try again for this occurrence")
	}
}

// Round-tripping matters because these live in the existing `facebook` JSON
// column. A field that marshals and does not scan back is a marker that resets
// on restart, which means a new Facebook event page every time the server
// starts.
func TestTheAnnouncedMarkerSurvivesTheDatabase(t *testing.T) {
	d := testDB(t)
	at := time.Date(2026, 8, 9, 20, 0, 0, 0, time.UTC)

	created, err := d.CreateDestination(&Destination{
		Name: "fb", Kind: "rtmp", Platform: PlatformFacebook,
		URL: "rtmps://live.example/rtmp", StreamKey: "k",
		Facebook: FacebookSettings{ScheduledFor: at, BroadcastID: "777"},
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if !got.Facebook.ScheduledFor.Equal(at) {
		t.Errorf("ScheduledFor = %v, want %v", got.Facebook.ScheduledFor, at)
	}
	if got.Facebook.BroadcastID != "777" {
		t.Errorf("BroadcastID = %q, want 777", got.Facebook.BroadcastID)
	}
	if !got.Facebook.AnnouncedFor(at) {
		t.Error("the marker did not survive the round trip")
	}
}

// Empty answers "is there anything to SEND at create time". The marker is
// bookkeeping, not a create-time parameter, so folding it in would make an
// announced destination look like one with crossposting configured -- and
// dropUnsendableSettings would then refuse to clear it on a platform change.
func TestTheMarkerDoesNotMakeSettingsLookNonEmpty(t *testing.T) {
	f := FacebookSettings{
		ScheduledFor: time.Date(2026, 8, 9, 20, 0, 0, 0, time.UTC),
		BroadcastID:  "777",
	}
	if !f.Empty() {
		t.Error("a destination with only an announcement marker reads as having " +
			"create-time settings to send")
	}
}

// The endpoint lives in its own columns and the toggle rides the existing
// facebook JSON blob. A field that marshals and does not scan back is a backup
// URL that disappears on restart -- and the destination then publishes one feed
// while the card claims two.
func TestTheBackupEndpointAndToggleSurviveTheDatabase(t *testing.T) {
	d := testDB(t)
	created, err := d.CreateDestination(&Destination{
		Name: "fb", Kind: "rtmp", Platform: PlatformFacebook,
		URL: "rtmps://live.example/rtmp", StreamKey: "primary-key",
		BackupURL: "rtmps://backup.example/rtmp", BackupStreamKey: "backup-key",
		Facebook: FacebookSettings{BackupIngest: true},
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.BackupURL != "rtmps://backup.example/rtmp" || got.BackupStreamKey != "backup-key" {
		t.Errorf("backup endpoint = %q / %q, want it preserved",
			got.BackupURL, got.BackupStreamKey)
	}
	if !got.Facebook.BackupIngest {
		t.Error("the toggle did not survive the round trip")
	}
}

// UPDATE is a separate statement from INSERT, and forgetting one of them is the
// realistic mistake: creating works, and then any later edit silently reverts.
func TestUpdatingADestinationKeepsItsBackupEndpoint(t *testing.T) {
	d := testDB(t)
	created, err := d.CreateDestination(&Destination{
		Name: "fb", Kind: "rtmp", Platform: PlatformFacebook,
		URL: "rtmps://live.example/rtmp", StreamKey: "k",
		BackupURL: "rtmps://backup.example/rtmp", BackupStreamKey: "bk",
		Facebook: FacebookSettings{BackupIngest: true},
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	created.Name = "renamed"
	if _, err := d.UpdateDestination(created); err != nil {
		t.Fatalf("UpdateDestination: %v", err)
	}
	got, _ := d.GetDestination(created.ID)
	if got.BackupURL == "" || got.BackupStreamKey == "" {
		t.Fatalf("an unrelated edit erased the backup endpoint: %q / %q",
			got.BackupURL, got.BackupStreamKey)
	}
	if !got.Facebook.BackupIngest {
		t.Error("an unrelated edit cleared the toggle")
	}
}

// Unlike the announcement marker, BackupIngest IS a create-time parameter, so
// it must count as a setting to send. Otherwise dropUnsendableSettings would
// treat a backup-enabled destination as empty.
func TestTheBackupToggleCountsAsASettingToSend(t *testing.T) {
	if (FacebookSettings{BackupIngest: true}).Empty() {
		t.Error("a destination asking for backup ingest reads as having nothing to send")
	}
}
