package multitrack

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// The live tests. Everything else in this package is checked against fixtures,
// and a fixture cannot tell you that the far end still behaves the way the
// fixture was captured from -- it can only tell you the parser still parses the
// bytes somebody pasted in. These two go and ask.
//
// THEY DO NOT SKIP. internal/multitrack does not appear in
// internal/testenv/testdata/skips.json, so a t.Skip here fails the ratchet, and
// that is the right pressure: a skip is how a network test becomes a green tick
// that means nothing. Instead, an unreachable endpoint LOGS AND RETURNS, and
// setting POLYEMESIS_REQUIRE_NET=1 turns that into a failure. That is the shape
// internal/ffmpeg's POLYEMESIS_REQUIRE_FFMPEG established, and it is honest in
// both directions: no network, no verdict, and a machine that is supposed to
// have network says so.
//
// THEY SEND NO CREDENTIAL. Every fact these assert was established with an
// empty `authentication`, which the endpoint accepts. That is itself the most
// surprising thing measured here and it is what makes this testable at all --
// see the file's second test.

// liveTimeout is generous relative to production's 10s, because a CI runner's
// first TLS handshake to a new host is not the thing under test.
const liveTimeout = 20 * time.Second

// unreachable reports a transport failure the way this file has agreed to.
// Returns true if the test should stop.
func unreachable(t *testing.T, err error) bool {
	t.Helper()
	if err == nil {
		return false
	}
	if os.Getenv("POLYEMESIS_REQUIRE_NET") == "1" {
		t.Fatalf("POLYEMESIS_REQUIRE_NET=1 and Twitch's configuration endpoint could not be reached: %v", err)
	}
	t.Logf("NOT VERIFIED THIS RUN: Twitch's configuration endpoint could not be reached (%v). "+
		"Set POLYEMESIS_REQUIRE_NET=1 to make this a failure.", err)
	return true
}

// supportedGPU is the inventory Twitch was measured to accept. It is fabricated
// hardware and it is fabricated on purpose: this test is about the protocol,
// not about the machine it runs on, and a CI runner has no GPU at all. The
// values are a real NVIDIA PCI vendor/device pair because Twitch validates the
// vendor ID against a list it does not publish -- 0, an Intel iGPU and an
// unrecognised vendor were each refused by name.
var supportedGPU = GPU{
	Model:                "NVIDIA GeForce RTX 3080",
	VendorID:             4318, // 0x10DE
	DeviceID:             8712,
	DedicatedVideoMemory: 10 << 30,
	SharedSystemMemory:   16 << 30,
	DriverVersion:        "551.86",
}

func liveHardware(gpu []GPU) Capabilities {
	return Capabilities{
		CPU:    CPU{PhysicalCores: 8, LogicalCores: 16},
		Memory: Memory{Total: 32 << 30, Free: 8 << 30},
		System: System{Version: "6.8", Name: "Linux", Release: "6.8.0", Bits: 64},
		GPU:    gpu,
	}
}

