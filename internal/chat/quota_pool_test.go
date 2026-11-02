package chat

import (
	"testing"
	"time"
)

// TestEveryYouTubeAdapterSharesTheClientsBudget: the daily allowance is the
// Google Cloud project's, not each channel's, so two connected YouTube
// channels draw on one budget.
func TestEveryYouTubeAdapterSharesTheClientsBudget(t *testing.T) {
	q := NewYouTubeQuota(10000, 200, nil)
	q.Bind("client-a")
	a, err := NewYouTube(YouTubeConfig{AccountRef: "one", Token: StaticToken("t"), Quota: q})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewYouTube(YouTubeConfig{AccountRef: "two", Token: StaticToken("t"), Quota: q})
	if err != nil {
		t.Fatal(err)
	}
	a.budget.spend(100)
	b.budget.spend(50)
	if got := q.Status().Used; got != 150 {
		t.Fatalf("the shared budget has %d used, want 150 from both adapters", got)
	}
}

// TestANewClientIDStartsANewTallyForEveryAdapter (R12): a different Google
// project has its own counter. The adapters hold the same budget, so they
// move to the new tally with it -- nothing is left on the old one.
func TestANewClientIDStartsANewTallyForEveryAdapter(t *testing.T) {
	q := NewYouTubeQuota(10000, 200, nil)
	q.Bind("client-a")
	a, _ := NewYouTube(YouTubeConfig{AccountRef: "one", Token: StaticToken("t"), Quota: q})
	a.budget.spend(500)
	q.Bind("client-a")
	if q.Status().Used != 500 {
		t.Fatal("rebinding the same client reset its tally")
	}
	q.Bind("client-b")
	if q.Status().Used != 0 || q.ClientID() != "client-b" {
		t.Fatalf("after the client changed: used %d client %q, want a fresh tally for client-b", q.Status().Used, q.ClientID())
	}
	a.budget.spend(7)
	if q.Status().Used != 7 {
		t.Fatal("the adapter kept spending on the old client's tally")
	}
}

// TestTheFirstBindKeepsWhatWasSpentBeforeIt (R3): calls metered before the
// credentials were read are the configured client's; binding to it is not a
// change of project.
func TestTheFirstBindKeepsWhatWasSpentBeforeIt(t *testing.T) {
	q := NewYouTubeQuota(10000, 200, nil)
	q.Spend(50)
	q.Bind("client-a")
	if q.Status().Used != 50 {
		t.Fatalf("used %d after the first bind, want the 50 spent before it", q.Status().Used)
	}
}

// TestTheShareCapsWhatOneUserMaySpend: analytics may spend at most its share
// of the allowance however much is left for everyone else.
func TestTheShareCapsWhatOneUserMaySpend(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	q := NewYouTubeQuota(1000, 0, func() time.Time { return now })
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
	q := NewYouTubeQuota(1000, 200, nil)
	q.Spend(799)
	if q.ReadsLeft() != 1 {
		t.Fatalf("reads left = %d, want 1 above the 200-unit reserve", q.ReadsLeft())
	}
	if !q.ChargeShare("analytics", 0.15, 1) {
		t.Fatal("refused the last unit above the reserve")
	}
	if q.ChargeShare("analytics", 0.15, 1) || q.Charge(1) {
		t.Fatal("a read spent into the send reserve")
	}
}

func TestPauseSpendsTheWholeAllowance(t *testing.T) {
	q := NewYouTubeQuota(1000, 0, nil)
	q.Pause()
	if q.Charge(1) || q.Afford(1) {
		t.Fatal("a paused budget still affords a read")
	}
}

func TestSetLimitsReachesTheSharedBudget(t *testing.T) {
	q := NewYouTubeQuota(10000, 200, nil)
	a, _ := NewYouTube(YouTubeConfig{AccountRef: "one", Token: StaticToken("t"), Quota: q})
	q.SetLimits(50000, 500)
	if q.Status().Limit != 50000 || a.budget.status().Limit != 50000 {
		t.Fatal("a raised allowance did not reach the shared budget")
	}
}

func TestANilQuotaRefusesAndSpendsNothing(t *testing.T) {
	var q *YouTubeQuota
	q.Spend(1)
	if q.Charge(1) || q.Afford(1) || q.ChargeShare("a", 1, 1) || q.ReadsLeft() != 0 {
		t.Fatal("a nil quota allowed a read")
	}
}
