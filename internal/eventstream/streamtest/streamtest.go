// Package streamtest is how a test proves that a producer emits an event type.
//
// Expect (on a hub subscription) and ExpectSSE (on an HTTP response body) are
// the helpers for it, on purpose: TestEveryCatalogedTypeHasAn
// EmissionTest (internal/eventstream) finds every call to streamtest.Expect in
// (and ExpectSSE) in the repository's tests outside internal/eventstream itself, reads which
// eventstream.Type constant each one names, and fails for any catalogued type
// that no test outside the hub's own package expects. A test there publishes
// envelopes by hand and proves nothing about a producer; a test anywhere else
// has had to drive the code that really emits the type.
package streamtest

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/eventstream"
)

// Wait is how long Expect waits. Generous, because a failure costs only time
// and a flake costs trust in the guard.
const Wait = 5 * time.Second

// Subscribe opens an unfiltered admin-shaped subscription closed at cleanup.
func Subscribe(t testing.TB, h *eventstream.Hub) *eventstream.Subscription {
	t.Helper()
	sub, _, _ := h.Subscribe(eventstream.Filter{}, "")
	t.Cleanup(sub.Close)
	return sub
}

// Expect returns the next event of type typ on sub, skipping events of other
// types, and fails the test if none arrives within Wait.
func Expect(t testing.TB, sub *eventstream.Subscription, typ eventstream.Type) eventstream.Envelope {
	t.Helper()
	deadline := time.After(Wait)
	for {
		select {
		case env, ok := <-sub.C:
			if !ok {
				t.Fatalf("the subscription closed before a %s event arrived", typ)
			}
			if env.Type == typ {
				return env
			}
		case <-deadline:
			t.Fatalf("no %s event within %s", typ, Wait)
			return eventstream.Envelope{}
		}
	}
}

// None fails if any event of type typ arrives within d.
func None(t testing.TB, sub *eventstream.Subscription, typ eventstream.Type, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case env, ok := <-sub.C:
			if !ok {
				return
			}
			if env.Type == typ {
				t.Fatalf("unexpected %s event: %+v", typ, env)
			}
		case <-deadline:
			return
		}
	}
}

// Frame is one parsed SSE event.
type Frame struct {
	ID    string
	Event string
	Env   eventstream.Envelope
}

// ReadFrame reads the next event from an SSE body, skipping comments and the
// retry line. It returns ok=false at EOF.
func ReadFrame(r *bufio.Reader) (Frame, bool) {
	var f Frame
	have := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return f, false
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if have {
				return f, true
			}
		case strings.HasPrefix(line, ":"), strings.HasPrefix(line, "retry:"):
		case strings.HasPrefix(line, "id: "):
			f.ID, have = line[4:], true
		case strings.HasPrefix(line, "event: "):
			f.Event, have = line[7:], true
		case strings.HasPrefix(line, "data: "):
			_ = json.Unmarshal([]byte(line[6:]), &f.Env)
			have = true
		}
	}
}

// ExpectSSE reads frames from an SSE body until one of type typ, failing the
// test at EOF or after Wait. The caller's response body must be closed by the
// caller, which also ends a read this abandons on timeout.
func ExpectSSE(t testing.TB, r *bufio.Reader, typ eventstream.Type) Frame {
	t.Helper()
	got := make(chan Frame, 1)
	go func() {
		for {
			f, ok := ReadFrame(r)
			if !ok {
				close(got)
				return
			}
			if f.Event == string(typ) {
				got <- f
				return
			}
		}
	}()
	select {
	case f, ok := <-got:
		if !ok {
			t.Fatalf("the stream ended before a %s event", typ)
		}
		if f.Env.Type != typ {
			t.Fatalf("SSE event %q carried a body of type %q", f.Event, f.Env.Type)
		}
		return f
	case <-time.After(Wait):
		t.Fatalf("no %s event on the stream within %s", typ, Wait)
		return Frame{}
	}
}
