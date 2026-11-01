package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/captions"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// TestCaptionsFollowOnlyEnabledDestinationsWhoseModeTheirPlatformAccepts
//
// The captioner is wanted by exactly the destinations that are enabled and
// captioned. A disabled destination, and an uncaptioned one, must not switch the
// recogniser on. (The platform check is enforced by the database validation at
// save time, so a mode the platform refuses cannot reach this code.) The delay each captioned destination runs with is
// carried along so the route can hold its cues back.
func TestCaptionsFollowOnlyEnabledDestinationsWhoseModeTheirPlatformAccepts(t *testing.T) {
	e := failoverEngine(t)
	twitch, err := e.store.CreateDestination(&db.Destination{
		Name: "twitch-cc", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
		URL: "rtmp://127.0.0.1:1/rtmp", StreamKey: "key", Enabled: true,
		AudioBitrate: 128, Profile: routing.DefaultProfile(),
		CaptionMode: "sei", DelaySeconds: 0,
	})
	if err != nil {
		t.Fatalf("create captioned twitch destination: %v", err)
	}
	e.sourceID = *twitch.SourceID
	if _, err := e.store.CreateDestination(&db.Destination{
		Name: "twitch-off", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
		URL: "rtmp://127.0.0.1:1/rtmp", StreamKey: "key", Enabled: false,
		AudioBitrate: 128, Profile: routing.DefaultProfile(),
		CaptionMode: "sei", SourceID: twitch.SourceID,
	}); err != nil {
		t.Fatalf("create disabled captioned destination: %v", err)
	}
	if _, err := e.store.CreateDestination(&db.Destination{
		Name: "custom-plain", Kind: db.DestRTMP, Platform: db.PlatformCustom,
		URL: "rtmp://127.0.0.1:1/rtmp", StreamKey: "key", Enabled: true,
		AudioBitrate: 128, Profile: routing.DefaultProfile(),
		CaptionMode: "", SourceID: twitch.SourceID,
	}); err != nil {
		t.Fatalf("create uncaptioned destination: %v", err)
	}

	in, err := e.captionIntentNow()
	if err != nil {
		t.Fatalf("captionIntentNow: %v", err)
	}
	if !in.on {
		t.Fatalf("intent is off, want on for the enabled twitch destination")
	}
	if len(in.targets) != 1 || in.targets[0].ID != twitch.ID || in.targets[0].Delay != 0 {
		t.Fatalf("targets = %+v, want only the enabled twitch destination with no delay", in.targets)
	}
	if in.track != transcribe.LiveTrackAuto {
		t.Fatalf("track = %d, want the automatic choice %d when the programme has none", in.track, transcribe.LiveTrackAuto)
	}
}

