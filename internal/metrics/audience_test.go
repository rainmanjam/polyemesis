package metrics

import (
	"strings"
	"testing"
)

// TestAudienceLeavesAnUnreportedPlatformWithoutAViewersSample: a platform
// that did not report has no viewers sample (not a 0) and a reported 0.
func TestAudienceLeavesAnUnreportedPlatformWithoutAViewersSample(t *testing.T) {
	got := parse(t, Render(testSnapshot()))
	if got[`polyemesis_platform_viewers{platform="twitch"}`] != "40" {
		t.Fatalf("twitch viewers = %q, want 40", got[`polyemesis_platform_viewers{platform="twitch"}`])
	}
	if v, ok := got[`polyemesis_platform_viewers{platform="kick"}`]; ok {
		t.Fatalf("kick did not report and has a viewers sample %q; an absent count must not be a 0", v)
	}
	if got[`polyemesis_platform_viewers_reported{platform="kick"}`] != "0" ||
		got[`polyemesis_platform_viewers_reported{platform="twitch"}`] != "1" {
		t.Fatalf("reported = kick %q twitch %q", got[`polyemesis_platform_viewers_reported{platform="kick"}`],
			got[`polyemesis_platform_viewers_reported{platform="twitch"}`])
	}
	if got["polyemesis_audience_viewers"] != "42" {
		t.Fatalf("total = %q, want 42", got["polyemesis_audience_viewers"])
	}
}

func TestAudienceWithNothingReportedHasNoTotalSample(t *testing.T) {
	s := testSnapshot()
	s.Audience = &Audience{Platforms: map[string]*int{"youtube": nil}}
	text := Render(s)
	if strings.Contains(text, "\npolyemesis_audience_viewers ") {
		t.Fatal("a total was published with nothing reported")
	}
	if !strings.Contains(text, "# TYPE polyemesis_audience_viewers gauge") {
		t.Fatal("the family header is missing, so a rule cannot tell absent from undeclared")
	}
	s.Audience = nil
	if strings.Contains(Render(s), "polyemesis_platform_viewers") {
		t.Fatal("audience families published with no sampler")
	}
}
