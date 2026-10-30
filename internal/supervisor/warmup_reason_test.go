package supervisor

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/failreason"
)

// WHERE THE DECODER WARM-UP MEETS THE FAILURE REASON.
//
// Two changes read the same stderr lines for different questions. The warm-up
// (warmup.go) demotes a video decoder's mid-GOP burst to info while no frame
// has been decoded and the window is open, so a healthy join is not counted as
// errors. The failure reason (noteExit, failreason.Classify) reads the raw
// stderr tail of a failed run to say why it failed. Nothing stopped the second
// from quoting the first: a run that died during warm-up with nothing else to
// say had a decoder line -- "Invalid data found when processing input" -- as
// the reason the operator was shown, on the card, in the alert and in the
// webhook, for a burst the log itself had just called harmless.
//
// So a line the warm-up demoted does not enter the classifier's tail. A real
// failure printed after it still classifies, and a decoder line past the
// window is an error again and may be the reason, as before.

func failedReason(t *testing.T, p *Process) *failreason.Reason {
	t.Helper()
	p.Start()
	waitFor(t, "the run to fail with a reason", func() bool {
		st := p.Status()
		return st.State == StateFailed && st.Reason != nil
	})
	return p.Status().Reason
}

func TestAWarmUpLineIsNeverTheFailureReason(t *testing.T) {
	p := testProcess(t, fakeExitSaying(1, warmupSliceError+"\n"+warmupDecoding), Spec{})
	r := failedReason(t, p)
	if strings.Contains(r.Line, "@ 0x") {
		t.Fatalf("reason = %+v: a decoder warm-up line became the failure reason", r)
	}
	// And the log still has them, at info: excluded from the reason, not lost.
	logs := p.Logs()
	if len(logs) < 2 || logs[0].Level != "info" {
		t.Fatalf("logs = %+v, want the warm-up lines kept at info", logs)
	}
}

func TestARealFailureAfterTheWarmUpBurstStillClassifies(t *testing.T) {
	p := testProcess(t, fakeExitSaying(1, warmupSliceError+"\n"+refusedKeyLine), Spec{})
	r := failedReason(t, p)
	if r.Cause != failreason.KeyRejected || r.Line != refusedKeyLine {
		t.Fatalf("reason = %+v, want key_rejected on %q", r, refusedKeyLine)
	}
}

func TestADecoderErrorPastTheWindowCanStillBeTheReason(t *testing.T) {
	p := testProcess(t, fakeExitSaying(1, warmupDecoding), Spec{})
	p.warmup = 0 // every line is past the window
	r := failedReason(t, p)
	if r.Line != warmupDecoding {
		t.Fatalf("reason = %+v, want the decoder error past the window kept as the line", r)
	}
}
