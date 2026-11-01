package api

import (
	"context"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// THE CAPTION GO-LIVE CHECK (#8). A YouTube destination that posts captions by
// HTTP POST is not taken to testing or live unless its caption URL is stored and
// the broadcast is set for POST captions with the monitor stream and a 30 s or
// 60 s delay. Those settings freeze once the broadcast leaves created/ready, so
// going live without them is a show whose captions can never appear.

func captionLifecycleRow(ready bool) *db.Destination {
	r := lifecycleTestRow(true, "ready")
	r.CaptionMode = "youtube_post"
	r.DelaySeconds = 30
	r.CaptionIngestReady = ready
	return r
}

func monitorOn() *bool { on := true; return &on }

// MUTATION: the captionGoLiveProblem check in considerRow removed. Observed:
// FAIL, "testing" was sent with no caption URL.
func TestACaptionDestinationIsNotTakenLiveWithoutItsCaptionURL(t *testing.T) {
	yt := &ytFake{status: "ready", streamStatus: "active", monitor: monitorOn(),
		captionType: "closedCaptionsHttpPost", delayMs: 30000}
	c, store, faults := lifecycleFixture(t, yt, captionLifecycleRow(false))
	c.sweepOnce(context.Background(), sweepEverything)

	if sent, _ := yt.snapshot(); len(sent) != 0 {
		t.Fatalf("transitions %v were sent for a caption destination with no caption URL", sent)
	}
	if len(*faults) == 0 {
		t.Fatalf("nothing was escalated")
	}
	got := store.get(3)
	if !strings.Contains(got.Lifecycle.Fault, "Refresh stream key") {
		t.Fatalf("the fault does not name the fix: %q", got.Lifecycle.Fault)
	}
	if !got.Enabled {
		t.Fatalf("the destination was switched off; the stream keeps going out")
	}
}

func TestACaptionDestinationIsNotTakenLiveOnABroadcastNotSetForPOSTCaptions(t *testing.T) {
	cases := []struct {
		name        string
		captionType string
		delayMs     int
		want        string
	}{
		{"captions disabled", "closedCaptionsDisabled", 30000, "closedCaptionsType is closedCaptionsDisabled"},
		{"embedded", "closedCaptionsEmbedded", 30000, "closedCaptionsType is closedCaptionsEmbedded"},
		{"no delay", "closedCaptionsHttpPost", 0, "30 or 60 seconds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yt := &ytFake{status: "ready", streamStatus: "active", monitor: monitorOn(),
				captionType: tc.captionType, delayMs: tc.delayMs}
			c, store, _ := lifecycleFixture(t, yt, captionLifecycleRow(true))
			c.sweepOnce(context.Background(), sweepEverything)
			if sent, _ := yt.snapshot(); len(sent) != 0 {
				t.Fatalf("transitions %v were sent", sent)
			}
			if f := store.get(3).Lifecycle.Fault; !strings.Contains(f, tc.want) {
				t.Fatalf("fault %q does not say %q", f, tc.want)
			}
		})
	}
}

func TestACaptionDestinationWithItsURLAndAPOSTBroadcastGoesToTesting(t *testing.T) {
	yt := &ytFake{status: "ready", streamStatus: "active", monitor: monitorOn(),
		captionType: "closedCaptionsHttpPost", delayMs: 30000}
	c, _, _ := lifecycleFixture(t, yt, captionLifecycleRow(true))
	c.sweepOnce(context.Background(), sweepEverything)
	sent, _ := yt.snapshot()
	if len(sent) != 1 || sent[0] != string(oauth.PhaseTesting) {
		t.Fatalf("transitions = %v, want [testing]", sent)
	}
}

// A destination without POST captions is untouched by the check, even on a
// broadcast with captions disabled.
func TestTheCaptionCheckLeavesOtherDestinationsAlone(t *testing.T) {
	yt := &ytFake{status: "ready", streamStatus: "active", monitor: monitorOn(),
		captionType: "closedCaptionsDisabled", delayMs: 0}
	c, _, _ := lifecycleFixture(t, yt, lifecycleTestRow(true, "ready"))
	c.sweepOnce(context.Background(), sweepEverything)
	if sent, _ := yt.snapshot(); len(sent) != 1 {
		t.Fatalf("transitions = %v, want one", sent)
	}
}
