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
