package api

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// pacingSpy is an adapter that meters against a daily allowance, which is the
// only capability these tests care about. It stands in for the YouTube adapter
// because a real one would need Google on the other end of a socket, and what
// is under test here is the SAVE PATH, not the polling.
type pacingSpy struct {
	mu      sync.Mutex
	calls   int
	units   int
	reserve int
}

func (p *pacingSpy) Platform() db.Platform { return db.PlatformYouTube }
func (p *pacingSpy) Account() string       { return "spy" }

func (p *pacingSpy) Run(ctx context.Context, _ chat.Sink) error {
	<-ctx.Done()
	return ctx.Err()
}

func (p *pacingSpy) SetQuota(units, reserve int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.units, p.reserve = units, reserve
}

func (p *pacingSpy) seen() (calls, units, reserve int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, p.units, p.reserve
}

// SAVING A RAISED ALLOWANCE REACHES A CONNECTION THAT IS ALREADY UP.
//
// This is the claim settingsReload makes for chat.youtubeQuotaUnits, and the
// reason it is ClassLive rather than ClassNextStart. #732 wired the number into
// NewYouTube, which chatAdapter calls once, from StartChat, from main -- so
// before this the operator saved a larger quota, was told it was saved, and
// went on polling at the old rate until the process restarted. That is the same
// defect #732 was filed for, moved one step later.
//
// Mutation: drop the ApplyYouTubeQuota call from handlePutSettings. Observed to
// fail with "the running adapter was never told: calls=0".
func TestSavingTheQuotaReachesAnAdapterThatIsAlreadyRunning(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())

	s.chat = chat.New()
	t.Cleanup(s.chat.Close)

	spy := &pacingSpy{}
	if err := s.chat.Attach(context.Background(), spy); err != nil {
		t.Fatalf("attach: %v", err)
	}

	var cur db.Settings
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &cur)
	cur.Chat.YouTubeQuotaUnits = 1_000_000
	cur.Chat.YouTubeQuotaReserve = 500
	send(t, h, sign, http.MethodPut, "/api/v1/settings", cur, http.StatusOK)

	calls, units, reserve := spy.seen()
	if calls == 0 {
		t.Fatal("the running adapter was never told: calls=0")
	}
	if units != 1_000_000 || reserve != 500 {
		t.Fatalf("the adapter got units=%d reserve=%d, want 1000000/500", units, reserve)
	}
}

// THE CONTROL FOR THE TEST ABOVE. A handler that pushed a hardcoded pair, or
// that pushed the package defaults on every save, would satisfy it. The numbers
// have to be the ones that were saved, so a second save with different numbers
// has to arrive as those.
func TestASecondSaveOverwritesTheAllowanceRatherThanRepeatingTheFirst(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())

	s.chat = chat.New()
	t.Cleanup(s.chat.Close)

	spy := &pacingSpy{}
	if err := s.chat.Attach(context.Background(), spy); err != nil {
		t.Fatalf("attach: %v", err)
	}

	var cur db.Settings
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &cur)

	cur.Chat.YouTubeQuotaUnits = 1_000_000
	cur.Chat.YouTubeQuotaReserve = 500
	send(t, h, sign, http.MethodPut, "/api/v1/settings", cur, http.StatusOK)

	cur.Chat.YouTubeQuotaUnits = 50_000
	cur.Chat.YouTubeQuotaReserve = 300
	send(t, h, sign, http.MethodPut, "/api/v1/settings", cur, http.StatusOK)

	calls, units, reserve := spy.seen()
	if calls != 2 {
		t.Fatalf("two saves produced %d pushes", calls)
	}
	if units != 50_000 || reserve != 300 {
		t.Fatalf("the adapter kept units=%d reserve=%d, want the second save's 50000/300", units, reserve)
	}
}

// An install with chat disabled has no Hub at all, and a settings save must not
// fall over on it. Every other Apply* on this side leans on the same nil check.
func TestSavingTheQuotaOnAnInstallWithNoChatHub(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	s.chat = nil

	var cur db.Settings
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &cur)
	cur.Chat.YouTubeQuotaUnits = 250_000
	send(t, h, sign, http.MethodPut, "/api/v1/settings", cur, http.StatusOK)
}
