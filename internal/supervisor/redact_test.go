// The credential guards for the two accessors an operator's screen reads from.
//
// A destination's argv is a publish URL, and with Facebook backup ingest on it
// is two of them; FFmpeg echoes the same URL to stderr when the connect fails.
// Those are the bytes these tests hold, and the balance they hold is two-sided:
// the key must be gone, and everything an operator opened the page to read must
// still be there.

package supervisor

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

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

// The supervisor's own logger is a sink in its own right, and it was the one
// left uncovered. LastError above is scrubbed, the process log is scrubbed,
// and "process exited" -- the line that fires on exactly the failure an
// operator will be sharing with somebody -- was handed the raw error.
//
// Found on a live multistream run: a platform refused the publish, the
// supervisor retried, and each retry wrote the key to server.log. process.log
// was clean, so the leak survived a check that only looked at the sink that
// had already been fixed. See #306.
//
// Proven able to fail against the committed tree by changing the Warn call in
// supervisor.go back to `"err", err`: the key reappeared in the log buffer and
// the first assertion below failed.
func TestTheProcessExitedLogLineCarriesNoStreamKey(t *testing.T) {
	// FFmpeg's real refusal, whose whole point is to name the URL it could
	// not open -- so the key travels inside the exit error, not the argv.
	refusal := "[out#0/flv @ 0x65431867e200] Error opening output rtmps://" +
		fbHost + ":443/rtmp/" + mainStreamKey + ": Connection refused"

	var buf syncBuffer
	spec := Spec{
		Name:    "dest:5",
		Kind:    "destination",
		Secrets: []string{mainStreamKey},
	}
	f := fakeExitSaying(251, refusal)
	spec.Bin, spec.Args = f.bin, f.args
	p := New(slog.New(slog.NewTextHandler(&buf, nil)), spec)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = p.Stop(ctx)
	})

	p.Start()
	waitFor(t, "the process to be recorded as failed", func() bool {
		return p.Status().State == StateFailed
	})
	waitFor(t, "the exit to reach the log", func() bool {
		return strings.Contains(buf.String(), "process exited")
	})

	got := buf.String()
	if strings.Contains(got, mainStreamKey) {
		t.Errorf("the \"process exited\" log line carries the stream key.\n" +
			"server.log is the file an operator copies to a forum when a\n" +
			"destination will not connect, and a refusal is when it fires.")
	}
	// Not merely absent: absent because it was masked. A scrub that dropped
	// the error text entirely would pass the check above and take the
	// diagnostic with it.
	if !strings.Contains(got, alerts.Mask) {
		t.Errorf("the key was neither present nor masked -- the error text\n"+
			"has gone missing rather than been scrubbed.\ngot: %s", got)
	}
	for _, want := range []string{"exit status 251", "Error opening output", fbHost, "Connection refused"} {
		if !strings.Contains(got, want) {
			t.Errorf("the log line has lost %q, which is what makes it worth\n"+
				"logging at all.\ngot: %s", want, got)
		}
	}
}

// The SECOND line that carries the same error, which the fix above missed.
//
// #311 scrubbed "process exited" and left "giving up on process" four lines
// below it in the same function, reading the same `msg`. The test above could
// not see it: it asserts on a supervisor that retries for ever, so the give-up
// branch never ran. A leak was fixed, a test was written to pin the fix, and
// the sibling path stayed open -- which is the shape this file already warns
// about at the top of the test above ("the leak survived a check that only
// looked at the sink that had already been fixed").
//
// This one is the worse of the two to leak on. It fires only after MaxRestarts
// consecutive failures, so it marks the moment a destination has been refused
// over and over -- which is precisely when an operator stops watching and
// starts copying server.log into an issue.
//
// Proven able to fail against the committed tree by changing the Error call in
// supervisor.go back to `"err", msg`: the key reappeared and the first
// assertion below failed.
func TestTheGivingUpLogLineCarriesNoStreamKey(t *testing.T) {
	refusal := "[out#0/flv @ 0x65431867e200] Error opening output rtmps://" +
		fbHost + ":443/rtmp/" + mainStreamKey + ": Connection refused"

	var buf syncBuffer
	f := fakeExitSaying(251, refusal)
	spec := Spec{
		Name:    "dest:5",
		Kind:    "destination",
		Secrets: []string{mainStreamKey},
		Bin:     f.bin,
		Args:    f.args,
		// The three that make the give-up branch reachable at all. Without
		// AutoRestart the process never retries and never gives up; without a
		// low MaxRestarts and a short backoff the test outlives its deadline
		// before the branch is entered.
		AutoRestart: true,
		MaxRestarts: 2,
		MinBackoff:  10 * time.Millisecond,
		MaxBackoff:  20 * time.Millisecond,
	}
	p := New(slog.New(slog.NewTextHandler(&buf, nil)), spec)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = p.Stop(ctx)
	})

	p.Start()
	waitFor(t, "the supervisor to give up", func() bool {
		return strings.Contains(buf.String(), "giving up on process")
	})

	got := buf.String()
	if strings.Contains(got, mainStreamKey) {
		t.Errorf("the \"giving up on process\" log line carries the stream key.\n" +
			"This fires after a destination has been refused repeatedly, which\n" +
			"is exactly when server.log gets copied into a bug report.")
	}
	if !strings.Contains(got, alerts.Mask) {
		t.Errorf("the key was neither present nor masked -- the error text has\n"+
			"gone missing rather than been scrubbed.\ngot: %s", got)
	}
	// The diagnostic has to survive the scrub, or the fix has traded a leak
	// for an unreadable failure.
	for _, want := range []string{"Error opening output", fbHost, "Connection refused"} {
		if !strings.Contains(got, want) {
			t.Errorf("the give-up line has lost %q, which is what makes it worth\n"+
				"logging at all.\ngot: %s", want, got)
		}
	}
}

// syncBuffer is a bytes.Buffer the run goroutine writes and the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
