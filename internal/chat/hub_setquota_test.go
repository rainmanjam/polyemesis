package chat

import (
	"context"
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// pacingAdapter is a fakeAdapter that also declares quotaPacer, so these tests
// can tell "the Hub reached every adapter" from "the Hub reached the ones that
// could take it".
type pacingAdapter struct {
	fakeAdapter

	mu      sync.Mutex
	calls   int
	units   int
	reserve int
}

func (p *pacingAdapter) SetQuota(units, reserve int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.units, p.reserve = units, reserve
}

func (p *pacingAdapter) seen() (calls, units, reserve int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, p.units, p.reserve
}

func TestSetQuotaReachesEveryPacingAdapter(t *testing.T) {
	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	yt := &pacingAdapter{fakeAdapter: fakeAdapter{platform: db.PlatformYouTube, account: "yt"}}
	other := &pacingAdapter{fakeAdapter: fakeAdapter{platform: db.PlatformKick, account: "kick"}}
	for _, a := range []Adapter{yt, other} {
		if err := h.Attach(ctx, a); err != nil {
			t.Fatalf("attach: %v", err)
		}
	}

	if applied := h.SetQuota(1_000_000, 500); applied != 2 {
		t.Fatalf("SetQuota reported %d adapters, want 2", applied)
	}
	for name, a := range map[string]*pacingAdapter{"youtube": yt, "kick": other} {
		calls, units, reserve := a.seen()
		if calls != 1 || units != 1_000_000 || reserve != 500 {
			t.Fatalf("%s got calls=%d units=%d reserve=%d, want 1/1000000/500", name, calls, units, reserve)
		}
	}
}

// An adapter with no daily allowance is skipped, and skipping it is not an
// error. IRC has nothing to meter; a Hub that refused, logged a warning, or
// counted it would be describing a problem that does not exist.
func TestSetQuotaSkipsAdaptersThatDoNotPace(t *testing.T) {
	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	paces := &pacingAdapter{fakeAdapter: fakeAdapter{platform: db.PlatformYouTube, account: "yt"}}
	plain := &fakeAdapter{platform: db.PlatformTwitch, account: "tw"}
	for _, a := range []Adapter{paces, plain} {
		if err := h.Attach(ctx, a); err != nil {
			t.Fatalf("attach: %v", err)
		}
	}

	// TWO ATTACHED, ONE COUNTED. A SetQuota that pushed into everything would
	// report 2 here and would panic or no-op against an adapter with no such
	// method; one that pushed into nothing would report 0. Only the right
	// answer is 1.
	if applied := h.SetQuota(20_000, 200); applied != 1 {
		t.Fatalf("SetQuota reported %d adapters, want 1 of the 2 attached", applied)
	}
	if calls, _, _ := paces.seen(); calls != 1 {
		t.Fatalf("the pacing adapter was called %d times, want 1", calls)
	}
}

// A settings save on an install with nothing connected is a normal Tuesday, not
// a failure. It has to return 0 rather than block or panic.
func TestSetQuotaWithNothingAttached(t *testing.T) {
	h := testHub(t)
	if applied := h.SetQuota(20_000, 200); applied != 0 {
		t.Fatalf("SetQuota on an empty hub reported %d, want 0", applied)
	}
}

// A nil Hub is what an install with chat disabled has, and every other Apply*
// on the API side leans on this rather than checking twice.
func TestSetQuotaOnANilHub(t *testing.T) {
	var h *Hub
	if applied := h.SetQuota(20_000, 200); applied != 0 {
		t.Fatalf("SetQuota on a nil hub reported %d, want 0", applied)
	}
}
