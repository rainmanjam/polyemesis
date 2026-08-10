package alerts

import (
	"strings"
	"testing"
)

// TestRedactKnownResiduals pins the shapes Redact DOES NOT MASK.
//
// It is the unusual kind of test: every row asserts a LEAK as the expected
// output. That is deliberate and it is the point. Redact's four limits are
// documented on the function, and a documented limit nobody executes is a
// comment that rots -- either because somebody "fixes" a row with one more
// regex, or because somebody reads the doc, disbelieves it, and builds a sink
// on top of a Redact call.
//
// IF YOU "FIX" A ROW HERE WITH A REGEX YOU ARE BACK ON THE TREADMILL #150 GOT
// OFF; THE FIX IS A SecretSet. The set of FFmpeg flag spellings is not
// enumerable, so each added pattern closes one measured shape and leaves the
// unmeasured ones exactly as open as they were, while making the next reader
// believe the function is a boundary. The structural answer already exists:
// declare the literal in supervisor.Spec.Secrets (or alerts.NewSecretSet at the
// sink) and the spelling stops mattering.
func TestRedactKnownResiduals(t *testing.T) {
	const k = "live_284729384_pQ8fZmT3xR9wLkYvB2nHsA"

	tests := []struct {
		name string
		in   string
		want string
		why  string
	}{
		{
			name: "an FFmpeg flag value is invisible: -rtmp_conn S:KEY",
			in:   "-rtmp_conn S:" + k,
			want: "-rtmp_conn S:" + k,
			why: "the grammar is `label SEP value` with SEP in {:,=}; `S` is not a " +
				"label in the table and `-rtmp_conn` is separated by a SPACE",
		},
		{
			name: "-passphrase KEY leaks although `passphrase` IS in the table",
			in:   "-passphrase " + k,
			want: "-passphrase " + k,
			why: "THE PROOF THAT THE FAILURE IS GRAMMATICAL AND NOT LEXICAL. " +
				"Enlarging secretParam cannot fix this row: the label is already " +
				"in it and the separator is a space, which the grammar does not " +
				"accept and must not, or every `-preset ultrafast` in the log " +
				"would come back masked",
		},
		{
			name: "-streamid KEY leaks for the same reason",
			in:   "-streamid " + k,
			want: "-streamid " + k,
			why:  "`streamid` is in the table too; same grammar, same result",
		},
		{
			name: "a bare credential with no label at all",
			in:   k,
			want: k,
			why:  "there is nothing here to recognise; only an exact-literal pass can",
		},
		{
			name: "JSON: the quotes sit between the separator and the value",
			in:   `{"token":"` + k + `"}`,
			want: `{"token":"` + k + `"}`,
			why: "this is the shape a third party's error body arrives in, which is " +
				"why hooks.DeliveryRecord.Response needs a SecretSet and not this",
		},
		{
			name: "an https PATH segment is not a stream key and is not masked",
			in:   "https://host/a/b/" + k,
			want: "https://host/a/b/" + k,
			why: "only the keyCarrying schemes have their last segment blanked. A " +
				"Slack/Discord webhook secret lives exactly here -- use " +
				"RedactWebhookURL, and see alerts.ClientErrorText for the error " +
				"path. Running Redact over this string is a NO-OP and must never " +
				"be recorded as a fix",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Redact(tc.in); got != tc.want {
				t.Errorf("Redact(%q) = %q, want %q\n\nThis row asserts a KNOWN RESIDUAL: %s\n\n"+
					"If you just made this pass by adding a pattern, revert it. See the "+
					"comment on this test.", tc.in, got, tc.want, tc.why)
			}
		})
	}
}

