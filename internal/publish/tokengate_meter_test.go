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

func TestTheYouTubeUploadIsChargedToItsMeter(t *testing.T) {
	spent := 0
	y := &YouTube{Base: "http://127.0.0.1:1", Meter: func(u int) { spent += u }}
	// The session open fails to connect, which is fine: videos.insert is billed
	// when the session opens, before the answer.
	_, _ = y.open(t.Context(), Run{}, 10)
	if spent != youTubeUploadCost {
		t.Fatalf("spent %d, want %d", spent, youTubeUploadCost)
	}
}
