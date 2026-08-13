package engine

// #306. THE STORED SPELLING OF A CREDENTIAL AND THE SPELLING THAT REACHES THE
// WIRE WERE ALLOWED TO DIVERGE, and SecretSet.Scrub only ever knew the stored
// one.
//
// Measured on a live run against real platforms. A destination's key carried a
// bracketed-paste artefact -- the real key followed by ESC [ 2 7 ; 2 ; 1 3 --
// so 65 bytes were stored. FFmpeg stopped reading the publish URL at the ESC,
// opened the 56-byte prefix, could not connect, and printed the prefix back:
//
//	dest:5: Error opening output file <ingest-url>/<STREAM KEY>
//
// Everything downstream was wired correctly. supervisor.(*Process).appendLog
// does apply p.scrub, and engine.destSecrets does declare the destination's
// key. The scrub found nothing because a 65-byte needle does not occur inside a
// 56-byte haystack, and process.log is not a buffer: supervisor.FileSink
// persists and rotates it into exactly the files that get collected into bug
// reports and support tarballs.
//
// The test below drives that path FOR REAL rather than asserting on the secret
// list. A test that inspected destSecrets' output would pass against a set full
// of literals that match nothing, which is precisely the failure mode being
// closed -- so what is asserted is that the used form does not survive to the
// two places it survived to in production: the on-disk process.log and the
// in-memory ring GET /processes/{name}/logs and the /ws stream read from.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// pasteArtefact is the exact byte sequence off the run in #306: what a
// bracketed-paste-aware terminal emitted into the field alongside the key.
const pasteArtefact = "\x1b[27;2;13"

// Named `sentinel` rather than `key`: see the note in
// internal/api/ws_policy_array_test.go. gitleaks matches on the identifier, so
// `const key = "<high-entropy string>"` is a finding, and a finding in the
// working tree disables the allowlist self-test entirely.
const wireSentinel = "SENTINEL-dest-wire-4c7ae91f38"

// destIngestURL is the destination's publish URL. It has to survive the scrub:
// an operator reading process.log after a failed go-live is reading it to find
// out WHICH destination refused them.
//
// NO APPLICATION PATH, and that is what makes this test measure the boundary
// rather than the residual. supervisor.(*Process).scrub is
// alerts.Redact(secrets.Scrub(s)): two passes, and Redact would mask the last
// segment of an rtmp URL all by itself -- but only when the path has TWO
// non-empty segments, because alerts.maskLastSegment returns the path untouched
// below that. `rtmp://host/app/KEY` is therefore covered twice over and proves
// nothing about either pass; `rtmp://host/KEY` is covered by the declared
// secrets ALONE, which is the shape this test needs and a perfectly ordinary
// ingest to be handed. The guard inside the test measures that rather than
// trusting this paragraph.
const destIngestURL = "rtmp://live.example.com"

