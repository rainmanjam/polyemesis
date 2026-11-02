package chat

import "testing"

// The vertical status read and the sampler charge the shared YouTubeQuota
// (quota_pool.go) rather than a chat adapter: these pin the two rules the
// adapter-level ChargeQuota used to carry, on the value that replaced it.

// TestChargeRefusesACallTheBudgetCannotAfford.
func TestChargeRefusesACallTheBudgetCannotAfford(t *testing.T) {
	q := NewYouTubeQuota(QuotaUnits(3), QuotaReserve(0), nil)
	for i := 0; i < 3; i++ {
		if !q.Charge(QuotaCostListLiveStreams) {
			t.Fatalf("charge %d refused with room left in a budget of 3", i+1)
		}
	}
	if q.Charge(QuotaCostListLiveStreams) {
		t.Fatal("a fourth one-unit charge was allowed against a budget of 3")
	}
}

// TestChargeRespectsTheSendReserve: a read charged from outside the chat loop
// cannot spend the units held back for sending and moderation.
func TestChargeRespectsTheSendReserve(t *testing.T) {
	q := NewYouTubeQuota(QuotaUnits(5), QuotaReserve(2), nil)
	for i := 0; i < 3; i++ {
		if !q.Charge(QuotaCostListLiveStreams) {
			t.Fatalf("charge %d refused above the reserve", i+1)
		}
	}
	if q.Charge(QuotaCostListLiveStreams) {
		t.Fatal("a read spent into the send reserve")
	}
}
