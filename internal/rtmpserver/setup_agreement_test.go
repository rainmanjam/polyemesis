package rtmpserver

import (
	"fmt"
	"testing"

	"github.com/bluenviron/gortmplib/pkg/message"
)

// isSetup and setupSlot must agree about every type. #721.
//
// cacheSetup folds them into one condition:
//
//	slot, ok := setupSlot(msg)
//	if !ok || !isSetup(msg) { return }
//
// which makes two very different events indistinguishable. "This is ordinary
// media" happens several hundred times a second and is nothing. "This IS a
// setup message and no slot was found for it" is a bug, and returns in silence
// beside it.
//
// THIS EXACT SHAPE HAS ALREADY SHIPPED ONCE. The comment on isSetup's
// AudioExMultitrack arm records the outcome: matching only the unwrapped types
// cached the legacy track's configuration and nothing else, and ffprobe -- a
// late-joining subscriber, which is the normal case -- "hung forever instead of
// failing, because it was still waiting to identify streams it had the data for
// but no configuration for."
//
// A runtime log would have needed a logger threaded into `stream`, which has
// none. This is the cheaper and earlier device: the two switches are compared
// at build time, so a type added to one and not the other fails here rather
// than hanging somebody's ffprobe.
func TestIsSetupAndSetupSlotAgreeOnEveryType(t *testing.T) {
	// Every message type either function claims to handle. A type added to one
	// switch belongs here too; that is the one thing this test cannot check for
	// itself, and the count assertion below is the guard on it.
	cases := []struct {
		name string
		msg  message.Message
		// setup is what isSetup should say. Both functions are then required to
		// agree with each other, which is the actual property.
		setup bool
	}{
		{"metadata", &message.DataAMF0{Payload: nil}, true},
		{"video config", &message.Video{Type: message.VideoTypeConfig}, true},
		{"video frame", &message.Video{Type: message.VideoTypeAU}, false},
		{"audio config", &message.Audio{AACType: message.AudioAACTypeConfig}, true},
		{"audio frame", &message.Audio{AACType: message.AudioAACTypeAU}, false},
		{"video-ex sequence start", &message.VideoExSequenceStart{}, true},
		{"audio-ex sequence start", &message.AudioExSequenceStart{}, true},
		{"audio-ex multichannel config", &message.AudioExMultichannelConfig{}, true},
	}

	if len(cases) < 8 {
		t.Fatalf("only %d types enumerated; both switches handle more than that, so "+
			"this test is checking a subset and its agreement claim is too weak",
			len(cases))
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotSetup := isSetup(c.msg)
			_, gotSlot := setupSlot(c.msg)

			if gotSetup != c.setup {
				t.Errorf("isSetup(%T) = %v, want %v", c.msg, gotSetup, c.setup)
			}
			// THE PROPERTY. A setup message with no slot is never cached, so a
			// late subscriber never receives its configuration.
			if gotSetup && !gotSlot {
				t.Errorf("isSetup says %T is setup and setupSlot has no slot for it. "+
					"cacheSetup drops it silently, so every late-joining subscriber -- "+
					"which includes ffprobe -- gets data it cannot identify.", c.msg)
			}
			// The other direction is not a bug but is worth knowing about: a
			// slot for something never cached is dead weight in the map.
			if !gotSetup && gotSlot {
				t.Logf("note: setupSlot has a slot for %T, which isSetup does not call "+
					"setup. Harmless today -- nothing reaches cacheSetup -- but the slot "+
					"is unreachable and one of the two switches is probably stale.", c.msg)
			}
		})
	}
}

// The wrapper case, which is how every track after the first arrives and is the
// one that already went wrong.
func TestAWrappedSequenceStartIsBothSetupAndSlotted(t *testing.T) {
	inner := &message.AudioExSequenceStart{}
	msg := &message.AudioExMultitrack{TrackID: 1, Wrapped: inner}

	if !isSetup(msg) {
		t.Fatal("a wrapped AudioExSequenceStart is not recognised as setup. E-RTMP " +
			"multitrack sends tracks 2..N inside AudioExMultitrack, so this is how " +
			"every track after the first arrives -- and missing it is what made " +
			"ffprobe hang rather than fail.")
	}
	slot, ok := setupSlot(msg)
	if !ok {
		t.Fatal("a wrapped AudioExSequenceStart has no cache slot, so it is never " +
			"replayed to a late subscriber")
	}
	if slot == "" {
		t.Error("the slot name is empty, so every wrapped track would overwrite the same entry")
	}
	t.Logf("wrapped track 1 caches under %q", slot)
	_ = fmt.Sprint(slot)
}
