package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/multitrack"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// The wiring guards for Twitch Enhanced Broadcasting.
//
// internal/multitrack was complete, tested and imported by nothing: the
// destination row carried a Multitrack column the API wrote, the store
// persisted and no code path read. These tests are about the seam that was
// missing, so every one of them drives the ENGINE -- startDest and
// planDestinations -- rather than the package the engine calls. A test written
// against multitrack.Negotiate would have passed on the day the feature was
// entirely unreachable, which is the exact failure being fixed.
//
// The negotiation is pointed at an httptest server through
// multitrack.Client.BaseURL, which is the one seam that package has and which
// nothing at runtime sets. Nothing here needs the network, and nothing here
// skips when it is absent -- see internal/multitrack/live_test.go, which
// passes vacuously offline and is therefore not evidence of anything.

// operatorTypedKey is what an operator put in the destination row.
//
// mintedFromTwitch is the credential Twitch answers a successful negotiation
// with. SYNTHETIC, and it cannot be otherwise -- a real one is a live stream
// key -- but the SHAPE is the measured one written down in
// multitrack.IngestEndpoint.Authentication: v1_<64 hex signature>_<8 hex>_<hex
// manifest>_<the operator's original key>. The original being a SUFFIX is the
// load-bearing part: it is what makes registering only the original look like
// protection while leaving the signature standing.
const (
	operatorTypedKey = "live_9876543210_TheOperatorTypedThis"
	mintedSigPrefix  = "v1_" + "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210" +
		"_0f1e2d3c_" + "7b2276223a312c2262223a343832307d_"
	mintedFromTwitch = mintedSigPrefix + operatorTypedKey

	negotiatedConfigID = "49456f79-a985-4011-941f-3cde9897a0c6"
	negotiatedHost     = "rtmps://ingest.global-contribute.live-video.net/app"
	storedIngest       = "rtmp://live.twitch.tv/app"
)

// negotiatedBody is a successful GetClientConfiguration response: one 1080p30
// rendition, a live audio track and a VOD audio track, and BOTH an RTMP and an
// RTMPS endpoint with RTMP listed first -- which is the order every measured
// response used and the reason Resolve does not take the first one.
func negotiatedBody() string {
	return `{
	  "meta": {"service":"IVS","schema_version":"2025-01-25","config_id":"` + negotiatedConfigID + `",
	           "required_encode_resource_estimate_percent":12},
	  "ingest_endpoints": [
	    {"protocol":"RTMP","url_template":"rtmp://ingest.global-contribute.live-video.net/app/{stream_key}",
	     "authentication":"` + mintedFromTwitch + `"},
	    {"protocol":"RTMPS","url_template":"rtmps://ingest.global-contribute.live-video.net/app/{stream_key}",
	     "authentication":"` + mintedFromTwitch + `"}
	  ],
	  "encoder_configurations": [
	    {"type":"obs_nvenc_h264_tex","width":1920,"height":1080,
	     "framerate":{"numerator":30,"denominator":1},"canvas_index":0,
	     "settings":{"bitrate":6000}}
	  ],
	  "audio_configurations": {
	    "live": [{"codec":"aac","track_id":0,"channels":2,"settings":{"bitrate":160}}],
	    "vod":  [{"codec":"aac","track_id":1,"channels":2,"settings":{"bitrate":160}}]
	  }
	}`
}

// refusedBody is the shape EVERY measured refusal came back in: HTTP 200, a
// status object saying error, and empty ladders. A refusal is the normal path
// on a host Twitch does not accept, not an exception.
const refusedBody = `{
  "meta": {"service":"IVS","schema_version":"2025-01-25","config_id":"c0ffee"},
  "status": {"result":"error",
             "html_en_us":"Your GPU is not currently supported by Twitch Enhanced Broadcasting"},
  "ingest_endpoints": [],
  "encoder_configurations": [],
  "audio_configurations": {"live": [], "vod": []}
}`

// declaredGPU is the operator's settings block, filled in. Without it
// multitrack.Negotiate short-circuits before any network call at all, so every
// test below that expects a request has to supply one -- which is itself the
// design under test: absence is the quiet default.
func declaredGPU() db.MultitrackSettings {
	return db.MultitrackSettings{GPUs: []db.MultitrackGPU{{
		Model: "NVIDIA GeForce RTX 4070", VendorID: db.PCIVendorNVIDIA,
		DeviceID: 9988, DedicatedVideoMemory: 12 << 30, DriverVersion: "550.54.14",
	}}}
}

// twitchRow is a Twitch RTMP destination with Enhanced Broadcasting switched on.
func twitchRow() *db.Destination {
	return &db.Destination{
		ID: 41, Name: "twitch", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
		URL: storedIngest, StreamKey: operatorTypedKey, Enabled: true,
		AudioBitrate: 160, Multitrack: true,
		Profile: routing.Profile{SampleRate: 48000},
	}
}

// mtEngine is an engine whose negotiation goes to a local server, with a
// declared GPU and a known picture size so the ask is a real one.
//
// requests counts what the far end actually received, which is how "once per
// start, not once per reconcile" is asserted without reading any log.
func mtEngine(t *testing.T, status int, body string) (*Engine, *atomic.Int64) {
	t.Helper()
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	e, _ := storeEngine(t)
	e.mtClient = &multitrack.Client{BaseURL: srv.URL}
	e.mu.Lock()
	e.settings.Multitrack = declaredGPU()
	e.videoInfo = &ffmpeg.VideoStream{Codec: "h264", Width: 1920, Height: 1080, FrameRate: 30}
	e.mu.Unlock()
	return e, &requests
}