// TestAKeyTruncatedAtAControlCharacterReachesNeitherProcessLogNorRing plants a
// key with a control character in it, drives a destination's real failure path,
// and asserts the key's USED form -- not its stored form -- is in neither sink.
//
// Both placements truncate at the FIRST control byte, which is the mechanism;
// they differ only in how much of the key survives into the URL. Neither is
// reached by strings.TrimSpace, because ESC is not whitespace -- a fix built on
// trimming alone would pass nothing here.
func TestAKeyTruncatedAtAControlCharacterReachesNeitherProcessLogNorRing(t *testing.T) {
	for _, tc := range []struct {
		name string
		// stored is what the database holds and what destSecrets is handed.
		stored string
		// used is what FFmpeg opened and printed back, i.e. the bytes that
		// actually reach process.log.
		used string
	}{
		// The measured case: the artefact lands after the whole key.
		{"artefact after the key", wireSentinel + pasteArtefact, wireSentinel},
		// The same mechanism with the escape inside the key, which is what a
		// paste into a field whose cursor was not at the end produces. FFmpeg
		// still stops at the first control byte, so it publishes -- and prints
		// back -- a SHORTER key that is still most of the credential.
		{"escape inside the key", wireSentinel[:20] + pasteArtefact + wireSentinel[20:], wireSentinel[:20]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// BUILT DIRECTLY, not through the store, and deliberately: this row
			// is one db.Destination.Validate now refuses. That refusal is the
			// fix; this is the layer behind it, and the whole reason for a
			// layer behind it is the row that got in anyway -- written by an
			// older release, or through a field the check does not cover.
			row := &db.Destination{
				ID: 5, Name: "twitch", Kind: db.DestRTMP,
				URL: destIngestURL, StreamKey: tc.stored,
			}

			// The line verbatim from the issue, with the URL FFmpeg assembled:
			// the publish URL up to the point it stopped reading.
			failure := "[out#0/flv @ 0x7f9c] Error opening output file " +
				destIngestURL + "/" + tc.used

			// THE FIXTURE GUARD. supervisor.(*Process).scrub runs the declared
			// secrets AND alerts.Redact, so a line Redact can mask on its own
			// would come back clean no matter what destSecrets emitted, and this
			// test would assert nothing while looking green. Measure it here
			// instead of reasoning about it: if Redact ever grows to cover this
			// shape, this fails and says the test stopped being a test.
			if !strings.Contains(alerts.Redact(failure), tc.used) {
				t.Fatalf("alerts.Redact removes the key from this line on its own, so nothing "+
					"below measures the declared-secret pass.\n  line:  %s\n  redact: %s\n"+
					"Pick a line shape the residual pass cannot see.", failure, alerts.Redact(failure))
			}

			dir := t.TempDir()
			sink, err := supervisor.NewFileSink(testLogger(), dir, 1, 2)
			if err != nil {
				t.Fatalf("open the on-disk log sink: %v", err)
			}
			t.Cleanup(func() { _ = sink.Close() })

			self, err := os.Executable()
			if err != nil {
				t.Fatalf("locate the test binary to re-execute as a child: %v", err)
			}
			t.Setenv(engineFakeChildStderr, failure)

			p := supervisor.New(testLogger(), supervisor.Spec{
				Name: "dest:5", Kind: "destination",
				Bin:  self,
				Args: []string{engineFakeChildFlag},
				// The one line under test. Everything else here is the
				// production wiring, unchanged.
				Secrets: destSecrets(row),
				LogSink: sink,
			})
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = p.Stop(ctx)
			})
			p.Start()

			// Wait on the RING, then close the sink. appendLog hands the line to
			// the sink before it returns, so a line visible here is already
			// queued, and Close drains what is queued.
			var ring []supervisor.LogLine
			deadline := time.Now().Add(15 * time.Second)
			for {
				ring = p.Logs()
				if containsAny(ring, "Error opening output file") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the child's stderr line never reached the log ring; nothing "+
						"below would be measuring anything. ring: %v", ring)
				}
				time.Sleep(time.Millisecond)
			}
			if err := sink.Close(); err != nil {
				t.Fatalf("close the on-disk log sink: %v", err)
			}
			onDisk, err := os.ReadFile(filepath.Join(dir, "process.log"))
			if err != nil {
				t.Fatalf("read process.log: %v", err)
			}

			// THE POSITIVE CONTROL, first. "The key is not in this file" and
			// "this file is empty" are the same green line, and the second is
			// what a broken fixture produces.
			if !strings.Contains(string(onDisk), "Error opening output file") {
				t.Fatalf("process.log does not carry the failure line at all, so its not "+
					"carrying the key proves nothing.\ngot: %s", onDisk)
			}

			if strings.Contains(string(onDisk), tc.used) {
				t.Errorf("the stream key FFmpeg actually used is in process.log in the clear.\n"+
					"  stored: %q\n  used:   %q\n"+
					"process.log is persisted and ROTATED, so this file is collected into bug "+
					"reports, support bundles and backups -- and a failure is exactly when an "+
					"operator copies logs to somebody else. Whoever reads this can broadcast "+
					"to the owner's channel.\ngot: %s", tc.stored, tc.used, onDisk)
			}
			for _, l := range ring {
				if strings.Contains(l.Text, tc.used) {
					t.Errorf("the stream key FFmpeg actually used is in the in-memory log ring, "+
						"which GET /processes/{name}/logs and the /ws log stream serve to any "+
						"READ-scoped token.\n  used: %q\n  line: %s", tc.used, l.Text)
				}
			}

			// The other half, and the one worth breaking a fix over. A scrub
			// that blanked the line would satisfy everything above and leave an
			// operator with a log they cannot use.
			masked := ""
			for _, l := range ring {
				if strings.Contains(l.Text, "Error opening output file") {
					masked = l.Text
				}
			}
			if !strings.Contains(masked, alerts.Mask) {
				t.Errorf("the key is gone from %q but nothing says a redaction happened, which "+
					"usually means the text was destroyed rather than masked", masked)
			}
			for _, want := range []string{"Error opening output file", "rtmp://", "live.example.com"} {
				if !strings.Contains(masked, want) {
					t.Errorf("the log line has lost %q, which is the answer to \"which "+
						"destination is down and why\".\ngot: %s", want, masked)
				}
			}
		})
	}
}

// containsAny reports whether any line in the ring carries sub.
func containsAny(lines []supervisor.LogLine, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l.Text, sub) {
			return true
		}
	}
	return false
}
