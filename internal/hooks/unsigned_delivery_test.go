package hooks

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A HOOK WHOSE SIGNING SECRET WILL NOT OPEN SENDS NOTHING. #715.
//
// It used to send everything, unsigned. db.scanHook loaded the row with an
// empty Secret when box.Open failed, and attempt skips the signature header on
// an empty secret -- so restoring a backup with a different secret.key, or
// rotating it, silently turned every webhook into an unauthenticated POST.
//
// At the far end that is indistinguishable from a forgery. The signing secret
// was the only thing that made the delivery trustworthy, and its absence is
// invisible to the receiver: an endpoint that verifies signatures rejects it,
// and one that does not now accepts anything anybody sends it.
func TestAHookWhoseSecretWillNotOpenDeliversNothingRatherThanSomethingUnsigned(t *testing.T) {
	rec := &recorder{}
	broken := Hook{
		ID: 1, Name: "deploy", Enabled: true,
		URL: "https://example.com/h",
		// What db.scanHook sets when box.Open fails: the row intact, the secret
		// gone, and the reason carried alongside.
		SecretUnreadable: SecretUnreadableReason(),
	}.Normalized()

	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) {
		return []Hook{broken}, nil
	}), WithDoer(rec), WithReloadInterval(10*time.Millisecond))
	runDispatcher(t, d)

	// No worker is started for it at all: Validate refuses it, which is what
	// makes the queue it would otherwise sit in not exist.
	time.Sleep(150 * time.Millisecond)
	if d.HasHooks() {
		t.Error("a hook with an unreadable secret has a live worker; reload " +
			"accepted it, so anything published now is queued for delivery")
	}

	for i := 0; i < 5; i++ {
		d.Publish(Event{Trigger: TriggerIngestPublished, At: time.Now(), Source: SourceRef{ID: 1}})
	}
	time.Sleep(150 * time.Millisecond)
	if got := rec.seen(); len(got) != 0 {
		t.Fatalf("%d delivery/deliveries went out for a hook whose secret could "+
			"not be read. Every one of them is unsigned, and the receiver cannot "+
			"tell it from a forgery: %q", len(got), got)
	}
}

// THE GUARD UNDER THE GUARD. attempt is the one place the signature is decided,
// and both delivery paths -- the worker's and the operator's Test button --
// reach it. Refusing here is what makes an unsigned delivery unreachable rather
// than merely filtered out of reload's worker set.
//
// Driven through Test, because that path deliberately does NOT run Validate:
// an operator pressing "send test" on a hook must not be the way this hazard
// gets back in.
func TestTheTestButtonAlsoRefusesAHookWithNoReadableSecret(t *testing.T) {
	rec := &recorder{}
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return nil, nil }),
		WithDoer(rec))

	broken := Hook{
		ID: 2, Name: "manual", Enabled: true,
		URL:              "https://example.com/h",
		SecretUnreadable: SecretUnreadableReason(),
	}
	res, err := d.Test(context.Background(), broken, TriggerIngestPublished)
	if err == nil {
		t.Fatal("Test reported success for a hook whose secret could not be read; " +
			"an unsigned POST went out and the operator was told it worked")
	}
	if !errors.Is(err, ErrSecretUnreadable) {
		// Not fatal: the redaction pass rewrites the text. What matters is that
		// something was refused and the reason names the secret.
		t.Logf("Test error does not wrap ErrSecretUnreadable (redaction rewrites "+
			"it): %v", err)
	}
	if res.Signature != "" {
		t.Errorf("a signature was reported for a secret that could not be read: %q",
			res.Signature)
	}
	if got := rec.seen(); len(got) != 0 {
		t.Fatalf("the test delivery was sent anyway, unsigned: %q", got)
	}
}

// And the ordinary hook is untouched: a guard that also stops the working case
// is not a fix, it is an outage.
func TestAHookWithAReadableSecretStillDelivers(t *testing.T) {
	rec := &recorder{}
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return oneHook(), nil }),
		WithDoer(rec), WithReloadInterval(10*time.Millisecond))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })

	d.Publish(Event{Trigger: TriggerIngestPublished, At: time.Now(), Source: SourceRef{ID: 1}})
	waitFor(t, func() bool { return len(rec.seen()) > 0 })
}