// startOne runs the real start path and hands back the published destination.
func startOne(t *testing.T, e *Engine, p destPlan) *destination {
	t.Helper()
	if err := e.startDest(p, e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	e.mu.Lock()
	d := e.dests[p.row.ID]
	e.mu.Unlock()
	if d == nil || d.proc == nil {
		t.Fatal("the destination was not published, so nothing is publishing to the platform " +
			"at all -- a negotiation must never be able to do that")
	}
	return d
}

func argvOf(d *destination) string { return strings.Join(d.proc.Args(), " ") }

// A SUCCESSFUL NEGOTIATION PUBLISHES TO THE MINTED TARGET, NOT THE STORED ONE.
//
// This is the assertion the whole feature turns on and the one it is easiest to
// ship without. Twitch answers a successful negotiation with a 312-character
// stream key that carries the agreed ladder SIGNED INSIDE IT, ending with the
// operator's own. Publishing to the negotiated host with the operator's own key
// WOULD CONNECT -- which is what makes it dangerous rather than merely wrong --
// and would send a ladder the ingest never agreed to. So "it went live and the
// card is green" is not evidence of anything here; the argv is.
//
// MUTATION: in startDest, delete the `if mt.Use { target = mt.Target }` block.
// Observed: FAIL, "published to the stored ingest". Restored with `command cp
// -f` from /tmp; `git diff --stat` clean.
// MUTATION: in negotiateDestination, set d.Target from row.Target() instead of
// out.Target. Observed: FAIL on the same assertion.
// MUTATION: in negotiateDestination, compose the target from out.Target.URL and
// row.StreamKey -- the plausible-looking half-fix. Observed: FAIL, "the
// operator's own key reached the wire".
func TestASuccessfulNegotiationPublishesToTheMintedTarget(t *testing.T) {
	e, requests := mtEngine(t, http.StatusOK, negotiatedBody())
	row := twitchRow()
	d := startOne(t, e, destPlan{row: row, spec: "spec"})
	argv := argvOf(d)

	if requests.Load() != 1 {
		t.Fatalf("the far end saw %d requests, want 1", requests.Load())
	}
	if !d.multitrack.Use {
		t.Fatalf("the negotiation was not taken up: verdict %q note %q",
			d.multitrack.Verdict, d.multitrack.Note)
	}
	// RTMPS, not the RTMP entry listed first: the stream key travels in the
	// RTMP connect as the stream name, so the cleartext endpoint would put this
	// credential on the wire unencrypted.
	if !strings.Contains(argv, negotiatedHost+"/"+mintedFromTwitch) {
		t.Errorf("did not publish to the negotiated RTMPS ingest with the minted key.\nargv: %s", argv)
	}
	if !strings.Contains(argv, "clientConfigId="+negotiatedConfigID) {
		t.Errorf("the clientConfigId is missing, so the ingest cannot match this publish to the "+
			"configuration it just issued.\nargv: %s", argv)
	}
	if strings.Contains(argv, storedIngest+"/"+operatorTypedKey) {
		t.Errorf("published to the stored ingest, so the negotiation was decoration.\nargv: %s", argv)
	}
	// The specific half-fix: negotiated host, operator's key. It connects, and
	// it sends a ladder the ingest never agreed to.
	if strings.Contains(argv, negotiatedHost+"/"+operatorTypedKey) {
		t.Errorf("the operator's own key reached the wire on the negotiated ingest, which would "+
			"connect and publish a ladder Twitch never agreed to.\nargv: %s", argv)
	}
}

// leakyChild is a stand-in FFmpeg that prints its own stream key to stderr,
// which is not a hypothetical: #310 is a destination that wrote its key to
// server.log on every retry, and internal/engine/secrets.go records a measured
// run where FFmpeg printed a 56-byte prefix of a key back on stderr.
//
// It prints the LAST PATH SEGMENT of its output target rather than the whole
// URL, and that is the load-bearing detail. alerts.Redact recognises an RTMP
// URL and masks its last segment on sight, so a line that still looks like a
// URL is scrubbed whether or not anything was registered as a secret -- a test
// built on one cannot fail. A bare token is the shape Redact has no rule for,
// so it is the shape where Spec.Secrets is the only protection there is.
// IT RE-EXECUTES THE TEST BINARY rather than writing a shell script, and that
// is not a style preference. The first version wrote a `#!/bin/sh` stand-in,
// which cannot run on Windows: `test: windows-latest` failed with "the stand-in
// ffmpeg never logged a line containing ...", because the child never started
// at all. Every other fake child in this package and in internal/supervisor
// already re-executes the test binary behind a flag for exactly this reason, so
// the portable mechanism was there to be used.
func leakyChild(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary to re-execute as a stand-in ffmpeg: %v", err)
	}
	// supervisor never sets cmd.Env, so the child inherits what t.Setenv puts
	// here -- the same route the existing fake child uses, and the only one
	// available when the engine builds the whole argv itself.
	t.Setenv(engineFakeChildEchoLast, leakyChildPrefix)
	return self
}

// leakyChildPrefix is what the stand-in prints before the segment it echoes.
const leakyChildPrefix = "stream key rejected: "

