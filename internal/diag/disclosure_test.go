package diag

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

/* THE TEST THIS FEATURE EXISTS BEHIND.
 *
 * The recorder's output is sent to somebody who does not have the machine. That
 * makes every captured field a disclosure, and it makes this file the thing that
 * has to be right -- more than the ring, more than the switch, more than the
 * export format.
 *
 * The model is the one this repository already uses for the same question: the
 * route ledger plants sixteen credentials in a high-privilege body and asserts
 * none are disclosed, and webhook_disclosure_test.go pins what Redact does and
 * does not cover. This plants a known secret in every shape a slog record can
 * carry it and asserts none survives into what would be exported.
 *
 * A SENTINEL LONG ENOUGH TO BE A SECRET. alerts.MinSecretLen is a real floor:
 * literals shorter than it are REFUSED by the set (and logged as refused,
 * deliberately) because masking a four-character string would mask half the
 * English language. A test using "abc" as its secret would prove nothing about
 * the set and would quietly be testing the residual pass instead.
 */

// A stream key shaped like the ones platforms actually issue, and long enough to
// clear alerts.MinSecretLen so the SET covers it rather than only the residual.
const sentinelKey = "live_884471209_kQxZ7fRvB2mNpL0sTdWyGhJcAe5U"

// A second one that is NEVER declared, so the residual pass is exercised on its
// own. If this survives it is a documented residual rather than a bug -- but the
// test says which is which rather than leaving it to be discovered.
const undeclaredBearer = "Bearer eyJhbGciOiJIUzI1NiJ9.undeclared.tokenvalue"

func recorderWithSentinel(t *testing.T) *Recorder {
	t.Helper()
	r := NewRecorder(64, alerts.NewSecretSet(nil, sentinelKey))
	r.SetRecording(true)
	return r
}

// exported renders what would leave the box, so the assertions are about the
// artefact rather than about an intermediate.
func exported(t *testing.T, r *Recorder) string {
	t.Helper()
	raw, err := json.Marshal(r.Records())
	if err != nil {
		t.Fatalf("marshal records: %v", err)
	}
	return string(raw)
}

func TestADeclaredSecretSurvivesNowhereInTheExport(t *testing.T) {
	r := recorderWithSentinel(t)
	log := slog.New(NewHandler(slog.NewTextHandler(io.Discard, nil), r))

	// Every shape a credential has actually reached a log line in this codebase.
	log.Info("publishing to destination", "url", "rtmps://live.example/app/"+sentinelKey)
	log.Info("refused", "key", sentinelKey)
	log.Info(sentinelKey + " appeared in the message itself")
	log.Info("argv", "cmd", []string{"ffmpeg", "-i", "in", "rtmp://x/" + sentinelKey})
	log.Info("wrapped", "err", errors.New("dial failed for "+sentinelKey))
	log.Info("nested", "detail", map[string]any{"stream": sentinelKey})
	// The KEY, not the value: an attribute named after the thing it describes.
	log.Info("keyed", sentinelKey, "present")

	out := exported(t, r)
	if strings.Contains(out, sentinelKey) {
		t.Fatalf("A DECLARED STREAM KEY REACHED THE EXPORT.\n\n"+
			"This buffer is sent to somebody who does not have the operator's box, so "+
			"a credential in it has left their control entirely — and polyemesis has "+
			"put stream keys in its own logs three times already (0.7.0's security "+
			"notes). The secret set is the mechanism that stops it and it did not.\n\n"+
			"export: %s", out)
	}
	// And the records are still useful: masking everything would also pass the
	// assertion above.
	if !strings.Contains(out, alerts.Mask) {
		t.Error("nothing was masked, so the sentinel never reached the recorder and " +
			"this test proved nothing")
	}
	if !strings.Contains(out, "publishing to destination") {
		t.Error("the surrounding message was destroyed along with the secret; an " +
			"export with no readable text is not a diagnostic")
	}
}

// THE SET IS THE MECHANISM, SO ROTATION IS THE FAILURE MODE.
//
// A key refreshed mid-session is a literal the set has never held. If SetSecrets
// is not called, the ring keeps the NEW key in the clear while masking the old
// one nobody is using — the exact inversion of what the operator expects.
func TestARotatedKeyIsMaskedOnceTheSetIsToldAboutIt(t *testing.T) {
	const rotated = "live_884471209_ZZrotatedZZ9988776655443322110099"
	r := recorderWithSentinel(t)
	log := slog.New(NewHandler(slog.NewTextHandler(io.Discard, nil), r))

	log.Info("before rotation", "key", rotated)
	if !strings.Contains(exported(t, r), rotated) {
		t.Fatal("fixture: the rotated key was already masked before the set was told " +
			"about it, so this test cannot show what SetSecrets is for")
	}

	r.Reset()
	r.SetSecrets(alerts.NewSecretSet(nil, sentinelKey, rotated))
	log.Info("after rotation", "key", rotated)

	if out := exported(t, r); strings.Contains(out, rotated) {
		t.Fatalf("a rotated key is still in the clear after SetSecrets.\n"+
			"Whatever refreshes destinations must call it, or the buffer masks only "+
			"keys that are no longer in use.\nexport: %s", out)
	}
}

