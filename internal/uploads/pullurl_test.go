package uploads

import "testing"

// UploadFromPullURL is what lets a consumer of a PULL SOURCE ask uploads.Verdict
// a question, and #201 is the consumer that was missing one. Every spelling
// ffmpeg.pullSource accepts has to come back out as the same name, because a
// spelling that validation accepts and this rejects is a spelling that reaches
// air ungated.
func TestUploadFromPullURLReadsBackWhatPullURLWrites(t *testing.T) {
	const stored = "show-c37688205ca09aa2.ts"
	name, ok := UploadFromPullURL(PullURL(stored))
	if !ok || name != stored {
		t.Fatalf("UploadFromPullURL(PullURL(%q)) = %q, %v; the two spellings of "+
			"one format have drifted", stored, name, ok)
	}
}

func TestUploadFromPullURLAcceptsEverySpellingValidationDoes(t *testing.T) {
	const stored = "show-c37688205ca09aa2.ts"
	for _, raw := range []string{
		"file://uploads/" + stored,
		"  file://uploads/" + stored + "  ", // pullSource trims
		"FILE://uploads/" + stored,          // pullSource lowercases the scheme
		`file://uploads\` + stored,          // pullSource normalises backslashes
		"file://Uploads/" + stored,          // two of three platforms are case-insensitive

		// #266. These four were the refutation: pullSource normalises through
		// filepath.Join, so all of them build the SAME -i as the canonical
		// spelling above, and the hand-written parse that used to live here
		// returned ("", false) for every one -- three gates blind at once. The
		// end-to-end measurement is in internal/api/pull_url_spelling_test.go.
		"file://uploads/./" + stored,
		"file://uploads//" + stored,
		"file://./uploads/" + stored,
		"file://uploads/././" + stored,
	} {
		name, ok := UploadFromPullURL(raw)
		if !ok || name != stored {
			t.Errorf("UploadFromPullURL(%q) = %q, %v; want %q -- a pull source "+
				"spelled this way names an upload and would go unchecked", raw, name, ok, stored)
		}
	}
}

func TestUploadFromPullURLClaimsNothingItIsNotSureOf(t *testing.T) {
	for _, raw := range []string{
		"",
		"rtsp://cam.local/stream1",
		"srt://peer.example:9000",
		"https://origin.example/live.ts",
		"file://recordings/show.ts", // inside the data directory, not an upload
		"file://uploads/",
		"file://uploads/sub/show.ts",
		"uploads/show.ts",

		// #266's other half. The collapsing spellings are claimed now, but
		// widening the parse must not have made it claim things the engine
		// opens elsewhere: a percent-encoded separator is a literal filename to
		// FFmpeg's file protocol and never a path, and ".." is refused by
		// pullSource outright rather than resolved, so neither is ever the -i
		// that opens this upload.
		"file://uploads%2Fshow.ts",
		"file://uploads/sub/../show.ts",
		"file://../uploads/show.ts",
	} {
		if name, ok := UploadFromPullURL(raw); ok {
			t.Errorf("UploadFromPullURL(%q) claimed the upload %q; the verdict gate "+
				"would refuse a source that names no upload at all", raw, name)
		}
	}
}
