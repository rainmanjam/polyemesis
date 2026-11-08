package db

import (
	"strings"
	"testing"
)

func TestYouTubeLiveNowIsOnForALinkedAccountAndOffForEverythingElse(t *testing.T) {
	acct := int64(1)
	base := func() *Destination {
		return &Destination{Platform: PlatformYouTube, Kind: DestRTMP, AccountID: &acct}
	}
	cases := []struct {
		name string
		edit func(*Destination)
		want bool
	}{
		{"linked account, nothing set: the default is on", func(*Destination) {}, true},
		{"switched to Studio's Stream now", func(d *Destination) { d.Compliance.YouTube = &YouTubeBroadcastControls{StudioStreamNow: true} }, false},
		{"a pinned stream alone does not turn it off", func(d *Destination) { d.Compliance.YouTube = &YouTubeBroadcastControls{StreamID: "s-1"} }, true},
		{"a pasted key with no account is never touched", func(d *Destination) { d.AccountID = nil }, false},
		{"another platform", func(d *Destination) { d.Platform = PlatformTwitch }, false},
		{"a non-RTMP destination", func(d *Destination) { d.Kind = DestSRT }, false},
	}
	for _, c := range cases {
		d := base()
		c.edit(d)
		if got := d.YouTubeLiveNow(); got != c.want {
			t.Errorf("%s: YouTubeLiveNow = %v, want %v", c.name, got, c.want)
		}
	}
	if (*Destination)(nil).YouTubeLiveNow() {
		t.Error("a nil destination cannot be live-now")
	}
}

// The two new controls are not toggles: they must not make a compliance block look non-empty, or every row that
// touched the picker would start pushing compliance to YouTube.
func TestPinnedStreamAndStudioStreamNowDoNotMakeComplianceNonEmpty(t *testing.T) {
	c := Compliance{YouTube: &YouTubeBroadcastControls{StreamID: "s-1", StudioStreamNow: true}}
	if !c.Empty() {
		t.Error("a pinned stream or Studio's Stream now made compliance non-empty, so it would be pushed")
	}
}

func TestAPinnedStreamIsValidatedAndYouTubeOnly(t *testing.T) {
	acct := int64(1)
	d := Destination{Name: "x", Platform: PlatformYouTube, Kind: DestRTMP, URL: "rtmp://a/live2", StreamKey: "k", AccountID: &acct}
	probs := func() string {
		if err := d.Validate(); err != nil {
			return err.Error()
		}
		return ""
	}
	d.Compliance.YouTube = &YouTubeBroadcastControls{StreamID: "bad id with spaces"}
	if !strings.Contains(probs(), "stream id") {
		t.Error("a malformed stream id was accepted")
	}
	d.Compliance.YouTube = &YouTubeBroadcastControls{StreamID: "abc-DEF_1.2"}
	if strings.Contains(probs(), "stream id") {
		t.Errorf("a well-formed stream id was refused: %v", probs())
	}
	d.Platform = PlatformTwitch
	if !strings.Contains(probs(), "pinned YouTube stream") {
		t.Error("a pinned YouTube stream was accepted on a Twitch destination")
	}
}

func TestTheYouTubeCreateFieldsAreValidated(t *testing.T) {
	cases := []struct {
		name string
		c    YouTubeBroadcastControls
		want string
	}{
		{"a malformed event id", YouTubeBroadcastControls{EventID: "not an id!"}, "event id"},
		{"an over-long description", YouTubeBroadcastControls{Description: strings.Repeat("d", YouTubeDescriptionMax+1)}, "description"},
		{"an over-long category", YouTubeBroadcastControls{Category: strings.Repeat("é", YouTubeCategoryMax+1)}, "category"},
		{"a malformed stream id", YouTubeBroadcastControls{StreamID: "a b"}, "stream id"},
	}
	for _, tc := range cases {
		probs := tc.c.Problems()
		if len(probs) != 1 || !strings.Contains(probs[0], tc.want) {
			t.Errorf("%s: problems = %v, want one naming %q", tc.name, probs, tc.want)
		}
	}
	ok := YouTubeBroadcastControls{EventID: "abc-DEF_1.2", Description: strings.Repeat("d", YouTubeDescriptionMax),
		Category: strings.Repeat("é", YouTubeCategoryMax), StreamID: "s-1"}
	if probs := ok.Problems(); len(probs) != 0 {
		t.Errorf("values at the limits were refused: %v", probs)
	}
	var nilControls *YouTubeBroadcastControls
	if nilControls.Problems() != nil || nilControls.OnlyForYouTube() {
		t.Error("a nil block has no problems and sets nothing")
	}
	for name, c := range map[string]YouTubeBroadcastControls{
		"event": {EventID: "e"}, "rebind": {RebindEvent: true}, "description": {Description: "d"},
		"category": {Category: "c"}, "360": {Projection360: true}, "stream": {StreamID: "s"}, "studio": {StudioStreamNow: true},
	} {
		if !c.OnlyForYouTube() {
			t.Errorf("%s: not reported as a YouTube-only setting, so a switch to another platform would keep it", name)
		}
	}
	if (&YouTubeBroadcastControls{LatencyPreference: "low"}).OnlyForYouTube() {
		t.Error("a latency preference is an ordinary control, not a YouTube-only create field")
	}
}