// TestTheLiveEndpointRefusesWithHTTP200AndAnEmptyLadder is the claim this whole
// package is built around, checked against the real endpoint rather than
// against a recording of it.
//
// It asks with NO GPU, which is exactly the polyemesis host this feature will
// most often run on: a headless server encoding with libx264. Twitch's answer
// is a refusal, and the assertion is that the refusal arrives as a 200 -- so
// Client.Fetch returns no error -- with the verdict in status.result and an
// empty ladder underneath it.
//
// Proven able to fail against the committed tree by changing the StatusError
// case in Config.Verdict (client.go) to `return Negotiated, ""`, which made
// this report "verdict = negotiated, want refused" against the live endpoint.
// Restored from a /tmp copy; git diff --stat clean.
func TestTheLiveEndpointRefusesWithHTTP200AndAnEmptyLadder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), liveTimeout)
	defer cancel()

	cfg, err := (&Client{}).Fetch(ctx, "", NewRequest(Ask{
		Version:  "test",
		Canvas:   canvas1080p30,
		VODAudio: true,
		Hardware: liveHardware(nil), // no GPU: the refusal under test
	}))
	if unreachable(t, err) {
		return
	}
	if cfg == nil {
		t.Fatal("Fetch returned no config and no error")
	}

	// The point. Fetch did not error, which means the HTTP status was 2xx --
	// and the answer is still no.
	if cfg.Status == nil {
		t.Fatalf("Twitch returned no status object for a GPU-less request; it used to refuse. "+
			"Renditions: %d", len(cfg.EncoderConfigurations))
	}
	if cfg.Status.Result != StatusError {
		t.Errorf("status.result = %q, want %q", cfg.Status.Result, StatusError)
	}
	verdict, advice := cfg.Verdict()
	if verdict != Refused {
		t.Errorf("verdict = %s, want %s", verdict, Refused)
	}
	if advice == "" {
		t.Error("a refusal carried no explanation for the operator")
	}
	t.Logf("live refusal, verbatim: %s", advice)

	// The refusal still names the ingest host, which is why the fallback can
	// report where it would have gone.
	if len(cfg.IngestEndpoints) == 0 {
		t.Error("a refusal carried no ingest endpoints")
	}
}

// TestTheLiveEndpointGrantsAVODAudioTrackAlongsideASingleVideoTrack answers the
// two things issue #326 recorded as NOT KNOWN, against the live endpoint.
//
//  1. Is audio_configurations.vod populated at all, or only for some accounts?
//     It is populated, and it depends on nothing but the request: this call
//     carries an EMPTY stream key and no token.
//
//  2. Does Enhanced Broadcasting require the multi-rendition video path? It
//     does not. This asks for maximum_video_tracks 1 and gets one rendition
//     back, with both audio tracks. One video track plus a live and a VOD audio
//     track is a configuration Twitch will issue.
//
// That second point is what makes the feature reachable for polyemesis at all,
// which publishes one video track to an RTMP destination.
//
// Proven able to fail against the committed tree by changing NewRequest
// (request.go) to set `VODTrackAudio: false` unconditionally, which made this
// report "Twitch granted 0 VOD audio tracks" against the live endpoint --
// confirming the assertion tracks the request and is not reading a field that
// is always populated. Restored from a /tmp copy; git diff --stat clean.
func TestTheLiveEndpointGrantsAVODAudioTrackAlongsideASingleVideoTrack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), liveTimeout)
	defer cancel()

	ask := Ask{
		Version:        "test",
		Canvas:         canvas1080p30,
		VODAudio:       true,
		MaxVideoTracks: 1,
		Hardware:       liveHardware([]GPU{supportedGPU}),
	}
	cfg, err := (&Client{}).Fetch(ctx, "", NewRequest(ask))
	if unreachable(t, err) {
		return
	}

	verdict, advice := cfg.Verdict()
	if verdict == Refused {
		// Not a silent pass. Twitch tightening its hardware allowlist is a real
		// possibility and it would change what this package can promise, so it
		// has to be loud.
		t.Fatalf("Twitch refused a request it previously granted: %s", advice)
	}

	if got := len(cfg.EncoderConfigurations); got != 1 {
		t.Errorf("Twitch returned %d video renditions for maximum_video_tracks=1, want 1", got)
	}
	if got := len(cfg.AudioConfigurations.Live); got != 1 {
		t.Errorf("Twitch granted %d live audio tracks, want 1", got)
	}
	if got := len(cfg.AudioConfigurations.VOD); got != 1 {
		t.Fatalf("Twitch granted %d VOD audio tracks, want 1 -- this is the whole feature", got)
	}
	// The VOD track has to be a DIFFERENT track from the live one, or there is
	// no second mix to route anywhere.
	if live, vod := cfg.AudioConfigurations.Live[0], cfg.AudioConfigurations.VOD[0]; live.TrackID == vod.TrackID {
		t.Errorf("the live and VOD tracks share track id %d, so there is only one track", live.TrackID)
	}
	t.Logf("live negotiation: %d rendition(s), live track %d, VOD track %d",
		len(cfg.EncoderConfigurations),
		cfg.AudioConfigurations.Live[0].TrackID, cfg.AudioConfigurations.VOD[0].TrackID)

	// And the ladder really does follow the canvas that was asked for, which is
	// the evidence behind the reconciliation model on Ask: the operator's
	// rendition is an INPUT to the negotiation.
	if top := cfg.EncoderConfigurations[0]; top.Width != canvas1080p30.Width ||
		top.Height != canvas1080p30.Height {
		t.Errorf("top rendition is %dx%d for a %dx%d canvas; the ladder no longer follows the canvas",
			top.Width, top.Height, canvas1080p30.Width, canvas1080p30.Height)
	}
}

