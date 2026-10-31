package captions

import (
	"testing"
	"time"
)

// TestKickIsSkippedWithAReasonAndRefusesEveryCaptionMode. Kick has no documented
// live caption route, so the honest state is that captions are not offered, and
// the reason must say so in a sentence an operator can read.
func TestKickIsSkippedWithAReasonAndRefusesEveryCaptionMode(t *testing.T) {
	s := SupportFor("kick")
	if s.Status != "skipped" || s.Reason == "" {
		t.Fatalf("kick support = %+v, want skipped with a reason", s)
	}
	for _, m := range []Mode{ModeSEI, ModeYouTubePOST, ModeFacebookSEI} {
		if Allowed("kick", m, true) {
			t.Fatalf("kick accepted caption mode %q; it must refuse every mode", m)
		}
	}
}

// TestFacebookSEIIsRefusedUntilItsFlagIsSet. The Facebook SEI path is built but
// unverified, so it is off by default, and turning it on is an explicit act.
func TestFacebookSEIIsRefusedUntilItsFlagIsSet(t *testing.T) {
	if Allowed("facebook", ModeFacebookSEI, false) {
		t.Fatalf("facebook_sei was allowed with its flag off")
	}
	if !Allowed("facebook", ModeFacebookSEI, true) {
		t.Fatalf("facebook_sei was refused with its flag on")
	}
	if SupportFor("facebook").Status != "provisional" {
		t.Fatalf("facebook is not marked provisional")
	}
}

// TestEachCaptionModeIsOnlyOfferedOnItsOwnPlatform stops a mode from being
// attached to a platform it was not built for. SEI for Twitch is the only SEI
// path marked go.
func TestEachCaptionModeIsOnlyOfferedOnItsOwnPlatform(t *testing.T) {
	if !Allowed("twitch", ModeSEI, false) {
		t.Fatalf("twitch refused its SEI mode")
	}
	if Allowed("twitch", ModeYouTubePOST, true) {
		t.Fatalf("twitch accepted the YouTube POST mode")
	}
	if Allowed("youtube", ModeSEI, true) {
		t.Fatalf("youtube accepted the Twitch SEI mode")
	}
	if SupportFor("twitch").Status != "go" {
		t.Fatalf("twitch SEI is not marked go")
	}
}

// TestAnUnknownPlatformIsNeverCaptioned. A platform added later is off until
// someone builds and decides its path, rather than inheriting a mode.
func TestAnUnknownPlatformIsNeverCaptioned(t *testing.T) {
	if Allowed("trovo", ModeSEI, true) || Allowed("example", ModeYouTubePOST, true) {
		t.Fatalf("an unknown platform accepted a caption mode")
	}
}

// TestOffIsAlwaysAllowed. The default must never be refused, or a destination
// that has no captions could not be saved.
func TestOffIsAlwaysAllowed(t *testing.T) {
	for _, p := range []string{"twitch", "youtube", "facebook", "kick", "unknown"} {
		if !Allowed(p, ModeOff, false) {
			t.Fatalf("off refused for %q", p)
		}
	}
}

// TestYouTubePOSTNeedsADelayOfExactlyThirtyOrSixtySeconds. YouTube accepts the
// caption POST only on those two broadcast delays, so any other delay is refused
// rather than sent and dropped.
func TestYouTubePOSTNeedsADelayOfExactlyThirtyOrSixtySeconds(t *testing.T) {
	cases := map[time.Duration]bool{
		0:                 false,
		29 * time.Second:  false,
		30 * time.Second:  true,
		45 * time.Second:  false,
		60 * time.Second:  true,
		120 * time.Second: false,
	}
	for d, want := range cases {
		if got := YouTubePOSTDelayOK(d); got != want {
			t.Errorf("YouTubePOSTDelayOK(%v) = %v, want %v", d, got, want)
		}
	}
}
