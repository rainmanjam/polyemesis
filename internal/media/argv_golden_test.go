package media

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/argvgold"
)

func TestMediaArgvIsPinned(t *testing.T) {
	for _, lvl := range []string{"", "debug"} {
		t.Setenv("POLYEMESIS_FFMPEG_LOGLEVEL", lvl)
		suffix := ""
		if lvl != "" {
			suffix = "_loglevel_" + lvl
		}
		cases := map[string][]string{
			"archive": ArchiveArgs(ArchiveSpec{Input: "/rec/in.mkv", Output: "/rec/out.mkv.partial", Codec: ArchiveCodec("hevc"), Encoder: "libx265", Quality: 28, Preset: "slow"}),
			"proxy":   ProxyArgs(ProxySpec{Input: "/rec/in.mkv", Output: "/rec/proxy.mp4.partial", Width: 640, Height: 360, Encoder: "libx264", Preset: "veryfast"}),
			"poster":  PosterArgs(PosterSpec{Input: "/rec/in.mkv", Output: "/rec/poster.jpg", DurationSeconds: 120, Width: 640, Height: 360}),
			"contact": ContactSheetArgs(ContactSheetSpec{Input: "/rec/in.mkv", Output: "/rec/sheet.jpg", DurationSeconds: 120, Cols: 4, Rows: 4, TileWidth: 160, TileHeight: 90}),
			"sprite":  SpriteArgs(SpriteSpec{Input: "/rec/in.mkv", OutputPattern: "/rec/sprite_%03d.jpg", DurationSeconds: 120, IntervalSeconds: 5, Cols: 5, Rows: 5, TileWidth: 160, TileHeight: 90}),
			"thumbnails": ThumbnailArgs(ThumbnailSpec{
				Input:           "/rec/in.mkv",
				DurationSeconds: 120,
				Poster:          PosterSpec{Output: "/rec/poster.jpg", Width: 640, Height: 360},
				ContactSheet:    ContactSheetSpec{Output: "/rec/sheet.jpg", Cols: 4, Rows: 4, TileWidth: 160, TileHeight: 90},
				Sprites:         SpriteSpec{OutputPattern: "/rec/sprite_%03d.jpg", Cols: 5, Rows: 5, TileWidth: 160, TileHeight: 90},
			}),
		}
		for name, argv := range cases {
			t.Run(name+suffix, func(t *testing.T) { argvgold.Check(t, name+suffix, argv) })
		}
	}
}
