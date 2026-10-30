package failreason

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fixture reads one captured stderr file into the lines the supervisor would
// have handed the classifier: one entry per line, blank lines dropped, exactly
// as runOnce's scanner keeps them.
func fixture(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name+".stderr"))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimRight(sc.Text(), "\r\n"); l != "" {
			out = append(out, l)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("fixture %s is empty", name)
	}
	return out
}

// manifestRanFor is how long the captured FFmpeg actually ran, from the
// manifest the capture script wrote. Using the measured figure rather than a
// made-up one is the point: "quick" is a claim about real timing.
func manifestRanFor(t *testing.T, name string) time.Duration {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "manifest.tsv"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	for _, row := range strings.Split(string(b), "\n") {
		cols := strings.Split(row, "\t")
		if len(cols) >= 3 && cols[0] == name {
			ms, err := strconv.Atoi(cols[2])
			if err != nil {
				t.Fatalf("manifest ran_ms for %s: %v", name, err)
			}
			return time.Duration(ms) * time.Millisecond
		}
	}
	t.Fatalf("fixture %s is not in the manifest", name)
	return 0
}

// TestEveryCapturedFailureIsClassifiedAsTheCauseItWasCapturedFor is the
// contract with the fixtures: each file is real FFmpeg 8.1.3 stderr against a
// local ingest (see testdata/README.md), and each row says what an operator
// should be told about it.
//
// THE LOOK-ALIKES ARE ASSERTED ON PURPOSE. An unknown RTMP application, an
// unconfigured MediaMTX path and an SRT stream id the server will not take
// print byte-for-byte what a refused key prints. They are pinned to the same
// cause so that nobody "fixes" one of them into a different answer without
// also explaining how FFmpeg's output could tell them apart -- it cannot.
func TestEveryCapturedFailureIsClassifiedAsTheCauseItWasCapturedFor(t *testing.T) {
	cases := []struct {
		fixture string
		want    Cause
		// line is a fragment of the line the classifier must point at. It is
		// the lower-layer line, not FFmpeg's generic "Error opening output"
		// trailer that ends nearly every capture.
		line string
	}{
		{"rtmp-onpublish-403", KeyRejected, "Error opening output rtmp://"},
		{"rtmp-auth-failed", KeyRejected, "Server error: authentication failed"},
		{"srt-wrong-passphrase", KeyRejected, "Peer rejected connection"},
		{"icecast-401", KeyRejected, "401 Unauthorized"},
		{"rtmp-unknown-app", KeyRejected, "Error opening output rtmp://"},
		{"rtmp-unconfigured-path", KeyRejected, "Error opening output rtmp://"},
		{"srt-unconfigured-path", KeyRejected, "Peer rejected connection"},
		{"icecast-503", PlatformNotReady, "503"},
		{"rtmp-already-publishing", PlatformNotReady, "Server error: Already publishing"},
		{"rtmp-dns-unresolvable", DNS, "Failed to resolve hostname"},
		{"rtmp-connection-refused", ConnectionRefused, "Connection refused"},
		{"rtmp-silent-ingest-timeout", Timeout, "Operation timed out"},
		{"rtmps-to-plain-port", TLS, "[tls @"},
		{"rtmps-untrusted-cert", TLS, "certificate verify failed"},
		{"flv-mpeg2-refused", CodecRejected, "not compatible with flv"},
		// The ingest dropped a codec it would not carry without saying why.
		// FFmpeg sees a broken pipe and nothing else, so the honest answer is
		// "unknown", with the raw last line -- see the README.
		{"rtmp-flv1-dropped", Unknown, "Error closing file: Broken pipe"},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			got, ok := Classify(Input{
				Stderr:    fixture(t, tc.fixture),
				Failed:    true,
				RanFor:    manifestRanFor(t, tc.fixture),
				Moved:     false,
				RateKnown: false,
			})
			if !ok {
				t.Fatalf("Classify returned no reason for a failed run")
			}
			if got.Cause != tc.want {
				t.Fatalf("cause = %q, want %q (line %q)", got.Cause, tc.want, got.Line)
			}
			if !strings.Contains(got.Line, tc.line) {
				t.Fatalf("line = %q, want it to contain %q", got.Line, tc.line)
			}
		})
	}
}

