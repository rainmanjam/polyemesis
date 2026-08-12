package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

/* #266's refutation, and it was right.

The gate this PR put in front of the source routes keyed on
uploads.UploadFromPullURL, which split the URL on its first "/" and refused any
name with another one in it. ffmpeg.pullSource does not parse that way: it
normalises through filepath.Join, which collapses ".", "" and duplicate
separators. MEASURED ON THIS BRANCH BEFORE THE FIX, PullDataDir=/data, upload
recorded uploads.UnverifiedVerdict(ReasonInterrupted):

	"file://uploads/unchecked-abcd1234.ts"     -i "file:/data/uploads/unchecked-abcd1234.ts"  PUT 400 refused
	"file://uploads/./unchecked-abcd1234.ts"   -i "file:/data/uploads/unchecked-abcd1234.ts"  PUT 200 STORED
	"file://uploads//unchecked-abcd1234.ts"    -i "file:/data/uploads/unchecked-abcd1234.ts"  PUT 200 STORED
	"file://./uploads/unchecked-abcd1234.ts"   -i "file:/data/uploads/unchecked-abcd1234.ts"  PUT 200 STORED
	"file://uploads/././unchecked-abcd1234.ts" -i "file:/data/uploads/unchecked-abcd1234.ts"  PUT 200 STORED

A byte-identical -i argument with four different verdicts. "Both routes now
refuse" was false: they refused one SPELLING of one path. And because all three
consumers -- pullSourceUploadProblems (#201), sourceIngestUploadProblem (#255),
and pullUploadUnchecked, the fail-open report this PR argued for on the grounds
that a monitoring script reads it -- called the same blind parser, none of them
saw the other four. The /sources field was empty for every one of them.

SO THE FIX IS IN THE PARSER AND NOT AT THE CALL SITES. Patching three handlers
would have reproduced the defect, since the fourth caller has not been written
yet. UploadFromPullURL now asks ffmpeg.PullFilePath -- the same normalisation
pullSource performs to build the -i -- so any spelling producing a given -i gets
one verdict by construction.

THE TABLE BELOW ASSERTS THE -i AS WELL AS THE VERDICT. A table that only checked
verdicts would keep passing if normalisation silently changed what gets opened,
which is the failure mode that produced this bug in the first place. */

// pullSpelling is one way of writing a path to a file in the uploads directory.
//
// form and arg carry %s for the upload's filename, because every row is driven
// twice: once at an upload recorded unchecked (must be refused) and once at an
// upload this server inspected and accepted (must NOT be refused). Without the
// second pass a handler that rejected every unusual spelling would satisfy every
// refusal assertion here and be called a fix.
type pullSpelling struct {
	what string // what this spelling does differently
	form string // the pull URL, with %s for the filename
	arg  string // the -i FFmpeg is handed, with %s; "" when the engine refuses to build one
	// names is whether this spelling names the STORED UPLOAD -- the one thing
	// uploads.Verdict can be asked about. Everything else in the row derives
	// from it: a save is refused iff it names an unchecked upload, and the
	// /sources report is populated iff it names one that was downgraded later.
	names bool
}

// The four spellings the verifier measured, plus the four shapes that decide
// whether the new normalisation is right rather than merely wider.
var pullSpellings = []pullSpelling{{
	what:  "the canonical spelling uploads.PullURL writes",
	form:  "file://uploads/%s",
	arg:   "file:/data/uploads/%s",
	names: true,
}, {
	what:  `a "." segment, which filepath.Join collapses`,
	form:  "file://uploads/./%s",
	arg:   "file:/data/uploads/%s",
	names: true,
}, {
	what:  "a doubled separator, which filepath.Join collapses",
	form:  "file://uploads//%s",
	arg:   "file:/data/uploads/%s",
	names: true,
}, {
	what:  `a leading "./", which filepath.Join collapses`,
	form:  "file://./uploads/%s",
	arg:   "file:/data/uploads/%s",
	names: true,
}, {
	what:  "two of them, because one collapse does not imply the loop",
	form:  "file://uploads/././%s",
	arg:   "file:/data/uploads/%s",
	names: true,
}, {
	// Already handled before the fix; kept because the fix REPLACED the code
	// that handled it, and a normalisation that dropped this tolerance would
	// open the hole on Windows that #201's backslash note closed.
	what:  "a Windows separator, which pullSource translates before anything else",
	form:  `file://uploads\%s`,
	arg:   "file:/data/uploads/%s",
	names: true,
}, {
	// The one place the gate is deliberately WIDER than the filesystem: on the
	// two case-insensitive platforms this builds for, this opens the upload.
	// Note the -i is NOT the canonical one -- on Linux it names a directory
	// that does not exist -- so this row also proves the assertion is on the
	// real argument and not on a normalised-away idea of it.
	what:  "a folded directory segment and surrounding whitespace",
	form:  "  FILE://Uploads/%s  ",
	arg:   "file:/data/Uploads/%s",
	names: true,
}, {
	// NOT the upload: a nested path is a different file, and the uploads store
	// holds bare names only. Refusing it would be the gate claiming something
	// it cannot ask uploads.Verdict about.
	what:  "a nested path, which is a different file and not a stored upload",
	form:  "file://uploads/sub/%s",
	arg:   "file:/data/uploads/sub/%s",
	names: false,
}, {
	// FFmpeg's file protocol does not percent-decode, so this is a filename
	// with a percent sign in it -- one that no upload can have, since stored
	// names are generated. A gate that decoded here would refuse a save over a
	// file the engine never opens, and miss the file it does.
	what:  "a percent-encoded separator, which the file protocol does not decode",
	form:  "file://uploads%%2F%s",
	arg:   "file:/data/uploads%%2F%s",
	names: false,
}, {
	// Refused by ffmpeg.ValidatePullURL before the upload gate is consulted:
	// pullSource rejects ".." outright rather than resolving it, so a traversal
	// that would have landed back inside uploads/ never becomes an -i at all.
	// arg:"" is that refusal, and the save routes answer 400 for a different
	// reason from every other refusal in this table.
	what:  `a ".." that would resolve back inside the uploads directory`,
	form:  "file://uploads/sub/../%s",
	arg:   "",
	names: false,
}}

