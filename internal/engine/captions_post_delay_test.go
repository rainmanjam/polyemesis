package engine

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// THE DELAY OF A YOUTUBE CAPTION POST DESTINATION IS YOUTUBE'S, NOT OURS.
//
// YouTube requires a 30 s or 60 s broadcast delay for captions by HTTP POST, and
// polyemesis sets it as the broadcast's broadcastStreamDelayMs from the
// destination's DelaySeconds. YouTube then holds the viewers back by it. Holding
// the destination's output back here as well put viewers twice the delay
// behind (about 120 s at 60 s), so this destination is sent undelayed and its
// captions are posted as soon as they are recognised, stamped with the moment
// the words reached YouTube's ingest.

// postDelayRow is a YouTube destination that posts captions, with a 30 s delay.
// Created disabled: a youtube_post destination may not be created enabled
// without its caption URL, and startDest does not consult the switch.
func postDelayRow(t *testing.T, e *Engine) *db.Destination {
	t.Helper()
	row := testDestination(0, nil)
	row.Name = "youtube-captions"
	row.Platform = db.PlatformYouTube
	row.URL = "rtmp://127.0.0.1:1/live2"
	row.CaptionMode = "youtube_post"
	row.DelaySeconds = 30
	row.Enabled = false
	row.SourceID = &e.sourceID
	saved, err := e.store.CreateDestination(row)
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	return saved
}

// TestAYouTubePOSTCaptionDestinationIsSentUndelayedBecauseYouTubeHoldsTheDelay.
//
// MUTATION: startDest gating the delay line on row.DelaySeconds again. Observed:
// FAIL, the destination was started behind a 30 s delay line.
func TestAYouTubePOSTCaptionDestinationIsSentUndelayedBecauseYouTubeHoldsTheDelay(t *testing.T) {
	e, _ := storeEngine(t)
	row := postDelayRow(t, e)
	if err := e.startDest(delayPlan(row), e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	d := destOf(e, row.ID)
	if d == nil {
		t.Fatal("the destination was not started")
	}
	if d.delay != nil {
		t.Fatal("a youtube_post destination was held back by polyemesis as well as by YouTube")
	}
	up := e.hub.Subscribers()
	if slices.Contains(up, delayDestSubName(row.ID)) {
		t.Fatalf("a delay line is reading the upstream hub: %v", up)
	}
	if !slices.Contains(up, destSubName(row.ID, "")) {
		t.Fatalf("the FFmpeg is not reading the upstream hub directly: %v", up)
	}
	// The card still says how far behind viewers are: the delay is real, it is
	// just applied by YouTube.
	if got := statusFor(t, e, row.ID).DelaySeconds; got != 30 {
		t.Fatalf("status DelaySeconds = %d, want the configured 30", got)
	}
}

// TestTheRestartSignatureFollowsTheDelayPolyemesisAppliesNotTheConfiguredOne:
// switching a delayed YouTube destination between captions by POST and no
// captions moves the delay between YouTube and polyemesis, so the output must
// restart to gain or lose its buffer. Changing a POST destination between 30 s
// and 60 s changes only what YouTube is told, which happens on the broadcast and
// not on the encoder, so it does not restart the output.
//
// MUTATION: destSpec reading row.DelaySeconds again. Observed: FAIL, the 30 s and
// 60 s POST destinations hashed differently.
func TestTheRestartSignatureFollowsTheDelayPolyemesisAppliesNotTheConfiguredOne(t *testing.T) {
	post := &db.Destination{ID: 7, Name: "yt", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
		URL: "rtmp://127.0.0.1:1/live2", StreamKey: "key", AudioBitrate: 160,
		CaptionMode: "youtube_post", DelaySeconds: 30}
	off := *post
	off.CaptionMode = "off"
	sixty := *post
	sixty.DelaySeconds = 60
	if delayPlan(post).spec == delayPlan(&off).spec {
		t.Fatal("moving the delay from YouTube to polyemesis did not restart the destination")
	}
	if delayPlan(post).spec != delayPlan(&sixty).spec {
		t.Fatal("a change of YouTube's own delay restarted the encoder output")
	}
}

// TestAPOSTCaptionIsStampedWithTheMomentItsWordsReachedYouTube: YouTube places a
// POSTed caption by its timestamp against the media it ingested, and the media
// now reaches YouTube undelayed. So the stamp is the moment the words were
// spoken (LiveCaption.SpokenAt), not the moment the recogniser finished with
// them, which is the recognition lag plus the cue's own length later.
//
// MUTATION: captionSink.Deliver stamping time.Now() again. Observed: FAIL, the
// stamp was the test's wall clock, not 12:00:05.000.
func TestAPOSTCaptionIsStampedWithTheMomentItsWordsReachedYouTube(t *testing.T) {
	e, _, posters, mu := postEngine(t)
	e.syncCaptionPosters(map[int64]string{4: plantedCaptionURL("four")})

	cue := transcribe.LiveCaption{}
	cue.Text, cue.StartMS, cue.EndMS = "HELLO WORLD", 0, 2000
	cue.At = time.Date(2026, 11, 1, 12, 0, 10, 0, time.UTC)
	cue.LagMS = 3000
	captionSink{e: e}.Deliver(4, cue)

	mu.Lock()
	four := posters[plantedCaptionURL("four")]
	mu.Unlock()
	eventually(t, "the cue to be posted", 5*time.Second, func() bool { return len(four.got()) > 0 })
	if got, want := four.got()[0], "2026-11-01T12:00:05.000\nHELLO WORLD\n"; got != want {
		t.Fatalf("posted %q, want %q", got, want)
	}
}

// TestAPOSTCaptionWithNoProductionTimeIsStampedNow: a cue that carries no At (a
// replay, a test) has no SpokenAt to speak of; it must not be posted as year one,
// which YouTube's window would throw away.
func TestAPOSTCaptionWithNoProductionTimeIsStampedNow(t *testing.T) {
	e, _, posters, mu := postEngine(t)
	e.syncCaptionPosters(map[int64]string{4: plantedCaptionURL("four")})

	cue := transcribe.LiveCaption{}
	cue.Text, cue.StartMS, cue.EndMS = "HELLO", 0, 1000
	before := time.Now().UTC()
	captionSink{e: e}.Deliver(4, cue)

	mu.Lock()
	four := posters[plantedCaptionURL("four")]
	mu.Unlock()
	eventually(t, "the cue to be posted", 5*time.Second, func() bool { return len(four.got()) > 0 })
	line, _, _ := strings.Cut(four.got()[0], "\n")
	at, err := time.Parse("2006-01-02T15:04:05.000", line)
	if err != nil {
		t.Fatalf("timestamp line %q: %v", line, err)
	}
	if at.Before(before.Add(-time.Second)) || at.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("stamped %s, want about now (%s)", at, before)
	}
}
