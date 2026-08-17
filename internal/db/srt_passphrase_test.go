package db

import (
	"strings"
	"testing"
)

/* THE PASSPHRASE ALPHABET, AND THE HALF OF THE FIX THAT LIVES HERE.
 *
 * Reported from a live install: a passphrase containing semicolons refused
 * every OBS connection, with a value the operator had typed correctly.
 *
 * ffmpeg.PublicIngestURL renders the passphrase into the URL an encoder copies,
 * and THE ENCODER DOES NOT DECODE IT -- FFmpeg's libsrt reads the option with
 * av_find_info_tag, which copies raw bytes. So a `;` escaped to `%3B` is SENT
 * as `%3B` and compared, literally, against the value stored here. It cannot
 * match, and nothing tells the operator why.
 *
 * The fix could have gone either way: emit the URL unescaped, or bound what may
 * be stored. Bounding it is the one that holds, because the characters that
 * genuinely cannot survive a query string -- `&` splits parameters, `#` starts
 * a fragment, whitespace ends the URL in the box being pasted into -- cannot be
 * rescued by any encoding at all. Escaping them yields a URL that PARSES and
 * refuses every connection, which is the original defect wearing a hat.
 *
 * So the refusal goes where the operator is standing.
 */

func srtProblems(t *testing.T, pass string) []string {
	t.Helper()
	i := IngestSettings{
		Mode: IngestSRT,
		SRT:  SRTSettings{Passphrase: pass, LatencyMS: 200},
	}
	return i.problems()
}

func TestAPassphraseTheEncoderCouldNotSendBackIsRefused(t *testing.T) {
	for _, tc := range []struct{ pass, why string }{
		{"sdsdsalsk;lak;lskd;askd", "the reported one: ; escapes to %3B"},
		{"amp&ersand&here", "& separates query parameters"},
		{"hash#fragment#here", "# begins a fragment"},
		{"pass with spaces ok", "whitespace terminates a URL"},
		{"plus+signs+here", "+ escapes, and means space to some parsers"},
		{"slash/and?question", "both escape"},
		{"equals=sign=inside", "= escapes"},
		{"percent%signs%here", "% escapes, and starts an escape sequence"},
	} {
		t.Run(tc.pass, func(t *testing.T) {
			probs := strings.Join(srtProblems(t, tc.pass), " | ")
			if !strings.Contains(probs, "passphrase cannot contain") {
				t.Errorf("accepted %q (%s).\n\nIt will be rendered into the ingest URL "+
					"and sent back as escape text, so the operator is refused with a "+
					"passphrase they typed correctly — and the URL polyemesis told them "+
					"to copy is the reason.\nproblems: %s", tc.pass, tc.why, probs)
			}
		})
	}
}

// AND THE ALPHABET SRT ACTUALLY USES MUST STILL WORK. Refusing more than
// necessary would be inventing a restriction and blaming SRT for it.
func TestAnUnreservedPassphraseIsAccepted(t *testing.T) {
	for _, pass := range []string{
		"sdsdsalsklakslskdaskd",
		"kQxZ7fRvB2mNpL0sTdWyGhJc",
		"dashes-and_underscores",
		"dots.and~tildes.here",
		"abcdefghij", // exactly SRT's floor of 10
	} {
		t.Run(pass, func(t *testing.T) {
			for _, p := range srtProblems(t, pass) {
				if strings.Contains(p, "passphrase") {
					t.Errorf("refused %q, which survives a URL untouched: %s", pass, p)
				}
			}
		})
	}
}

// The length rule keeps working, and reports separately: an operator who typed
// something short AND punctuated should be told both, not one at a time.
func TestTheLengthRuleAndTheAlphabetRuleAreBothReported(t *testing.T) {
	probs := strings.Join(srtProblems(t, "a;b"), " | ")
	if !strings.Contains(probs, "10-79") {
		t.Errorf("no length complaint for a 3-character passphrase: %s", probs)
	}
	if !strings.Contains(probs, "cannot contain") {
		t.Errorf("no alphabet complaint for a passphrase containing ';': %s", probs)
	}
}
