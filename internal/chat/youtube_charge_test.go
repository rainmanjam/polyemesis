package chat

import "testing"

// TestChargeQuotaRefusesACallTheBudgetCannotAfford pins the pacer seam the vertical status read
// uses (#7, correction 6). A call charged from outside the chat loop spends from the same budget
// the chat adapter polls against, and once the budget has no room the charge is refused rather
// than spent into a reserve the adapter is not allowed to touch.
func TestChargeQuotaRefusesACallTheBudgetCannotAfford(t *testing.T) {
	y := &YouTubeAdapter{budget: newBudget(QuotaUnits(3), QuotaReserve(0), nil)}
	for i := 0; i < 3; i++ {
		if !y.ChargeQuota(QuotaCostListLiveStreams) {
			t.Fatalf("charge %d refused with room left in a budget of 3", i+1)
		}
	}
	if y.ChargeQuota(QuotaCostListLiveStreams) {
		t.Fatal("a fourth one-unit charge was allowed against a budget of 3")
	}
}

// TestChargeQuotaWithNoBudgetIsNotARefusal pins that an adapter which does not pace (or a nil one)
// answers yes. Refusing there would stop the status read on a build with no chat connection.
func TestChargeQuotaWithNoBudgetIsNotARefusal(t *testing.T) {
	var nilAdapter *YouTubeAdapter
	if !nilAdapter.ChargeQuota(1) {
		t.Fatal("a nil adapter refused a charge; it has no budget to be out of")
	}
	if !(&YouTubeAdapter{}).ChargeQuota(1) {
		t.Fatal("an adapter with no budget refused a charge")
	}
}