// TestEverySpellingOfOneInputArgumentGetsOneVerdict drives each spelling through
// the engine's own -i construction and then through all three consumers of the
// upload gate.
func TestEverySpellingOfOneInputArgumentGetsOneVerdict(t *testing.T) {
	const (
		unchecked = "unchecked-abcd1234.ts"
		checked   = "checked-abcd1234.ts"
	)

	for _, sp := range pullSpellings {
		t.Run(sp.what, func(t *testing.T) {
			raw := fmt.Sprintf(sp.form, unchecked)

			// (1) WHAT THE ENGINE ACTUALLY OPENS. Asserted first and separately,
			// because every verdict below is only meaningful as a statement
			// about this string. PullDataDir is a literal rather than the
			// fixture's temp directory: normalisation is what is under test and
			// it does not depend on where the data lives.
			spec := ffmpeg.IngestSpec{Kind: ffmpeg.IngestPull, PullURL: raw, PullDataDir: "/data"}
			gotArg, argErr := spec.PullSource()
			switch wantArg := fmt.Sprintf(sp.arg, unchecked); {
			case sp.arg == "" && argErr == nil:
				t.Fatalf("the engine built -i %q for %q; this spelling is supposed to be "+
					"refused before it becomes an input argument", gotArg, raw)
			case sp.arg == "":
				// Refused, as intended. Nothing downstream to key on.
			case argErr != nil:
				t.Fatalf("the engine refused to build an -i for %q: %v; want %q",
					raw, argErr, wantArg)
			case gotArg != wantArg:
				t.Fatalf("the engine builds -i %q for %q, want %q -- the verdicts below "+
					"are assertions ABOUT this string, so they prove nothing until it is "+
					"the string this test thinks it is", gotArg, raw, wantArg)
			}

			// (2) THE PARSER THE GATES KEY ON. It has to agree with (1) about
			// whether this argument opens a stored upload.
			gotName, gotOK := uploads.UploadFromPullURL(raw)
			if gotOK != sp.names || (sp.names && gotName != unchecked) {
				t.Fatalf("uploads.UploadFromPullURL(%q) = %q, %v; want %q, %v -- the -i is %q, "+
					"and a gate that disagrees with the engine about which file that names "+
					"is a gate on a spelling",
					raw, gotName, gotOK, map[bool]string{true: unchecked}[sp.names], sp.names, gotArg)
			}

			// A save is refused when it names an unchecked upload, and also
			// when the engine would not dial it at all.
			refused := sp.names || sp.arg == ""

			t.Run("PUT /sources", func(t *testing.T) {
				h, store, sign := sourceServer(t)
				seedSpellingUploads(t, h)

				// THE CONTROL FIRST. The same spelling at an upload this server
				// inspected must still be accepted, or "refused" below is
				// satisfied by a handler that dislikes punctuation.
				wantChecked := http.StatusOK
				if sp.arg == "" {
					wantChecked = http.StatusBadRequest
				}
				putSourceIngest(t, h, sign, 1,
					pullIngest(t, store, fmt.Sprintf(sp.form, checked)), wantChecked)

				want := http.StatusOK
				if refused {
					want = http.StatusBadRequest
				}
				body := putSourceIngest(t, h, sign, 1, pullIngest(t, store, raw), want)

				if sp.names {
					for _, s := range []string{unchecked, uploads.ReasonInterrupted} {
						if !strings.Contains(string(body), s) {
							t.Errorf("the refusal does not mention %q: %s", s, body)
						}
					}
				}
				// AND IT DID NOT REACH THE ROW. The engine reads its ingest
				// from here (effectiveSettings), so a 400 that stored the URL
				// anyway would leave the hole exactly where it was.
				got, err := store.GetSource(1)
				if err != nil {
					t.Fatal(err)
				}
				if stored := strings.Contains(got.Ingest.Pull.URL, unchecked); stored == refused {
					t.Errorf("row pull.url = %q after a save that was %s", got.Ingest.Pull.URL,
						map[bool]string{true: "refused", false: "accepted"}[refused])
				}
			})

			t.Run("POST /sources", func(t *testing.T) {
				h, store, sign := sourceServer(t)
				seedSpellingUploads(t, h)

				create := func(name, url string, want int) []byte {
					t.Helper()
					r := jsonRequest(t, http.MethodPost, "/api/v1/sources",
						map[string]any{"name": name, "ingest": pullIngest(t, store, url)})
					sign(r)
					w := do(t, h, r)
					if w.Code != want {
						t.Fatalf("POST /api/v1/sources pull=%q: status %d, want %d, body %s",
							url, w.Code, want, w.Body.String())
					}
					return w.Body.Bytes()
				}

				wantChecked := http.StatusCreated
				if sp.arg == "" {
					wantChecked = http.StatusBadRequest
				}
				create("spelling control", fmt.Sprintf(sp.form, checked), wantChecked)

				want := http.StatusCreated
				if refused {
					want = http.StatusBadRequest
				}
				create("spelling subject", raw, want)
			})

			// The #201 gate, which had this same bypass before the parser was
			// fixed and which this PR added two more callers to. It is included
			// because the fix is supposed to close all three at once.
			t.Run("PUT /settings", func(t *testing.T) {
				h, _, sign := sourceServer(t)
				seedSpellingUploads(t, h)

				wantChecked := http.StatusOK
				if sp.arg == "" {
					wantChecked = http.StatusBadRequest
				}
				savePullSource(t, h, sign, "ingest", fmt.Sprintf(sp.form, checked), wantChecked)

				want := http.StatusOK
				if refused {
					want = http.StatusBadRequest
				}
				savePullSource(t, h, sign, "ingest", raw, want)
			})
		})
	}
}

