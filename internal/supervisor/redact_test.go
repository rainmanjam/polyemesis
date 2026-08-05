// The credential guards for the two accessors an operator's screen reads from.
//
// A destination's argv is a publish URL, and with Facebook backup ingest on it
// is two of them; FFmpeg echoes the same URL to stderr when the connect fails.
// Those are the bytes these tests hold, and the balance they hold is two-sided:
// the key must be gone, and everything an operator opened the page to read must
// still be there.

package supervisor

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

// The shape from the audit: a Facebook live destination publishing to the
// primary and backup ingests. The key is the last path segment of each URL,
// and it is the whole of the credential -- anyone holding it can take the
// broadcast over.
const (
	mainStreamKey = "FB-101234567890123-0-AbMainKeyMaterial"
	backupKey     = "FB-101234567890123-0-XyBackupKeyMaterial"
	srtPassphrase = "a-relay-passphrase"
	fbHost        = "live-api-s.facebook.com"
)

func facebookDestArgv() []string {
	return []string{
		"-hide_banner",
		"-i", "srt://127.0.0.1:6001?passphrase=" + srtPassphrase,
		"-c:v", "libx264", "-b:v", "6000k",
		"-f", "flv", "rtmps://" + fbHost + ":443/rtmp/" + mainStreamKey,
		"-f", "flv", "rtmps://" + fbHost + ":443/rtmp/" + backupKey,
	}
}

// Proven able to fail against the committed tree by deleting the line
// `a = alerts.Redact(a)` from CommandString in supervisor.go: the primary key,
// the backup key and the SRT passphrase all reappeared in the rendered command
// and every secretGone assertion below failed.
func TestTheRenderedCommandCarriesNoStreamKey(t *testing.T) {
	p := New(discardLog(), Spec{Name: "dest:3", Kind: "destination",
		Bin: "ffmpeg", Args: facebookDestArgv()})

	got := p.CommandString()

	for _, secret := range []string{mainStreamKey, backupKey, srtPassphrase} {
		if strings.Contains(got, secret) {
			t.Errorf("CommandString() still carries %q.\n"+
				"This string is rendered on the monitoring page and pasted into support tickets; "+
				"the key in it is enough to take the broadcast over.\ngot: %s", secret, got)
		}
	}
	// The backup key is the one that arrived most recently and has no separate
	// masking anywhere else, so name it: a redactor that caught only the first
	// URL in the argv would pass everything above except this.
	if strings.Count(got, alerts.Mask) < 3 {
		t.Errorf("expected the primary URL, the backup URL and the passphrase to be masked "+
			"(%d masks), got %d in: %s", 3, strings.Count(got, alerts.Mask), got)
	}
}

// The other half, and the one worth breaking the fix over: an operator reads
// this to find out WHICH destination is refusing them and why. A redactor that
// blanked the whole command would satisfy the test above and be useless.
//
// Proven able to fail against the committed tree by changing `alerts.Redact` to
// `alerts.RedactWebhookURL` in CommandString in supervisor.go -- the signatures
// match, so it compiles, and every argument collapsed to "[redacted]".
func TestTheRenderedCommandIsStillDiagnosable(t *testing.T) {
	p := New(discardLog(), Spec{Name: "dest:3", Kind: "destination",
		Bin: "ffmpeg", Args: facebookDestArgv()})

	got := p.CommandString()

	for _, want := range []string{"ffmpeg", "-hide_banner", "libx264", "6000k", "-f", "flv",
		"rtmps://", fbHost, "/rtmp/", "srt://127.0.0.1:6001"} {
		if !strings.Contains(got, want) {
			t.Errorf("CommandString() has lost %q, which is how an operator tells this "+
				"destination from the other seven.\ngot: %s", want, got)
		}
	}
}

