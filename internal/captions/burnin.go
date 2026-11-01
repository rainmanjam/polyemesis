package captions

import "strings"

// SubtitlesFilter returns the FFmpeg filter that burns a subtitle file into the
// picture. The shipped image's FFmpeg is built with libass, so the subtitles
// filter is present there (checked against alpine:3.24 with ffmpeg 8.1.3-r0).
//
// Burn-in needs a re-encode of the rendition it is applied to, so it cannot be
// done on a copied stream. Wiring it into a rendition is a separate step, and
// this only builds the filter argument, with the path escaped as filterOptionValue
// describes.
func SubtitlesFilter(path string) string {
	return "subtitles=filename=" + filterOptionValue(path)
}

// filterOptionValue escapes a value for use as a filter option inside a
// filtergraph. FFmpeg parses it twice, so it is escaped twice, innermost first:
//
//  1. for the filter's option parser, which splits key=value pairs on ':' and
//     treats '\' and '\” as escape and quote characters;
//  2. for the filtergraph parser, which runs first and splits the graph on '[',
//     ']', ',' and ';', consuming one level of '\' and '\” as it goes.
//
// This is the order the FFmpeg filters documentation gives under "Notes on
// filtergraph escaping". The result is meant for an argv element, so no shell
// level is added.
func filterOptionValue(v string) string {
	return escapeWith(escapeWith(v, `\':`), `\'[],;`)
}

// escapeWith puts a backslash in front of every rune of v that is in special.
func escapeWith(v, special string) string {
	var b strings.Builder
	for _, r := range v {
		if strings.ContainsRune(special, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
