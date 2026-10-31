package db

import (
	"errors"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// A template is stored per schedule. The round trip must keep every operator-
// facing field, and the thumbnail must NOT come back on the ordinary read, because
// the bytes are a 2 MB field and the settings screen needs only to know one exists.
func TestABroadcastTemplateRoundTripsItsFieldsAndKeepsItsThumbnailOutOfTheRead(t *testing.T) {
	d := testDB(t)
	saved, err := d.SaveBroadcastTemplate(scheduler.BroadcastTemplate{
		ScheduleID:   42,
		TitlePattern: "Friday live {date}",
		Description:  "Every week at seven.",
		Privacy:      "unlisted",
		Category:     "Gaming",
		Lookahead:    3,
	})
	if err != nil {
		t.Fatalf("SaveBroadcastTemplate: %v", err)
	}
	if saved.TitlePattern != "Friday live {date}" || saved.Privacy != "unlisted" || saved.Lookahead != 3 {
		t.Fatalf("saved template lost fields: %+v", saved)
	}
	if err := d.SetBroadcastTemplateThumbnail(42, []byte("\x89PNG-fake-bytes"), "image/png"); err != nil {
		t.Fatalf("SetBroadcastTemplateThumbnail: %v", err)
	}

	got, err := d.GetBroadcastTemplate(42)
	if err != nil {
		t.Fatalf("GetBroadcastTemplate: %v", err)
	}
	if !got.HasThumbnail {
		t.Fatalf("a stored thumbnail is reported as absent")
	}
	if len(got.Thumbnail) != 0 {
		t.Fatalf("the ordinary read carried %d thumbnail bytes; it must not", len(got.Thumbnail))
	}
}

// Saving the text again must not wipe the thumbnail: the operator edits the title and
// expects the picture to stay. The thumbnail has its own write.
func TestSavingATemplateAgainKeepsItsThumbnail(t *testing.T) {
	d := testDB(t)
	if _, err := d.SaveBroadcastTemplate(scheduler.BroadcastTemplate{ScheduleID: 5, TitlePattern: "Show {date}"}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := d.SetBroadcastTemplateThumbnail(5, []byte("img"), "image/jpeg"); err != nil {
		t.Fatalf("thumbnail: %v", err)
	}
	if _, err := d.SaveBroadcastTemplate(scheduler.BroadcastTemplate{ScheduleID: 5, TitlePattern: "Show renamed {date}"}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	got, err := d.GetBroadcastTemplate(5)
	if err != nil {
		t.Fatalf("GetBroadcastTemplate: %v", err)
	}
	if !got.HasThumbnail {
		t.Fatalf("editing the title dropped the thumbnail")
	}
}

// A thumbnail for a template that does not exist has nowhere to live, and must be
// refused as not-found rather than silently creating a half-template.
func TestAThumbnailForATemplateThatDoesNotExistIsNotFound(t *testing.T) {
	d := testDB(t)
	if err := d.SetBroadcastTemplateThumbnail(99, []byte("img"), "image/png"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("thumbnail on a missing template = %v, want ErrNotFound", err)
	}
}

// The occurrence claim is the compare-and-set that stops two sweeps creating the same
// platform event twice. The second claim for the same schedule, destination and instant
// must be refused with the sentinel, not stored beside the first.
func TestClaimingAnOccurrenceTwiceIsRefusedSoOneEventIsCreated(t *testing.T) {
	d := testDB(t)
	at := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	o := scheduler.Occurrence{ScheduleID: 7, DestinationID: 3, At: at, State: scheduler.OccurrencePending}

	id, err := d.ClaimOccurrence(o)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := d.ClaimOccurrence(o); !errors.Is(err, ErrOccurrenceClaimed) {
		t.Fatalf("second claim = %v, want ErrOccurrenceClaimed", err)
	}
	if err := d.ResolveOccurrence(id, "vid-123", "Show 2026-11-03"); err != nil {
		t.Fatalf("ResolveOccurrence: %v", err)
	}
	got, err := d.OccurrenceFor(7, 3, at)
	if err != nil {
		t.Fatalf("OccurrenceFor: %v", err)
	}
	if got.State != scheduler.OccurrenceUpcoming || got.PlatformEventID != "vid-123" {
		t.Fatalf("resolved occurrence = state %q event %q, want upcoming with the event id", got.State, got.PlatformEventID)
	}
}

// A failed create releases its claim, so the next sweep is free to try again.
// A claim that outlived its failure would suppress the show for good.
func TestAReleasedClaimCanBeMadeAgain(t *testing.T) {
	d := testDB(t)
	at := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	o := scheduler.Occurrence{ScheduleID: 8, DestinationID: 4, At: at, State: scheduler.OccurrencePending}
	id, err := d.ClaimOccurrence(o)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := d.DeleteOccurrence(id); err != nil {
		t.Fatalf("release claim: %v", err)
	}
	if _, err := d.ClaimOccurrence(o); err != nil {
		t.Fatalf("claim after release = %v, want it to succeed", err)
	}
}

// Skipping keeps the row, so the look-ahead does not create the occurrence again.
// Deleting it would let the next sweep resurrect the event the operator removed.
func TestASkippedOccurrenceStaysOnRecordSoTheLookaheadDoesNotRecreateIt(t *testing.T) {
	d := testDB(t)
	at := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	id, err := d.ClaimOccurrence(scheduler.Occurrence{ScheduleID: 9, DestinationID: 1, At: at, State: scheduler.OccurrencePending})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := d.ResolveOccurrence(id, "vid-9", "t"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := d.SetOccurrenceState(id, scheduler.OccurrenceSkipped); err != nil {
		t.Fatalf("skip: %v", err)
	}
	got, err := d.OccurrenceFor(9, 1, at)
	if err != nil {
		t.Fatalf("a skipped occurrence was removed from the record: %v", err)
	}
	if got.State != scheduler.OccurrenceSkipped {
		t.Fatalf("state = %q, want skipped", got.State)
	}
}

// The calendar reads a window, and must return only upcoming and skipped rows that fall
// inside it, in time order.
func TestListOccurrencesReturnsTheWindowInTimeOrder(t *testing.T) {
	d := testDB(t)
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i, day := range []int{5, 2, 9} {
		at := base.AddDate(0, 0, day)
		id, err := d.ClaimOccurrence(scheduler.Occurrence{ScheduleID: 1, DestinationID: int64(i + 1), At: at, State: scheduler.OccurrencePending})
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if err := d.ResolveOccurrence(id, "e", "t"); err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
	}
	got, err := d.ListOccurrences(base, base.AddDate(0, 0, 6))
	if err != nil {
		t.Fatalf("ListOccurrences: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("window returned %d rows, want 2 (days 2 and 5)", len(got))
	}
	if !got[0].At.Before(got[1].At) {
		t.Fatalf("window is not in time order: %v then %v", got[0].At, got[1].At)
	}
}