// waitForLog polls the supervisor's ring for a captured stderr line.
func waitForLog(t *testing.T, d *destination, want string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, l := range d.proc.Logs() {
			if strings.Contains(l.Text, want) {
				return l.Text
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the stand-in ffmpeg never logged a line containing %q", want)
	return ""
}

// THE MINTED KEY IS REGISTERED FOR SCRUBBING IN FULL, BY THE WIRING.
//
// destSecrets has documented `extra` as "the Twitch Enhanced Broadcasting
// minted key" since before anything passed one, and
// TestTheMintedKeyIsMaskedWholeAndNotJustItsTail pins what destSecrets does
// with a value it is HANDED. Neither of them says the engine hands it one, and
// on the day this was written nothing did. That is the gap this closes, so it
// is asserted through the whole path: a real child process, printing the key it
// was given, read back through supervisor.Process.Logs -- which is the same
// scrubbing every log line, process.log entry and Status.LastError goes through.
//
// THE ASSERTION IS NOT "the operator's key is absent". That passes on the
// broken version, because the operator's key IS registered and IS a suffix of
// the minted one. It is that the SIGNATURE PREFIX is gone, which happens only
// if the minted value was declared in its own right. A partially redacted live
// credential reads as protection to anyone glancing at the file, which is worse
// than none: #310 and #324 were both this class.
//
// MUTATION: in startDest, `Secrets: destSecrets(row, mt.MintedKey)` ->
// `destSecrets(row)`. Observed: FAIL, "the minted key's signature survived
// scrubbing", printing the masked-tail form. Restored with `command cp -f` from
// /tmp; `diff` against the backup clean.
// MUTATION: in negotiateDestination, leave d.MintedKey empty while still
// setting d.Target. Observed: FAIL on the same assertion -- which is the point:
// publishing with a credential and not declaring it is the half-wired state.
func TestTheMintedKeyReachesTheProcessSecretSet(t *testing.T) {
	e, _ := mtEngine(t, http.StatusOK, negotiatedBody())
	e.tools.FFmpeg = leakyChild(t)
	d := startOne(t, e, destPlan{row: twitchRow(), spec: "spec"})

	line := waitForLog(t, d, "stream key rejected")
	if strings.Contains(line, mintedFromTwitch) {
		t.Fatalf("the whole minted key reached the log:\n%s", line)
	}
	if strings.Contains(line, mintedSigPrefix) {
		t.Errorf("the minted key's signature survived scrubbing, so the log carries a "+
			"partially redacted live credential:\n%s", line)
	}
	if strings.Contains(line, "fedcba9876543210") {
		t.Errorf("the signature hex is still in the log line:\n%s", line)
	}
	if strings.Contains(line, operatorTypedKey) {
		t.Errorf("the operator's own key survived scrubbing:\n%s", line)
	}
}

// The negative control, and it exists because the test above could pass for the
// wrong reason. alerts.Redact runs a residual pattern pass after the declared
// literals are removed, and if THAT were what removed the signature then
// destSecrets would be untested and the protection an accident.
//
// This asserts the gap is real: with only the destination's own key registered,
// the signature IS still there in a bare log line. If this ever starts failing,
// the protection has moved and the comment above needs rewriting rather than
// the test deleting.
//
// It also records WHY the assertion above is made on a bare token rather than
// on Process.CommandString: for a line that still looks like an RTMP URL,
// Redact masks the last path segment on sight and the mutation cannot be seen
// at all. Measured, and asserted here so the next reader does not have to
// rediscover it.
func TestTheResidualPassAloneDoesNotRemoveTheMintedSignature(t *testing.T) {
	row := twitchRow()
	set := alerts.NewSecretSet(nil, destSecrets(row)...)

	bare := set.Scrub("stream key rejected: " + mintedFromTwitch + "?clientConfigId=abc")
	if !strings.Contains(alerts.Redact(bare), mintedSigPrefix) {
		t.Fatalf("the signature was already gone without registering the minted key, so "+
			"TestTheMintedKeyReachesTheProcessSecretSet is not testing what it says:\n%s", bare)
	}

	urlish := set.Scrub("Failed to open " + negotiatedHost + "/" + mintedFromTwitch)
	if strings.Contains(alerts.Redact(urlish), mintedSigPrefix) {
		t.Error("alerts.Redact no longer masks a whole RTMP path segment, so the guard above " +
			"could be made simpler -- assert on Process.CommandString instead of spawning a child")
	}
}

// A REFUSAL FALLS BACK, PUBLISHES, AND SAYS SO ONCE.
//
// Twitch refuses any client whose GPU it does not accept, and polyemesis is
// installed on the operator's own server -- so on most installs this is the
// path every time, for ever. Three separate things have to be true of it and
// each is asserted here: the stream still goes out, it goes out to the ordinary
// ingest, and the operator is told once rather than never and once rather than
// on every reconcile.
//
// "ONCE" IS MEASURED AT THE FAR END, not by counting log lines. The negotiation
// is made in startDest, which runs once per START -- a destination that
// survives a reconcile is left strictly alone and a reconnect reuses the argv
// the supervisor already holds. Three further reconcile sweeps over the same
// plan are what proves that, and they prove it for the respawn case too, since
// a respawn never re-enters this path at all.
//
// MUTATION: add `_ = e.negotiateFor(context.Background(), p.row)` beside the
// applyDestPolicy call on startDestinations' already-running branch, which is
// what "renegotiate every sweep" would look like. Observed: FAIL, "the far end
// saw 4 requests, want 1". Restored from /tmp; `diff` clean.
// (Putting the same call INSIDE that branch's e.mu critical section deadlocks
// instead of failing -- e.Settings takes e.mu -- so the mutation is placed
// after the unlock, where a real implementation would have to be anyway.)
func TestARefusalFallsBackToTheOrdinaryIngestAndSaysSoOnce(t *testing.T) {
	e, requests := mtEngine(t, http.StatusOK, refusedBody)
	row := twitchRow()
	p := destPlan{row: row, spec: "spec"}

	d := startOne(t, e, p)
	argv := argvOf(d)
	if !strings.Contains(argv, storedIngest+"/"+operatorTypedKey) {
		t.Errorf("did not fall back to the ordinary Twitch ingest, so a refusal took the "+
			"destination off the air.\nargv: %s", argv)
	}
	if d.multitrack.Use {
		t.Error("a refused configuration was taken up")
	}
	if d.multitrack.Verdict != multitrack.Refused {
		t.Errorf("verdict %q, want %q", d.multitrack.Verdict, multitrack.Refused)
	}
	if d.multitrack.Note == "" {
		t.Error("nothing was said about the fallback, so the operator has a toggle that " +
			"silently does nothing -- which is exactly what the dialog promises it does not")
	}
	// Twitch's own sentence reaches the operator, rather than being swallowed
	// and replaced with ours. It is the only explanation of a refusal there is.
	if !strings.Contains(d.multitrack.Note, "GPU is not currently supported") {
		t.Errorf("Twitch's explanation did not reach the note: %q", d.multitrack.Note)
	}

	// Three more sweeps that leave it running.
	for range 3 {
		e.startDestinations(map[int64]destPlan{row.ID: p})
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("the far end saw %d requests, want 1: the negotiation is being re-made on "+
			"every reconcile, so the operator is told once per sweep and every broadcast pays "+
			"a round trip it does not need", got)
	}
	e.mu.Lock()
	still := e.dests[row.ID]
	e.mu.Unlock()
	if still == nil || still.multitrack.Note != d.multitrack.Note {
		t.Error("the note did not survive a reconcile that left the destination running, so " +
			"the card would go blank while the destination kept publishing")
	}
}

// A NEGOTIATION THAT FAILS OR HANGS MUST NOT FAIL OR HANG THE BROADCAST.
//
// The call is on the path between the operator pressing the button and anything
// reaching a viewer. A platform having a bad day is not a reason for a
// broadcast not to start, and it is not a reason for one to start eleven
// minutes late either.
//
// MUTATION: in startDest, drop the context.WithTimeout and pass
// context.Background(). Observed: PASS on the first two arms -- because
// multitrack.Client's own 10s timeout still fires -- and the third arm HANGS to
// the test binary's own deadline. That is why the deadline in startDest is
// described as a backstop rather than as the timeout, and why the third arm
// exists at all.
func TestANegotiationThatFailsOrHangsStillPublishes(t *testing.T) {
	t.Run("the platform returns 500", func(t *testing.T) {
		e, _ := mtEngine(t, http.StatusInternalServerError, `{"error":"nope"}`)
		d := startOne(t, e, destPlan{row: twitchRow(), spec: "spec"})
		if argv := argvOf(d); !strings.Contains(argv, storedIngest+"/"+operatorTypedKey) {
			t.Errorf("a 5xx from Twitch took the destination off the air.\nargv: %s", argv)
		}
		if d.multitrack.Use {
			t.Error("a failed negotiation was taken up")
		}
		if d.multitrack.Note == "" {
			t.Error("a failed negotiation said nothing at all")
		}
	})

	t.Run("the platform never answers", func(t *testing.T) {
		blocked := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			<-blocked
		}))
		t.Cleanup(func() { close(blocked); srv.Close() })

		e, _ := storeEngine(t)
		// The client's own timeout, shortened. The real one is 10s and is what
		// bounds this in production; a test that waited for it would be a test
		// about patience.
		e.mtClient = &multitrack.Client{BaseURL: srv.URL, HTTP: &http.Client{Timeout: 100 * time.Millisecond}}
		e.mu.Lock()
		e.settings.Multitrack = declaredGPU()
		e.videoInfo = &ffmpeg.VideoStream{Width: 1920, Height: 1080, FrameRate: 30}
		e.mu.Unlock()

		started := time.Now()
		d := startOne(t, e, destPlan{row: twitchRow(), spec: "spec"})
		if took := time.Since(started); took > 5*time.Second {
			t.Errorf("a hung platform held the broadcast open for %v", took)
		}
		if argv := argvOf(d); !strings.Contains(argv, storedIngest+"/"+operatorTypedKey) {
			t.Errorf("a hung negotiation took the destination off the air.\nargv: %s", argv)
		}
	})

	// The BACKSTOP, on its own, with the client's own timeout removed.
	//
	// This arm exists because of a mutation that did NOT fail: deleting the
	// context.WithTimeout in startDest changes nothing while multitrack.Client
	// carries its own 10s timeout, so the arm above cannot see it. This
	// repository's standing rule is that a line no test can be made to fail for
	// is not a fix (see the argument written out under pullURLSecrets), so the
	// line either goes or is tested for what it actually protects: a Client
	// whose *http.Client has no Timeout at all, which is a legal value and one
	// a future caller could easily pass.
	//
	// MUTATION: delete the context.WithTimeout from startDest and pass
	// context.Background(). Observed: this arm HANGS until the test binary's
	// own 10-minute panic. Restored from /tmp; `diff` clean.
	t.Run("the transport has no timeout of its own", func(t *testing.T) {
		blocked := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			<-blocked
		}))
		t.Cleanup(func() { close(blocked); srv.Close() })

		e, _ := storeEngine(t)
		e.mtClient = &multitrack.Client{BaseURL: srv.URL, HTTP: &http.Client{}}
		e.mu.Lock()
		e.settings.Multitrack = declaredGPU()
		e.videoInfo = &ffmpeg.VideoStream{Width: 1920, Height: 1080, FrameRate: 30}
		e.mu.Unlock()

		restore := multitrackDeadline
		multitrackDeadline = 150 * time.Millisecond
		t.Cleanup(func() { multitrackDeadline = restore })

		started := time.Now()
		d := startOne(t, e, destPlan{row: twitchRow(), spec: "spec"})
		if took := time.Since(started); took > 5*time.Second {
			t.Errorf("a transport with no timeout held the broadcast open for %v", took)
		}
		if argv := argvOf(d); !strings.Contains(argv, storedIngest+"/"+operatorTypedKey) {
			t.Errorf("not publishing to the ordinary ingest.\nargv: %s", argv)
		}
	})
}