// TestRedactPerElementIsStrictlyWorse pins the direction of the fourth limit.
//
// Redact's matches are SUBSTRINGS of its input, so splitting text on whitespace
// before calling it can only sever a label from its value -- it can never
// create a match that the joined text did not have. The delta is therefore
// one-directional, and this test asserts both halves: the three shapes where
// per-element is measurably worse, and the claim that nothing is better.
//
// The bug this pins is the one #150 removed structurally. Every argv egress now
// runs supervisor.Spec.Secrets over the elements FIRST, which is exact and does
// not care about the split; Redact runs afterwards over the JOINED string.
//
// If you "fix" a row here with a regex you are back on the treadmill #150 got
// off; the fix is a SecretSet.
func TestRedactPerElementIsStrictlyWorse(t *testing.T) {
	const k = "live_284729384_pQ8fZmT3xR9wLkYvB2nHsA"

	perElement := func(s string) string {
		fields := strings.Fields(s)
		out := make([]string, len(fields))
		for i, f := range fields {
			out[i] = Redact(f)
		}
		return strings.Join(out, " ")
	}

	// The three measured deltas. All are `label: value` where the split lands
	// on the space, which is the CANONICAL, CORRECTLY-SPELLED HTTP header form
	// -- the one an operator is most likely to type into expert mode.
	worse := []struct {
		in         string
		wantWhole  string
		wantPerEl  string
		wantLeaked bool
	}{
		{
			in:        "Authorization: Bearer " + k,
			wantWhole: "Authorization: " + Mask + " " + Mask,
			wantPerEl: "Authorization: Bearer " + k,
		},
		{
			in:        "Authorization: Basic " + k,
			wantWhole: "Authorization: " + Mask + " " + Mask,
			wantPerEl: "Authorization: Basic " + k,
		},
		{
			in:        "token: " + k,
			wantWhole: "token: " + Mask,
			wantPerEl: "token: " + k,
		},
	}

	for _, tc := range worse {
		t.Run("worse/"+tc.in[:strings.Index(tc.in, ":")], func(t *testing.T) {
			if got := Redact(tc.in); got != tc.wantWhole {
				t.Errorf("whole-text Redact(%q) = %q, want %q", tc.in, got, tc.wantWhole)
			}
			got := perElement(tc.in)
			if got != tc.wantPerEl {
				t.Errorf("per-element Redact over %q = %q, want %q (the leak)", tc.in, got, tc.wantPerEl)
			}
			if !strings.Contains(got, k) {
				t.Errorf("per-element over %q no longer leaks the key. If that is a real "+
					"improvement, good -- but re-read the doc on Redact before deleting "+
					"this row, because the reason per-element is banned is that it CANNOT "+
					"be better, and a row that stopped leaking means the grammar changed.", tc.in)
			}
		})
	}

	// The other half of "strictly": over the corpus this package is actually
	// pointed at, per-element is never BETTER than whole-text. "Better" is
	// tested as "masked something the whole-text pass left alone", which for a
	// substring matcher is impossible -- so this asserts the property rather
	// than sampling it.
	corpus := []string{
		"Authorization: Bearer " + k,
		"Authorization: Basic " + k,
		"Authorization:Bearer\\ " + k,
		"token: " + k,
		"-passphrase " + k,
		"-streamid " + k,
		"-rtmp_conn S:" + k,
		"-metadata comment=" + k,
		"rtmps://live.twitch.tv/app/" + k,
		"srt://host:9000?passphrase=" + k,
		"http://admin:hunter2@example.test/hook",
		`{"token":"` + k + `"}`,
		"connection refused",
		"-preset ultrafast -c:v libx264",
	}
	for _, in := range corpus {
		whole := Redact(in)
		per := perElement(in)
		wholeMasks := strings.Count(whole, Mask)
		perMasks := strings.Count(per, Mask)
		if perMasks > wholeMasks {
			t.Errorf("per-element masked MORE of %q than whole-text did "+
				"(per=%q whole=%q). Redact matches substrings, so this should be "+
				"impossible; something in the grammar now depends on the split.",
				in, per, whole)
		}
	}
}

// TestRedactIsIdempotentOverItsOwnMask covers the Scrub-then-Redact path.
//
// supervisor.Process.scrub is `Redact(secrets.Scrub(s))`: the exact pass writes
// Mask into the text and the residual pass then reads it back. Mask ends in
// ']', which splitTrailer used to peel as sentence punctuation, so every
// destination URL on the retained MQTT topic came out as
// "rtmps://h/app/[redacted]]".
func TestRedactIsIdempotentOverItsOwnMask(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"rtmps://h/app/" + Mask, "rtmps://h/app/" + Mask},
		{"rtmps://h/app/" + Mask + ".", "rtmps://h/app/" + Mask + "."},
		{"published to rtmps://h/app/" + Mask + " ok", "published to rtmps://h/app/" + Mask + " ok"},
		// The ordinary case must not regress: a real key still goes.
		{"rtmps://h/app/live_284729384_pQ8fZmT3xR9wLkYvB2nHsA", "rtmps://h/app/" + Mask},
		// And genuine sentence punctuation is still peeled rather than eaten
		// into the masked path.
		{"failed on rtmp://h/app/live_284729384_pQ8fZmT3xR9wLkYvB2nHsA.", "failed on rtmp://h/app/" + Mask + "."},
	}
	for _, tc := range tests {
		if got := Redact(tc.in); got != tc.want {
			t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Idempotence stated directly: a second pass over already-redacted text is
	// a fixed point.
	for _, tc := range tests {
		once := Redact(tc.in)
		if twice := Redact(once); twice != once {
			t.Errorf("Redact is not idempotent: %q -> %q -> %q", tc.in, once, twice)
		}
	}
}