// Nothing is captured while recording is off. The switch has to be a real
// switch: a recorder that keeps filling when "off" is a buffer of credentials
// nobody knows exists.
func TestNothingIsCapturedWhileRecordingIsOff(t *testing.T) {
	r := NewRecorder(64, alerts.NewSecretSet(nil, sentinelKey))
	log := slog.New(NewHandler(slog.NewTextHandler(io.Discard, nil), r))

	log.Info("while off", "key", sentinelKey)
	if got := len(r.Records()); got != 0 {
		t.Fatalf("captured %d records with recording off", got)
	}
	if r.Seen() != 0 {
		t.Fatalf("counted %d records with recording off", r.Seen())
	}
}

// The residual pass, measured rather than assumed. An UNDECLARED bearer token is
// what alerts.Redact is for, and this states the coverage explicitly so nobody
// has to rediscover it from the export.
func TestTheResidualPassCoversAnUndeclaredBearerToken(t *testing.T) {
	r := recorderWithSentinel(t)
	log := slog.New(NewHandler(slog.NewTextHandler(io.Discard, nil), r))

	log.Info("upstream refused", "detail", "Authorization: "+undeclaredBearer)

	if out := exported(t, r); strings.Contains(out, "undeclared.tokenvalue") {
		t.Errorf("an undeclared bearer token survived the residual pass.\n"+
			"The set cannot cover it — nothing declared it — so alerts.Redact is the "+
			"only thing between it and the export.\nexport: %s", out)
	}
}

// The ring must forget rather than grow, and it must forget the OLDEST.
func TestTheRingIsBoundedAndKeepsTheMostRecent(t *testing.T) {
	r := NewRecorder(4, alerts.NewSecretSet(nil, sentinelKey))
	r.SetRecording(true)
	log := slog.New(NewHandler(slog.NewTextHandler(io.Discard, nil), r))

	for _, m := range []string{"one", "two", "three", "four", "five", "six"} {
		log.Info(m)
	}

	got := r.Records()
	if len(got) != 4 {
		t.Fatalf("ring holds %d records, want its capacity of 4 — an unbounded buffer "+
			"on a box streaming for eight hours is a memory leak with a switch on it",
			len(got))
	}
	if got[0].Message != "three" || got[3].Message != "six" {
		t.Errorf("ring kept %q..%q, want three..six (oldest dropped, newest kept)",
			got[0].Message, got[3].Message)
	}
	if r.Seen() != 6 {
		t.Errorf("Seen()=%d, want 6 — the export has to be able to say the capture was "+
			"truncated rather than showing six lines as though they were all of them",
			r.Seen())
	}
}

// The switch returns to the CONFIGURED level, not to info.
func TestTurningDebugOffReturnsToTheConfiguredLevel(t *testing.T) {
	s := NewSwitch(slog.LevelWarn)
	if s.Level() != slog.LevelWarn {
		t.Fatalf("starts at %v, want warn", s.Level())
	}
	s.SetDebug(true)
	if s.Level() != slog.LevelDebug {
		t.Fatalf("debug on gives %v", s.Level())
	}
	s.SetDebug(false)
	if s.Level() != slog.LevelWarn {
		t.Errorf("after debug off the level is %v, want warn. Returning to info would "+
			"quietly change what an operator's box records, having been asked only to "+
			"turn something off.", s.Level())
	}
}

// The wrapper must not change what the process already logs.
func TestWrappingDoesNotChangeTheExistingOutput(t *testing.T) {
	var plain, wrapped strings.Builder
	opts := &slog.HandlerOptions{Level: slog.LevelDebug,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		}}
	slog.New(slog.NewTextHandler(&plain, opts)).Info("hello", "n", 1)

	r := NewRecorder(8, nil)
	r.SetRecording(true)
	slog.New(NewHandler(slog.NewTextHandler(&wrapped, opts), r)).Info("hello", "n", 1)

	if plain.String() != wrapped.String() {
		t.Errorf("the wrapper changed the process's own log output.\n plain:   %q\n "+
			"wrapped: %q\nDebug mode has to be safe to leave wired in permanently, "+
			"which means invisible when it is off.", plain.String(), wrapped.String())
	}
}