// NOTHING IS ASKED WHEN NO GPU IS DECLARED, and the operator is not shown a
// fault for it.
//
// This is the default state of every install and it is the reason the settings
// block is declared rather than derived: with nothing declared there is nothing
// honest to send, so no round trip is spent at go-live to be told so.
//
// MUTATION: in multitrackGPUs, return one fabricated GPU when the list is
// empty. Observed: FAIL, "a request was made with no hardware declared".
func TestWithNoGPUDeclaredNothingIsAskedAndNothingIsAFault(t *testing.T) {
	e, requests := mtEngine(t, http.StatusOK, negotiatedBody())
	e.mu.Lock()
	e.settings.Multitrack = db.MultitrackSettings{}
	e.mu.Unlock()

	d := startOne(t, e, destPlan{row: twitchRow(), spec: "spec"})
	if got := requests.Load(); got != 0 {
		t.Errorf("a request was made with no hardware declared (%d), so every GPU-less install "+
			"pays a network round trip at go-live for an answer that is already known", got)
	}
	if argv := argvOf(d); !strings.Contains(argv, storedIngest+"/"+operatorTypedKey) {
		t.Errorf("not publishing to the ordinary ingest.\nargv: %s", argv)
	}
	if d.err != "" {
		t.Errorf("an undeclared GPU was recorded as a destination error (%q); an operator on a "+
			"rented server has not misconfigured anything", d.err)
	}
	if d.multitrack.Note == "" {
		t.Error("nothing was said, so the toggle is on and silently inert")
	}
}

