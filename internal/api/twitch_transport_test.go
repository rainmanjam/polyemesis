package api

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// A key refresh must not move a Twitch destination to another transport than the
// one the operator chose: the twitch-rtmps preset tells them to fall back to
// plain RTMP if TLS is refused, and a refresh that wrote the OAuth URL over it
// would undo that.
func TestRefreshKeepsATwitchDestinationsTransport(t *testing.T) {
	const (
		rtmps = "rtmps://live.twitch.tv:443/app" // what the Twitch provider answers
		rtmp  = "rtmp://live.twitch.tv/app"
	)
	for _, c := range []struct {
		name     string
		platform db.Platform
		preset   string
		stored   string
		want     string
	}{
		{"plain preset stays RTMP", db.PlatformTwitch, "twitch", rtmp, rtmp},
		{"plain preset with a stale RTMPS URL is put back on RTMP", db.PlatformTwitch, "twitch", "rtmps://live.twitch.tv/app", rtmp},
		{"rtmps preset stays RTMPS", db.PlatformTwitch, "twitch-rtmps", rtmps, rtmps},
		{"rtmps preset on RTMP is put on RTMPS", db.PlatformTwitch, "twitch-rtmps", rtmp, rtmps},
		{"unlabelled RTMP keeps RTMP", db.PlatformTwitch, "", rtmp, rtmp},
		{"unlabelled RTMPS keeps RTMPS", db.PlatformTwitch, "", "rtmps://live.twitch.tv/app", rtmps},
		{"new OAuth destination with no URL defaults to RTMPS", db.PlatformTwitch, "", "", rtmps},
		{"another platform takes what its provider sent", db.PlatformKick, "", rtmp, rtmps},
	} {
		t.Run(c.name, func(t *testing.T) {
			cur := &db.Destination{Platform: c.platform, PresetID: c.preset, URL: c.stored}
			if got := ingestURLKeepingTransport(cur, rtmps); got != c.want {
				t.Fatalf("URL = %q, want %q", got, c.want)
			}
		})
	}
}
