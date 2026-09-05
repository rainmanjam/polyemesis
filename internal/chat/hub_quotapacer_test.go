package chat

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// THE REAL ADAPTER, NOT A FAKE THAT AGREES WITH THE INTERFACE.
//
// Every other test of Hub.SetQuota attaches a pacingAdapter, which exists to
// declare quotaPacer and record what it was told. That proves the Hub finds
// pacers and skips the rest; it proves nothing about the only adapter an
// operator actually has. Renaming YouTubeAdapter.SetQuota built, vetted and
// passed chat, api and engine, because the two fakes were renamed alongside it
// and no production caller was left to notice -- an operator would have been
// told their raised allowance was saved while the running adapter never heard
// about it.
//
// So this pushes through a real NewYouTube adapter, over a real Hub, and reads
// the answer off the adapter's OWN reported quota rather than off a spy that
// merely remembers being called. `var _ quotaPacer = (*YouTubeAdapter)(nil)` in
// hub.go catches the rename at compile time; this catches a SetQuota that
// compiles, satisfies the interface and does nothing useful.
func TestSetQuotaReachesTheRealYouTubeAdapterAndItsReportedQuota(t *testing.T) {
	// A broadcast list with nothing live in it, so Run settles into "waiting
	// for a broadcast" after one call instead of polling a chat.
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		fmt.Fprint(w, `{"items":[]}`)
	})

	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	yt := ytAdapter(t, stub.URL, func(c *YouTubeConfig) {
		c.QuotaUnits = 10_000
		c.QuotaReserve = 200
		// Park until the test ends rather than spinning through the wait.
		c.Sleep = func(ctx context.Context, _ time.Duration) bool {
			<-ctx.Done()
			return false
		}
	})
	if err := h.Attach(ctx, yt); err != nil {
		t.Fatalf("attach: %v", err)
	}

	// The two numbers are deliberately far apart and of different magnitudes,
	// so a swap on the way down cannot land on the right answer by luck: a
	// swapped pair reaches clampQuota as (500, 1000000), which drops the
	// reserve and reports a limit of 500.
	if applied := h.SetQuota(1_000_000, 500); applied != 1 {
		t.Fatalf("the hub reached %d adapters, want the one real YouTube adapter attached", applied)
	}

	q := yt.Health().Quota
	if q == nil {
		t.Fatal("the adapter reported no quota at all")
	}
	if q.Limit != 1_000_000 {
		t.Fatalf("the adapter reports a limit of %d after a save of 1,000,000 -- "+
			"the operator was told it was saved and the pacer never heard", q.Limit)
	}

	// QuotaStatus does not carry the reserve, so this is the nearest thing to
	// the adapter's own account of it. It is asserted because the limit alone
	// would pass on a SetQuota that dropped the reserve on the floor, which is
	// the half of the pair that keeps sending possible.
	yt.budget.mu.Lock()
	reserve := yt.budget.reserve
	yt.budget.mu.Unlock()
	if reserve != 500 {
		t.Fatalf("the adapter's reserve is %d after a save of 500", reserve)
	}
}

// POSITIVE CONTROL: the assertion above must be capable of failing.
//
// An adapter that reported 1,000,000 whatever it was told -- a stubbed status,
// a limit fixed at construction -- would pass that test forever. This one
// attaches an identical adapter, pushes NOTHING into it, and requires it to
// still report the allowance it was built with.
func TestAnAdapterNobodyPushedToStillReportsTheAllowanceItWasBuiltWith(t *testing.T) {
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		fmt.Fprint(w, `{"items":[]}`)
	})

	yt := ytAdapter(t, stub.URL, func(c *YouTubeConfig) {
		c.QuotaUnits = 10_000
		c.QuotaReserve = 200
	})

	q := yt.Health().Quota
	if q == nil {
		t.Fatal("the adapter reported no quota at all")
	}
	if q.Limit != 10_000 {
		t.Fatalf("an adapter built with 10,000 units reports %d before anything was pushed into it", q.Limit)
	}
}
