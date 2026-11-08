package publish

import "testing"

func TestTheGatesMeterIsANoOpUntilSetThenCharges(t *testing.T) {
	var g TokenGate
	g.Charge(1600) // no meter yet: must not panic
	spent := 0
	g.Set(nil, func(u int) { spent += u })
	g.Charge(1600)
	g.Charge(0)
	if spent != 1600 {
		t.Fatalf("spent %d, want 1600", spent)
	}
}

// videos.insert has a quota bucket of its own, so opening an upload session
// must not draw on the shared allowance the gate's meter feeds.
func TestTheYouTubeUploadDoesNotChargeTheSharedMeter(t *testing.T) {
	var g TokenGate
	spent := 0
	g.Set(nil, func(u int) { spent += u })
	y := &YouTube{Base: "http://127.0.0.1:1"}
	_, _ = y.open(t.Context(), Run{}, 10)
	if spent != 0 {
		t.Fatalf("the shared meter was charged %d for a videos.insert", spent)
	}
}
