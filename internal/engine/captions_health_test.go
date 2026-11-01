package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// TestACaptionerThatStoppedRunningIsNotCurrentEvenWhenItsSignatureMatches: the
// audio tap or the transcription loop can exit on its own and leave the captioner
// pointer set. Its signature still matches the intent, so a reconcile that only
// compared the two returned early, and captions stayed enabled but dead until the
// settings changed. A captioner that is no longer running must count as not
// current, so the reconcile rebuilds it.
//
// MUTATION, internal/engine/engine.go: drop the cur.Running() check in
// captionerIsCurrent. Observed: FAIL -- a stopped captioner is reported current.
func TestACaptionerThatStoppedRunningIsNotCurrentEvenWhenItsSignatureMatches(t *testing.T) {
	stopped := &transcribe.LiveCaptioner{} // zero value: never started, so Running() is false
	if captionerIsCurrent(stopped, "sig", "sig") {
		t.Fatalf("a stopped captioner with a matching signature was reported current; captions would stay dead")
	}
	if captionerIsCurrent(nil, "", "sig") {
		t.Fatalf("no captioner while captions are wanted was reported current")
	}
	if !captionerIsCurrent(nil, "", "") {
		t.Fatalf("no captioner and no captions wanted must be current")
	}
}
