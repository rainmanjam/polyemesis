package captions

import "strings"

// SubtitlesFilter returns the FFmpeg filter that burns a subtitle file into the
// picture. The shipped image's FFmpeg is built with libass, so the subtitles
// filter is present there (checked against alpine:3.24 with ffmpeg 8.1.3-r0).
//
// Burn-in needs a re-encode of the rendition it is applied to, so it cannot be
// done on a copied stream. Wiring it into a rendition is a separate step, and
// this only builds the filter argument, with the path escaped for the filtergraph
// syntax.
func SubtitlesFilter(path string) string {
	var b strings.Builder
	b.WriteString("subtitles=filename=")
	for _, r := range path {
		switch r {
		case '\\', ':', '\'', ',', ';', '[', ']', ' ':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
