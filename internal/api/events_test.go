package api

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/eventstream/streamtest"
	"github.com/rainmanjam/polyemesis/internal/failreason"
	"github.com/rainmanjam/polyemesis/internal/hooks"
)

// eventsServer is the real router with an events hub attached, served over a
// real listener: SSE is about what reaches the socket and when, which a
// ResponseRecorder cannot show.
func eventsServer(t *testing.T, opts ...eventstream.Option) (*httptest.Server, *Server, http.Handler, func(*http.Request), *eventstream.Hub) {
	t.Helper()
	h, _, sign := renditionServer(t, defaultTools())
	s := serverUnderTest(t, h)
	hub := eventstream.NewHub(opts...)
	s.stream = hub
	s.revokedMu.Lock()
	s.wsPingEvery = wsTestPing
	s.revokedMu.Unlock()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, s, h, sign, hub
}

// openEvents opens the stream with a bearer token and returns the response.
func openEvents(t *testing.T, srv *httptest.Server, query, token string, header http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/events"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

// waitForListener blocks until the handler has subscribed, so a publish in the
// test cannot race the connection it is meant for.
func waitForListener(t *testing.T, hub *eventstream.Hub) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !hub.HasSubscribers() {
		if time.Now().After(deadline) {
			t.Fatal("the events handler never subscribed")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestTheEventsStreamIsUnbufferedSSEWithTheWebhookEnvelope: the headers that
// keep a proxy from buffering, the preamble flushed BEFORE any event (a proxy or
// a server that buffered would deliver nothing until it had a buffer's worth),
// then one event in the SSE form with the webhook's flat failure fields and
// #20's whole failure reason.
func TestTheEventsStreamIsUnbufferedSSEWithTheWebhookEnvelope(t *testing.T) {
	srv, _, h, sign, hub := eventsServer(t)
	tok := createScopedToken(t, h, sign, "dash", db.ScopeAdmin)
	res := openEvents(t, srv, "", tok, nil)

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	for k, want := range map[string]string{
		"Content-Type":      "text/event-stream; charset=utf-8",
		"Cache-Control":     "no-cache, no-transform",
		"X-Accel-Buffering": "no",
	} {
		if got := res.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q; the stream must never be readable cross-origin", got)
	}

	r := bufio.NewReader(res.Body)
	first, err := readLineWithin(r, 2*time.Second)
	if err != nil || !strings.HasPrefix(first, "retry: ") {
		t.Fatalf("first line = %q (%v): the preamble was not flushed before any event", first, err)
	}

	waitForListener(t, hub)
	hub.Publish(eventstream.Envelope{
		Type:        eventstream.TypeDestinationDown,
		Source:      &hooks.SourceRef{ID: 1, Name: "Main"},
		Destination: &hooks.DestinationRef{ID: 3, Name: "YouTube", Platform: "youtube"},
		Reason:      "stopped", Cause: failreason.KeyRejected, Held: true, KeyChanged: true,
		Failure: &failreason.Reason{Cause: failreason.KeyRejected, Held: true, Rejections: 3},
	})
	f := streamtest.ExpectSSE(t, r, eventstream.TypeDestinationDown)
	if f.ID == "" || f.ID != f.Env.ID {
		t.Errorf("SSE id %q and body id %q must both be set and agree", f.ID, f.Env.ID)
	}
	if f.Env.SpecVersion != eventstream.SpecVersion || f.Env.Destination.Name != "YouTube" ||
		f.Env.Cause != failreason.KeyRejected || !f.Env.Held || !f.Env.KeyChanged ||
		f.Env.Failure == nil || f.Env.Failure.Rejections != 3 {
		t.Errorf("envelope = %+v", f.Env)
	}
}

func readLineWithin(r *bufio.Reader, d time.Duration) (string, error) {
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() { s, err := r.ReadString('\n'); ch <- res{s, err} }()
	select {
	case got := <-ch:
		return got.s, got.err
	case <-time.After(d):
		return "", io.ErrNoProgress
	}
}

// TestTheEventsStreamTakesTheAPIsOwnAuthAndNothingElse: no credential is a 401;
// a token in the query string is not a credential (there is no such parameter,
// and there must never be one); a session cookie works, because it is what
// EventSource sends.
func TestTheEventsStreamTakesTheAPIsOwnAuthAndNothingElse(t *testing.T) {
	srv, _, h, sign, _ := eventsServer(t)
	tok := createScopedToken(t, h, sign, "dash", db.ScopeAdmin)

	if res := openEvents(t, srv, "", "", nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("no credential: status %d, want 401", res.StatusCode)
	}
	for _, q := range []string{"?token=", "?access_token=", "?apiToken="} {
		if res := openEvents(t, srv, q+tok, "", nil); res.StatusCode != http.StatusUnauthorized {
			t.Errorf("credential in %s: status %d, want 401", q, res.StatusCode)
		}
	}

	req := jsonRequest(t, http.MethodGet, "/api/v1/events", nil)
	sign(req)
	header := http.Header{}
	for _, ck := range req.Cookies() {
		header.Add("Cookie", ck.Name+"="+ck.Value)
	}
	if res := openEvents(t, srv, "", "", header); res.StatusCode != http.StatusOK {
		t.Errorf("session cookie: status %d, want 200", res.StatusCode)
	}
}

// TestACrossOriginEventStreamIsRefused: another site cannot hold a stream open
// on the operator's cookie. Same rule as /ws's CheckOrigin.
func TestACrossOriginEventStreamIsRefused(t *testing.T) {
	srv, _, h, sign, _ := eventsServer(t)
	tok := createScopedToken(t, h, sign, "dash", db.ScopeAdmin)
	res := openEvents(t, srv, "", tok, http.Header{"Origin": {"https://evil.example"}})
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin: status %d, want 403", res.StatusCode)
	}
	same := "http://" + strings.TrimPrefix(srv.URL, "http://")
	if res := openEvents(t, srv, "", tok, http.Header{"Origin": {same}}); res.StatusCode != http.StatusOK {
		t.Errorf("same origin: status %d, want 200", res.StatusCode)
	}
}

// TestAnUnknownTopicIsABadRequest rather than a silent subscription to nothing.
func TestAnUnknownTopicIsABadRequest(t *testing.T) {
	srv, _, h, sign, _ := eventsServer(t)
	tok := createScopedToken(t, h, sign, "dash", db.ScopeAdmin)
	res := openEvents(t, srv, "?topics=destination,polls", tok, nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "destination") {
		t.Errorf("the 400 does not list the valid topics: %s", body)
	}
}

// TestAReadTokenIsNeverSentChatOnTheEventsStream: the third door to chat content
// gets the answer the other two give. Chat is published FIRST, so a build that
// sent it would have it arrive before the destination event the test waits on.
func TestAReadTokenIsNeverSentChatOnTheEventsStream(t *testing.T) {
	srv, _, h, sign, hub := eventsServer(t)
	tok := createScopedToken(t, h, sign, "monitor", db.ScopeRead)
	res := openEvents(t, srv, "", tok, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("a read token was refused the stream: %d", res.StatusCode)
	}
	waitForListener(t, hub)
	hub.Publish(eventstream.Envelope{Type: eventstream.TypeChatMessage, Data: map[string]string{"text": "a viewer's words"}})
	hub.Publish(eventstream.Envelope{Type: eventstream.TypeChatModeration, Data: map[string]string{"userId": "u1"}})
	hub.Publish(eventstream.Envelope{Type: eventstream.TypeDestinationUp})

	r := bufio.NewReader(res.Body)
	for {
		f, ok := streamtest.ReadFrame(r)
		if !ok {
			t.Fatal("the stream ended")
		}
		if spec, _ := eventstream.Lookup(f.Env.Type); spec.ReadScope == eventstream.Withheld {
			t.Fatalf("a read token was sent %s", f.Env.Type)
		}
		if f.Env.Type == eventstream.TypeDestinationUp {
			return
		}
	}
}

// TestLastEventIDResumesAndAnUnprovableResumeIsAGap: EventSource's reconnect
// header replays what was missed; an id the server cannot vouch for -- here one
// from another process -- gets everything held, preceded by stream.gap.
func TestLastEventIDResumesAndAnUnprovableResumeIsAGap(t *testing.T) {
	srv, _, h, sign, hub := eventsServer(t)
	tok := createScopedToken(t, h, sign, "dash", db.ScopeAdmin)

	sub, _, _ := hub.Subscribe(eventstream.Filter{}, "")
	hub.Publish(eventstream.Envelope{Type: eventstream.TypeIngestPublished})
	hub.Publish(eventstream.Envelope{Type: eventstream.TypeRecordingStarted})
	first := <-sub.C
	second := <-sub.C
	sub.Close()

	res := openEvents(t, srv, "", tok, http.Header{"Last-Event-ID": {first.ID}})
	r := bufio.NewReader(res.Body)
	f := streamtest.ExpectSSE(t, r, eventstream.TypeRecordingStarted)
	if f.ID != second.ID {
		t.Errorf("resumed with %s, want %s", f.ID, second.ID)
	}

	res = openEvents(t, srv, "", tok, http.Header{"Last-Event-ID": {"another-process-7"}})
	r = bufio.NewReader(res.Body)
	gap := streamtest.ExpectSSE(t, r, eventstream.TypeStreamGap)
	if gap.ID != "" {
		t.Errorf("stream.gap carried id %q; it is about this connection and must not move Last-Event-ID", gap.ID)
	}
	streamtest.ExpectSSE(t, r, eventstream.TypeIngestPublished)

	// The query form, for a client that cannot set the header.
	res = openEvents(t, srv, "?lastEventId="+first.ID, tok, nil)
	if f := streamtest.ExpectSSE(t, bufio.NewReader(res.Body), eventstream.TypeRecordingStarted); f.ID != second.ID {
		t.Errorf("?lastEventId resumed with %s, want %s", f.ID, second.ID)
	}
}

// TestRevokingATokenEndsItsOpenEventStream is #159 for this endpoint: the
// operator's lever after a leak has to reach the connection already open, within
// one heartbeat.
func TestRevokingATokenEndsItsOpenEventStream(t *testing.T) {
	srv, _, h, sign, hub := eventsServer(t)
	const name = "leaked"
	tok := createScopedToken(t, h, sign, name, db.ScopeRead)
	id := tokenIDByName(t, h, sign, name)
	res := openEvents(t, srv, "", tok, nil)
	waitForListener(t, hub)

	ended := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, res.Body); close(ended) }()

	r := jsonRequest(t, http.MethodDelete, "/api/v1/auth/tokens/"+strconv.FormatInt(id, 10), nil)
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	select {
	case <-ended:
	case <-time.After(10*wsTestPing + 3*time.Second):
		t.Fatal("a revoked token's event stream was still open")
	}
	if hub.HasSubscribers() {
		t.Error("the revoked stream's subscription was not released")
	}
}