// Proven able to fail against the committed tree by deleting the line
// `lines[i].Text = alerts.Redact(lines[i].Text)` from Logs in supervisor.go:
// the publish URL came back whole and the key assertion failed.
func TestLogsCarryNoStreamKeyOutOfTheProcess(t *testing.T) {
	p := New(discardLog(), Spec{Name: "dest:3", Kind: "destination"})
	// What FFmpeg actually prints when a publish endpoint refuses it: the whole
	// output URL, key included. This is the line the audit is about.
	p.appendLog("[out#1/flv @ 0x7f9c] Error opening output rtmps://"+fbHost+
		":443/rtmp/"+backupKey+": Connection refused", "error")

	lines := p.Logs()
	if len(lines) != 1 {
		t.Fatalf("expected the one appended line, got %d", len(lines))
	}
	got := lines[0].Text

	if strings.Contains(got, backupKey) {
		t.Errorf("Logs() still carries the stream key FFmpeg printed.\ngot: %s", got)
	}
	if !strings.Contains(got, alerts.Mask) {
		t.Errorf("the key was neither present nor masked; the line was rewritten some "+
			"third way.\ngot: %s", got)
	}
	// The diagnosis has to survive the masking, or the operator has traded a
	// leak for a log they cannot use.
	for _, want := range []string{"Error opening output", "rtmps://", fbHost, "Connection refused"} {
		if !strings.Contains(got, want) {
			t.Errorf("Logs() has lost %q, which is the answer to \"why is this destination down\".\ngot: %s", want, got)
		}
	}
}

// Proven able to fail against the committed tree by changing `alerts.Redact` to
// `alerts.RedactWebhookURL` in Logs in supervisor.go -- the signatures match,
// so it compiles, and every ordinary line collapsed to "[redacted]".
//
// This is the guard against overcorrecting. Almost every line in the ring is an
// ordinary FFmpeg progress or warning line with no credential in it, and those
// must arrive byte for byte.
func TestOrdinaryLogLinesArePassedThroughUntouched(t *testing.T) {
	ordinary := []string{
		"frame= 1234 fps= 30 q=28.0 size=    2048kB time=00:00:41.13 bitrate= 407.9kbits/s speed=1.01x",
		"[flv @ 0x600001a0] Failed to update header with correct duration",
		"Past duration 0.639999 too large",
		"[libx264 @ 0x7f8] using SAR=1/1",
	}
	p := New(discardLog(), Spec{Name: "dest:3"})
	for _, l := range ordinary {
		p.appendLog(l, classify(l))
	}

	lines := p.Logs()
	if len(lines) != len(ordinary) {
		t.Fatalf("appended %d lines, got %d back", len(ordinary), len(lines))
	}
	for i, want := range ordinary {
		if lines[i].Text != want {
			t.Errorf("an ordinary log line was altered on the way out.\n got: %s\nwant: %s",
				lines[i].Text, want)
		}
	}
}

// Status.LastError is the same leak as Logs, reached by a different route and
// travelling further. runOnce joins the last three stderr lines classified as
// error or fatal into the process's exit error, and FFmpeg's complaint about a
// refused publish endpoint is one of those lines, key and all.
//
// Where it goes matters more than where it comes from. engine copies it onto
// the dashboard snapshot, and cmd/polyemesis/mqtt.go copies it into
// SourceState.IngestError, which is published RETAINED -- so a key that reaches
// this field is left sitting on the broker after polyemesis exits, readable by
// every subscriber, behind no session at all.
//
// Proven able to fail against the committed tree by changing `alerts.Redact(p.lastErr)`
// back to `p.lastErr` in Status in supervisor.go.
func TestStatusLastErrorCarriesNoStreamKey(t *testing.T) {
	p := New(discardLog(), Spec{Name: "dest:3", Kind: "destination"})
	// Set through the same path a real exit takes.
	p.setState(StateFailed, "exit status 1: [out#1/flv @ 0x7f9c] Error opening output rtmps://"+
		fbHost+":443/rtmp/"+backupKey+": Connection refused")

	got := p.Status().LastError

	if strings.Contains(got, backupKey) {
		t.Errorf("Status().LastError carries the stream key.\n"+
			"It is serialised by GET /processes, by the dashboard snapshot, and -- "+
			"retained -- onto an MQTT topic that outlives this process.\ngot: %s", got)
	}
	if !strings.Contains(got, alerts.Mask) {
		t.Errorf("the key was neither present nor masked.\ngot: %s", got)
	}
	for _, want := range []string{"exit status 1", "Error opening output", fbHost, "Connection refused"} {
		if !strings.Contains(got, want) {
			t.Errorf("LastError has lost %q, which is the whole reason the field exists.\ngot: %s", want, got)
		}
	}
}

