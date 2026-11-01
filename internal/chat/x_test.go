package chat

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// xChatStub serves the X endpoints the chat adapter calls. It records every
// request so a test can assert on the exact path, method and body, and it
// answers from the fixtures below. Nothing reaches api.x.com: the adapter's base
// is pointed at this server.
type xChatStub struct {
	mu    sync.Mutex
	calls []string // "METHOD path body"
	srv   *httptest.Server
	// streamLines is what GET /2/activity/stream writes, one JSON document per line.
	streamLines []string
	// streamDelay holds the stream open before its first line, so a test can prove the stream outlives a short
	// client timeout.
	streamDelay time.Duration
	// broadcasts is what GET /2/broadcasts answers.
	broadcasts string
}

func newXChatStub(t *testing.T) *xChatStub {
	t.Helper()
	st := &xChatStub{
		broadcasts: `{"data":[{"id":"B1run","state":"RUNNING"},{"id":"B0done","state":"ENDED"}]}`,
	}
	st.srv = httptest.NewServer(http.HandlerFunc(st.serve))
	t.Cleanup(st.srv.Close)
	return st
}

func (st *xChatStub) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	st.mu.Lock()
	st.calls = append(st.calls, r.Method+" "+r.URL.Path+" "+string(body))
	st.mu.Unlock()

	switch {
	case r.URL.Path == "/2/users/me":
		_, _ = io.WriteString(w, `{"data":{"id":"1234567890","username":"sundaypractice"}}`)
	case r.Method == http.MethodGet && r.URL.Path == "/2/broadcasts":
		_, _ = io.WriteString(w, st.broadcasts)
	case r.Method == http.MethodPost && r.URL.Path == "/2/activity/subscriptions":
		_, _ = io.WriteString(w, `{"data":{"subscription":{"subscription_id":"sub-42"}}}`)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/2/activity/subscriptions/"):
		_, _ = io.WriteString(w, `{"data":{"deleted":true}}`)
	case r.Method == http.MethodGet && r.URL.Path == "/2/activity/stream":
		w.Header().Set("Content-Type", "application/json")
		if st.streamDelay > 0 {
			time.Sleep(st.streamDelay)
		}
		for _, ln := range st.streamLines {
			_, _ = io.WriteString(w, ln+"\n")
		}
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/chat"):
		_, _ = io.WriteString(w, `{"data":{"success":true,"timestamp":"1761904800000000000"}}`)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/chat/mutes"):
		_, _ = io.WriteString(w, `{"data":{"muted":true}}`)
	case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/chat/mutes/"):
		_, _ = io.WriteString(w, `{"data":{"muted":false}}`)
	case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/chat/"):
		_, _ = io.WriteString(w, `{"data":{"deleted":true}}`)
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
	}
}

func (st *xChatStub) recorded() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]string(nil), st.calls...)
}

func (st *xChatStub) adapter(t *testing.T, now time.Time) *XAdapter {
	t.Helper()
	a, err := NewX(XConfig{
		AccountRef: "1234567890",
		Channel:    "sundaypractice",
		Token:      StaticToken("user-token"),
		AppBearer:  "app-bearer",
		APIBase:    st.srv.URL,
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewX: %v", err)
	}
	return a
}

// --------------------------------------------------------------------- send

// TestXSendRefusesMoreThan140RunesWithoutARequest: X's spec caps a chat message
// at 140 characters (SendBroadcastChatRequest.text, maxLength 140). A longer
// message is refused locally, so no request is spent on a certain refusal.
func TestXSendRefusesMoreThan140RunesWithoutARequest(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Unix(1761904800, 0))
	long := strings.Repeat("é", 141)
	if _, err := a.Send(context.Background(), long); err == nil {
		t.Fatal("a 141-character message was accepted")
	}
	if n := len(st.recorded()); n != 0 {
		t.Errorf("%d request(s) were made for an over-long message; there must be none", n)
	}
}

