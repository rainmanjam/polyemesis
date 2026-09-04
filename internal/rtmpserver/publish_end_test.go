package rtmpserver

import (
	"testing"
	"time"
)

// A SUBSCRIBER MUST BE ENDED WHEN THE PUBLISH ENDS. #674
//
// Nothing used to tell them. A subscriber stayed attached to a stream that
// would never produce another byte, and the engine's ingest child -- exactly
// such a subscriber -- sat reading nothing. Measured on the acceptance rig: its
// out_time froze at 40021ms (the publisher's own duration) and it wrote ZERO
// bytes for eighty seconds. The relay hub it feeds dropped to ~6 packets/second,
// every destination on that hub starved together, and a destination created in
// that window could not characterise audio it was never sent.
//
// engine.go states the intent: "The ingest listener is expected to exit whenever
// the streamer stops, so it must come back fast." It could not exit, because the
// end of the publish never reached it.
func TestASubscriberIsEndedWhenThePublishEnds(t *testing.T) {
	st := &stream{subs: map[*subscriber]struct{}{}}
	a := &subscriber{done: make(chan struct{})}
	b := &subscriber{done: make(chan struct{})}
	st.subs[a] = struct{}{}
	st.subs[b] = struct{}{}

	// THE REAL TEARDOWN, not a copy of it.
	if n := endSubscribers(st); n != 2 {
		t.Fatalf("ended %d of 2 subscribers", n)
	}

	for i, sub := range []*subscriber{a, b} {
		select {
		case <-sub.done:
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d was not ended when the publish ended.\n\n"+
				"It stays attached to a stream that will never produce another byte. The "+
				"engine's ingest is such a subscriber: it then writes nothing while its "+
				"relay hub starves, and every destination on that hub fails to characterise "+
				"audio it was never sent. #674.", i)
		}
	}
}

// close() is called from several teardown paths and must stay idempotent: a
// double close panicked and took the daemon down, ending every live broadcast
// on the install at once. See #496.
// A nil stream is ordinary -- the publisher may have been the last thing
// holding it -- and must not panic the disconnect path.
func TestEndingSubscribersOfANilStreamIsSafe(t *testing.T) {
	if n := endSubscribers(nil); n != 0 {
		t.Fatalf("endSubscribers(nil) = %d, want 0", n)
	}
}

func TestEndingASubscriberTwiceIsSafe(t *testing.T) {
	sub := &subscriber{done: make(chan struct{})}
	sub.close()
	sub.close() // must not panic
	select {
	case <-sub.done:
	default:
		t.Fatal("done was not closed")
	}
}