// A DESTINATION THAT DID NOT OPT IN IS UNTOUCHED, byte for byte.
//
// The commonest destination there is. It must not negotiate, must not carry a
// note, and must publish exactly what it published before any of this existed.
//
// MUTATION: in wantsMultitrack, drop the `row.Multitrack &&` term. Observed:
// FAIL, "a destination that did not opt in negotiated".
func TestADestinationThatDidNotOptInNegotiatesNothing(t *testing.T) {
	e, requests := mtEngine(t, http.StatusOK, negotiatedBody())
	row := twitchRow()
	row.Multitrack = false

	d := startOne(t, e, destPlan{row: row, spec: "spec"})
	if got := requests.Load(); got != 0 {
		t.Errorf("a destination that did not opt in negotiated (%d requests)", got)
	}
	if d.multitrack.Asked || d.multitrack.Note != "" {
		t.Errorf("an ordinary destination carries an Enhanced Broadcasting note: %+v", d.multitrack)
	}
	if argv := argvOf(d); !strings.Contains(argv, storedIngest+"/"+operatorTypedKey) {
		t.Errorf("an ordinary destination's target moved.\nargv: %s", argv)
	}
}

// ------------------------------------------------- the second (VOD) audio track

// pairSource is an ingest with two audio tracks, so a primary and a secondary
// mix can both select something real.
func pairSource() routing.Source {
	return routing.Source{Tracks: []routing.Track{
		{Index: 0, Channels: 2, Codec: "aac"},
		{Index: 1, Channels: 2, Codec: "aac"},
	}}
}

func mixProfile(track int) routing.Profile {
	return routing.Profile{
		Mode:       routing.ModeSimple,
		Tracks:     []routing.TrackSel{{Track: track, Enabled: true, Gain: 1}},
		Normalize:  routing.NormOff,
		SampleRate: 48000,
	}
}

// mapsASecondMix reports whether this argv actually MAPS a second audio mix,
// as opposed to merely containing one in the filter graph.
//
// The distinction is the whole assertion. A graph carrying the secondary
// namespace but no -map for it is not two tracks on the wire -- and FFmpeg
// refuses a filter_complex output nothing maps, so a gate implemented by
// clearing the label alone would produce a destination that will not start
// rather than one that sends one track. Only the -map is looked at.
func mapsASecondMix(argv []string) bool {
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "-map" && strings.HasPrefix(argv[i+1], "["+routing.SecondaryPrefix) {
			return true
		}
	}
	return false
}