// TestXSendPostsToTheRunningBroadcast checks the path is the RUNNING broadcast,
// not the ENDED one listed first in some other account's history.
func TestXSendPostsToTheRunningBroadcast(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Unix(1761904800, 0))
	m, err := a.Send(context.Background(), "hello chat")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	var posted bool
	for _, c := range st.recorded() {
		if strings.HasPrefix(c, "POST /2/broadcasts/B1run/chat ") && strings.Contains(c, `"text":"hello chat"`) {
			posted = true
		}
	}
	if !posted {
		t.Errorf("no POST to /2/broadcasts/B1run/chat with the text; calls: %v", st.recorded())
	}
	// X answers with a nanosecond timestamp and no message id, so the echo carries
	// a synthesised id and is marked as ours. PROVISIONAL: the activity stream may
	// also deliver our own message; dedupe on the echo is unverified against X.
	if !m.Echo || m.Text != "hello chat" || m.Platform != db.PlatformX {
		t.Errorf("echo = %+v", m)
	}
}

// TestXSendWithNoRunningBroadcastSaysSo: a sending account that is not live has
// nowhere to post. The error names that, rather than a bare 404.
func TestXSendWithNoRunningBroadcastSaysSo(t *testing.T) {
	st := newXChatStub(t)
	st.broadcasts = `{"data":[{"id":"B0done","state":"ENDED"}]}`
	a := st.adapter(t, time.Unix(1761904800, 0))
	_, err := a.Send(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "no running") {
		t.Errorf("Send with nothing running = %v, want an error saying no broadcast is running", err)
	}
}

// ------------------------------------------------------------------- delete

// TestXDeleteRefusesANonNumericMessageIDWithoutARequest: X's message id is digits
// only (^[0-9]{1,19}$). Anything else is refused before a request.
func TestXDeleteRefusesANonNumericMessageIDWithoutARequest(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Unix(1761904800, 0))
	if err := a.Delete(context.Background(), "../../etc"); err == nil {
		t.Fatal("a non-numeric message id was accepted")
	}
	if n := len(st.recorded()); n != 0 {
		t.Errorf("%d request(s) were made for a bad id; there must be none", n)
	}
}

// TestXDeleteRemovesTheMessageFromTheRunningBroadcast: X's only moderation
// removal is DELETE /2/broadcasts/{id}/chat/{message_id}. There is no hide.
func TestXDeleteRemovesTheMessageFromTheRunningBroadcast(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Unix(1761904800, 0))
	if err := a.Delete(context.Background(), "1761904800123"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	want := "DELETE /2/broadcasts/B1run/chat/1761904800123 "
	found := false
	for _, c := range st.recorded() {
		if strings.HasPrefix(c, want) {
			found = true
		}
	}
	if !found {
		t.Errorf("no %q call; calls: %v", want, st.recorded())
	}
}

// ---------------------------------------------------------------- ban / mute

// TestXBanUsesAbsoluteMillisecondsForATimeout: X's mute takes end_at_ms, an
// absolute Unix time in milliseconds. A duration is converted at the call site.
// The unit is the trap: a seconds value here would mute someone until 1970.
func TestXBanUsesAbsoluteMillisecondsForATimeout(t *testing.T) {
	st := newXChatStub(t)
	now := time.Unix(1761904800, 0)
	a := st.adapter(t, now)
	if err := a.Ban(context.Background(), "555", 10*time.Minute, "spam"); err != nil {
		t.Fatalf("Ban: %v", err)
	}
	want := fmt.Sprintf(`"end_at_ms":"%d"`, now.Add(10*time.Minute).UnixMilli())
	var got string
	for _, c := range st.recorded() {
		if strings.HasPrefix(c, "POST /2/broadcasts/B1run/chat/mutes ") {
			got = c
		}
	}
	if got == "" {
		t.Fatalf("no mute POST; calls: %v", st.recorded())
	}
	if !strings.Contains(got, want) {
		t.Errorf("mute body = %q, want it to contain %s (an absolute millisecond timestamp)", got, want)
	}
	if strings.Contains(got, "reason") {
		t.Error("X's mute body has no reason field; sending one would be silently dropped or refused")
	}
}