// TestAHeartbeatKeepsAQuietStreamAlive: a comment every heartbeat, so a proxy
// with a read timeout does not cut an install where nothing is happening.
func TestAHeartbeatKeepsAQuietStreamAlive(t *testing.T) {
	srv, _, h, sign, _ := eventsServer(t)
	tok := createScopedToken(t, h, sign, "dash", db.ScopeAdmin)
	res := openEvents(t, srv, "", tok, nil)
	r := bufio.NewReader(res.Body)
	deadline := time.Now().Add(10*wsTestPing + 3*time.Second)
	for time.Now().Before(deadline) {
		line, err := readLineWithin(r, time.Until(deadline))
		if err != nil {
			break
		}
		if strings.TrimSpace(line) == ": heartbeat" {
			return
		}
	}
	t.Fatal("no heartbeat on a quiet stream")
}

// TestAClientThatStopsReadingIsDisconnected: the per-client bound, end to end.
// The client never reads; the server's writes back up into the socket; the
// handler stops draining its subscription; the hub's bounded buffer fills; the
// hub disconnects it. Nothing grows without bound and no other client pays.
func TestAClientThatStopsReadingIsDisconnected(t *testing.T) {
	srv, _, h, sign, hub := eventsServer(t, eventstream.WithClientBuffer(4))
	tok := createScopedToken(t, h, sign, "slow", db.ScopeAdmin)
	_ = openEvents(t, srv, "", tok, nil) // never read
	waitForListener(t, hub)

	big := strings.Repeat("x", 64<<10)
	deadline := time.Now().Add(20 * time.Second)
	for hub.Kicked() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("a client that never read was never disconnected")
		}
		hub.Publish(eventstream.Envelope{Type: eventstream.TypeIngestPublished, Reason: big})
	}
	if hub.HasSubscribers() {
		t.Error("the disconnected client is still counted as a subscriber")
	}
}