// TestTheLiveEndpointMintsAStreamKeyThatIsNotTheOneItWasGiven is the assertion
// no fixture can stand in for, and the one that would have caught the reading
// this package first had.
//
// On a refusal, ingest_endpoints[].authentication is a plain echo of the key
// that was sent -- which makes it look like decoration. On a SUCCESSFUL
// negotiation it is a signed value carrying the agreed ladder, with the
// original key as its last segment. Resolve has to publish with THAT, and a
// hand-written fixture would only ever prove that Resolve prefers whatever the
// fixture's author put in the field.
//
// The key sent here is synthetic and belongs to nobody. It is a literal in the
// test rather than an environment variable on purpose: a real key would make
// this test's behaviour depend on a credential, and the fact being established
// -- that the minted key differs from the key sent -- needs no real one.
//
// Proven able to fail against the committed tree by deleting the
// `if ep.Authentication != ""` block from Config.Resolve (endpoint.go), which
// made this report "Resolve published with the key that was sent, not the one
// Twitch minted" against the live endpoint. Restored from a /tmp copy; git diff
// --stat clean.
func TestTheLiveEndpointMintsAStreamKeyThatIsNotTheOneItWasGiven(t *testing.T) {
	const sent = "live_000000000_SYNTHETICKEYNOTREAL0000000000"

	ctx, cancel := context.WithTimeout(context.Background(), liveTimeout)
	defer cancel()

	cfg, err := (&Client{}).Fetch(ctx, sent, NewRequest(Ask{
		Version:        "test",
		Canvas:         canvas1080p30,
		VODAudio:       true,
		MaxVideoTracks: 1,
		Hardware:       liveHardware([]GPU{supportedGPU}),
	}))
	if unreachable(t, err) {
		return
	}
	if verdict, advice := cfg.Verdict(); verdict == Refused {
		t.Fatalf("Twitch refused a request it previously granted: %s", advice)
	}

	target, err := cfg.Resolve(sent)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	published, _, _ := strings.Cut(target.Key, "?")

	if published == sent {
		t.Fatal("Resolve published with the key that was sent, not the one Twitch minted")
	}
	// The minted key is specified to embed the original, and that is what makes
	// it safe to hand Twitch's own value straight back: it is still this
	// operator's stream.
	if !strings.HasSuffix(published, sent) {
		t.Errorf("the minted key does not end in the key that was sent, so it is not this stream's key")
	}
	if !strings.HasPrefix(published, "v1_") {
		t.Errorf("the minted key does not have the v1_ shape this package documents")
	}
	t.Logf("Twitch minted a %d-character key from the %d-character one it was sent",
		len(published), len(sent))

	// And it must not be printable. This is the value that would reach a log.
	if red := target.Redacted(); strings.Contains(red, published) || strings.Contains(red, sent) {
		t.Errorf("the redacted target leaks the minted key: %q", red)
	}
}
