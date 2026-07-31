package alerts

import (
	"context"
	"sync"
	"testing"
	"time"
)

// SetRetry exists because the budget became an operator setting, and a setting
// that only takes effect on restart is one an operator changes, sees nothing
// happen, and changes again.
//
// Two properties matter and they pull in opposite directions: a saved change
// must reach the NEXT delivery, and it must not disturb one already in flight.

func TestSetRetryChangesTheBudgetForTheNextDelivery(t *testing.T) {
	// Five 500s available, so the attempt count is decided by the budget alone
	// rather than by running out of replies.
	doer := &fakeDoer{replies: []reply{
		{status: 500}, {status: 500}, {status: 500}, {status: 500}, {status: 500},
	}}
	n := New(quietLog(), RuleFunc(func() ([]Rule, error) { return nil, nil }),
		WithDoer(doer),
		WithRetry(4, time.Second, 3*time.Second),
		WithSleep(func(context.Context, time.Duration) bool { return true }))

	n.SetRetry(2)

	_ = n.post(context.Background(), Delivery{
		Rule:  testRule(),
		Items: []Item{{Event: downEvent("1", base), Count: 1, First: base, Last: base}},
	})

	if got := doer.count(); got != 2 {
		t.Errorf("post made %d attempts, want 2 -- SetRetry did not reach the next delivery", got)
	}
}

// The budget is read ONCE, before the retry loop. Lowering it while a delivery
// is mid-flight must not strand an attempt that has already been slept for, and
// the loop must not compare its counter against a number that is moving under
// it.
//
// This also guards the -race property: without the snapshot, the delivery
// goroutine reads n.attempts unsynchronised while a settings save writes it.
func TestSetRetryDoesNotDisturbADeliveryAlreadyInFlight(t *testing.T) {
	doer := &fakeDoer{replies: []reply{
		{status: 500}, {status: 500}, {status: 500}, {status: 500},
	}}

	var once sync.Once
	n := New(quietLog(), RuleFunc(func() ([]Rule, error) { return nil, nil }),
		WithDoer(doer),
		WithRetry(4, time.Second, 3*time.Second))

	// Change the budget from inside the first backoff, which is the narrowest
	// point at which a settings save can land during a delivery.
	n.sleep = func(context.Context, time.Duration) bool {
		once.Do(func() { n.SetRetry(1) })
		return true
	}

	_ = n.post(context.Background(), Delivery{
		Rule:  testRule(),
		Items: []Item{{Event: downEvent("1", base), Count: 1, First: base, Last: base}},
	})

	if got := doer.count(); got != 4 {
		t.Errorf("post made %d attempts, want 4 -- the in-flight delivery adopted a budget "+
			"that changed underneath it", got)
	}
}

func TestSetRetryIgnoresNonsense(t *testing.T) {
	n := New(quietLog(), RuleFunc(func() ([]Rule, error) { return nil, nil }),
		WithRetry(3, time.Second, time.Second))

	for _, bad := range []int{0, -1} {
		n.SetRetry(bad)
		if got := n.retryBudget(); got != 3 {
			t.Errorf("SetRetry(%d) changed the budget to %d; it must be ignored", bad, got)
		}
	}
}

// A nil Notifier is a real case, not defensive padding: Manager.SetAlertRetry
// walks every engine and an engine that has not built its notifier yet returns
// nil from Alerts().
func TestSetRetryToleratesANilNotifier(t *testing.T) {
	var n *Notifier
	n.SetRetry(3) // must not panic
}

// The Notifier is seeded from the same constant db.DefaultSettings uses. Pinned
// on this side too, so the pair is anchored rather than merely compared.
func TestNewUsesDefaultAttempts(t *testing.T) {
	n := New(quietLog(), RuleFunc(func() ([]Rule, error) { return nil, nil }))
	if got := n.retryBudget(); got != DefaultAttempts {
		t.Errorf("a Notifier built with no options has budget %d, want DefaultAttempts (%d)",
			got, DefaultAttempts)
	}
}