// TestEverySpellingIsReportedOnTheSourceListing is the fail-open half, and the
// verifier's second consequence: pullUploadUnchecked read the same blind parser,
// so for four of the five spellings that reach the engine there was no badge, no
// /sources field, and nothing at all for the monitoring script this PR argued
// justified computing the warning server-side.
//
// The inherited shape is the only one that can be observed here, since a save
// naming an already-unchecked upload is now refused: store the URL while the
// upload is verified, downgrade the verdict underneath, then read the listing.
func TestEverySpellingIsReportedOnTheSourceListing(t *testing.T) {
	const name = "inherited-abcd1234.ts"

	for _, sp := range pullSpellings {
		if sp.arg == "" {
			continue // never becomes a stored pull source at all
		}
		t.Run(sp.what, func(t *testing.T) {
			h, store, sign := sourceServer(t)
			srv := serverUnderTest(t, h)
			seedUpload(t, srv, name)
			seedVerdict(t, srv, name, uploads.VerifiedVerdict(uploads.MediaInfo{AudioTracks: 2}))

			raw := fmt.Sprintf(sp.form, name)
			putSourceIngest(t, h, sign, 1, pullIngest(t, store, raw), http.StatusOK)
			seedVerdict(t, srv, name, uploads.UnverifiedVerdict(uploads.ReasonProbeUnusable))

			var rows []struct {
				ID                  int64  `json:"id"`
				PullUploadUnchecked string `json:"pullUploadUnchecked"`
			}
			decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/sources", nil, http.StatusOK), &rows)
			if len(rows) == 0 {
				t.Fatal("the sources listing came back empty")
			}
			got := rows[0].PullUploadUnchecked

			if !sp.names {
				// THE CONTROL. A spelling that opens a different file must stay
				// silent, or the field is decoration rather than a report.
				if got != "" {
					t.Errorf("a pull source spelled %q reports %q, but its -i is %q -- "+
						"that is not this upload", raw, got, fmt.Sprintf(sp.arg, name))
				}
				return
			}
			if got == "" {
				t.Fatalf("a source pulling from %q -- -i %q, the same file the canonical "+
					"spelling names -- reports nothing at all, so the monitoring script "+
					"this field exists for sees a clean row over an uninspected file",
					raw, fmt.Sprintf(sp.arg, name))
			}
			for _, want := range []string{name, uploads.ReasonProbeUnusable} {
				if !strings.Contains(got, want) {
					t.Errorf("the report does not mention %q: %s", want, got)
				}
			}
		})
	}
}

// seedSpellingUploads puts both fixtures in the store: one recorded as never
// inspected, one inspected and accepted.
func seedSpellingUploads(t *testing.T, h http.Handler) {
	t.Helper()
	srv := serverUnderTest(t, h)
	seedUpload(t, srv, "unchecked-abcd1234.ts")
	seedUpload(t, srv, "checked-abcd1234.ts")
	seedVerdict(t, srv, "unchecked-abcd1234.ts",
		uploads.UnverifiedVerdict(uploads.ReasonInterrupted))
	seedVerdict(t, srv, "checked-abcd1234.ts",
		uploads.VerifiedVerdict(uploads.MediaInfo{AudioTracks: 2}))
}
