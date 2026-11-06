package rtmpserver

import (
	"testing"
	"time"

	"github.com/bluenviron/gortmplib/pkg/message"
)

// A subscriber that cannot keep up is dropped from this message and counted,
// rather than allowed to block the publisher.
func TestFanOutDropsForASubscriberThatCannotKeepUp(t *testing.T) {
	s := New(quiet(), "127.0.0.1:0", nil)
	full := &subscriber{ch: make(chan message.Message, 1)}
	full.ch <- &message.Video{}
	ready := &subscriber{ch: make(chan message.Message, 1)}
	st := &stream{subs: map[*subscriber]struct{}{full: {}, ready: {}}}

	if !s.fanOut(st, &message.Video{}) {
		t.Fatal("fanOut dropped a publisher that has subscribers")
	}
	if full.dropped != 1 {
		t.Errorf("full subscriber dropped = %d, want 1", full.dropped)
	}
	if ready.dropped != 0 || len(ready.ch) != 1 {
		t.Errorf("ready subscriber dropped = %d with %d queued, want 0 and 1", ready.dropped, len(ready.ch))
	}
}

// Nobody has read the stream for longer than the grace: the publisher goes.
func TestFanOutDropsThePublisherAfterTheGrace(t *testing.T) {
	s := New(quiet(), "127.0.0.1:0", nil)
	st := &stream{subs: map[*subscriber]struct{}{}, emptySince: time.Now().Add(-subscriberGrace - time.Second)}
	if s.fanOut(st, &message.Video{}) {
		t.Fatal("fanOut kept a publisher nobody has read for longer than the grace")
	}
}
