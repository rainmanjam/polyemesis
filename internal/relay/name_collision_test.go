package relay

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// A NAME ALREADY IN USE IS REFUSED, AND THE CONSUMER HOLDING IT KEEPS RECEIVING.
// #711.
//
// The map assignment used to be bare: `h.subs[name] = &subscriber{...}`. The
// replaced consumer keeps running, keeps a correct command line, and keeps a
// green card on the monitoring page — and receives nothing. Nothing about the
// process, its target URL or its status reveals it.
//
// Worse, the hub logged "relay subscriber added" either way, so the log
// positively confirmed the wrong thing.
//
// Three devices existed to avoid the collision and all three were rung zero: a
// naming convention in destinations.go, a lock in engine.go, and a comment in
// setup.go. It had bitten twice. This is the sink refusing.
func TestATakenNameIsRefusedAndTheConsumerHoldingItStillReceives(t *testing.T) {
	h := newTestHub(t)

	first, firstPort := boundSubscriber(t)
	mustSubscribe(t, h, "dest:1", firstPort)

	// The collision: a second consumer, a different port, the same name.
	second, secondPort := boundSubscriber(t)
	url, err := h.Subscribe("dest:1", secondPort)
	if !errors.Is(err, ErrSubscriberExists) {
		t.Fatalf("Subscribe under a live name = %q, %v; want ErrSubscriberExists", url, err)
	}
	if url != "" {
		t.Errorf("a URL was handed out for a refused subscription: %q", url)
	}

	// THE PROPERTY THAT MATTERS. Not the error -- the delivery. The whole
	// failure was that the first consumer went quiet while looking healthy, so
	// this asserts against the socket rather than against the bookkeeping.
	payload := []byte("still yours")
	publish(t, h, payload, 3)
	waitForRx(t, h, 1)
	assertDelivered(t, "the consumer that holds the name", first, payload, 3)

	// And the refused one got nothing, which is the other half: a refusal that
	// still delivered would mean two processes reading one name.
	if got := h.Subscribers(); len(got) != 1 || got[0] != "dest:1" {
		t.Errorf("Subscribers() = %v, want exactly [dest:1]", got)
	}
	_ = second
}

// Unsubscribe with a name this hub does not have removes nothing and no longer
// says it removed something. #711's mirror.
//
// delete() on an absent key is a no-op and the log line said "relay subscriber
// removed" regardless — so a teardown naming the wrong subscriber reported
// success while leaving the real one forwarding into a process that is gone.
func TestUnsubscribingANameTheHubDoesNotHaveLeavesTheOthersAlone(t *testing.T) {
	// ITS OWN LOGGER, because the observable difference is the LOG LINE. Nothing
	// else changes: delete() on an absent key is a no-op either way, so a test
	// that only checks the subscriber set passes against the bug.
	var logged bytes.Buffer
	h, err := New(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})), 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	mustSubscribe(t, h, "dest:1", 5001)
	logged.Reset()

	h.Unsubscribe("dest:2") // never subscribed

	line := logged.String()
	if strings.Contains(line, "relay subscriber removed") {
		t.Errorf("the hub reported removing a subscriber it never had. A teardown "+
			"naming the wrong subscriber then reads as successful cleanup while the "+
			"real one goes on forwarding into a process that is gone:\n%s", line)
	}
	if !strings.Contains(line, "does not have") {
		t.Errorf("nothing was said about an Unsubscribe that removed nothing:\n%s", line)
	}

	got := h.Subscribers()
	if len(got) != 1 || got[0] != "dest:1" {
		t.Fatalf("Subscribers() = %v, want [dest:1]", got)
	}
	// And the real one still goes when it is named.
	h.Unsubscribe("dest:1")
	if got := h.Subscribers(); len(got) != 0 {
		t.Errorf("Subscribers() = %v, want none", got)
	}
}