// TestCaptionIntentCarriesTheProgrammesChosenTrack checks that the programme's
// caption track reaches the captioner. Track 0 is a real track, so it must not
// be mistaken for "automatic".
func TestCaptionIntentCarriesTheProgrammesChosenTrackAndNotAutomaticForTrackZero(t *testing.T) {
	e := failoverEngine(t)
	dest, err := e.store.CreateDestination(&db.Destination{
		Name: "cc", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
		URL: "rtmp://127.0.0.1:1/rtmp", StreamKey: "key", Enabled: true,
		AudioBitrate: 128, Profile: routing.DefaultProfile(), CaptionMode: "sei",
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}
	e.sourceID = *dest.SourceID
	zero := 0
	src, err := e.store.GetSource(e.sourceID)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	src.CaptionTrack = &zero
	if err := e.store.UpdateSource(src); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	in, err := e.captionIntentNow()
	if err != nil {
		t.Fatalf("captionIntentNow: %v", err)
	}
	if in.track != 0 {
		t.Fatalf("track = %d, want 0 when the programme chose track 0", in.track)
	}
}

// TestADegradedCaptionerStaysOffThroughReconcilesUntilTheOperatorSavesSomething
// is the health-guard latch. The guard switches captions off to protect the
// stream. A reconcile that re-derives the switch from the same destinations must
// not switch them back on, or the captioner that was just stopped restarts.
func TestADegradedCaptionerStaysOffThroughReconcilesUntilTheOperatorSavesSomething(t *testing.T) {
	e := &Engine{}
	target := []captions.CueTarget{{ID: 1, Delay: 0}}
	first := captionIntent{on: true, track: transcribe.LiveTrackAuto, targets: target}
	first.sig = intentSig(first)
	e.applyCaptionIntent(first)
	if !e.captOn {
		t.Fatalf("first intent did not switch captions on")
	}

	// What onCaptionsDegraded writes when the health guard fires.
	e.mu.Lock()
	e.captOn, e.captCfg.Enabled, e.captWarn = false, false, "stream headroom low"
	e.mu.Unlock()

	e.applyCaptionIntent(first)
	if e.captOn {
		t.Fatalf("a reconcile with an unchanged intent switched a degraded captioner back on")
	}

	// The operator changes a delay: a save, so the switch re-arms.
	changed := captionIntent{on: true, track: transcribe.LiveTrackAuto,
		targets: []captions.CueTarget{{ID: 1, Delay: 30 * time.Second}}}
	changed.sig = intentSig(changed)
	e.applyCaptionIntent(changed)
	if !e.captOn || e.captWarn != "" {
		t.Fatalf("a saved change did not re-arm the switch: on=%t warn=%q", e.captOn, e.captWarn)
	}
}

// TestTeardownClosesTheRouteSoNoHeldCaptionArrivesAfterTheCaptionerStops is the
// teardown rule. A cue held for a delayed destination must be cancelled when the
// captioner comes down, not delivered afterwards.
func TestTeardownClosesTheRouteSoNoHeldCaptionArrivesAfterTheCaptionerStops(t *testing.T) {
	e := &Engine{}
	sink := &countingSink{}
	route := captions.NewCueRouter(sink, nil, nil)
	route.SetTargets([]captions.CueTarget{{ID: 9, Delay: time.Hour}})
	route.Emit(transcribe.LiveCaption{Segment: transcribe.Segment{Text: "held"}, At: time.Now()})
	if route.Pending() != 1 {
		t.Fatalf("setup: pending = %d, want 1 held cue", route.Pending())
	}
	e.mu.Lock()
	e.captRoute = route
	e.mu.Unlock()

	e.teardownCaptions(nil, 0, nil, nil)

	if route.Pending() != 0 {
		t.Fatalf("pending after teardown = %d, want 0", route.Pending())
	}
	if e.captRoute != nil {
		t.Fatalf("the engine still holds the route after teardown")
	}
}

// countingSink counts deliveries. A held cue must never reach it after teardown.
type countingSink struct{ n int }

// Deliver counts the cue.
func (s *countingSink) Deliver(int64, transcribe.LiveCaption) { s.n++ }

// TestAnOnIntentStoresANormalizedValidConfigurationOnAZeroEngine is the guard on
// the path the destinations take. The captioner must never receive a zero
// window or step, even though the intent carries only a switch and a track.
func TestAnOnIntentStoresANormalizedValidConfigurationOnAZeroEngine(t *testing.T) {
	e := &Engine{}
	in := captionIntent{on: true, track: 1, targets: []captions.CueTarget{{ID: 1}}}
	in.sig = intentSig(in)
	e.applyCaptionIntent(in)
	if err := e.captCfg.Validate(); err != nil {
		t.Fatalf("stored captioner config is invalid: %v", err)
	}
	if e.captCfg.Window == 0 || e.captCfg.Step == 0 {
		t.Fatalf("stored config has a zero window (%v) or step (%v)", e.captCfg.Window, e.captCfg.Step)
	}
	if e.captCfg.Track != 1 || !e.captOn {
		t.Fatalf("track = %d, on = %t; want track 1 and captions on", e.captCfg.Track, e.captOn)
	}
}

// TestChangingADelayThroughTheRouteSyncCancelsTheHeldCaptions covers the reconcile
// sync. A delay the operator changes must reach the live route at once, and any
// caption held under the old delay must be cancelled rather than released late.
func TestChangingADelayThroughTheRouteSyncCancelsTheHeldCaptions(t *testing.T) {
	e := &Engine{}
	sink := &countingSink{}
	route := captions.NewCueRouter(sink, nil, nil)
	route.SetTargets([]captions.CueTarget{{ID: 9, Delay: time.Hour}})
	route.Emit(transcribe.LiveCaption{Segment: transcribe.Segment{Text: "held"}, At: time.Now()})
	e.mu.Lock()
	e.captRoute = route
	e.mu.Unlock()

	e.syncCaptionRoute([]captions.CueTarget{{ID: 9, Delay: 2 * time.Hour}})

	if route.Pending() != 0 {
		t.Fatalf("pending after a delay change = %d, want the held caption cancelled", route.Pending())
	}
	if got := e.captionTargets(); len(got) != 1 || got[0].Delay != 2*time.Hour {
		t.Fatalf("engine targets = %+v, want the new 2 h delay", got)
	}
}

// TestCaptionIntentWithNoStoreIsOffAndDoesNotPanic covers the partly built engine
// that a test or a degraded start can produce. It must read as no captions, not
// crash the reconcile.
func TestCaptionIntentWithNoStoreIsOffAndDoesNotPanic(t *testing.T) {
	e := &Engine{}
	in, err := e.captionIntentNow()
	if err != nil {
		t.Fatalf("captionIntentNow with no store: %v", err)
	}
	if in.on || len(in.targets) != 0 || in.sig == "" {
		t.Fatalf("intent = %+v, want off with no targets and a signature", in)
	}
}

// TestAProgrammesCaptionPolicyReachesCaptionsAlreadyRunningWithoutARestart is the
// #8 custom-vocabulary and masked-words promise at the engine. The policy saved
// on the programme is read on the next reconcile and applied by the fanout of
// the captioner that is ALREADY running: the fanout asks for the policy on each
// cue rather than keeping the one it started with, so a save mid-show changes
// the next caption without restarting speech recognition. The policy is not
// part of the intent's signature, so it cannot re-arm a degraded captioner.
//
// MUTATION: the fanout built with the policy captured at start (Policy:
// e.captionPolicy()). Observed: FAIL, the cue after the save was unmasked.
func TestAProgrammesCaptionPolicyReachesCaptionsAlreadyRunningWithoutARestart(t *testing.T) {
	e := failoverEngine(t)
	dest, err := e.store.CreateDestination(&db.Destination{
		Name: "cc", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
		URL: "rtmp://127.0.0.1:1/rtmp", StreamKey: "key", Enabled: true,
		AudioBitrate: 128, Profile: routing.DefaultProfile(), CaptionMode: "sei",
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}
	e.sourceID = *dest.SourceID

	var shown []string
	fan := e.captionFanout(func(c transcribe.LiveCaption) { shown = append(shown, c.Text) }, nil, nil)
	fan.Emit(transcribe.LiveCaption{Segment: transcribe.Segment{Text: "darn polyemesis"}})

	before, err := e.captionIntentNow()
	if err != nil {
		t.Fatalf("captionIntentNow: %v", err)
	}
	src, err := e.store.GetSource(e.sourceID)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	src.CaptionPolicy = db.CaptionPolicy{Vocabulary: []string{"Polyemesis"}, Blocklist: []string{"darn"}}
	if err := e.store.UpdateSource(src); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	after, err := e.captionIntentNow()
	if err != nil {
		t.Fatalf("captionIntentNow after the save: %v", err)
	}
	if after.sig != before.sig {
		t.Fatalf("a policy save changed the intent signature (%q -> %q); it would re-arm a degraded captioner", before.sig, after.sig)
	}
	e.useCaptionPolicy(after.policy)
	fan.Emit(transcribe.LiveCaption{Segment: transcribe.Segment{Text: "darn polyemesis"}})

	want := []string{"darn polyemesis", "d*** Polyemesis"}
	if len(shown) != 2 || shown[0] != want[0] || shown[1] != want[1] {
		t.Fatalf("captions shown = %q, want %q", shown, want)
	}
}

// TestACaptionGoesToTheProgrammesCueLogAndToThePlayerSidecar checks the one
// sidecar path every live caption takes: the WebVTT file beside the HLS window
// (the public player's live track), the programme's cue log under the data
// directory (the VOD track and the exports of what viewers saw) and the
// programme's burn file (what a tier burning captions in draws). All three get
// the caption as it went out.
//
// MUTATION: the sidecar writes the WebVTT file only. Observed: FAIL, the cue log
// was empty.
func TestACaptionGoesToTheProgrammesCueLogAndToThePlayerSidecar(t *testing.T) {
	e := failoverEngine(t)
	e.cfg.DataDir = t.TempDir()
	vtt, err := transcribe.OpenLiveVTT(filepath.Join(t.TempDir(), transcribe.LiveVTTName))
	if err != nil {
		t.Fatal(err)
	}
	defer vtt.Close()
	cueLog := e.openCueLog()
	if cueLog == nil {
		t.Fatal("no cue log opened for a programme with a data directory")
	}
	burn := e.openBurnWriter()
	if burn == nil {
		t.Fatal("no burn writer for a programme with a data directory")
	}
	sidecar := e.captionSidecar(vtt, cueLog, burn)
	spoken := time.Now().Add(-5 * time.Second)
	c := transcribe.LiveCaption{Segment: transcribe.Segment{EndMS: 1000, Text: "on air"}, At: spoken.Add(time.Second)}
	if err := sidecar(c); err != nil {
		t.Fatalf("sidecar: %v", err)
	}
	_ = cueLog.Close()
	if vtt.Cues() != 1 {
		t.Fatalf("WebVTT sidecar holds %d cues, want 1", vtt.Cues())
	}
	got, err := captions.ReadCueLog(captions.CueLogPath(e.cfg.DataDir, e.sourceID), spoken.Add(-time.Second), spoken.Add(time.Minute))
	if err != nil {
		t.Fatalf("ReadCueLog: %v", err)
	}
	if len(got) != 1 || got[0].Text != "on air" {
		t.Fatalf("cue log = %+v, want the one caption", got)
	}
	// And the burn file, which a rendition burning captions in draws.
	b, err := os.ReadFile(captions.BurnPath(e.cfg.DataDir, e.sourceID))
	if err != nil || string(b) != "on air" {
		t.Fatalf("burn file = %q (%v), want the caption on screen", b, err)
	}
	_ = burn.Close()
}
