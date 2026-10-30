package api

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/eventstream/streamtest"
	"github.com/rainmanjam/polyemesis/internal/hooks"
)

// The route-coverage ledger's view of GET /api/v1/events: a counterpart proof
// for its excuse, and inspectors for the two shapes it emits (the sse body and
// the X-Accel-Buffering header).
//
// The value sweep cannot read this route for the reason it cannot read /ws: the
// response does not end. The ledger fixture wires no events hub, so the sweep's
// GET is a 503 at once, which is what the excuse's Want observes; the proof
// below is what reads real stream bytes.

// sseBytes opens the stream on s with authz, runs publish once the handler has
// subscribed, and returns everything that arrived within window.
//
// s.stream is attached for the duration and detached afterwards, so the
// fixture the rest of the ledger shares is left exactly as it was found.
func sseBytes(t *testing.T, s *Server, authz string, window time.Duration, publish func(*eventstream.Hub)) (string, http.Header) {
	t.Helper()
	hub := eventstream.NewHub()
	prev := s.stream
	s.stream = hub
	defer func() { s.stream = prev }()

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", authz)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("open the event stream: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		_ = res.Body.Close()
		t.Fatalf("the event stream answered %d", res.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !hub.HasSubscribers() {
		if time.Now().After(deadline) {
			t.Fatal("the events handler never subscribed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	publish(hub)

	// Read until the window closes, then close the body to end the read.
	var b strings.Builder
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&b, res.Body); close(done) }()
	time.Sleep(window)
	_ = res.Body.Close()
	<-done
	return b.String(), res.Header
}

// plantEvents publishes, through the real hub, an event of every family that
// could carry the fixture's planted credential: chat (withheld from a read
// token), and a destination failure whose error and failure line are FFmpeg's
// words with the publish URL in them (redacted for everybody).
func plantEvents(h *eventstream.Hub) {
	url := "rtmp://a.rtmp.youtube.com/live2/" + sentinelDestKey
	h.Publish(eventstream.Envelope{Type: eventstream.TypeChatMessage,
		Data: map[string]string{"text": "my key is " + sentinelDestKey}})
	h.Publish(eventstream.Envelope{
		Type:        eventstream.TypeDestinationDown,
		Destination: &hooks.DestinationRef{ID: 1, Name: "ledger", Platform: "youtube"},
		Reason:      "stopped", Error: url + ": Input/output error",
	})
	h.Publish(eventstream.Envelope{Type: eventstream.TypeDestinationUp,
		Destination: &hooks.DestinationRef{ID: 1, Name: "ledger"}})
}

// proveEventStream is the excuse's counterpart. The positive control is the
// admin's expert view of the destination, which reads the stream key raw; the
// read token's stream, with that key planted in a withheld chat message and in
// a redacted error, must carry none of it.
func proveEventStream(t *testing.T) proofResult {
	h, _, sign := runningDestServer(t)
	s := serverUnderTest(t, h)
	read := createScopedToken(t, h, sign, "ledger-events", db.ScopeRead)
	body, _ := sseBytes(t, s, "Bearer "+read, 750*time.Millisecond, plantEvents)
	if !strings.Contains(body, "event: "+string(eventstream.TypeDestinationUp)) {
		t.Errorf("the read token's event stream delivered nothing the proof planted, so the "+
			"absence of the sentinel in it proves nothing:\n%s", truncateForFailure(body))
	}
	return proofResult{
		Pattern: patternDriven(t, h, http.MethodGet, "/api/v1/events"),
		High:    bodyOf(t, h, sign, "/api/v1/destinations/1/expert"),
		Read:    body,
	}
}

// inspectSSE reads a real event off a real stream on the shared rig, as the
// read principal, and requires it to be the typed envelope the docs promise:
// an id, an event line naming a catalogued type, and a data line whose body
// names the same type.
func inspectSSE(t *testing.T, rig shapeRig) shapeObservation {
	t.Helper()
	body, _ := sseBytes(t, rig.s, "Bearer "+rig.read, 500*time.Millisecond, plantEvents)
	r := bufio.NewReader(strings.NewReader(body))
	seen := 0
	for {
		f, ok := streamtest.ReadFrame(r)
		if !ok {
			break
		}
		seen++
		if _, catalogued := eventstream.Lookup(f.Env.Type); !catalogued || f.Event != string(f.Env.Type) || f.ID == "" {
			t.Errorf("the sse inspector read a frame that is not a typed, numbered, catalogued "+
				"event: id=%q event=%q body type=%q", f.ID, f.Event, f.Env.Type)
		}
	}
	if seen == 0 {
		t.Errorf("the sse inspector read no event at all:\n%s", truncateForFailure(body))
	}
	return shapeObservation{Shape: "sse", Sample: body}
}

// inspectXAccelBufferingHeader witnesses the header on a real stream response.
// It is the one that tells nginx not to buffer, and a stream a proxy buffers
// delivers nothing until the buffer fills.
func inspectXAccelBufferingHeader(t *testing.T, rig shapeRig) shapeObservation {
	t.Helper()
	_, header := sseBytes(t, rig.s, "Bearer "+rig.read, 50*time.Millisecond, func(*eventstream.Hub) {})
	return witnessHeader(t, "response-header/X-Accel-Buffering", "X-Accel-Buffering", "no", header)
}