// A VOD MIX ON A TWITCH DESTINATION THAT DID NOT OPT IN IS NOT SENT, AND IS
// EXPLAINED.
//
// The ordinary Twitch RTMP ingest carries ONE audio track --
// db.AudioEncoding.copyProblems says so in as many words and refuses a copied
// multitrack RTMP destination for that reason. The engine nonetheless compiled
// the pair on row.VODProfile != nil alone and never read row.Multitrack, so
// this destination pushed TWO tracks at that ingest, silently.
// db.Destination.VODProfile's own comment claimed "the engine reports it" and
// nothing did.
//
// Decided at PLAN time, because it does not depend on anything a network call
// could say.
//
// MUTATION: in planDestinations, delete the
// `case twitchOneAudioTrack(row) && !wantsMultitrack(row)` arm. Observed: FAIL,
// "a second audio mix is mapped". Restored from /tmp; `git diff` clean.
// MUTATION: keep the arm but leave p.vodDropped empty. Observed: FAIL, "nothing
// explains why".
func TestAVODMixOnANonMultitrackTwitchDestinationIsNotSent(t *testing.T) {
	e, _ := storeEngine(t)
	row := twitchRow()
	row.Multitrack = false
	row.Profile = mixProfile(0)
	vod := mixProfile(1)
	row.VODProfile = &vod

	plans := e.planDestinations([]*db.Destination{row}, nil, pairSource(), "", false)
	p := plans[row.ID]
	if p.err != "" {
		t.Fatalf("the destination will not run at all: %s", p.err)
	}
	argv := e.destArgs(row, p.compiled, "udp://127.0.0.1:1", row.Target())
	if mapsASecondMix(argv) {
		t.Errorf("a second audio mix is mapped to Twitch's ordinary RTMP ingest, which carries "+
			"one audio track.\nargv: %s", strings.Join(argv, " "))
	}
	if p.vodDropped == "" {
		t.Error("nothing explains why the configured second mix is not being sent, so the " +
			"setting silently undoes itself -- which is the state db.Destination.VODProfile's " +
			"comment promised the engine would not leave it in")
	}
	if !strings.Contains(p.vodDropped, "Enhanced Broadcasting") {
		t.Errorf("the explanation does not name the fix: %q", p.vodDropped)
	}
}

// A VOD MIX ON A TWITCH DESTINATION WHOSE NEGOTIATION FAILED IS NOT SENT
// EITHER.
//
// The case the plan cannot decide, and the one that matters most: the operator
// DID everything right -- toggle on, second mix configured -- and Twitch said
// no, which is the ordinary answer on a server with no GPU. Publishing the pair
// anyway is the same silent two-track push wearing a green card.
//
// MUTATION: in startDest, delete the `if p.oneMix != nil && !mt.Use` block.
// Observed: FAIL, "a second audio mix is mapped". Restored from /tmp; `git
// diff` clean.
// MUTATION: replace the block's body with `compiled.SecondOutLabel = ""` -- the
// obvious one-line version. Observed: PASS on the -map assertion and FAIL on
// the filter-graph one below, which is why that assertion is here: FFmpeg
// refuses a filter_complex output nothing maps, so that mutation trades a
// silent two-track push for a destination that will not start at all.
func TestAVODMixIsNotSentWhenTheNegotiationDoesNotSucceed(t *testing.T) {
	e, _ := mtEngine(t, http.StatusOK, refusedBody)
	row := twitchRow()
	row.Profile = mixProfile(0)
	vod := mixProfile(1)
	row.VODProfile = &vod

	plans := e.planDestinations([]*db.Destination{row}, nil, pairSource(), "", false)
	p := plans[row.ID]
	if p.err != "" {
		t.Fatalf("the destination will not run at all: %s", p.err)
	}
	// The plan DOES compile the pair here -- the answer was not knowable yet --
	// and keeps the single-mix graph beside it. Both halves are asserted, or a
	// gate that simply never compiles the pair would pass this test and break
	// the feature for the machines it works on.
	if p.oneMix == nil {
		t.Fatal("the plan kept no single-mix fallback, so a refused negotiation has nothing to " +
			"fall back to")
	}
	if !mapsASecondMix(e.destArgs(row, p.compiled, "udp://127.0.0.1:1", row.Target())) {
		t.Fatal("the plan did not compile the pair at all, so this destination could never send " +
			"a VOD track even where Twitch grants one")
	}

	d := startOne(t, e, p)
	argv := d.proc.Args()
	if mapsASecondMix(argv) {
		t.Errorf("a second audio mix is mapped after a refused negotiation.\nargv: %s",
			strings.Join(argv, " "))
	}
	// The graph on the command line must not name the secondary mix either.
	// Clearing the label while leaving the graph is the plausible one-line
	// version of this gate, and FFmpeg refuses a filter_complex output that
	// nothing maps -- so it converts a silent defect into a dead destination.
	if strings.Contains(strings.Join(argv, " "), routing.SecondaryPrefix) {
		t.Errorf("the secondary mix is still in the filter graph with nothing mapping it, which "+
			"FFmpeg refuses outright.\nargv: %s", strings.Join(argv, " "))
	}
	if d.vodDropped == "" {
		t.Error("nothing explains why the second mix is not being sent")
	}
}

