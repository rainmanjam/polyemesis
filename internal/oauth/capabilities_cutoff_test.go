package oauth

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// A countdown is only as good as the cutoff it counts to. These tests pin the
// rule that a cutoff is measured from the instant the broadcast went live, that
// a limit nobody verified yields no cutoff at all (so Kick never shows a timer),
// and that a destination with no recorded start yields none either.

// TestCutoffIsMeasuredFromTheLiveStartForAStatedLimit checks the arithmetic on
// the one stated broadcast-duration cap: 48 hours from the moment of going live.
func TestCutoffIsMeasuredFromTheLiveStartForAStatedLimit(t *testing.T) {
	lim, ok := RolloverLimitFor(db.PlatformTwitch)
	if !ok {
		t.Fatal("no rollover limit for Twitch")
	}
	start := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	got, ok := lim.CutoffFor(start)
	if !ok {
		t.Fatal("Twitch has a stated 48 h cap; CutoffFor reported no cutoff")
	}
	want := start.Add(48 * time.Hour)
	if !got.Equal(want) {
		t.Errorf("Twitch cutoff = %s, want %s (48 h after the live start)", got, want)
	}
}

// TestKickNeverGetsACutoffBecauseItsLimitIsUnverified is the rule that stops a
// countdown running on a guess. Kick's limit is LimitUnverified with no number,
// so there must be no cutoff whatever the start time.
func TestKickNeverGetsACutoffBecauseItsLimitIsUnverified(t *testing.T) {
	lim, ok := RolloverLimitFor(db.PlatformKick)
	if !ok {
		t.Skip("Kick has no limit row at all, which is also a state with no countdown")
	}
	if _, ok := lim.CutoffFor(time.Now()); ok {
		t.Errorf("Kick reported a cutoff from an unverified limit; no countdown may run on a guess")
	}
}

// TestNoCutoffWithoutAKnownLiveStart refuses to count from the zero time. A
// broadcast whose start the platform did not report would otherwise show a
// countdown counted from year one, which reads as already overdue.
func TestNoCutoffWithoutAKnownLiveStart(t *testing.T) {
	lim, ok := RolloverLimitFor(db.PlatformTwitch)
	if !ok {
		t.Fatal("no rollover limit for Twitch")
	}
	if _, ok := lim.CutoffFor(time.Time{}); ok {
		t.Errorf("a zero live start produced a cutoff; it must produce none")
	}
}

// TestYouTubeRolloverIsDrivenByItsArchiveLimit checks that the limit a YouTube
// rollover counts to is the 12-hour archive cap, which is the one the platform
// actually enforces on a long stream, and not some broadcast-duration figure.
func TestYouTubeRolloverIsDrivenByItsArchiveLimit(t *testing.T) {
	lim, ok := RolloverLimitFor(db.PlatformYouTube)
	if !ok {
		t.Fatal("no rollover limit for YouTube")
	}
	if lim.Kind != LimitKindArchiveDuration || lim.Hours != 12 {
		t.Errorf("YouTube rollover limit = %s %vh, want archiveDuration 12h", lim.Kind, lim.Hours)
	}
}

// TestNoLimitForAPlatformWithoutARow keeps the lookup honest for a platform the
// table does not describe, such as a custom RTMP destination: no limit, so no
// countdown and no rollover.
func TestNoLimitForAPlatformWithoutARow(t *testing.T) {
	if _, ok := RolloverLimitFor(db.Platform("custom-rtmp")); ok {
		t.Errorf("a platform with no limit row produced a rollover limit")
	}
}
