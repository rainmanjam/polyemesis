package ffmpeg

import (
	"strings"
	"testing"
)

/* THE PASSPHRASE POLYEMESIS PRINTS MUST BE THE PASSPHRASE THE ENCODER SENDS.
 *
 * Reported from a real install: OBS pointed at the URL the dashboard rendered,
 * with a passphrase containing semicolons, and the connection was refused with
 * a passphrase the operator had typed correctly.
 *
 * PublicIngestURL builds its query with url.Values.Encode(), which
 * percent-encodes: a `;` becomes `%3B`. FFmpeg's libsrt does NOT percent-decode
 * option values -- it reads them with av_find_info_tag, which copies the raw
 * bytes -- so what leaves OBS is the literal text `sdsdsalsk%3Blak...`. The Go
 * listener in internal/srtserver compares that against the cleartext value in
 * the database, which still contains the semicolons. They can never match.
 *
 * So the URL is not wrong as a URL. It is wrong as INPUT TO THE THING IT IS FOR,
 * which is the same defect as #306 -- "the stored spelling of a stream key and
 * the spelling that reached the wire were allowed to differ" -- arriving in a
 * place that fix did not reach.
 *
 * WHY THIS IS ASSERTED WITHOUT DECODING. A test that parsed the URL with
 * net/url and compared the decoded value would PASS against the bug, because
 * Go's parser undoes exactly the encoding FFmpeg ignores. The assertion has to
 * read the query the way the consumer reads it: the raw bytes after
 * `passphrase=`, decoded not at all.
 *
 * Alphanumeric passphrases are unaffected, which is why this survived: nothing
 * in the fixtures had a character that percent-encodes.
 */

// rawQueryValue returns the bytes after `key=` up to the next `&`, with no
// decoding -- av_find_info_tag's behaviour, which is what actually consumes
// this URL.
func rawQueryValue(url, key string) (string, bool) {
	q := url
	if i := strings.IndexByte(q, '?'); i >= 0 {
		q = q[i+1:]
	}
	for _, pair := range strings.Split(q, "&") {
		name, value, ok := strings.Cut(pair, "=")
		if ok && name == key {
			return value, true
		}
	}
	return "", false
}

func TestTheRenderedPassphraseIsWhatAnEncoderWillActuallySend(t *testing.T) {
	// THE CHARACTERS THAT MUST SURVIVE, and the distinction from the ones below
	// is not squeamishness -- it is what the consumer can actually parse.
	// av_find_info_tag splits the query on `&` and takes everything after the
	// first `=`. So `;` `/` `?` `+` `=` are all ordinary bytes in a value and
	// must arrive unchanged. `&`, `#` and whitespace cannot survive ANY URL and
	// are refused at entry instead -- see PassphraseIsURLSafe.
	// The alphabet db.IngestSettings.problems() permits: RFC 3986 unreserved.
	// Nothing here is escaped by url.Values.Encode(), which is the property that
	// makes the URL correct -- so this is the assertion that the two halves of
	// the fix agree with each other.
	for _, pass := range []string{
		"sdsdsalsklakslskdaskd",    // the reported passphrase, semicolons removed
		"kQxZ7fRvB2mNpL0sTdWyGhJc", // a generated-looking one
		"dashes-and_underscores",
		"dots.and~tildes.here",
	} {
		t.Run(pass, func(t *testing.T) {
			s := IngestSpec{Kind: IngestSRT, SRTPort: 6000, SRTLatencyMS: 200, SRTPassphrase: pass}
			got := s.PublicIngestURL("stream.example.com")

			raw, ok := rawQueryValue(got, "passphrase")
			if !ok {
				t.Fatalf("no passphrase in the rendered URL: %s", got)
			}
			if raw != pass {
				t.Errorf("the URL carries a passphrase the encoder cannot send back.\n"+
					"  configured: %q\n  on the wire: %q\n  url: %s\n\n"+
					"FFmpeg's libsrt reads this option with av_find_info_tag, which does "+
					"NOT percent-decode, so the literal bytes above are what reaches "+
					"internal/srtserver — which compares them against the cleartext value "+
					"in the database. An operator with a correct passphrase is refused, "+
					"and the URL they were told to copy is the reason.",
					pass, raw, got)
			}
		})
	}
}

// The alphanumeric case must keep working, so a fix cannot be "stop encoding
// and hope": these are the passphrases every existing install has.
func TestAnOrdinaryPassphraseIsStillRenderedUnchanged(t *testing.T) {
	const pass = "kQxZ7fRvB2mNpL0sTdWyGhJc"
	s := IngestSpec{Kind: IngestSRT, SRTPort: 6000, SRTLatencyMS: 200, SRTPassphrase: pass}
	got := s.PublicIngestURL("stream.example.com")
	if raw, _ := rawQueryValue(got, "passphrase"); raw != pass {
		t.Errorf("an alphanumeric passphrase was altered: %q -> %q", pass, raw)
	}
}

// And the rest of the query must stay in the shape docs/OBS.md documents, so a
// fix to one parameter cannot quietly reshape the others.
func TestTheIngestURLKeepsTheParametersTheDocsPromise(t *testing.T) {
	s := IngestSpec{Kind: IngestSRT, SRTPort: 6000, SRTLatencyMS: 200}
	got := s.PublicIngestURL("stream.example.com")

	for key, want := range map[string]string{
		"mode":      "caller",
		"transtype": "live",
		// Microseconds, which docs/TESTING.md states explicitly: 200 ms.
		"latency": "200000",
	} {
		if raw, ok := rawQueryValue(got, key); !ok || raw != want {
			t.Errorf("%s = %q (present %v), want %q — docs/OBS.md prints this URL for "+
				"operators to copy", key, raw, ok, want)
		}
	}
	if _, ok := rawQueryValue(got, "passphrase"); ok {
		t.Error("a passphrase parameter appeared for a source that has none")
	}
}

// THE OTHER HALF OF THE FIX LIVES IN internal/db, AND THIS RECORDS WHY IT IS
// NOT HERE.
//
// The URL is only correct because the passphrase alphabet is bounded:
// db.IngestSettings.problems() refuses anything outside RFC 3986's unreserved
// set, so url.Values.Encode() has nothing to escape. The two are one fix in two
// packages, and internal/db cannot be imported from here.
//
// db/settings_test.go carries the refusal cases. What this asserts is the
// property that makes the pairing sound: every character db permits survives
// Encode() untouched. If somebody widens that alphabet without reading this,
// the round-trip test above starts failing -- which is the intended alarm.
func TestEveryPermittedPassphraseCharacterSurvivesEncoding(t *testing.T) {
	const permitted = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.~"
	s := IngestSpec{Kind: IngestSRT, SRTPort: 6000, SRTLatencyMS: 200, SRTPassphrase: permitted}
	got := s.PublicIngestURL("stream.example.com")

	raw, ok := rawQueryValue(got, "passphrase")
	if !ok {
		t.Fatalf("no passphrase in %s", got)
	}
	if raw != permitted {
		t.Errorf("a character db permits does not survive the URL.\n  in:  %q\n  out: %q\n\n"+
			"The consumer does not percent-decode, so anything escaped here reaches "+
			"the server as its %%XX text and never matches. Either narrow the alphabet "+
			"in db.IngestSettings.problems(), or stop using url.Values.Encode().",
			permitted, raw)
	}
}