// THE BUNDLE IS WHAT ACTUALLY LEAVES, so the disclosure assertion is repeated
// against it rather than against Records(). A scrub that held everywhere except
// the export path would be the only failure that matters.
func TestTheBundleCarriesNoDeclaredSecretAndSaysWhenItIsTruncated(t *testing.T) {
	r := NewRecorder(3, alerts.NewSecretSet(nil, sentinelKey))
	r.SetRecording(true)
	sw := NewSwitch(slog.LevelInfo)
	sw.SetDebug(true)
	log := slog.New(NewHandler(slog.NewTextHandler(io.Discard, nil), r))

	for i := 0; i < 8; i++ {
		log.Info("publishing", "url", "rtmps://live.example/app/"+sentinelKey)
	}

	var buf strings.Builder
	b := Build("v0.7.0", "linux/amd64", r, sw, time.Unix(0, 0).UTC())
	if err := b.Encode(&buf); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, sentinelKey) {
		t.Fatalf("a declared stream key is in the BUNDLE — the artefact that gets "+
			"attached to a support thread.\n%s", out)
	}
	// Truncation must be stated. Eight records into a ring of three.
	if !b.Capture.Truncated || b.Capture.Held != 3 || b.Capture.Seen != 8 {
		t.Errorf("capture info = %+v, want held=3 seen=8 truncated=true. A bundle that "+
			"dropped five lines and does not say so reads as a quiet system, and an "+
			"engineer would conclude the fault left no trace.", b.Capture)
	}
	if b.Capture.Level != "DEBUG" {
		t.Errorf("level = %q, want DEBUG — \"debug mode was on\" and \"the level was "+
			"actually debug\" are different claims, and only one explains a sparse "+
			"capture", b.Capture.Level)
	}
}

// The bundle must not carry an operator's own naming. Absence is hard to test
// directly, so this pins the SHAPE: the top-level keys are a closed set, and
// adding one is a decision somebody has to make here.
func TestTheBundleShapeIsAClosedSet(t *testing.T) {
	r := NewRecorder(4, nil)
	var buf strings.Builder
	if err := Build("v", "p", r, NewSwitch(slog.LevelInfo), time.Unix(0, 0)).Encode(&buf); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(buf.String()), &got); err != nil {
		t.Fatalf("bundle is not valid JSON: %v", err)
	}
	want := map[string]bool{
		"generatedAt": true, "version": true, "platform": true,
		"capture": true, "records": true,
	}
	for k := range got {
		if !want[k] {
			t.Errorf("the bundle grew a %q field. Every field here leaves the "+
				"operator's machine, so adding one is a disclosure decision that needs "+
				"its own test — particularly anything carrying their own naming "+
				"(destination names are frequently a client's name).", k)
		}
		delete(want, k)
	}
	for k := range want {
		t.Errorf("the bundle lost its %q field", k)
	}
}

// A PULL SOURCE'S CREDENTIALS, WHICH THE FIRST VERSION OF THIS FEATURE LEAKED.
//
// The recorder's secret set was built from destinations alone — where a stream
// GOES — and said nothing about where it comes FROM. A pull source is addressed
// by a URL that routinely carries credentials, engine.go logs that URL, and it
// therefore reached the exported bundle behind nothing but the residual pass.
//
// Found by an external review of this branch before it was pushed, not by any
// test here, which is why this one exists.
func TestAPullSourceCredentialDoesNotReachTheExport(t *testing.T) {
	const camPass = "rtspCameraPassw0rd2026"
	const cdnToken = "hdnts_exp1798761600_acl_hmac_9f2c1d4b8a6e3f07"
	const publishToken = "Dxl1Gevc3Tas4XHlMxAGMXwmO0bJ96Hj"

	// The set as the handler now builds it: destination AND source literals.
	r := NewRecorder(64, alerts.NewSecretSet(nil, camPass, cdnToken, publishToken))
	r.SetRecording(true)
	log := slog.New(NewHandler(slog.NewTextHandler(io.Discard, nil), r))

	// The shapes engine.go actually logs.
	log.Info("ingest started", "mode", "pull", "url", "rtsp://operator:"+camPass+"@camera.local/stream1")
	log.Info("pull failed", "url", "https://cdn.example/live/index.m3u8?"+cdnToken)
	log.Info("srt publish accepted", "streamid", publishToken)

	out := exported(t, r)
	for _, secret := range []string{camPass, cdnToken, publishToken} {
		if strings.Contains(out, secret) {
			t.Errorf("a source credential reached the export: %q\n\n"+
				"The bundle is sent to somebody who does not have the operator's box. "+
				"A camera password, a CDN token or a publish token in it is a stranger "+
				"who can watch or publish.\nexport: %s", secret, out)
		}
	}
}
