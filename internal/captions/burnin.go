package captions

import "github.com/rainmanjam/polyemesis/internal/ffmpeg"

// SubtitlesFilter returns the FFmpeg filter that burns a subtitle file into the
// picture. The shipped image's FFmpeg is built with libass, so the subtitles
// filter is present there (checked against alpine:3.24 with ffmpeg 8.1.3-r0).
//
// It is for a FINISHED file only. The subtitles filter reads its whole file
// once, when the filter starts: measured against the shipped image's FFmpeg
// 8.1.3, a cue appended after the encode began was never drawn. Live captions
// are therefore burned into a rendition with drawtext's textfile and reload=1
// instead (see burn.go and ffmpeg.TextSpec.TextFile). This builds the filter
// argument for a recorded caption file.
//
// The path is escaped by ffmpeg.GraphArgPath, the one filtergraph escaper in the
// tree: for the option parser and then for the graph parser, with forward
// slashes on Windows and boundary whitespace kept. This package used to carry a
// second copy that had neither of the last two.
func SubtitlesFilter(path string) string {
	return "subtitles=filename=" + ffmpeg.GraphArgPath(path)
}
