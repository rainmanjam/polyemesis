package db

import (
	"encoding/json"
	"fmt"
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

// The endpoint and the intent that gates it live in three columns of their own.
// A field that marshals and does not scan back is a backup URL that disappears
// on restart -- and the destination then publishes one feed while the card
// claims two.
//
// NOT a Facebook destination, deliberately. The intent is platform-neutral and
// so is every column it sits in; a round trip proved against a Facebook row
// would still pass if the toggle had been left inside the facebook JSON blob.
//
// Mutation, run against a committed tree: drop `backup_ingest_wanted` from the
// INSERT's column list and `dst.BackupIngestWanted` from its arguments (the
// placeholder count has to come down with it, or SQLite refuses the statement
// and the whole package fails rather than this guard). Observed FAIL: "the
// intent did not survive the round trip".
func TestTheBackupEndpointAndIntentSurviveTheDatabase(t *testing.T) {
	d := testDB(t)
	created, err := d.CreateDestination(&Destination{
		Name: "twitch", Kind: "rtmp", Platform: PlatformTwitch,
		URL: "rtmp://live.example/app", StreamKey: "primary-key",
		BackupURL: "rtmp://backup.example/app", BackupStreamKey: "backup-key",
		BackupIngestWanted: true,
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.BackupURL != "rtmp://backup.example/app" || got.BackupStreamKey != "backup-key" {
		t.Errorf("backup endpoint = %q / %q, want it preserved",
			got.BackupURL, got.BackupStreamKey)
	}
	if !got.BackupIngestWanted {
		t.Error("the intent did not survive the round trip, so redundancy an " +
			"operator switched on is off again after the next restart")
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
	// The endpoint is CHANGED, not just carried. Asserting that an unrelated
	// edit leaves it intact cannot catch a broken UPDATE: the row still holds
	// whatever the INSERT put there, which is the right answer for the wrong
	// reason. Measured -- a mutation dropping backup_url from the UPDATE left
	// that version of this test green.
	created.Name = "renamed"
	created.BackupURL = "rtmps://backup2.example/rtmp"
	created.BackupStreamKey = "rotated-backup-key"
	created.Facebook.BackupIngest = false
	if _, err := d.UpdateDestination(created); err != nil {
		t.Fatalf("UpdateDestination: %v", err)
	}
	got, _ := d.GetDestination(created.ID)
	if got.BackupURL != "rtmps://backup2.example/rtmp" {
		t.Errorf("BackupURL = %q, want the updated value; the UPDATE does not carry it",
			got.BackupURL)
	}
	if got.BackupStreamKey != "rotated-backup-key" {
		t.Errorf("BackupStreamKey = %q, want the rotated key; a key rotation would "+
			"be accepted and silently discarded", got.BackupStreamKey)
	}
	if got.Facebook.BackupIngest {
		t.Error("turning the toggle off did not persist")
	}
}

// TWO SCHEDULES, ONE DESTINATION -- the shape that shipped broken and that no
// test covered. A start schedule naming no destinations names them all, which
// is the commonest shape, so two of them both reach every Facebook destination.
// With one marker per destination the second schedule found the first's
// broadcast, moved it to its own occurrence, and the next sweep moved it back:
// a time-change notification to subscribers every five minutes forever, and one
// of the two shows with no event page at all.
//
// Mutation: in facebook.go, change Announce's appended entry from
// `ScheduleID: scheduleID` to `ScheduleID: 0`. Both shows are then filed under
// the same key and the second AnnouncementFor returns the first's broadcast.
// Observed: red on the "schedule 2 holds" assertion.
func TestTwoSchedulesOnOneDestinationKeepSeparateBroadcasts(t *testing.T) {
	now := time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC)
	tuesday := now.AddDate(0, 0, 1)
	thursday := now.AddDate(0, 0, 3)

	var f FacebookSettings
	f.Announce(1, tuesday, "tuesday-show", now)
	f.Announce(2, thursday, "thursday-show", now)

	if !f.AnnouncedFor(tuesday) || !f.AnnouncedFor(thursday) {
		t.Fatalf("both occurrences must read as announced, got %+v", f.Announcements)
	}
	one, ok := f.AnnouncementFor(1)
	if !ok || one.BroadcastID != "tuesday-show" {
		t.Errorf("schedule 1 holds %q, want tuesday-show", one.BroadcastID)
	}
	two, ok := f.AnnouncementFor(2)
	if !ok || two.BroadcastID != "thursday-show" {
		t.Errorf("schedule 2 holds %q, want thursday-show -- with one marker per "+
			"destination this is the first show's broadcast, and the two schedules "+
			"move it back and forth forever", two.BroadcastID)
	}
}

// The upgrade path. A single-pair marker is already in operators' databases, and
// it has no schedule id -- so the first schedule that needs a broadcast ADOPTS
// it rather than creating a second event page beside one people are already
// subscribed to. Once adopted it must not be adoptable again, or two schedules
// would end up holding the same live_video, which is the defect above.
//
// Mutation: in facebook.go, make merged return `out` unconditionally (delete the
// fold of the single-pair marker). The stored broadcast then reads as absent.
// Observed: red on "an upgraded row lost its broadcast".
func TestAMarkerWrittenBeforeSchedulesWereKeyedIsAdoptedOnce(t *testing.T) {
	at := time.Date(2026, 8, 9, 20, 0, 0, 0, time.UTC)
	now := at.Add(-2 * time.Hour)

	// The exact JSON an 0.2.0 row holds.
	var f FacebookSettings
	if err := json.Unmarshal([]byte(
		`{"scheduledFor":"2026-08-09T20:00:00Z","broadcastId":"777"}`), &f); err != nil {
		t.Fatalf("decode a 0.2.0 facebook column: %v", err)
	}
	if !f.AnnouncedFor(at) {
		t.Fatal("an upgraded row lost its broadcast, so the next sweep would " +
			"create a second event page for a show already announced")
	}
	held, ok := f.AnnouncementFor(5)
	if !ok || held.BroadcastID != "777" {
		t.Fatalf("schedule 5 was not offered the existing broadcast, got %+v", held)
	}

	// Schedule 5 moves it, which re-keys it.
	f.Announce(5, at.AddDate(0, 0, 7), "777", now)
	if _, ok := f.AnnouncementFor(9); ok {
		t.Error("a second schedule was offered the same broadcast after it had " +
			"been adopted; the two would move one event page back and forth")
	}
	if len(f.Announcements) != 1 {
		t.Errorf("the adopted marker was kept beside the re-keyed one: %+v", f.Announcements)
	}
}

// The bound. A destination on a daily schedule passes an occurrence every day,
// and a list that kept every one of them is a row that only ever grows.
//
// Mutation: in facebook.go, widen announcementRetention to
// `24 * 3650 * time.Hour`, which is the prune never firing. Observed: red, 11
// markers kept instead of 1. (Deleting the switch arm instead does not build --
// `stale` goes unused -- so the retention is the honest one-line version.)
func TestMarkersForShowsThatHaveBeenAndGoneAreDropped(t *testing.T) {
	now := time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC)

	var f FacebookSettings
	// Ten schedules whose shows are all long past, then one that is not. Ten
	// DIFFERENT broadcasts: the same id twice is replaced rather than kept, so a
	// loop that reused one would prove nothing about accumulation.
	for i := range 10 {
		f.Announce(int64(i+1), now.AddDate(0, 0, -30+i), fmt.Sprintf("old-%d", i), now)
	}
	f.Announce(99, now.Add(2*time.Hour), "next", now)

	if len(f.Announcements) != 1 {
		t.Fatalf("kept %d markers, want 1 -- everything but the show still ahead "+
			"has been and gone: %+v", len(f.Announcements), f.Announcements)
	}
	if f.BroadcastID != "next" {
		t.Errorf("the card links to %q, want the show that has not happened yet", f.BroadcastID)
	}
}

// The mirror is what the destination card links to, and it must name the
// SOONEST announced show rather than the last one written -- a sweep announces
// in schedule order, not in time order.
//
// Mutation: in facebook.go, reverse Announce's sort comparison to
// `return kept[j].Occurrence.Before(kept[i].Occurrence)`. Observed: red -- the
// card links to next week's show while tonight's is the one on air. (Deleting
// the sort instead does not build: the sort import goes unused.)
func TestTheCardLinksToTheSoonestAnnouncedShow(t *testing.T) {
	now := time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC)

	var f FacebookSettings
	f.Announce(1, now.AddDate(0, 0, 6), "next-week", now)
	f.Announce(2, now.Add(3*time.Hour), "tonight", now)

	if f.BroadcastID != "tonight" {
		t.Errorf("the card links to %q, want tonight's show", f.BroadcastID)
	}
	if !f.ScheduledFor.Equal(now.Add(3 * time.Hour)) {
		t.Errorf("the mirrored time is %v, want tonight's", f.ScheduledFor)
	}
}

// Same reasoning as TestTheMarkerDoesNotMakeSettingsLookNonEmpty, for the list
// that replaced the pair. Empty answers "is there anything to SEND at create
// time"; a destination that has been announced has nothing extra to send.
//
// Mutation: in facebook.go, add `len(f.Announcements) == 0 &&` to Empty.
// Observed: red.
func TestThePerShowMarkersDoNotMakeSettingsLookNonEmpty(t *testing.T) {
	now := time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC)
	var f FacebookSettings
	f.Announce(1, now.Add(time.Hour), "777", now)
	if !f.Empty() {
		t.Error("a destination with only announcement markers reads as having " +
			"create-time settings to send, so dropUnsendableSettings would refuse " +
			"to clear it on a platform change")
	}
}

