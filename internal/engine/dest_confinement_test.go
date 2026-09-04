package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// destWritesAFile decides whether a destination's output path is confined to
// the data directory. #712.
//
// The four shipped kinds are the easy half. What this exists for is the FIFTH:
// db.DestKind and ffmpeg.DestKind are declared independently and joined by a
// conversion that compiles for any string, so a kind added to db reaches the
// default arm here with nothing in between. Returning false there was an
// arbitrary-file-write primitive handed to whoever adds it.
func TestAnUnknownDestinationKindIsConfinedLikeAFileWriter(t *testing.T) {
	for _, c := range []struct {
		kind    db.DestKind
		url     string
		confine bool
		why     string
	}{
		{db.DestFile, "out.mp4", true, "a file destination writes a file"},
		{db.DestAudio, "out.mp3", true, "a local audio target writes a file"},
		{db.DestAudio, "icecast://host/mount", false, "a streamed audio target writes no file"},
		{db.DestRTMP, "rtmp://host/app/key", false, "rtmp writes no file"},
		{db.DestSRT, "srt://host:9000", false, "srt writes no file"},
		{
			// THE DEVICE. Not a kind that exists -- a kind that does not, which
			// is the only state the default arm is reachable from and the exact
			// shape the next db.DestKind will arrive in.
			db.DestKind("hls-or-whatever-comes-next"), "somewhere/out.m3u8", true,
			"a kind this build does not know is confined rather than trusted",
		},
	} {
		got := destWritesAFile(&db.Destination{Kind: c.kind, URL: c.url})
		if got != c.confine {
			t.Errorf("destWritesAFile(%q, %q) = %v, want %v -- %s",
				c.kind, c.url, got, c.confine, c.why)
		}
	}
}