// TestAClientThatStopsReadingDoesNotHoldItsConnectionForever: the hub letting go
// is half of it. The server has no WriteTimeout -- it would end every stream --
// so the handler's per-write deadline is the only thing that unblocks a write
// to a client that has stopped reading. If it were a silent no-op (a writer
// wrapper without Unwrap makes http.ResponseController answer ErrNotSupported),
// the handler goroutine and the connection would be held for ever. Measured on
// the connection itself, not on the hub.
func TestAClientThatStopsReadingDoesNotHoldItsConnectionForever(t *testing.T) {
	// Parallel: it builds its own server and touches no package state. See
	// "THE PARALLEL SET" in parallel_test.go.
	t.Parallel()
	h, _, sign := renditionServer(t, defaultTools())
	s := serverUnderTest(t, h)
	hub := eventstream.NewHub(eventstream.WithClientBuffer(4))
	s.stream = hub
	tok := createScopedToken(t, h, sign, "stuck", db.ScopeAdmin)

	closed := make(chan struct{})
	srv := httptest.NewUnstartedServer(s.Handler())
	var once sync.Once
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateClosed {
			once.Do(func() { close(closed) })
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)

	// A raw connection with a tiny receive buffer that is never read.
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.(*net.TCPConn).SetReadBuffer(4096)
	_, _ = conn.Write([]byte("GET /api/v1/events HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer " + tok + "\r\n\r\n"))
	waitForListener(t, hub)

	big := strings.Repeat("x", 64<<10)
	for i := 0; hub.HasSubscribers() && i < 100_000; i++ {
		hub.Publish(eventstream.Envelope{Type: eventstream.TypeIngestPublished, Reason: big})
	}
	select {
	case <-closed:
	case <-time.After(writeWait + 10*time.Second):
		t.Fatalf("the server still held the connection %s after the client stopped reading; "+
			"the per-write deadline is not reaching the connection", writeWait+10*time.Second)
	}
}