// The list rides the same `facebook` JSON column the pair does, which is what
// makes per-show state possible without a migration. UPDATE is a separate
// statement from INSERT and forgetting one of them is the realistic mistake, so
// the second show is added by an UPDATE.
//
// Mutation: drop `facebook=?` (and its argument) from UpdateDestination's
// UPDATE. Observed: red -- the second show's marker never lands, and every
// later sweep announces it again.
func TestThePerShowMarkersSurviveTheDatabase(t *testing.T) {
	d := testDB(t)
	now := time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC)
	tuesday := now.AddDate(0, 0, 1)
	thursday := now.AddDate(0, 0, 3)

	var f FacebookSettings
	f.Announce(1, tuesday, "tuesday-show", now)
	created, err := d.CreateDestination(&Destination{
		Name: "fb", Kind: "rtmp", Platform: PlatformFacebook,
		URL: "rtmps://live.example/rtmp", StreamKey: "k", Facebook: f,
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	created.Facebook.Announce(2, thursday, "thursday-show", now)
	if _, err := d.UpdateDestination(created); err != nil {
		t.Fatalf("UpdateDestination: %v", err)
	}

	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if !got.Facebook.AnnouncedFor(tuesday) || !got.Facebook.AnnouncedFor(thursday) {
		t.Fatalf("the markers did not survive the round trip: %+v", got.Facebook.Announcements)
	}
	two, ok := got.Facebook.AnnouncementFor(2)
	if !ok || two.BroadcastID != "thursday-show" {
		t.Errorf("schedule 2 holds %q after a reload, want thursday-show", two.BroadcastID)
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
