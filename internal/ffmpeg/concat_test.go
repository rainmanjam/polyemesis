package ffmpeg

import "testing"

// The quoting rule is the demuxer's, not the shell's: single quotes, and a path
// containing one must close, escape and reopen, because there is no backslash
// escape inside quotes. A list that gets this wrong parses as something else
// entirely.
//
// The mutation: drop the ReplaceAll and this fails.
func TestAPathContainingAQuoteStaysOnePath(t *testing.T) {
	got := ConcatList([]ConcatEntry{{Path: "/media/Tom's stream/a.ts"}})
	want := "file '/media/Tom'\\''s stream/a.ts'\n"
	if got != want {
		t.Errorf("ConcatList = %q, want %q", got, want)
	}
}

// A duration directive follows its file line, and is omitted when unknown --
// concat infers one in that case, and an inaccurate directive is worse than
// none.
//
// The mutation: emit "duration 0" for a zero and this fails.
func TestADurationIsWrittenAfterItsFileAndOmittedWhenUnknown(t *testing.T) {
	got := ConcatList([]ConcatEntry{
		{Path: "/a.ts", DurationMS: 2500},
		{Path: "/b.ts"},
	})
	want := "file '/a.ts'\nduration 2.500\nfile '/b.ts'\n"
	if got != want {
		t.Errorf("ConcatList = %q, want %q", got, want)
	}
}
