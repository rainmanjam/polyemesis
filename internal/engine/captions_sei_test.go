package engine

import (
	"io"
	"log/slog"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/captions"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// seiEngine is the smallest Engine that can build an SEI stage: a logger and a
// port allocator. No FFmpeg, no store, so these tests run on every machine.
func seiEngine(t *testing.T) *Engine {
	t.Helper()
	return &Engine{
		log:   slog.Default(),
		alloc: relay.NewPortAllocator(relayPortBase, relayPortSpan),
	}
}

// TestAnSEIStageIsWantedOnlyForAPlatformAndModeThatTakesSEICaptions pins the
// gate that decides, from the saved row alone, whether a destination runs
// through the stage. Kick is off by design, YouTube's POST path is not SEI, and
// Facebook is behind its provisional flag.
func TestAnSEIStageIsWantedOnlyForAPlatformAndModeThatTakesSEICaptions(t *testing.T) {
	cases := []struct {
		name     string
		platform db.Platform
		mode     captions.Mode
		facebook bool
		want     bool
	}{
		{"twitch with SEI captions gets the stage", db.PlatformTwitch, captions.ModeSEI, false, true},
		{"twitch with captions off gets no stage", db.PlatformTwitch, captions.ModeOff, false, false},
		{"youtube with its POST path gets no SEI stage", db.PlatformYouTube, captions.ModeYouTubePOST, false, false},
		{"kick with SEI is refused: no documented requirement", db.PlatformKick, captions.ModeSEI, false, false},
		{"facebook SEI is refused while its provisional flag is closed", db.PlatformFacebook, captions.ModeFacebookSEI, false, false},
		{"facebook SEI gets the stage once its provisional flag is open", db.PlatformFacebook, captions.ModeFacebookSEI, true, true},
	}
	for _, c := range cases {
		row := &db.Destination{Platform: c.platform, CaptionMode: string(c.mode)}
		got := wantsSEIStage(row, c.facebook)
		if got != c.want {
			t.Errorf("%s: wantsSEIStage = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestTheFFmpegOfACaptionedDestinationReadsItsOwnStageNotTheUpstreamHub is the
// placement contract. The stage reads the upstream hub, and the destination's
// FFmpeg, its backup and its teardown all read the stage's hub. Dropping that
// swap leaves captions written to a stream nothing reads.
func TestTheFFmpegOfACaptionedDestinationReadsItsOwnStageNotTheUpstreamHub(t *testing.T) {
	e := seiEngine(t)
	upstream, err := relay.New(e.log, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()

	row := &db.Destination{ID: 41, Name: "twitch", Platform: db.PlatformTwitch, CaptionMode: string(captions.ModeSEI)}
	capt, err := e.startCaptionStage(row, upstream)
	if err != nil {
		t.Fatalf("startCaptionStage: %v", err)
	}
	if capt == nil {
		t.Fatal("startCaptionStage returned no stage for a Twitch destination with SEI captions")
	}

	d := &destination{row: row, capt: capt}
	if got := d.readHub(upstream); got != capt.hub {
		t.Fatalf("the destination reads %p, want its stage hub %p (upstream is %p)", got, capt.hub, upstream)
	}
	if !seiHasSubscriber(upstream, capt.inSub) {
		t.Fatalf("the stage does not subscribe to the upstream hub under %q", capt.inSub)
	}

	e.closeCaptionStage(capt)
	if seiHasSubscriber(upstream, capt.inSub) {
		t.Fatalf("the stage still subscribes to the upstream hub after close")
	}
	if _, ok := e.seiStages.Load(row.ID); ok {
		t.Fatalf("the stage is still registered for destination %d after close", row.ID)
	}
}

// TestACaptionDeliveredToADestinationReachesItsOwnStageAndNoOthers checks the
// sink. The CueRouter's timers call Deliver, so it must find the stage by the
// destination id and drop a caption for a destination with no stage.
func TestACaptionDeliveredToADestinationReachesItsOwnStageAndNoOthers(t *testing.T) {
	e := seiEngine(t)
	upstream, err := relay.New(e.log, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()

	row := &db.Destination{ID: 7, Name: "twitch", Platform: db.PlatformTwitch, CaptionMode: string(captions.ModeSEI)}
	capt, err := e.startCaptionStage(row, upstream)
	if err != nil || capt == nil {
		t.Fatalf("startCaptionStage: capt=%v err=%v", capt, err)
	}
	defer e.closeCaptionStage(capt)

	cue := transcribe.LiveCaption{}
	cue.Text = "HELLO"
	cue.StartMS, cue.EndMS = 0, 3000

	captionSink{e: e}.Deliver(7, cue)
	captionSink{e: e}.Deliver(8, cue)

	if got := capt.stage.Queued(); got != 1 {
		t.Fatalf("the stage holds %d captions after one delivery to its destination, want 1", got)
	}
}

// seiHasSubscriber reports whether a hub has a subscriber under name.
func seiHasSubscriber(h *relay.Hub, name string) bool {
	for _, s := range h.Subscribers() {
		if s == name {
			return true
		}
	}
	return false
}

// The manager hands the one startup value to every engine it configures: a
// destination on an engine that missed it would be staged (or not) differently
// from the same destination on its neighbour.
func TestTheManagerHandsTheFacebookCaptionSwitchToEveryEngine(t *testing.T) {
	m := NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), config.Config{}, nil, nil, events.NewBroker())
	m.SetFacebookCaptions(true)

	e := &Engine{}
	m.applyEngineSettings(e, m.engineSettingsSnapshot())
	if !e.facebookCaptions.Load() {
		t.Error("an engine configured through the manager did not get the Facebook caption switch")
	}
	m.SetFacebookCaptions(false)
	m.applyEngineSettings(e, m.engineSettingsSnapshot())
	if e.facebookCaptions.Load() {
		t.Error("closing the switch did not reach the engine")
	}
}
