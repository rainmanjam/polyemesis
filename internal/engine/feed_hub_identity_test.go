package engine

import (
	"log/slog"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/relay"
)

func quietRelay(t *testing.T) *relay.Hub {
	t.Helper()
	h, err := relay.New(slog.Default(), 0)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

// The primary feed's signature must identify the hub it IS READING, not the one
// the settings say it should be.
//
// The window: reconcileOutputs calls detachFeedForSilence, which drops selMu,
// and only then does reconcileSilence swap the hub holding e.mu alone. A 500ms
// selector sweep landing in that gap computed the NEW silenceSig -- it comes
// from settings, which have already changed -- while downstreamFeedInput handed
// it the OLD hub, and detachFeedForSilence had just zeroed feedAt so the
// respawn backoff did not stop it.
//
// The feed then carried a signature matching exactly what reconcileSelector was
// about to ask for, so reconcileSelector found cur.upstream == want and a
// running process and left it alone. Permanently: the selector's hub carried
// zero bytes, every destination reported running, and nothing raised an error,
// because from each layer's own point of view nothing had failed.
func TestThePrimaryFeedSignatureFollowsTheHubItActuallyReads(t *testing.T) {
	const silenceSig = "tier-v2"

	oldHub := quietRelay(t)
	newHub := quietRelay(t)
	if oldHub.Port() == newHub.Port() {
		t.Fatal("precondition: two hubs on the same port")
	}

	// What a feed started INSIDE the window carries: the new settings-derived
	// signature, but the old hub underneath it.
	inWindow := primaryFeedSig(silenceSig, oldHub.Port())
	// What reconcileSelector asks for once the swap has happened.
	afterSwap := primaryFeedSig(silenceSig, newHub.Port())

	if inWindow == afterSwap {
		t.Fatal("a feed left reading the CLOSED silence tier carries the same upstream " +
			"signature as one reading the new tier, so reconcileSelector sees a match " +
			"and leaves it alone forever: the selector hub carries zero bytes, every " +
			"destination reads running, and nothing publishes")
	}
}

// The port must not become churn: a steady tier has to keep the same signature
// across sweeps, or every 500ms tick would respawn the feed.
func TestTheFeedSignatureIsStableWhileTheTierIs(t *testing.T) {
	h := quietRelay(t)
	a := primaryFeedSig("tier-v2", h.Port())
	b := primaryFeedSig("tier-v2", h.Port())
	if a != b {
		t.Error("the primary feed signature is not stable for an unchanged tier; the " +
			"selector sweep would tear the feed down and rebuild it twice a second")
	}
}

// And a settings change still moves it even when the hub happens not to.
func TestASettingsChangeStillMovesTheFeedSignature(t *testing.T) {
	h := quietRelay(t)
	if primaryFeedSig("tier-v2", h.Port()) == primaryFeedSig("tier-v3", h.Port()) {
		t.Error("a changed silence signature no longer moves the feed signature; the " +
			"feed would never be rebuilt onto a reconfigured tier")
	}
}

// The engine-level accessor has to answer for the tier that is actually up.
func TestSourceHubPortFollowsTheSilenceTier(t *testing.T) {
	ingest := quietRelay(t)
	tier := quietRelay(t)

	e := &Engine{hub: ingest}
	if got := e.sourceHubPort(); got != ingest.Port() {
		t.Errorf("with no silence tier, sourceHubPort = %d, want the ingest's %d", got, ingest.Port())
	}

	e.silence = &silenceTier{hub: tier}
	if got := e.sourceHubPort(); got != tier.Port() {
		t.Errorf("with a silence tier up, sourceHubPort = %d, want the tier's %d", got, tier.Port())
	}

	// Neither: the zero value, not a panic.
	empty := &Engine{}
	if got := empty.sourceHubPort(); got != 0 {
		t.Errorf("with no hub at all, sourceHubPort = %d, want 0", got)
	}
}
