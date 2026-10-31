package captions

import "testing"

// TestBurnInFilterNamesTheSubtitleFileAndEscapesItsPath. The filtergraph reads
// colons, commas, brackets and quotes as syntax, so a path containing one of
// them must be escaped or the filter graph fails to parse, and the rendition
// fails with it.
func TestBurnInFilterNamesTheSubtitleFileAndEscapesItsPath(t *testing.T) {
	cases := map[string]string{
		"/data/captions.srt":    "subtitles=filename=/data/captions.srt",
		"/data/a:b.srt":         `subtitles=filename=/data/a\:b.srt`,
		"/data/it's, [odd].srt": `subtitles=filename=/data/it\'s\,\ \[odd\].srt`,
		`/data/back\slash.srt`:  `subtitles=filename=/data/back\\slash.srt`,
	}
	for in, want := range cases {
		if got := SubtitlesFilter(in); got != want {
			t.Errorf("SubtitlesFilter(%q) = %q, want %q", in, got, want)
		}
	}
}
