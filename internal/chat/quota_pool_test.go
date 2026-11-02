package chat

import (
	"testing"
	"time"
)

// TestEveryYouTubeAdapterOfOneClientSharesOneBudget: the daily allowance is
// the Google Cloud project's, not each channel's, so two connected YouTube
// channels on one client draw on one budget.
func TestEveryYouTubeAdapterOfOneClientSharesOneBudget(t *testing.T) {
	pool := NewYouTubeQuotaPool(10000, 200, nil)
	q := pool.For("client-a")
	a, err := NewYouTube(YouTubeConfig{AccountRef: "one", Token: StaticToken("t"), Quota: q})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewYouTube(YouTubeConfig{AccountRef: "two", Token: StaticToken("t"), Quota: pool.For("client-a")})
	if err != nil {
		t.Fatal(err)
	}
	a.budget.spend(100)
	b.budget.spend(50)
	if got := q.Status().Used; got != 150 {
		t.Fatalf("the shared budget has %d used, want 150 from both adapters", got)
	}
	if pool.For("client-b") == q {
		t.Fatal("a second Google client shares the first one's budget")
	}
}

// TestTheShareCapsWhatOneUserMaySpend: analytics may spend at most its share
// of the allowance however much is left for everyone else.
func TestTheShareCapsWhatOneUserMaySpend(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	q := NewYouTubeQuotaPool(1000, 0, func() time.Time { return now }).For("c")
	for i := 0; i < 150; i++ {
		if !q.ChargeShare("analytics", 0.15, 1) {
			t.Fatalf("refused read %d inside a 150-unit share", i+1)
		}
	}
	if q.ChargeShare("analytics", 0.15, 1) {
		t.Fatal("the share let analytics spend past 15% of the allowance")
	}
	if !q.Charge(1) {
		t.Fatal("the share cap refused someone else")
	}
	if q.Status().Used != 151 {
		t.Fatalf("used = %d, want 151", q.Status().Used)
	}
	if left := q.ShareLeft("analytics", 0.15); left != 0 {
		t.Fatalf("share left = %d, want 0", left)
	}
}

// TestAShareStopsAtTheReserveToo: the share is inside the read budget, so it
// never spends the units held back for sending.
func TestAShareStopsAtTheReserveToo(t *testing.T) {
	q := NewYouTubeQuotaPool(1000, 200, nil).For("c")
	q.Spend(799)
	if !q.ChargeShare("analytics", 0.15, 1) {
		t.Fatal("refused the last unit above the reserve")
	}
	if q.ChargeShare("analytics", 0.15, 1) {
		t.Fatal("the share spent into the send reserve")
	}
}

func TestPauseSpendsTheWholeAllowance(t *testing.T) {
	q := NewYouTubeQuotaPool(1000, 0, nil).For("c")
	q.Pause()
	if q.Charge(1) || q.Afford(1) {
		t.Fatal("a paused budget still affords a read")
	}
}

func TestPoolLimitsReachEveryClient(t *testing.T) {
	pool := NewYouTubeQuotaPool(10000, 200, nil)
	a, b := pool.For("a"), pool.For("b")
	pool.SetLimits(50000, 500)
	if a.Status().Limit != 50000 || b.Status().Limit != 50000 || pool.For("new").Status().Limit != 50000 {
		t.Fatal("a raised allowance did not reach every client's budget")
	}
}
