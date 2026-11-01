package db

import (
	"strings"
	"testing"
)

// The X go-live title (#3). X takes a broadcast's title only on the PUBLISH transition, and nothing changes it while
// the broadcast runs, so the title has to be stored on the destination before the show: it is read hours later, by
// the lifecycle coordinator, and it must survive a restart and the reset of the lifecycle block between shows.

// TestAnXDestinationKeepsItsGoLiveTitleThroughCreateAndUpdate proves both write paths store the title and the read
// path returns it. A column missing from destColumns, the insert or the update list would read back empty.
//
// MUTATION: go_live_title removed from destUpdateCols. Observed: FAIL, the update left the first title in place.
func TestAnXDestinationKeepsItsGoLiveTitleThroughCreateAndUpdate(t *testing.T) {
	d := testDB(t)
	in := validDest()
	in.Platform = PlatformX
	in.GoLiveTitle = "Sunday practice, live"
	created, err := d.CreateDestination(in)
	if err != nil {
		t.Fatalf("create X destination with a go-live title: %v", err)
	}
	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.GoLiveTitle != "Sunday practice, live" {
		t.Fatalf("go-live title after create = %q", got.GoLiveTitle)
	}
	got.GoLiveTitle = "Week 2"
	if _, err := d.UpdateDestination(got); err != nil {
		t.Fatalf("update the go-live title: %v", err)
	}
	again, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("read back after update: %v", err)
	}
	if again.GoLiveTitle != "Week 2" {
		t.Fatalf("go-live title after update = %q, want Week 2", again.GoLiveTitle)
	}
}

// TestAGoLiveTitleIsRefusedWhereNothingWouldSendIt: only X reads the stored title. On any other platform it would be
// kept and never sent, which is a setting that silently does nothing, so it is refused with the reason.
func TestAGoLiveTitleIsRefusedWhereNothingWouldSendIt(t *testing.T) {
	in := validDest()
	in.Platform = PlatformTwitch
	in.GoLiveTitle = "Sunday practice"
	err := in.Validate()
	if err == nil || !strings.Contains(err.Error(), "go-live title") {
		t.Fatalf("Validate on a Twitch destination with a go-live title = %v, want a go-live title refusal", err)
	}
}

// TestAnXGoLiveTitleLongerThanAPostIsRefused: X documents the title as "subject to X post length limits" and gives
// no number, so the 280-character post limit is the bound (PROVISIONAL until a live PUBLISH confirms it). A longer
// title is refused at save, not at go-live, when nobody could fix it in time.
func TestAnXGoLiveTitleLongerThanAPostIsRefused(t *testing.T) {
	in := validDest()
	in.Platform = PlatformX
	in.GoLiveTitle = strings.Repeat("é", XGoLiveTitleMax)
	if err := in.Validate(); err != nil {
		t.Fatalf("a %d-character title was refused: %v", XGoLiveTitleMax, err)
	}
	in.GoLiveTitle += "x"
	if err := in.Validate(); err == nil || !strings.Contains(err.Error(), "go-live title") {
		t.Fatalf("Validate on a %d-character title = %v, want a refusal", XGoLiveTitleMax+1, err)
	}
}