// TestTheThrottledUploadIsUploadTooSlowFromItsTimingAlone covers the one cause
// that is not in stderr at all. The capture's stderr holds nothing but -re lag
// notices and the flv header lines; what makes it "upload too slow" is that
// media moved and the delivery rate collapsed. The rate here is the one the
// capture's own -progress stream reported in its final block (0.0151x), and
// the test pins that the classifier reaches the verdict from the rate, not
// from any sentence.
func TestTheThrottledUploadIsUploadTooSlowFromItsTimingAlone(t *testing.T) {
	got, ok := Classify(Input{
		Stderr:    fixture(t, "rtmp-throttled-upload"),
		Failed:    true,
		RanFor:    manifestRanFor(t, "rtmp-throttled-upload"),
		Moved:     true,
		Rate:      0.0151,
		RateKnown: true,
	})
	if !ok || got.Cause != UploadTooSlow {
		t.Fatalf("got %+v (ok=%v), want upload_too_slow", got, ok)
	}
	if got.Quick {
		t.Fatal("a run of a minute is not a quick rejection")
	}
}

// TestASourceOutageIsNotUploadTooSlow: a rate of exactly zero is a stall, and
// a stall is what every destination shows when the INGEST stops. Calling that
// a slow upload would send the operator to their uplink for a camera that was
// unplugged. Only crawling -- moving, but slower than realtime -- counts.
func TestASourceOutageIsNotUploadTooSlow(t *testing.T) {
	got, ok := Classify(Input{
		Stderr:    []string{"[out#0/flv @ 0x1] Error muxing a packet", "Error closing file: Broken pipe"},
		Failed:    true,
		RanFor:    5 * time.Minute,
		Moved:     true,
		Rate:      0,
		RateKnown: true,
	})
	if !ok || got.Cause == UploadTooSlow {
		t.Fatalf("got %+v, want anything but upload_too_slow", got)
	}
}

// TestASpecificCauseInStderrBeatsASlowRate: a TLS failure on a reconnect is a
// TLS failure whatever the previous run's rate was.
func TestASpecificCauseInStderrBeatsASlowRate(t *testing.T) {
	got, _ := Classify(Input{
		Stderr:    fixture(t, "rtmps-untrusted-cert"),
		Failed:    true,
		RanFor:    time.Minute,
		Moved:     true,
		Rate:      0.3,
		RateKnown: true,
	})
	if got.Cause != TLS {
		t.Fatalf("cause = %q, want tls", got.Cause)
	}
}

// TestACleanExitHasNoReason is the control capture: a key the ingest accepted,
// a run that ended because -t said so, exit 0. A classifier that finds a
// failure in that teaches operators to ignore the field.
func TestACleanExitHasNoReason(t *testing.T) {
	if got, ok := Classify(Input{Stderr: fixture(t, "rtmp-accepted"), Failed: false, RanFor: 4 * time.Second}); ok {
		t.Fatalf("got %+v for a clean exit, want no reason", got)
	}
}

// TestQuickIsAboutHowSoonTheRunEnded pins the window the retry hold counts in.
// A refusal at publish arrives in well under a second (the captures measured
// 0.3-3.4 s); a run that lasted a minute and then failed was not refused, it
// was delivering.
func TestQuickIsAboutHowSoonTheRunEnded(t *testing.T) {
	lines := fixture(t, "rtmp-auth-failed")
	fast, _ := Classify(Input{Stderr: lines, Failed: true, RanFor: 3 * time.Second})
	slow, _ := Classify(Input{Stderr: lines, Failed: true, RanFor: QuickWindow + time.Second})
	if !fast.Quick || slow.Quick {
		t.Fatalf("quick: fast=%v slow=%v, want true/false", fast.Quick, slow.Quick)
	}
}

