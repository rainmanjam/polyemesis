package chat

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
)

/* A TIMEOUT WITH NO DURATION BANNED A VIEWER FOR EVER.
 *
 * Banner's own contract is explicit: "A zero duration is PERMANENT; any positive
 * duration is a timeout." performAutomod used to pass finding.TimeoutSeconds
 * straight through, so a stored rule with ActionTimeout and no timeoutSeconds --
 * which validation used to allow -- became a permanent ban, and was logged as a
 * successful timeout.
 *
 * Validation now refuses to create such a rule. This covers the rules ALREADY
 * STORED, which no validation change can reach.
 */

// banningAdapter is a fakeAdapter that also satisfies Banner and records the
// duration it was handed.
type banningAdapter struct {
	*fakeAdapter
	mu    sync.Mutex
	bans  []time.Duration
	users []string
}

func (b *banningAdapter) Ban(_ context.Context, userID string, d time.Duration, _ string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bans = append(b.bans, d)
	b.users = append(b.users, userID)
	return nil
}

// Unban completes the Banner contract; automod never calls it.
func (b *banningAdapter) Unban(_ context.Context, _ string) error { return nil }

var _ Banner = (*banningAdapter)(nil)

func (b *banningAdapter) recorded() []time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]time.Duration(nil), b.bans...)
}

func automodFinding(a automod.Action, seconds int) automod.Verdict {
	f := automod.Finding{Checker: automod.CheckerRules, Action: a, TimeoutSeconds: seconds}
	return automod.Verdict{Findings: []automod.Finding{f}, Act: []automod.Finding{f}}
}

func runOneAction(t *testing.T, v automod.Verdict) *banningAdapter {
	t.Helper()
	h := testHub(t)
	inner := &fakeAdapter{
		platform: db.PlatformTwitch, account: "acct",
		messages: []Message{{ID: "m1", Author: Author{ID: "u1"}, Text: "hmm"}},
	}
	a := &banningAdapter{fakeAdapter: inner}
	h.SetModerator(&stubModerator{verdict: v})
	if err := h.Attach(context.Background(), a); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Only the ban is waited on. A successful ban RETRACTS the message, so
	// waiting for it to appear in history first is a race this test would lose
	// exactly when the code under test works.
	waitFor(t, "the action to be performed", func() bool { return len(a.recorded()) == 1 })
	return a
}

func TestATimeoutWithNoDurationIsNotAPermanentBan(t *testing.T) {
	a := runOneAction(t, automodFinding(automod.ActionTimeout, 0))
	got := a.recorded()[0]
	if got <= 0 {
		t.Fatalf("timeout ran for %v — Banner reads a zero duration as PERMANENT, "+
			"so a rule that asked for a timeout and carried no duration removed a "+
			"viewer for ever and reported it as a success", got)
	}
	if want := automod.DefaultTimeoutSeconds * time.Second; got != want {
		t.Errorf("timeout = %v, want the default %v", got, want)
	}
}

func TestAConfiguredTimeoutIsPassedThroughUnchanged(t *testing.T) {
	a := runOneAction(t, automodFinding(automod.ActionTimeout, 45))
	if got, want := a.recorded()[0], 45*time.Second; got != want {
		t.Errorf("timeout = %v, want the configured %v — the operator's duration "+
			"must not be replaced by the default", got, want)
	}
}

// A ban is still permanent, which is the whole reason the timeout path needs
// its own conversion.
func TestABanIsStillPermanent(t *testing.T) {
	a := runOneAction(t, automodFinding(automod.ActionBan, 0))
	if got := a.recorded()[0]; got != 0 {
		t.Errorf("ban ran for %v, want a zero duration — that is what every "+
			"adapter reads as permanent, and what the moderator UI sends", got)
	}
}
