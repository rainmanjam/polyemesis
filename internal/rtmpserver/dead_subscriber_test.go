package rtmpserver

import (
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bluenviron/gortmplib/pkg/message"
)

// newSub builds a registered subscriber backed by a real socket pair, and hands
// back the client end so a test can kill the peer.
func newSub(t *testing.T, s *Server, key PublisherKey) (*subscriber, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	sub := &subscriber{
		ch:   make(chan message.Message, subscriberQueue),
		done: make(chan struct{}),
		conn: server,
	}
	s.mu.Lock()
	st := s.streams[key]
	if st == nil {
		st = &stream{subs: map[*subscriber]struct{}{}, slots: map[string]int{}}
		s.streams[key] = st
	}
	st.subs[sub] = struct{}{}
	s.mu.Unlock()
	t.Cleanup(func() { _ = client.Close(); sub.close() })
	return sub, client
}

// A subscriber whose peer goes away while NOTHING IS PUBLISHING must be
// noticed, or Ready lies about it.
//
// This is the one case the done channel did not cover. It closes on server Stop
// and when serveSubscriber's loop exits -- but with no publisher there are no
// writes to fail, so the loop parks forever and HasSubscriber, which counts map
// entries, kept reporting a reader that was a closed socket.
func TestASubscriberWhosePeerVanishesIsWokenWithNoPublisherLive(t *testing.T) {
	s := New(quiet(), "127.0.0.1:0", nil)
	sub, client := newSub(t, s, PublisherKey{SourceID: 1})

	if !s.HasSubscriber(1, false) {
		t.Fatal("precondition: the subscriber is not registered")
	}

	go sub.watchPeer()

	// The peer's FFmpeg exits. Nothing is publishing, so there is no write that
	// could fail: only the read side can tell us.
	_ = client.Close()

	select {
	case <-sub.done:
	case <-time.After(3 * time.Second):
		t.Fatal("a subscriber whose peer closed was never woken. With no publisher " +
			"live there are no writes to fail, so nothing else notices, and Ready " +
			"keeps reporting a dead reader as a live one -- which admits a publisher " +
			"into a stream nobody is reading")
	}
}

// The watchdog must not fire on a peer that is merely quiet, nor on one sending
// the acknowledgements a real RTMP client sends.
func TestAQuietOrChattySubscriberIsLeftAlone(t *testing.T) {
	s := New(quiet(), "127.0.0.1:0", nil)
	sub, client := newSub(t, s, PublisherKey{SourceID: 1})
	go sub.watchPeer()

	// Chatty: a window acknowledgement, which nothing here acts on.
	go func() { _, _ = client.Write(make([]byte, 16)) }()

	select {
	case <-sub.done:
		t.Fatal("a live subscriber was closed; a quiet or acknowledging peer is the " +
			"normal case and must not be reaped")
	case <-time.After(300 * time.Millisecond):
	}
}

// The watchdog has to be WIRED IN. Every test above calls watchPeer directly,
// so all of them would pass with the call site deleted -- which is exactly the
// mistake that produced the readiness-grace gap.
func TestThePeerWatchdogIsWiredIntoTheSubscriberPath(t *testing.T) {
	b, err := os.ReadFile("rtmpserver.go")
	if err != nil {
		t.Fatalf("read rtmpserver.go: %v", err)
	}
	if !strings.Contains(string(b), "go sub.watchPeer()") {
		t.Error("serveSubscriber no longer starts the peer watchdog. watchPeer exists " +
			"but nothing calls it, so a subscriber whose FFmpeg exited while nothing " +
			"was publishing stays parked forever and Ready keeps counting it")
	}
}
