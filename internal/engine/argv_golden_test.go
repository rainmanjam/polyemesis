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
		argvgold.LogLevel(t, lvl)
		suffix := ""
		if lvl != "" {
			suffix = "_loglevel_" + lvl
		}
		cases := map[string][]string{
			"selector_feed":           ffmpeg.RelayFeedArgs(in, out, 12.5),
			"selector_feed_conform_0": ffmpeg.ConformedFeedArgs(in, out, 12.5, nil),
			"selector_feed_conform_1": ffmpeg.ConformedFeedArgs(in, out, 12.5, two[:1]),
			"selector_feed_conform_2": ffmpeg.ConformedFeedArgs(in, out, 12.5, two),
			"selector_playlist_feed":  ffmpeg.PlaylistFeedArgs("/data/playlist.txt", out),
		}
		for name, argv := range cases {
			if name != "selector_playlist_feed" {
				argvgold.RequireRelayConsumer(t, name, argv)
			}
			argvgold.RequireRelayProducer(t, name, argv)
			t.Run(name+suffix, func(t *testing.T) { argvgold.Check(t, name+suffix, argv) })
		}
	}
}