// TestXPermanentBanOmitsEndAt: no end_at_ms means muted until unmuted.
func TestXPermanentBanOmitsEndAt(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Unix(1761904800, 0))
	if err := a.Ban(context.Background(), "555", 0, ""); err != nil {
		t.Fatalf("Ban: %v", err)
	}
	for _, c := range st.recorded() {
		if strings.HasPrefix(c, "POST /2/broadcasts/B1run/chat/mutes ") && strings.Contains(c, "end_at_ms") {
			t.Errorf("a permanent mute carried end_at_ms: %q", c)
		}
	}
}

// TestXUnbanDeletesTheMute maps Unban onto DELETE .../chat/mutes/{user_id}.
func TestXUnbanDeletesTheMute(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Unix(1761904800, 0))
	if err := a.Unban(context.Background(), "555"); err != nil {
		t.Fatalf("Unban: %v", err)
	}
	found := false
	for _, c := range st.recorded() {
		if strings.HasPrefix(c, "DELETE /2/broadcasts/B1run/chat/mutes/555 ") {
			found = true
		}
	}
	if !found {
		t.Errorf("no unmute call; calls: %v", st.recorded())
	}
}

// ------------------------------------------------------------------- read

// TestXRunWithoutAnAppBearerIsFatal: the activity stream is app-only (its spec
// security is the bearer scheme). Without that token the adapter cannot read
// chat, and reconnecting cannot fix that, so the error is Fatal and names it.
func TestXRunWithoutAnAppBearerIsFatal(t *testing.T) {
	st := newXChatStub(t)
	a, err := NewX(XConfig{
		AccountRef: "1234567890",
		Token:      StaticToken("user-token"),
		APIBase:    st.srv.URL,
	})
	if err != nil {
		t.Fatalf("NewX: %v", err)
	}
	err = a.Run(context.Background(), SinkFunc(func(Message) {}))
	if !IsFatal(err) {
		t.Fatalf("Run without a bearer = %v, want a Fatal error", err)
	}
	if !strings.Contains(err.Error(), "bearer") {
		t.Errorf("error %q does not name the missing bearer token", err)
	}
}

// TestXRunReadsBroadcastChatFromTheActivityStream is the read path end to end
// against the stub: subscribe as the user, stream as the app, deliver only the
// broadcast.chat events, and remove the subscription on exit.
func TestXRunReadsBroadcastChatFromTheActivityStream(t *testing.T) {
	st := newXChatStub(t)
	st.streamLines = []string{
		`{"data":{"event_type":"follow.follow","event_uuid":"1","created_at":"2026-10-31T12:00:00.000Z","payload":{}}}`,
		`{"data":{"event_type":"broadcast.chat","event_uuid":"2","created_at":"2026-10-31T12:00:01.000Z","payload":{"message_id":"1761904801000","broadcast_id":"B1run","message":"hi from chat","is_subscriber":false,"is_moderator":false,"author":{"data":{"id":"777","username":"viewer","name":"Viewer"}}}}}`,
	}
	a := st.adapter(t, time.Unix(1761904800, 0))

	var got []Message
	var mu sync.Mutex
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := a.Run(ctx, SinkFunc(func(m Message) {
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
		cancel() // one message is the whole assertion
	}))
	_ = err // the stream ends by cancel; the sink is what matters

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("delivered %d message(s), want exactly the one broadcast.chat event: %+v", len(got), got)
	}
	if got[0].Text != "hi from chat" || got[0].Author.ID != "777" || got[0].Author.Name != "Viewer" {
		t.Errorf("message = %+v", got[0])
	}
	if got[0].Platform != db.PlatformX || got[0].Account != "1234567890" {
		t.Errorf("platform/account = %q/%q", got[0].Platform, got[0].Account)
	}
	var subscribed, removed bool
	for _, c := range st.recorded() {
		if strings.HasPrefix(c, "POST /2/activity/subscriptions ") && strings.Contains(c, `"event_type":"broadcast.chat"`) {
			subscribed = true
		}
		if strings.HasPrefix(c, "DELETE /2/activity/subscriptions/sub-42 ") {
			removed = true
		}
	}
	if !subscribed {
		t.Error("the broadcast.chat subscription was never created")
	}
	if !removed {
		t.Error("the subscription was not removed on exit; it would keep running on X's side")
	}
}

