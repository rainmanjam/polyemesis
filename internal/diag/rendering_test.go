package diag

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

/* THE RENDERING IS PART OF THE SCRUB, AND THREE WAYS IT WAS NOT.
 *
 * scrubValue renders an unrecognised value with json.Marshal and then scrubs the
 * rendering. That closes the disclosure found in #422 — but only when the
 * rendering still CONTAINS the literal the set is looking for. Three shapes
 * where it does not:
 *
 *   a []byte renders as base64, so the credential is present and unrecognisable
 *   json.Marshal escapes & < > by default, so a credential containing one is
 *     transformed out of exact-match range
 *   a control character renders as six bytes, so the size cap counts one and
 *     the file carries six
 *
 * The first two are disclosure. The third is the bound this package claims about
 * itself being false by a factor of six.
 */

func encodeBundle(t *testing.T, r *Recorder) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Build("v0", "linux/amd64", r, NewSwitch(slog.LevelInfo), time.Now()).Encode(&buf); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.String()
}

// A DECLARED CREDENTIAL INSIDE A []byte MUST NOT SURVIVE.
//
// json.Marshal emits base64 for a []byte, so the scrub runs over text that no
// longer contains the literal. The stranger receiving the bundle decodes it in
// one step.
func TestACredentialInsideAByteSliceIsScrubbed(t *testing.T) {
	r := NewRecorder(8, alerts.NewSecretSet(nil, sentinelKey))
	r.SetRecording(true)
	r.Observe(Record{Level: "INFO", Message: "response body",
		Attrs: map[string]any{"body": []byte("prefix " + sentinelKey + " suffix")}})

	out := encodeBundle(t, r)
	if strings.Contains(out, sentinelKey) {
		t.Error("the credential is in the bundle verbatim")
	}
	// DECODE, DO NOT PATTERN-MATCH. The first version of this check compared
	// against base64Of(sentinelKey) — the encoding of the key ALONE, which never
	// appears inside the encoding of a longer string because base64 packs three
	// bytes to four characters and the alignment differs. It passed while the
	// credential was fully disclosed. Every base64-looking run in the bundle is
	// decoded and searched instead.
	for _, dec := range decodeAllBase64(out) {
		if strings.Contains(dec, sentinelKey) {
			t.Errorf("the credential reached the bundle base64-encoded, which the "+
				"recipient decodes in one step: %.60q", dec)
		}
	}
}

// decodeAllBase64 returns the decoding of every base64-looking token in s.
func decodeAllBase64(s string) []string {
	var out []string
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' ||
			r >= '0' && r <= '9' || r == '+' || r == '/' || r == '=')
	}) {
		if len(tok) < 16 {
			continue
		}
		if b, err := base64.StdEncoding.DecodeString(tok); err == nil {
			out = append(out, string(b))
		}
	}
	return out
}

// A DECLARED CREDENTIAL CONTAINING & < OR > MUST NOT SURVIVE.
//
// encoding/json escapes those three to & < > by default, and
// SecretSet.Scrub is an exact-literal replace, so the literal is no longer
// present in the text being scrubbed. Camera and CDN passwords routinely carry
// an ampersand.
func TestACredentialContainingAnAmpersandIsScrubbed(t *testing.T) {
	const pw = "p&notArealPw.notreal"
	r := NewRecorder(8, alerts.NewSecretSet(nil, pw))
	r.SetRecording(true)
	r.Observe(Record{Level: "INFO", Message: "pull source refused",
		Attrs: map[string]any{"detail": map[string]string{"passphrase": pw}}})

	out := encodeBundle(t, r)
	if strings.Contains(out, pw) {
		t.Error("the credential is in the bundle verbatim")
	}
	// ASSERT ON THE DECODED VALUE, NOT THE ESCAPED SPELLING. The first version
	// searched the raw file for the single-backslash escape and passed — the
	// attribute is itself a JSON document nested inside the bundle's JSON, so on
	// disk that escape is DOUBLED. The credential was fully present and the test
	// said nothing. Decoding is the only spelling-proof check: whatever escaping
	// was applied, the recipient's parser undoes it.
	var b Bundle
	if err := json.Unmarshal([]byte(out), &b); err != nil {
		t.Fatalf("bundle does not decode: %v", err)
	}
	for _, rec := range b.Records {
		for k, v := range rec.Attrs {
			str, _ := v.(string)
			if strings.Contains(str, pw) {
				t.Errorf("attribute %q carries the credential: %q", k, str)
				continue
			}
			var inner map[string]string
			if json.Unmarshal([]byte(str), &inner) == nil {
				for _, iv := range inner {
					if strings.Contains(iv, pw) {
						t.Errorf("attribute %q decodes to %q — the credential survived "+
							"because the value was escaped BEFORE the exact-match scrub "+
							"ran, so the set never saw its literal", k, iv)
					}
				}
			}
		}
	}
}

// THE CAP MUST BOUND THE FILE, NOT THE ARITHMETIC.
//
// A control character is one byte in the record and six in the JSON. Counting
// the former while claiming a ceiling on the latter makes the ceiling false by
// a factor of six — measured at 785,437 bytes against an asserted 393,216.
func TestControlCharactersCannotInflateTheEncodedBundle(t *testing.T) {
	const capacity = 16
	r := NewRecorder(capacity, alerts.NewSecretSet(nil))
	r.SetRecording(true)
	for range capacity * 2 {
		r.Observe(Record{Level: "INFO", Message: strings.Repeat("\x00", MaxRecordBytes*2)})
	}

	out := encodeBundle(t, r)
	ceiling := capacity * MaxRecordBytes * 3
	if len(out) > ceiling {
		t.Errorf("the encoded bundle is %d bytes against a ceiling of %d. The cap "+
			"counts raw bytes and JSON writes six per control character, so the "+
			"stated bound is not a bound.", len(out), ceiling)
	}
	// And it is still valid JSON.
	var b Bundle
	if err := json.Unmarshal([]byte(out), &b); err != nil {
		t.Fatalf("bundle does not decode: %v", err)
	}
}