// TestOnlyKeyAndCodecRefusalsAreHoldable is the rule the retry hold is built
// on. Retrying a refused key or a refused codec cannot succeed until somebody
// changes something; every network cause can clear by itself, and so can a
// platform that is not ready yet -- in particular "already publishing", which
// is the platform still holding the previous session and clears when it times
// out. Holding on those would take a destination off air that was about to
// come back.
func TestOnlyKeyAndCodecRefusalsAreHoldable(t *testing.T) {
	holdable := map[Cause]bool{KeyRejected: true, CodecRejected: true}
	for _, c := range AllCauses() {
		if c.Holdable() != holdable[c] {
			t.Errorf("%s.Holdable() = %v, want %v", c, c.Holdable(), holdable[c])
		}
	}
	for _, c := range []Cause{DNS, ConnectionRefused, Timeout} {
		if !c.Network() {
			t.Errorf("%s.Network() = false, want true", c)
		}
	}
}

// TestUnknownSkipsFFmpegsBookkeepingLines: the raw line shown for an unknown
// cause is meant to be the last thing FFmpeg said about the failure, not the
// flv muxer complaining it could not seek back to patch a header, which ends
// nearly every RTMP run including the healthy ones.
func TestUnknownSkipsFFmpegsBookkeepingLines(t *testing.T) {
	got, _ := Classify(Input{
		Stderr: []string{
			"[vost#0:0/flv @ 0x1] Error submitting a packet to the muxer: Broken pipe",
			"    Last message repeated 1 times",
			"[flv @ 0x2] Failed to update header with correct duration.",
			"[flv @ 0x2] Failed to update header with correct filesize.",
			"[vist#0:0/wrapped_avframe @ 0x3] Resumed reading at pts 1.0 with rate 1.050 after a lag of 3.3s",
		},
		Failed: true,
		RanFor: time.Minute,
	})
	if got.Cause != Unknown || !strings.Contains(got.Line, "Error submitting a packet") {
		t.Fatalf("got %+v", got)
	}
}

// TestUnknownWithNoStderrStillSaysSomething: a child that died without a word
// (a signal, an OOM kill) still gets a reason, carrying the exit error, so the
// card never shows an empty "unknown".
func TestUnknownWithNoStderrStillSaysSomething(t *testing.T) {
	got, ok := Classify(Input{Failed: true, Exit: "signal: killed", RanFor: time.Minute})
	if !ok || got.Cause != Unknown || got.Line != "signal: killed" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}

// TestTheReasonJSONShapeIsTheContractOtherOutletsPublish pins the field names.
// The card, MQTT, the webhook envelope, the alert payload and the Stream Deck
// plugin all carry this value under these names, and the events API (#17) is
// to publish it as-is. Renaming a field here breaks somebody's script.
func TestTheReasonJSONShapeIsTheContractOtherOutletsPublish(t *testing.T) {
	r := Reason{
		Cause: KeyRejected, Line: "Server error: authentication failed", Quick: true,
		Held: true, Rejections: 3, KeyChanged: true,
		At: time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC),
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"cause":"key_rejected","line":"Server error: authentication failed","quick":true,"held":true,"rejections":3,"keyChanged":true,"at":"2026-10-30T10:00:00Z"}`
	if string(b) != want {
		t.Fatalf("json =\n%s\nwant\n%s", b, want)
	}
}

// TestEveryCauseHasAnEnglishLabel: the label is what alerts, the timeline and
// the webhook text say, so a cause added without one would arrive as a bare
// code in somebody's Slack.
func TestEveryCauseHasAnEnglishLabel(t *testing.T) {
	seen := map[string]Cause{}
	for _, c := range AllCauses() {
		l := c.Label()
		if l == "" || l == string(c) {
			t.Errorf("%s has no label", c)
		}
		if prev, dup := seen[l]; dup {
			t.Errorf("%s and %s share the label %q", c, prev, l)
		}
		seen[l] = c
	}
}

// TestTheSentenceNamesHeldAndARotatedKey: the two facts that change what the
// operator does next must survive into the English every non-UI outlet uses.
func TestTheSentenceNamesHeldAndARotatedKey(t *testing.T) {
	s := Reason{Cause: KeyRejected, Held: true, Rejections: 3, KeyChanged: true}.Sentence()
	for _, want := range []string{"stream key rejected", "held after 3 quick rejections", "different stream key"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q does not say %q", s, want)
		}
	}
	if got := (Reason{Cause: Unknown, Line: "Broken pipe"}).Sentence(); got != "unknown failure: Broken pipe" {
		t.Errorf("unknown sentence = %q", got)
	}
}
