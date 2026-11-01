package oauth

import (
	"strings"
	"testing"
)

// The platform caps that Wave 3 rollover (#19) has to plan around are facts
// about each platform's published limits, and a fact with no source is how a
// countdown on a destination card ends up wrong. These tests hold the limit
// table to the sourcing rule the capability matrix already follows: a limit
// that is stated must carry a link, and a limit nobody has verified must say so
// rather than carry a guessed number.

// TestEveryStatedPlatformLimitCarriesAnHTTPSSource refuses a stated limit with
// no link behind it. An unverified limit is allowed to have no number and no
// source, because that is the honest state for it.
func TestEveryStatedPlatformLimitCarriesAnHTTPSSource(t *testing.T) {
	for _, l := range PlatformLimits() {
		if l.Status == LimitUnverified {
			if l.Hours != 0 {
				t.Errorf("%s %s: status is unverified but Hours is %v; an unverified limit must not carry a number", l.PresetID, l.Kind, l.Hours)
			}
			continue
		}
		if l.Hours <= 0 {
			t.Errorf("%s %s: status %q but Hours is %v; a stated limit needs a positive number", l.PresetID, l.Kind, l.Status, l.Hours)
		}
		if !strings.HasPrefix(l.Source, "https://") {
			t.Errorf("%s %s: status %q but Source %q is not an https link", l.PresetID, l.Kind, l.Status, l.Source)
		}
	}
}

// TestPlatformLimitsNameRealCapabilityRows ties each limit to a row of the
// capability matrix. A limit for a preset the matrix does not describe would be
// a countdown with no card to show it on.
func TestPlatformLimitsNameRealCapabilityRows(t *testing.T) {
	rows := map[string]bool{}
	for _, p := range platformCapabilities {
		rows[p.PresetID] = true
	}
	for _, l := range PlatformLimits() {
		if !rows[l.PresetID] {
			t.Errorf("platform limit for preset %q, which the capability matrix does not describe", l.PresetID)
		}
	}
}

// TestTheRolloverCapsAreRecordedWithTheirValues pins the three caps #19 names
// and the one it leaves open. Changing a number here is a deliberate act, and
// the test makes it show up in review rather than drift silently.
func TestTheRolloverCapsAreRecordedWithTheirValues(t *testing.T) {
	want := map[string]struct {
		kind   LimitKind
		hours  float64
		status LimitStatus
	}{
		"twitch":   {LimitKindBroadcastDuration, 48, LimitReported},
		"youtube":  {LimitKindArchiveDuration, 12, LimitDocumented},
		"facebook": {LimitKindBroadcastDuration, 8, LimitDocumented},
		"kick":     {LimitKindBroadcastDuration, 0, LimitUnverified},
	}
	got := map[string]PlatformLimit{}
	for _, l := range PlatformLimits() {
		got[l.PresetID+"/"+string(l.Kind)] = l
	}
	for key, w := range want {
		name := string(w.kind)
		l, ok := got[key+"/"+name]
		if !ok {
			t.Errorf("no %s limit recorded for %s", name, key)
			continue
		}
		if l.Hours != w.hours || l.Status != w.status {
			t.Errorf("%s: got hours=%v status=%q, want hours=%v status=%q", key, l.Hours, l.Status, w.hours, w.status)
		}
	}
}

// TestTheYouTubeArchiveLimitCitesTheArchivePage pins the source of the YouTube
// 12-hour figure to the archive help page, not the DVR page it was first copied
// from. The DVR page says DVR "may be limited or unavailable" past 12 hours; the
// archive page says a longer stream "may not be captured at all", which is the
// consequence a rollover exists to prevent, so it is the citation the countdown
// must show.
func TestTheYouTubeArchiveLimitCitesTheArchivePage(t *testing.T) {
	for _, l := range PlatformLimits() {
		if l.PresetID != "youtube" || l.Kind != LimitKindArchiveDuration {
			continue
		}
		if !strings.Contains(l.Source, "support.google.com/youtube/answer/6247592") {
			t.Errorf("YouTube archive limit cites %q; it must cite the archive help page, answer/6247592", l.Source)
		}
		if strings.Contains(l.Source, "9296823") {
			t.Errorf("YouTube archive limit still cites the DVR page, answer/9296823, which does not state the archive consequence")
		}
		return
	}
	t.Fatal("no YouTube archive limit recorded")
}
