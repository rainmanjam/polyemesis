package ffmpeg

import (
	"fmt"
	"strings"
)

// ConcatEntry is one line of a concat demuxer list.
type ConcatEntry struct {
	Path string
	// DurationMS overrides what the demuxer would infer. Zero omits the
	// directive, which is correct when nothing has measured the file:
	// FFmpeg's own estimate beats a number somebody guessed.
	//
	// It exists for when a profile drifts enough that the demuxer's own
	// estimate stops being enough — not because the current one needs it.
	// Measured on three real derivatives concatenated, looped past two full
	// wraps, and probed with and without a per-entry duration: the packet
	// stream was byte-identical either way. See
	// internal/playlistmedia/concat_behaviour_test.go.
	DurationMS int64
}

// ConcatList renders the concat demuxer's list file.
//
// ONE implementation, in internal/ffmpeg, because internal/clipper and
// internal/playlistmedia both need it and neither can import the other. A
// private copy in each is how the four TrimSpace sites collapsed into
// db.PlaylistUploadName got there in the first place.
//
// Single quotes are the demuxer's own quoting, and a path containing one has to
// close, escape and reopen: there is no backslash escape inside quotes.
func ConcatList(entries []ConcatEntry) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString("file '")
		b.WriteString(strings.ReplaceAll(e.Path, "'", `'\''`))
		b.WriteString("'\n")
		if e.DurationMS > 0 {
			fmt.Fprintf(&b, "duration %.3f\n", float64(e.DurationMS)/1000)
		}
	}
	return b.String()
}
