package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/argvgold"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

func TestSelectorFeedArgvIsPinned(t *testing.T) {
	const in, out = "udp://127.0.0.1:20050", "udp://127.0.0.1:20051"
	two := []ffmpeg.AudioStream{
		{Index: 0, SampleRate: 48000, Channels: 2, Bitrate: 160000},
		{Index: 1, SampleRate: 44100, Channels: 1, Bitrate: 0},
	}
	for _, lvl := range []string{"", "debug"} {
		t.Setenv("POLYEMESIS_FFMPEG_LOGLEVEL", lvl)
		suffix := ""
		if lvl != "" {
			suffix = "_loglevel_" + lvl
		}
		cases := map[string][]string{
			"selector_feed":           relayFeedArgs(in, out, 12.5),
			"selector_feed_conform_0": conformedFeedArgs(in, out, 12.5, nil),
			"selector_feed_conform_1": conformedFeedArgs(in, out, 12.5, two[:1]),
			"selector_feed_conform_2": conformedFeedArgs(in, out, 12.5, two),
			"selector_playlist_feed":  playlistFeedArgs("/data/playlist.txt", out),
		}
		for name, argv := range cases {
			t.Run(name+suffix, func(t *testing.T) { argvgold.Check(t, name+suffix, argv) })
		}
	}
}