// TestXRunKeepsTheActivityStreamOpenPastTheControlClientTimeout: the control client carries a 30 second
// whole-request Timeout, and Go applies that to reading the response body too. The activity stream is long-lived,
// so the same client cut it every 30 seconds, which tore the chat run down and rebuilt its subscription over and over.
// The stream must outlive the control client's timeout. The test uses a 50ms timeout and a stream that stays silent
// for 200ms before its first chat line, so the old client would drop the line.
func TestXRunKeepsTheActivityStreamOpenPastTheControlClientTimeout(t *testing.T) {
	st := newXChatStub(t)
	st.streamDelay = 200 * time.Millisecond
	st.streamLines = []string{
		`{"data":{"event_type":"broadcast.chat","event_uuid":"2","payload":{"message_id":"M9","broadcast_id":"B1run","message":"still here","author":{"data":{"id":"5","name":"Late"}}}}}`,
	}
	a := st.adapter(t, time.Unix(1761904800, 0))
	a.cfg.HTTP = &http.Client{Timeout: 50 * time.Millisecond}

	var got []Message
	var mu sync.Mutex
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = a.Run(ctx, SinkFunc(func(m Message) {
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
		cancel()
	}))

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].Text != "still here" {
		t.Fatalf("delivered %+v, want the chat line that arrives after the control timeout; the stream was cut", got)
	}
}

// TestXRunDeliversOnlyChatFromThisAccountsRunningBroadcast: the activity stream
// belongs to the whole app, not to one account, so a second connected X account,
// or a subscription left behind by an earlier run, puts other broadcasts' chat
// on the same stream. Stamping every event with this adapter's account would show
// another account's chat as this one's, and send its moderation to the wrong
// broadcast. Only chat for the broadcast this account is running may pass; an
// event that names no broadcast at all is dropped too.
func TestXRunDeliversOnlyChatFromThisAccountsRunningBroadcast(t *testing.T) {
	st := newXChatStub(t)
	st.streamLines = []string{
		`{"data":{"event_type":"broadcast.chat","event_uuid":"1","payload":{"message_id":"1761904801001","broadcast_id":"B9other","message":"from someone else's show","author":{"data":{"id":"1","name":"Other"}}}}}`,
		`{"data":{"event_type":"broadcast.chat","event_uuid":"2","payload":{"message_id":"1761904801002","message":"no broadcast named","author":{"data":{"id":"2","name":"Nobody"}}}}}`,
		`{"data":{"event_type":"broadcast.chat","event_uuid":"3","payload":{"message_id":"1761904801003","broadcast_id":"B1run","message":"mine","author":{"data":{"id":"3","name":"Mine"}}}}}`,
	}
	a := st.adapter(t, time.Unix(1761904800, 0))

	var got []Message
	var mu sync.Mutex
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = a.Run(ctx, SinkFunc(func(m Message) {
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
		if m.Text == "mine" {
			cancel()
		}
	}))

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].Text != "mine" {
		t.Fatalf("delivered %+v, want only the message from this account's running broadcast B1run", got)
	}
}

// TestXRunDoesNotLeakTheBearerIntoItsError: a refused stream must not carry the
// app token into an error an operator will read or log.
func TestXRunDoesNotLeakTheBearerIntoItsError(t *testing.T) {
	st := newXChatStub(t)
	a, err := NewX(XConfig{
		AccountRef: "1234567890",
		Token:      StaticToken("user-token"),
		AppBearer:  "bearer-canary-xyz",
		APIBase:    st.srv.URL,
	})
	if err != nil {
		t.Fatalf("NewX: %v", err)
	}
	err = a.Run(context.Background(), SinkFunc(func(Message) {}))
	if err != nil && strings.Contains(err.Error(), "bearer-canary-xyz") {
		t.Errorf("the app bearer appeared in the error: %v", err)
	}
}
