package db

import (
	"strings"
	"testing"
)

// The #8 settings half. Two values are stored: the per-destination caption mode
// (destinations.caption_mode) and the per-programme caption track
// (sources.caption_track). Storing them is not sending them. Nothing in the
// engine reads either value yet, and the CHANGELOG says so.

// captionDest is a destination on the given platform that differs from the
// defaults in every field the caption rules read, so a test that passes is
// reading the rule rather than a zero value.
func captionDest(platform Platform, delay int) *Destination {
	d := validDest()
	d.Platform = platform
	d.DelaySeconds = delay
	return d
}

// TestDestinationCaptionModeRoundTripsThroughCreateAndUpdate proves the mode is
// written and read back by both the create and the update path. A column that
// existed but was missing from destColumns or the update list would store
// nothing and read back as off, and nothing else in the suite would notice.
//
// MUTATION: caption_mode removed from destUpdateCols. Observed: FAIL, the
// update left "sei" in place rather than writing "off".
func TestDestinationCaptionModeRoundTripsThroughCreateAndUpdate(t *testing.T) {
	d := testDB(t)
	in := captionDest(PlatformTwitch, 0)
	in.CaptionMode = "sei"
	created, err := d.CreateDestination(in)
	if err != nil {
		t.Fatalf("create twitch destination with caption mode sei: %v", err)
	}
	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.CaptionMode != "sei" {
		t.Fatalf("caption mode after create = %q, want sei", got.CaptionMode)
	}
	got.CaptionMode = "off"
	if _, err := d.UpdateDestination(got); err != nil {
		t.Fatalf("update caption mode to off: %v", err)
	}
	again, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("read back after update: %v", err)
	}
	if again.CaptionMode != "off" {
		t.Fatalf("caption mode after update = %q, want off", again.CaptionMode)
	}
}

// TestDestinationCaptionModeEmptyIsStoredAsOff keeps the zero value honest. A
// destination built in Go with no mode set must store "off", because the column
// is NOT NULL and an empty string is not one of the modes the engine knows.
func TestDestinationCaptionModeEmptyIsStoredAsOff(t *testing.T) {
	d := testDB(t)
	created, err := d.CreateDestination(captionDest(PlatformTwitch, 0))
	if err != nil {
		t.Fatalf("create with no caption mode: %v", err)
	}
	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.CaptionMode != "off" {
		t.Fatalf("empty caption mode stored as %q, want off", got.CaptionMode)
	}
}

// TestDestinationValidateRefusesEveryCaptionModeThePlatformCannotCarry is the
// decision table, enforced at the one place a destination is refused. Kick has
// no documented route, so every mode is refused there. An unknown mode is refused
// everywhere, so a typo cannot become a mode the engine silently ignores.
//
// MUTATION: the captions.Allowed call removed from Validate. Observed: FAIL on
// the kick and unknown-mode rows.
func TestDestinationValidateRefusesEveryCaptionModeThePlatformCannotCarry(t *testing.T) {
	cases := []struct {
		name     string
		platform Platform
		delay    int
		mode     string
		wantErr  bool
	}{
		{"kick refuses sei", PlatformKick, 0, "sei", true},
		{"kick refuses youtube_post", PlatformKick, 30, "youtube_post", true},
		{"twitch takes sei", PlatformTwitch, 0, "sei", false},
		{"twitch refuses youtube_post", PlatformTwitch, 30, "youtube_post", true},
		{"unknown mode refused", PlatformTwitch, 0, "burn", true},
		{"off always allowed", PlatformKick, 0, "off", false},
		{"empty is off", PlatformKick, 0, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dst := captionDest(c.platform, c.delay)
			dst.CaptionMode = c.mode
			err := dst.Validate()
			if c.wantErr && err == nil {
				t.Fatalf("Validate accepted caption mode %q on %s, want refusal", c.mode, c.platform)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("Validate refused caption mode %q on %s: %v", c.mode, c.platform, err)
			}
		})
	}
}