// AN UNPROBED INGEST DROPS THE SECOND MIX, AND SAYS SO.
//
// The provisional path drops the pair on EVERY platform -- a provisional
// compile already runs on a guessed channel layout and a second guessed mix on
// top of it doubles what is approximate -- and that is deliberate. What was
// wrong is that it was the only one of the three drops that set no vodDropped,
// so it reached neither the destination card nor the log. It is also the drop
// an operator is least able to work out for themselves, because unlike the
// other two it is not caused by anything they configured.
//
// Non-Twitch on purpose: the sibling test above asserts a non-Twitch
// destination is never told its second mix was dropped, and this is the one
// case where it must be. A Twitch row here could pass on the Twitch arm alone.
//
// MUTATION: fold the `case provisional` arm back into the one above it, so it
// drops the mix and sets nothing. Observed: FAIL, "nothing explains why".
func TestAVODMixOnAnUnprobedIngestIsNotSentAndSaysSo(t *testing.T) {
	e, _ := storeEngine(t)
	row := twitchRow()
	row.Platform = db.PlatformYouTube
	row.Multitrack = false
	row.URL = "rtmp://a.rtmp.youtube.com/live2"
	row.Profile = mixProfile(0)
	vod := mixProfile(1)
	row.VODProfile = &vod

	// The last argument is `provisional`.
	plans := e.planDestinations([]*db.Destination{row}, nil, pairSource(), "", true)
	p := plans[row.ID]
	if p.err != "" {
		t.Fatalf("the destination will not run at all: %s", p.err)
	}
	if mapsASecondMix(e.destArgs(row, p.compiled, "udp://127.0.0.1:1", row.Target())) {
		t.Error("a second mix was compiled on a guessed channel layout, which is the thing the " +
			"provisional path exists not to do")
	}
	if p.vodDropped == "" {
		t.Fatal("nothing explains why the second mix is not being sent, so an operator watching " +
			"a configured mix fail to appear has nothing to read -- on the one drop that is not " +
			"their configuration's fault")
	}
	// It must not read as the Twitch one. "Switch on Enhanced Broadcasting" is
	// advice that does nothing here, and this destination is not even on Twitch.
	if strings.Contains(p.vodDropped, "Enhanced Broadcasting") {
		t.Errorf("the explanation offers the Twitch fix for a problem that is not Twitch's and "+
			"not the operator's: %q", p.vodDropped)
	}
	if !strings.Contains(p.vodDropped, "probed") {
		t.Errorf("the explanation does not name the cause: %q", p.vodDropped)
	}
}

// AND THE GENERIC TWO-MIX EGRESS IS UNTOUCHED.
//
// The regression guard. routing.CompilePair, ffmpeg.SecondAudioOutLabel and the
// -map that joins them are correct and were measured arriving as two distinct
// tracks through this project's own RTMP ingest. The gate above is about TWITCH
// -- a platform whose ordinary ingest takes one track -- and it must not become
// a rule about second audio tracks in general.
//
// MUTATION: in twitchOneAudioTrack, drop the `row.Platform == db.PlatformTwitch`
// term. Observed: FAIL on both arms, "the second audio mix was dropped".
func TestAVODMixOnANonTwitchDestinationIsStillSent(t *testing.T) {
	for _, platform := range []db.Platform{db.PlatformYouTube, db.PlatformCustom} {
		t.Run(string(platform), func(t *testing.T) {
			e, _ := storeEngine(t)
			row := twitchRow()
			row.Platform = platform
			row.Multitrack = false
			row.URL = "rtmp://a.rtmp.youtube.com/live2"
			row.Profile = mixProfile(0)
			vod := mixProfile(1)
			row.VODProfile = &vod

			plans := e.planDestinations([]*db.Destination{row}, nil, pairSource(), "", false)
			p := plans[row.ID]
			if p.err != "" {
				t.Fatalf("the destination will not run at all: %s", p.err)
			}
			if p.oneMix != nil {
				t.Error("a non-Twitch destination was given a single-mix fallback, so its second " +
					"track is conditional on a negotiation it never makes")
			}
			if p.vodDropped != "" {
				t.Errorf("a non-Twitch destination was told its second mix is dropped: %q", p.vodDropped)
			}
			argv := e.destArgs(row, p.compiled, "udp://127.0.0.1:1", row.Target())
			if !mapsASecondMix(argv) {
				t.Errorf("the second audio mix was dropped from a destination that is not Twitch "+
					"and never asked Twitch anything.\nargv: %s", strings.Join(argv, " "))
			}
		})
	}
}

// THE REDUNDANT FEED NEVER CARRIES THE SECOND TRACK ON A TWITCH DESTINATION.
//
// A negotiated Enhanced Broadcasting configuration names ONE ingest endpoint.
// The backup publishes to the operator's own BackupURL, which is an ordinary
// one-track RTMP ingest whatever the negotiation said -- so a primary that won
// a second track must still send one track on its backup, or the redundant feed
// is the same silent two-track push on exactly the feed that takes over when
// the primary drops.
//
// Driven through startDestinations rather than by calling backupCompiled, and
// that is not ceremony. The first draft asserted on backupCompiled's return
// value directly; the mutation that matters -- passing p.compiled at the CALL
// SITE, which is where the defect would actually live -- left that assertion
// passing. A helper that is right and unused is the same defect wearing a
// green tick.
//
// MUTATION: in startDestinations, pass p.compiled to reconcileBackup instead of
// backupCompiled(p), on the fresh-start branch. Observed: FAIL, "the backup
// feed carries a second audio mix". Restored from /tmp; `diff` clean.
// MUTATION: make backupCompiled return p.compiled unconditionally. Observed:
// FAIL on the same assertion.
func TestTheBackupFeedOfATwitchDestinationCarriesOneAudioTrack(t *testing.T) {
	e, _ := mtEngine(t, http.StatusOK, refusedBody)
	row := twitchRow()
	row.Profile = mixProfile(0)
	vod := mixProfile(1)
	row.VODProfile = &vod
	row.BackupURL = "rtmp://backup.twitch.example/app"
	row.BackupStreamKey = "backup-key"
	row.BackupIngestWanted = true

	plans := e.planDestinations([]*db.Destination{row}, nil, pairSource(), "", false)
	if p := plans[row.ID]; p.oneMix == nil {
		t.Fatal("no single-mix graph was kept, so there is nothing for this to assert about")
	}
	e.startDestinations(plans)

	e.mu.Lock()
	d := e.dests[row.ID]
	e.mu.Unlock()
	if d == nil || d.backup == nil {
		t.Fatalf("no redundant feed was started: %v", d)
	}
	if mapsASecondMix(d.backup.Args()) {
		t.Errorf("the backup feed carries a second audio mix to an ordinary one-track ingest.\n"+
			"argv: %s", strings.Join(d.backup.Args(), " "))
	}
	// And the primary is the one-mix graph too, or this test would pass on a
	// build where the whole pair was simply never compiled.
	if mapsASecondMix(d.proc.Args()) {
		t.Error("the primary carries a second audio mix after a refused negotiation")
	}
}

