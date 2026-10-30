package metrics

import (
	"regexp"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/failreason"
)

// WHY A DESTINATION IS FAILING IS ON THE SCRAPE (#13, from #20). The current
// cause is one-hot like the state series, the hold is its own gauge, and every
// failed run is counted under its cause.
func TestTheScrapeSaysWhyADestinationIsFailing(t *testing.T) {
	got := parse(t, Render(testSnapshot()))

	for series, want := range map[string]string{
		`polyemesis_destination_failure_reason{id="9",name="Twitch",cause="key_rejected"}`: "1",
		`polyemesis_destination_failure_reason{id="9",name="Twitch",cause="timeout"}`:      "0",
		`polyemesis_destination_held{id="9",name="Twitch"}`:                                "1",
		`polyemesis_destination_failures_total{id="9",name="Twitch",cause="key_rejected"}`: "3",
		`polyemesis_destination_failures_total{id="9",name="Twitch",cause="timeout"}`:      "1",
		`polyemesis_destination_failures_total{id="9",name="Twitch",cause="dns"}`:          "0",
		// A healthy destination has every cause at 0 and is not held.
		`polyemesis_destination_failure_reason{id="3",name="Archive",cause="unknown"}`: "0",
		`polyemesis_destination_held{id="3",name="Archive"}`:                           "0",
	} {
		v, ok := got[series]
		if !ok {
			t.Errorf("series %s is missing", series)
			continue
		}
		if v != want {
			t.Errorf("%s = %s, want %s", series, v, want)
		}
	}
}

// EVERY CAUSE IS PRESENT AT 0 BEFORE ANYTHING HAS FAILED. A key refused three
// times inside one scrape interval would otherwise first appear at 3, and
// increase() over a series with no earlier sample reads 0 -- the failure would
// never show on the dashboard that exists to show it.
func TestEveryCauseIsEmittedForEveryDestinationBeforeItFails(t *testing.T) {
	out := Render(Snapshot{Destinations: []Destination{{ID: 1, Name: "Fresh"}}})
	got := parse(t, out)
	for _, c := range failreason.AllCauses() {
		for _, fam := range []string{"polyemesis_destination_failures_total", "polyemesis_destination_failure_reason"} {
			series := fam + `{id="1",name="Fresh",cause="` + string(c) + `"}`
			if v, ok := got[series]; !ok || v != "0" {
				t.Errorf("%s = %q (present %v), want 0", series, v, ok)
			}
		}
	}
}

// THE CAUSE LABEL IS BOUNDED. Whatever a caller hands in -- here a cause that is
// not in the enum and is shaped like a publish URL with a key in it -- the label
// value is one of the nine codes, and the text does not reach the exposition.
func TestACauseOutsideTheEnumIsRenderedAsUnknownAndNeverPassedThrough(t *testing.T) {
	const secret = "sk_live_DO_NOT_LEAK_9f3a"
	bogus := failreason.Cause("rtmp://a.rtmp.youtube.com/live2/" + secret)
	out := Render(Snapshot{Destinations: []Destination{{
		ID: 4, Name: "YouTube", Cause: bogus,
		Failures: map[failreason.Cause]int{bogus: 2, failreason.Unknown: 1},
	}}})
	if strings.Contains(out, secret) {
		t.Fatalf("the exposition carries text from a cause outside the enum:\n%s", out)
	}
	got := parse(t, out)
	if v := got[`polyemesis_destination_failure_reason{id="4",name="YouTube",cause="unknown"}`]; v != "1" {
		t.Errorf("current cause outside the enum: unknown = %q, want 1", v)
	}
	if v := got[`polyemesis_destination_failures_total{id="4",name="YouTube",cause="unknown"}`]; v != "3" {
		t.Errorf("failures outside the enum fold into unknown: %q, want 3", v)
	}
	assertCauseLabelsAreKnown(t, out)
}

// assertCauseLabelsAreKnown fails on any cause="..." label value that is not one
// of failreason.AllCauses().
func assertCauseLabelsAreKnown(t *testing.T, out string) {
	t.Helper()
	known := map[string]bool{}
	for _, c := range failreason.AllCauses() {
		known[string(c)] = true
	}
	for _, m := range regexp.MustCompile(`cause="([^"]*)"`).FindAllStringSubmatch(out, -1) {
		if !known[m[1]] {
			t.Errorf("cause label %q is not a published cause", m[1])
		}
	}
}