// TestDestinationValidateHoldsYouTubePOSTToThe30And60SecondDelay is the YouTube
// correction from the verdicts: the HTTP POST caption path is documented only
// with a 30 s or 60 s broadcast delay, so a destination that asks for it at any
// other delay is refused at save time rather than posting late at go-live.
//
// The embedded route (sei on YouTube) has no delay requirement, and must not be
// refused for lacking one.
//
// MUTATION: the YouTubePOSTDelayOK check removed. Observed: FAIL on the
// zero-delay and 45-second rows.
func TestDestinationValidateHoldsYouTubePOSTToThe30And60SecondDelay(t *testing.T) {
	cases := []struct {
		mode    string
		delay   int
		wantErr bool
	}{
		{"youtube_post", 0, true},
		{"youtube_post", 45, true},
		{"youtube_post", 30, false},
		{"youtube_post", 60, false},
		{"sei", 0, false},
	}
	for _, c := range cases {
		dst := captionDest(PlatformYouTube, c.delay)
		dst.CaptionMode = c.mode
		err := dst.Validate()
		if c.wantErr && err == nil {
			t.Fatalf("youtube caption %q at %ds delay accepted, want refusal", c.mode, c.delay)
		}
		if !c.wantErr && err != nil {
			t.Fatalf("youtube caption %q at %ds delay refused: %v", c.mode, c.delay, err)
		}
	}
}

// TestSourceCaptionTrackRoundTripsAndNilMeansAuto proves the per-programme
// track choice survives a write and a read. nil is the automatic choice, the
// same one the captioner makes today, and it must come back as nil rather than
// as track 0, which is a real and different track.
//
// MUTATION: caption_track dropped from the sources UPDATE. Observed: FAIL, the
// updated track read back as the old value.
func TestSourceCaptionTrackRoundTripsAndNilMeansAuto(t *testing.T) {
	d := testDB(t)
	src := &Source{Name: "Programme A", Ingest: DefaultSettings().Ingest}
	if err := d.CreateSource(src); err != nil {
		t.Fatalf("create source: %v", err)
	}
	got, err := d.GetSource(src.ID)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if got.CaptionTrack != nil {
		t.Fatalf("new source caption track = %d, want nil (auto)", *got.CaptionTrack)
	}
	two := 2
	got.CaptionTrack = &two
	if err := d.UpdateSource(got); err != nil {
		t.Fatalf("set caption track 2: %v", err)
	}
	again, err := d.GetSource(src.ID)
	if err != nil {
		t.Fatalf("read after update: %v", err)
	}
	if again.CaptionTrack == nil || *again.CaptionTrack != 2 {
		t.Fatalf("caption track after update = %v, want 2", again.CaptionTrack)
	}
	again.CaptionTrack = nil
	if err := d.UpdateSource(again); err != nil {
		t.Fatalf("clear caption track: %v", err)
	}
	cleared, err := d.GetSource(src.ID)
	if err != nil {
		t.Fatalf("read after clear: %v", err)
	}
	if cleared.CaptionTrack != nil {
		t.Fatalf("cleared caption track = %d, want nil (auto)", *cleared.CaptionTrack)
	}
}

// TestSourceCaptionTrackRefusesNegativeAndAbsurdIndexes keeps the stored
// index sane. -1 is not a choice here: the choice is nil. Anything past the
// ceiling is a typo or hostile input and is refused before it is stored.
func TestSourceCaptionTrackRefusesNegativeAndAbsurdIndexes(t *testing.T) {
	d := testDB(t)
	for _, bad := range []int{-1, -5, maxCaptionTrack + 1} {
		v := bad
		src := &Source{Name: "bad track", Ingest: DefaultSettings().Ingest, CaptionTrack: &v}
		err := d.CreateSource(src)
		if err == nil || !strings.Contains(err.Error(), "caption track") {
			t.Fatalf("caption track %d accepted (err=%v), want refusal naming caption track", bad, err)
		}
	}
}