// ------------------------------------------------------------------ the ask

// WHAT GOES OUT IS WHAT THE OPERATOR CONFIGURED, and nothing is invented.
//
// multitrack.Ask's doc records the measurement this rests on: Twitch DERIVES
// the returned ladder from the canvas the client says it is producing, so an
// operator who picked 720p gets a 720p negotiation by virtue of being asked.
// A request that sent a fixed 1080p canvas would negotiate a ladder for a
// picture this destination does not send, and Reconcile would then report a
// divergence about a number nobody chose.
//
// MUTATION: in multitrackCanvas, ignore the rendition and use the ingest's size
// always. Observed: FAIL, "canvas 1920x1080, want the rendition's 1280x720".
// MUTATION: in multitrackAsk, hardcode VODAudio true. Observed: FAIL, "asked
// for a VOD audio track for a destination with no second mix".
func TestTheRequestDescribesThisDestinationAndNotAGuess(t *testing.T) {
	var body atomic.Pointer[multitrack.Request]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req multitrack.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		body.Store(&req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(refusedBody))
	}))
	t.Cleanup(srv.Close)

	e, store := storeEngine(t)
	e.mtClient = &multitrack.Client{BaseURL: srv.URL}
	e.mu.Lock()
	e.settings.Multitrack = declaredGPU()
	e.videoInfo = &ffmpeg.VideoStream{Width: 1920, Height: 1080, FrameRate: 30}
	e.mu.Unlock()

	rend, err := store.CreateRendition(&db.Rendition{
		Name: "720p", Width: 1280, Height: 720, FPS: 60, VideoBitrate: 4500,
		Encoder: db.EncoderX264, Preset: "veryfast", GOPSeconds: 2,
	})
	if err != nil {
		t.Fatalf("CreateRendition: %v", err)
	}
	row := twitchRow()
	row.RenditionID = &rend.ID

	startOne(t, e, destPlan{row: row, spec: "spec"})

	req := body.Load()
	if req == nil {
		t.Fatal("no request reached the far end")
	}
	if len(req.Preferences.Canvases) != 1 {
		t.Fatalf("sent %d canvases, want 1 -- Twitch was measured refusing a request with none",
			len(req.Preferences.Canvases))
	}
	c := req.Preferences.Canvases[0]
	if c.Width != 1280 || c.Height != 720 {
		t.Errorf("canvas %dx%d, want the rendition's 1280x720: the ladder Twitch returns is "+
			"derived from this, so asking about a picture this destination does not send "+
			"negotiates a configuration it cannot honour", c.Width, c.Height)
	}
	if c.Framerate.Numerator != 60 || c.Framerate.Denominator != 1 {
		t.Errorf("framerate %d/%d, want the rendition's 60/1",
			c.Framerate.Numerator, c.Framerate.Denominator)
	}
	if req.Preferences.VODTrackAudio {
		t.Error("asked for a VOD audio track for a destination with no second mix; Twitch would " +
			"return a track nothing feeds and wait for it")
	}
	if req.Client.Name != multitrack.ClientName {
		t.Errorf("client name %q, want %q -- Twitch quotes this back to the operator",
			req.Client.Name, multitrack.ClientName)
	}
	if len(req.Capabilities.GPU) != 1 || req.Capabilities.GPU[0].VendorID != db.PCIVendorNVIDIA {
		t.Errorf("the declared GPU did not reach the request: %+v", req.Capabilities.GPU)
	}
	// The stream key IS the authentication field. Asserted because sending the
	// wrong thing here -- an OAuth token, which is what the issue expected --
	// produces a refusal that looks like a hardware problem.
	if req.Authentication != operatorTypedKey {
		t.Errorf("authentication %q, want the destination's stream key", req.Authentication)
	}
}

// A DESTINATION WHOSE PICTURE SIZE IS NOT KNOWN ASKS NOTHING.
//
// Before the ingest has been probed there is no honest canvas to send, and
// Twitch was measured refusing a request with no canvases. Sending a 0x0 one
// would be a statement about the composition that is not true.
//
// MUTATION: in negotiateDestination, delete the zero-canvas guard. Observed:
// FAIL, "a request was made with no known picture size".
func TestADestinationWithNoKnownPictureSizeAsksNothing(t *testing.T) {
	e, requests := mtEngine(t, http.StatusOK, negotiatedBody())
	e.mu.Lock()
	e.videoInfo = nil
	e.mu.Unlock()

	d := startOne(t, e, destPlan{row: twitchRow(), spec: "spec"})
	if got := requests.Load(); got != 0 {
		t.Errorf("a request was made with no known picture size (%d)", got)
	}
	if argv := argvOf(d); !strings.Contains(argv, storedIngest+"/"+operatorTypedKey) {
		t.Errorf("not publishing to the ordinary ingest.\nargv: %s", argv)
	}
	if d.multitrack.Note == "" {
		t.Error("nothing was said about why nothing was asked")
	}
}