// ------------------------------------------------- go-live, stop and the fault

// TestGoLiveAndStopReachTheEventsStream drives the lifecycle coordinator against
// the fake platform: a testing broadcast with bytes arriving is moved live, and
// a disabled destination's live broadcast is ended. Each confirmed transition
// is announced through the server's own announceBroadcastPhase.
func TestGoLiveAndStopReachTheEventsStream(t *testing.T) {
	hub := eventstream.NewHub()
	sub := streamtest.Subscribe(t, hub)
	s := &Server{stream: hub}

	monitorOn := true
	yt := &ytFake{status: "testing", streamStatus: "active", monitor: &monitorOn}
	c, _, _ := lifecycleFixture(t, yt, lifecycleTestRow(true, "testing"))
	c.announce = s.announceBroadcastPhase
	c.sweepOnce(context.Background(), sweepEverything)
	live := streamtest.Expect(t, sub, eventstream.TypeBroadcastLive)
	if live.Destination == nil || live.Destination.ID != 3 || live.Source == nil || live.Source.ID != 1 {
		t.Errorf("broadcast.live = %+v", live)
	}
	if d := live.Data.(map[string]any); d["broadcastId"] != "bcast-1" {
		t.Errorf("data = %v", d)
	}

	yt2 := &ytFake{status: "live", streamStatus: "active", monitor: &monitorOn}
	c2, _, _ := lifecycleFixture(t, yt2, lifecycleTestRow(false, "live"))
	c2.announce = s.announceBroadcastPhase
	c2.sweepOnce(context.Background(), sweepEverything)
	done := streamtest.Expect(t, sub, eventstream.TypeBroadcastComplete)
	if d := done.Data.(map[string]any); !strings.EqualFold(d["phase"].(string), "complete") {
		t.Errorf("broadcast.complete phase = %v", d["phase"])
	}
}

// TestARefusedTransitionIsNotAnnouncedAsGoLive: only what the platform
// CONFIRMED is announced. A refusal is a broadcast.fault, through
// escalateBroadcastFault, and never a broadcast.live.
func TestARefusedTransitionIsNotAnnouncedAsGoLive(t *testing.T) {
	hub := eventstream.NewHub()
	sub := streamtest.Subscribe(t, hub)
	s := &Server{stream: hub, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.auditSink = func(alerts.Event) {}

	monitorOff := false
	yt := &ytFake{status: "testing", streamStatus: "active", monitor: &monitorOff,
		refuseReason: "concurrentBroadcastsExceedLimit"}
	row := lifecycleTestRow(true, "testing")
	row.StreamKey = "super-secret-stream-key"
	c, _, faults := lifecycleFixture(t, yt, row)
	c.announce = s.announceBroadcastPhase
	c.sweepOnce(context.Background(), sweepEverything)
	if len(*faults) == 0 {
		t.Fatal("nothing was escalated; the platform's refusal was not seen")
	}
	for _, f := range *faults {
		s.escalateBroadcastFault(f)
	}
	fault := streamtest.Expect(t, sub, eventstream.TypeBroadcastFault)
	if fault.Destination == nil || fault.Destination.ID != 3 || strings.Contains(fault.Reason, row.StreamKey) {
		t.Errorf("broadcast.fault = %+v", fault)
	}
	streamtest.None(t, sub, eventstream.TypeBroadcastLive, 100*time.Millisecond)
}