// Args is the raw argv on purpose -- expert mode has to reason about the
// arguments themselves, and cannot do that against a masked copy. Stated as a
// test so that "why is one of these masked and not the other" has an answer
// that does not depend on reading both doc comments.
//
// Proven able to fail against the committed tree by rewriting the one-line body of
// Args in supervisor.go to `return strings.Fields(p.CommandString())`, which
// compiles and hands back the masked rendering.
func TestArgsStaysRawBecauseItIsNotForDisplay(t *testing.T) {
	p := New(discardLog(), Spec{Name: "dest:3", Bin: "ffmpeg", Args: facebookDestArgv()})

	if !strings.Contains(strings.Join(p.Args(), " "), mainStreamKey) {
		t.Error("Args() has been masked too. Expert mode strips its own additions back off " +
			"this argv to show an operator their edit; against a masked copy the strip " +
			"silently stops matching and the editor shows the wrong command.")
	}
}

// capturingSink records what the process handed it, unchanged. A real FileSink
// would do the same thing and then write it to disk.
type capturingSink struct{ lines []LogLine }

func (c *capturingSink) WriteLog(l LogLine) { c.lines = append(c.lines, l) }

// Logs() was not the only way a line leaves the process, and masking there
// covered the reader that prompted the fix while missing the two that matter
// more.
//
// LogSink is a FileSink in production, so an unmasked line became a PERMANENT
// one in process.log -- and a log file is the artifact people attach to bug
// reports and ship in support tarballs, which the database beside it never is.
//
// Proven able to fail against the committed tree by changing appendLog's
// `Text: alerts.Redact(text)` back to `Text: text`: measured FAIL, "the log
// sink was handed the stream key".
func TestTheLogSinkIsNeverHandedAStreamKey(t *testing.T) {
	sink := &capturingSink{}
	p := New(discardLog(), Spec{Name: "dest:3", Kind: "destination", LogSink: sink})
	p.appendLog("[out#1/flv @ 0x7f9c] Error opening output rtmps://"+fbHost+
		":443/rtmp/"+backupKey+": Connection refused", "error")

	if len(sink.lines) != 1 {
		t.Fatalf("the sink received %d lines, want the one appended", len(sink.lines))
	}
	got := sink.lines[0].Text
	if strings.Contains(got, backupKey) {
		t.Errorf("the log sink was handed the stream key, and FileSink writes it to "+
			"process.log where it stays for ever.\ngot: %s", got)
	}
	if !strings.Contains(got, alerts.Mask) {
		t.Errorf("the key was neither present nor masked; the line was rewritten some "+
			"third way.\ngot: %s", got)
	}
	// A masked log nobody can diagnose from is a worse trade than the leak.
	for _, want := range []string{"Error opening output", fbHost, "Connection refused"} {
		if !strings.Contains(got, want) {
			t.Errorf("the sink line has lost %q, which is why the destination is down.\ngot: %s", want, got)
		}
	}
}

// OnLog publishes to the event bus, which the console's live log panel reads
// over the WebSocket. That socket is behind requireAuth so no privilege
// boundary is crossed -- but it is precisely the panel an operator screenshots
// into a ticket, which is the hazard the redaction policy exists for.
//
// Proven able to fail against the committed tree by changing appendLog's
// `Text: alerts.Redact(text)` back to `Text: text`: measured FAIL, "the OnLog
// callback was handed the stream key".
func TestTheOnLogCallbackIsNeverHandedAStreamKey(t *testing.T) {
	var seen []LogLine
	p := New(discardLog(), Spec{
		Name: "dest:3", Kind: "destination",
		OnLog: func(l LogLine) { seen = append(seen, l) },
	})
	p.appendLog("[out#1/flv @ 0x7f9c] Error opening output rtmps://"+fbHost+
		":443/rtmp/"+backupKey+": Connection refused", "error")

	if len(seen) != 1 {
		t.Fatalf("the callback received %d lines, want the one appended", len(seen))
	}
	if got := seen[0].Text; strings.Contains(got, backupKey) {
		t.Errorf("the OnLog callback was handed the stream key, and it goes straight "+
			"to the console's log panel.\ngot: %s", got)
	}
}
