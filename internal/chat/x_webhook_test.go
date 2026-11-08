package chat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// xRun starts a.Run against sink and returns when the adapter is live (registered and subscribed), plus a stop that
// ends the run and returns its error. Live is read from the adapter, not guessed from a sleep.
func xRun(t *testing.T, a *XAdapter, sink Sink) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, sink) }()
	deadline := time.Now().Add(5 * time.Second)
	for !a.registered() {
		select {
		case err := <-done:
			cancel()
			t.Fatalf("Run ended before it was live: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("Run never became live")
		}
		time.Sleep(2 * time.Millisecond)
	}
	var once sync.Once
	var err error
	stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Error("Run did not return after cancel")
			}
		})
		return err
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

// xRunErr runs a.Run to its own end (a registration failure) and returns the error.
func xRunErr(t *testing.T, a *XAdapter) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.Run(ctx, SinkFunc(func(Message) {}))
}

func chatEnvelope(uuid, user, broadcast, msgID, text string) []byte {
	return []byte(fmt.Sprintf(`{"data":{"event_uuid":%q,"created_at":"2026-10-07T19:17:36.304Z","filter":{"user_id":%q},"event_type":"broadcast.chat","tag":"polyemesis","payload":{"broadcast_id":%q,"message_id":%q,"message":%q,"is_subscriber":false,"is_moderator":false,"reply_to":"1791387165736000000","author":{"data":{"id":"777","username":"viewer","name":"Viewer"}}}}}`,
		uuid, user, broadcast, msgID, text))
}

func removeEnvelope(uuid, user, broadcast, msgID string) []byte {
	return []byte(fmt.Sprintf(`{"data":{"event_uuid":%q,"filter":{"user_id":%q},"event_type":"broadcast.chat.remove","tag":"polyemesis","payload":{"broadcast_id":%q,"message_id":%q}}}`,
		uuid, user, broadcast, msgID))
}

// waitFor polls cond up to 5 seconds.
func xWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// ------------------------------------------------------------- the public address

func TestXRunWithNoPublicAddressIsFatalAndSaysWhy(t *testing.T) {
	st := newXChatStub(t)
	a, err := NewX(XConfig{
		AccountRef: "1234567890", Token: StaticToken("user-token"), AppBearer: "app-bearer", APIBase: st.srv.URL,
		WebhookProblem: "tls.hostname is not set",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = xRunErr(t, a)
	if !IsFatal(err) {
		t.Fatalf("Run with no public address = %v, want Fatal", err)
	}
	for _, want := range []string{"public HTTPS address", "tls.hostname is not set"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if n := len(st.recorded()); n != 0 {
		t.Errorf("%d request(s) were made with no usable address: %v", n, st.recorded())
	}
}

func TestXWebhookURLProblem(t *testing.T) {
	for _, c := range []struct {
		url, wantSub string
	}{
		{"https://polyemesis.example.com/api/v1/webhooks/x", ""},
		{"http://polyemesis.example.com/api/v1/webhooks/x", "HTTPS"},
		{"https://polyemesis.example.com:8443/api/v1/webhooks/x", "port"},
		{"https://polyemesis.example.com:443/api/v1/webhooks/x", "port"},
		{"https://" + strings.Repeat("a", 190) + ".example.com/api/v1/webhooks/x", "200"},
		{"not a url", "HTTPS"},
		{"", "no public address"},
	} {
		got := xWebhookURLProblem(c.url)
		if c.wantSub == "" && got != "" || c.wantSub != "" && !strings.Contains(got, c.wantSub) {
			t.Errorf("xWebhookURLProblem(%q) = %q, want %q", c.url, got, c.wantSub)
		}
	}
}

func TestXRunRefusesAnUnusableWebhookURLWithoutARequest(t *testing.T) {
	st := newXChatStub(t)
	a, _ := NewX(XConfig{AccountRef: "1", Token: StaticToken("user-token"), AppBearer: "app-bearer", APIBase: st.srv.URL,
		WebhookURL: "https://polyemesis.example.com:8443/api/v1/webhooks/x"})
	err := xRunErr(t, a)
	if !IsFatal(err) || !strings.Contains(err.Error(), "port") {
		t.Fatalf("Run with a ported URL = %v, want a Fatal error naming the port", err)
	}
	if n := len(st.recorded()); n != 0 {
		t.Errorf("requests were made for a URL X rejects by rule: %v", st.recorded())
	}
}

// ------------------------------------------------------------- registration

// TestXRunCreatesTheWebhookAndBothSubscriptions: nothing registered yet. The webhook is created with the APP bearer,
// the two subscriptions with the USER token (the stub refuses the wrong credential), each carrying the webhook id,
// the account's user id, and the polyemesis tag. They are removed with the user token on a clean stop, and the
// webhook is left in place (it is shared by every X account and reused on the next start).
func TestXRunCreatesTheWebhookAndBothSubscriptions(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Unix(1761904800, 0))
	stop := xRun(t, a, SinkFunc(func(Message) {}))

	whs := st.webhookList()
	if len(whs) != 1 || whs[0].URL != xStubHookURL {
		t.Fatalf("webhooks = %+v, want exactly one at %s", whs, xStubHookURL)
	}
	subs := st.subList()
	if len(subs) != 2 {
		t.Fatalf("subscriptions = %+v, want broadcast.chat and broadcast.chat.remove", subs)
	}
	seen := map[string]bool{}
	for _, s := range subs {
		seen[fmt.Sprint(s["event_type"])] = true
		if s["webhook_id"] != whs[0].ID || s["tag"] != "polyemesis" || fmt.Sprint(s["filter"]) != "map[user_id:1234567890]" {
			t.Errorf("subscription %+v does not carry the webhook id, tag and user id", s)
		}
	}
	if !seen["broadcast.chat"] || !seen["broadcast.chat.remove"] {
		t.Errorf("event types subscribed = %v", seen)
	}
	if n := st.countCalls("GET /2/activity/stream"); n != 0 {
		t.Errorf("the old activity stream was opened %d time(s)", n)
	}

	if err := stop(); err != nil {
		t.Errorf("Run after a clean stop = %v, want nil", err)
	}
	// A Run's exit never deletes subscriptions: they persist per app and user and the next Run reuses them.
	// Deleting happens on an explicit disconnect only (RemoveSubscriptions).
	if left := st.subList(); len(left) != 2 {
		t.Errorf("subscriptions after a clean stop = %+v, want both left in place for the next run", left)
	}
	if got := st.webhookList(); len(got) != 1 {
		t.Errorf("the shared webhook was deleted on stop: %+v", got)
	}
	if n := st.countCalls("DELETE /2/webhooks"); n != 0 {
		t.Errorf("DELETE /2/webhooks called %d time(s)", n)
	}
}

func TestXRunReusesAMatchingValidWebhook(t *testing.T) {
	st := newXChatStub(t)
	st.webhooks = []xStubWebhook{
		{ID: "55", URL: "https://other.example.org/hook", Valid: true},
		{ID: "56", URL: xStubHookURL, Valid: true},
	}
	a := st.adapter(t, time.Unix(1761904800, 0))
	xRun(t, a, SinkFunc(func(Message) {}))
	if n := st.countCalls("POST /2/webhooks"); n != 0 {
		t.Errorf("a webhook was created although one matched: %v", st.recorded())
	}
	if n := st.countCalls("PUT /2/webhooks"); n != 0 {
		t.Errorf("a valid webhook was re-validated")
	}
	for _, s := range st.subList() {
		if s["webhook_id"] != "56" {
			t.Errorf("subscription %+v is on the wrong webhook; want 56", s)
		}
	}
}

func TestXRunRevalidatesAnInvalidWebhookWithPUTThenReLists(t *testing.T) {
	st := newXChatStub(t)
	st.webhooks = []xStubWebhook{{ID: "56", URL: xStubHookURL, Valid: false}}
	a := st.adapter(t, time.Unix(1761904800, 0))
	xRun(t, a, SinkFunc(func(Message) {}))
	if n := st.countCalls("PUT /2/webhooks/56"); n != 1 {
		t.Errorf("PUT /2/webhooks/56 = %d, want 1; calls %v", n, st.recorded())
	}
	if n := st.countCalls("POST /2/webhooks"); n != 0 {
		t.Errorf("a second webhook was created for an invalid one")
	}
	// PUT's data.valid only says the CRC was triggered, so the adapter lists again to read the verdict.
	if n := st.countCalls("GET /2/webhooks"); n != 2 {
		t.Errorf("GET /2/webhooks = %d, want 2 (before and after the PUT)", n)
	}
}

func TestXRunReportsAWebhookThatStaysInvalid(t *testing.T) {
	st := newXChatStub(t)
	st.putFixes = false
	st.webhooks = []xStubWebhook{{ID: "56", URL: xStubHookURL, Valid: false}}
	a := st.adapter(t, time.Unix(1761904800, 0))
	err := xRunErr(t, a)
	if err == nil || IsFatal(err) {
		t.Fatalf("Run = %v, want a retryable error (the address may be fixed in a moment)", err)
	}
	for _, want := range []string{"public HTTPS address X can reach", "webhook CRC failed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if n := st.countCalls("POST /2/activity/subscriptions"); n != 0 || len(st.subList()) != 0 {
		t.Errorf("subscriptions were created on a dead webhook")
	}
}

func TestXRunReportsACreateWhoseCRCFailed(t *testing.T) {
	for name, tweak := range map[string]func(*xChatStub){
		"valid:false in the answer": func(st *xChatStub) { st.createValid = false },
		"an error from X": func(st *xChatStub) {
			st.createStatus = 400
			st.createBody = `{"title":"Invalid Request","detail":"CrcValidationFailed: timed out"}`
		},
	} {
		t.Run(name, func(t *testing.T) {
			st := newXChatStub(t)
			tweak(st)
			a := st.adapter(t, time.Unix(1761904800, 0))
			err := xRunErr(t, a)
			if err == nil || IsFatal(err) {
				t.Fatalf("Run = %v, want a retryable error", err)
			}
			for _, want := range []string{"public HTTPS address X can reach", "webhook CRC failed"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "app-bearer") {
				t.Errorf("the app bearer leaked into the error: %v", err)
			}
		})
	}
}

// TestXRunTwoAccountsCreateTheWebhookOnce: every connected X account runs this registration against the same app and
// the same URL. Without serialisation each lists, finds nothing, and creates, which leaves duplicates (or an X
// refusal for the second).
func TestXRunTwoAccountsCreateTheWebhookOnce(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapterFor(t, "1234567890", time.Unix(1761904800, 0))
	b := st.adapterFor(t, "999", time.Unix(1761904800, 0))
	var wg sync.WaitGroup
	for _, ad := range []*XAdapter{a, b} {
		wg.Add(1)
		go func(ad *XAdapter) {
			defer wg.Done()
			xRun(t, ad, SinkFunc(func(Message) {}))
		}(ad)
	}
	wg.Wait()
	if n := st.countCalls("POST /2/webhooks"); n != 1 {
		t.Errorf("POST /2/webhooks = %d, want exactly 1", n)
	}
}

// TestXRunReusesAWebhookACreateRaceLeftBehind: the create is refused (another process won the race), but the webhook
// now exists, so one more list finds it and the run goes on.
func TestXRunReusesAWebhookACreateRaceLeftBehind(t *testing.T) {
	st := newXChatStub(t)
	st.createStatus = 409
	st.createRaces = true
	a := st.adapter(t, time.Unix(1761904800, 0))
	xRun(t, a, SinkFunc(func(Message) {}))
	if len(st.webhookList()) != 1 {
		t.Errorf("webhooks = %+v", st.webhookList())
	}
}

func TestXRunReusesMatchingSubscriptionsAndLeavesThemOnStop(t *testing.T) {
	st := newXChatStub(t)
	st.webhooks = []xStubWebhook{{ID: "56", URL: xStubHookURL, Valid: true}}
	st.subs = []map[string]any{
		{"subscription_id": "7001", "event_type": "broadcast.chat", "filter": map[string]any{"user_id": "1234567890"}, "webhook_id": "56", "tag": "polyemesis"},
		{"subscription_id": "7002", "event_type": "broadcast.chat.remove", "filter": map[string]any{"user_id": "1234567890"}, "webhook_id": "56", "tag": "polyemesis"},
		// someone else's, on another user, which must be left alone.
		{"subscription_id": "7003", "event_type": "broadcast.chat", "filter": map[string]any{"user_id": "42"}, "webhook_id": "56", "tag": "polyemesis"},
	}
	a := st.adapter(t, time.Unix(1761904800, 0))
	stop := xRun(t, a, SinkFunc(func(Message) {}))
	if n := st.countCalls("POST /2/activity/subscriptions"); n != 0 {
		t.Errorf("subscriptions were created although matching ones exist: %v", st.recorded())
	}
	_ = stop()
	if left := st.subList(); len(left) != 3 {
		t.Errorf("after stop, subscriptions = %+v; want all three untouched", left)
	}
}

func TestXRunTreatsAnExpiredSubscriptionAsAbsent(t *testing.T) {
	st := newXChatStub(t)
	st.webhooks = []xStubWebhook{{ID: "56", URL: xStubHookURL, Valid: true}}
	st.subs = []map[string]any{
		{"subscription_id": "7001", "event_type": "broadcast.chat", "filter": map[string]any{"user_id": "1234567890"}, "webhook_id": "56", "tag": "polyemesis", "expires_at": "2020-01-01T00:00:00.000Z"},
	}
	a := st.adapter(t, time.Unix(1761904800, 0))
	xRun(t, a, SinkFunc(func(Message) {}))
	if n := st.countCalls("POST /2/activity/subscriptions"); n != 2 {
		t.Errorf("subscription creates = %d, want 2 (the expired one is no subscription)", n)
	}
}

// TestXRunReplacesAPolyemesisSubscriptionOnAStaleWebhook: our own subscription pointing at a webhook that no longer
// is the current one would keep the new one from ever being created, so it is removed first.
func TestXRunReplacesAPolyemesisSubscriptionOnAStaleWebhook(t *testing.T) {
	st := newXChatStub(t)
	st.webhooks = []xStubWebhook{{ID: "56", URL: xStubHookURL, Valid: true}}
	st.subs = []map[string]any{
		{"subscription_id": "7001", "event_type": "broadcast.chat", "filter": map[string]any{"user_id": "1234567890"}, "webhook_id": "9999", "tag": "polyemesis"},
	}
	a := st.adapter(t, time.Unix(1761904800, 0))
	xRun(t, a, SinkFunc(func(Message) {}))
	if n := st.countCalls("DELETE /2/activity/subscriptions/7001"); n != 1 {
		t.Errorf("the stale subscription was not removed: %v", st.recorded())
	}
	for _, s := range st.subList() {
		if s["webhook_id"] != "56" {
			t.Errorf("subscription %+v is still on a stale webhook", s)
		}
	}
}

func TestXRunRefusesASubscriptionWithNoID(t *testing.T) {
	st := newXChatStub(t)
	st.subscription = `{"data":{"subscription":{}}}`
	a := st.adapter(t, time.Unix(1761904800, 0))
	err := xRunErr(t, a)
	if err == nil || !IsFatal(err) {
		t.Fatalf("Run with an empty subscription_id = %v, want a Fatal error", err)
	}
}

func TestXRunDoesNotLeakTheBearerIntoItsError(t *testing.T) {
	st := newXChatStub(t)
	a, _ := NewX(XConfig{AccountRef: "1", Token: StaticToken("user-token"), AppBearer: "bearer-canary-xyz",
		APIBase: st.srv.URL, WebhookURL: xStubHookURL})
	if err := xRunErr(t, a); err == nil || strings.Contains(err.Error(), "bearer-canary-xyz") {
		t.Errorf("want an error that does not carry the bearer, got %v", err)
	}
}

// ------------------------------------------------------------- events

func collect(t *testing.T) (*recordingSink, func(n int)) {
	t.Helper()
	s := &recordingSink{}
	return s, func(n int) {
		t.Helper()
		xWaitFor(t, fmt.Sprintf("%d delivered message(s)", n), func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return len(s.messages) >= n
		})
	}
}

func TestXWebhookChatEventReachesTheSinkAsAMessage(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC))
	sink, wait := collect(t)
	xRun(t, a, sink)

	if got := a.IngestWebhook(chatEnvelope("u-1", "1234567890", "1mGPaZaAdNqJN", "2090000000000000004", "hi from chat")); got != XIngestQueued {
		t.Fatalf("IngestWebhook = %v, want queued", got)
	}
	wait(1)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	m := sink.messages[0]
	if m.ID != "2090000000000000004" || m.Text != "hi from chat" || m.Author.ID != "777" || m.Author.Name != "Viewer" || m.ReplyToID != "1791387165736000000" {
		t.Errorf("message = %+v", m)
	}
	if m.Platform != db.PlatformX || m.Account != "1234567890" || m.Channel != "sundaypractice" {
		t.Errorf("platform/account/channel = %q/%q/%q", m.Platform, m.Account, m.Channel)
	}
	if want := time.Date(2026, 10, 7, 19, 17, 36, 304000000, time.UTC); !m.At.Equal(want) {
		t.Errorf("At = %v, want the event's created_at %v", m.At, want)
	}
}

func TestXWebhookDedupesByEventUUIDAndDropsEmptyText(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Unix(1761904800, 0))
	sink, wait := collect(t)
	xRun(t, a, sink)

	for i := 0; i < 3; i++ { // X retries and replays
		a.IngestWebhook(chatEnvelope("u-dup", "1234567890", "B", "2090000000000000005", "once"))
	}
	a.IngestWebhook(chatEnvelope("u-blank", "1234567890", "B", "2090000000000000006", "   "))
	a.IngestWebhook(chatEnvelope("u-2", "1234567890", "B", "2090000000000000007", "second"))
	wait(2)
	time.Sleep(30 * time.Millisecond)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.messages) != 2 || sink.messages[0].Text != "once" || sink.messages[1].Text != "second" {
		t.Errorf("delivered %+v, want [once second]", sink.messages)
	}
}

func TestXWebhookDedupeIsBounded(t *testing.T) {
	s := newXSeen(3)
	for i := 0; i < 10; i++ {
		s.add(fmt.Sprint("u", i))
	}
	if s.len() != 3 {
		t.Errorf("seen holds %d ids, want the bound 3", s.len())
	}
	if s.has("u0") || !s.has("u9") {
		t.Errorf("eviction order wrong: u0=%v u9=%v", s.has("u0"), s.has("u9"))
	}
	// A hit refreshes: u7 survives two more inserts, u8 does not.
	s.has("u7")
	s.add("a")
	s.add("b")
	if !s.has("u7") || s.has("u8") {
		t.Errorf("a looked-up id was not kept: u7=%v u8=%v", s.has("u7"), s.has("u8"))
	}
}

// TestXWebhookRemoveEventRetractsTheMessage: broadcast.chat.remove carries the message_id of the message delivered
// earlier, which is the id the hub stored it under, so Retract removes it from the pane.
func TestXWebhookRemoveEventRetractsTheMessage(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Unix(1761904800, 0))
	sink, _ := collect(t)
	xRun(t, a, sink)
	if got := a.IngestWebhook(removeEnvelope("u-r", "1234567890", "B", "2090000000000000004")); got != XIngestQueued {
		t.Fatalf("IngestWebhook(remove) = %v", got)
	}
	xWaitFor(t, "a retraction", func() bool { r, _ := sink.snapshot(); return len(r) == 1 })
	if r, _ := sink.snapshot(); r[0] != "2090000000000000004" {
		t.Errorf("retracted %v", r)
	}
	if len(sink.messages) != 0 {
		t.Errorf("a remove event delivered a message: %+v", sink.messages)
	}
}

// TestXWebhookNeverCrossesAccounts: one webhook serves every connected X account. An event is this adapter's only
// when its filter.user_id is the id Run resolved for the account.
func TestXWebhookNeverCrossesAccounts(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Unix(1761904800, 0))
	sink, _ := collect(t)
	xRun(t, a, sink)
	if got := a.IngestWebhook(chatEnvelope("u-x", "555", "B", "2090000000000000008", "for someone else")); got != XIngestWrongAccount {
		t.Fatalf("IngestWebhook for another user = %v, want wrong-account", got)
	}
	time.Sleep(30 * time.Millisecond)
	if len(sink.messages) != 0 {
		t.Errorf("another account's chat reached this sink: %+v", sink.messages)
	}
}

func TestXWebhookIngestStates(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Unix(1761904800, 0))
	if got := a.IngestWebhook(chatEnvelope("u", "1234567890", "B", "1", "x")); got != XIngestNotRunning {
		t.Errorf("before Run = %v, want not-running", got)
	}
	stop := xRun(t, a, SinkFunc(func(Message) {}))
	for name, body := range map[string][]byte{
		"not json":    []byte(`nope`),
		"no data":     []byte(`{"x":1}`),
		"no uuid":     []byte(`{"data":{"event_type":"broadcast.chat","filter":{"user_id":"1234567890"},"payload":{}}}`),
		"no user":     []byte(`{"data":{"event_uuid":"1","event_type":"broadcast.chat","payload":{}}}`),
		"empty array": []byte(`[]`),
	} {
		if got := a.IngestWebhook(body); got != XIngestMalformed {
			t.Errorf("%s = %v, want malformed", name, got)
		}
	}
	other := []byte(`{"data":{"event_uuid":"9","filter":{"user_id":"1234567890"},"event_type":"follow.follow","payload":{}}}`)
	if got := a.IngestWebhook(other); got != XIngestIgnored {
		t.Errorf("other event type = %v, want ignored", got)
	}
	_ = stop()
	if got := a.IngestWebhook(chatEnvelope("u2", "1234567890", "B", "1", "x")); got != XIngestNotRunning {
		t.Errorf("after stop = %v, want not-running", got)
	}
}

// TestXWebhookFullInboxDropsInsteadOfBlocking: the handler must answer X within 10 seconds however slow the sink is.
func TestXWebhookFullInboxDropsInsteadOfBlocking(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Unix(1761904800, 0))
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	xRun(t, a, SinkFunc(func(Message) { <-release }))
	deadline := time.After(3 * time.Second)
	full := false
	for i := 0; i < xInboxSize*2 && !full; i++ {
		select {
		case <-deadline:
			t.Fatal("IngestWebhook blocked")
		default:
		}
		full = a.IngestWebhook(chatEnvelope(fmt.Sprint("u", i), "1234567890", "B", fmt.Sprint(1000+i), "m")) == XIngestFull
	}
	if !full {
		t.Error("a never-draining sink never reported a full inbox")
	}
	once.Do(func() { close(release) })
}

func TestXWebhookUser(t *testing.T) {
	if got := XWebhookUser(chatEnvelope("u", "1234567890", "B", "1", "x")); got != "1234567890" {
		t.Errorf("XWebhookUser = %q", got)
	}
	for _, b := range []string{``, `{`, `{"data":{}}`, `[]`} {
		if got := XWebhookUser([]byte(b)); got != "" {
			t.Errorf("XWebhookUser(%q) = %q", b, got)
		}
	}
}

// ------------------------------------------------------------- review round: ordering, ownership, errors

// TestXRunIsReadyToReceiveBeforeTheSubscriptionsExist: X starts delivering the moment a subscription exists, so the
// inbox must already be open then, or the first events are answered into the void.
func TestXRunIsReadyToReceiveBeforeTheSubscriptionsExist(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Unix(1761904800, 0))
	var mu sync.Mutex
	var verdicts []XIngest
	st.onSubscribe = func() {
		mu.Lock()
		defer mu.Unlock()
		verdicts = append(verdicts, a.IngestWebhook(chatEnvelope(fmt.Sprint("early-", len(verdicts)), "1234567890", "B", "2090000000000000020", "early")))
	}
	xRun(t, a, SinkFunc(func(Message) {}))
	mu.Lock()
	defer mu.Unlock()
	if len(verdicts) == 0 {
		t.Fatal("no subscription was created")
	}
	for i, v := range verdicts {
		if v != XIngestQueued {
			t.Errorf("event %d arriving as subscription %d was created = %v, want queued", i, i+1, v)
		}
	}
}

func TestXIngestDuplicateIsKnownEvenWhenNotRunning(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Unix(1761904800, 0))
	sink, wait := collect(t)
	stop := xRun(t, a, sink)
	a.IngestWebhook(chatEnvelope("u-d", "1234567890", "B", "2090000000000000030", "once"))
	wait(1)
	if got := a.IngestWebhook(chatEnvelope("u-d", "1234567890", "B", "2090000000000000030", "once")); got != XIngestDuplicate {
		t.Errorf("replay while live = %v, want duplicate", got)
	}
	_ = stop()
	if got := a.IngestWebhook(chatEnvelope("u-d", "1234567890", "B", "2090000000000000030", "once")); got != XIngestDuplicate {
		t.Errorf("replay after stop = %v, want duplicate (already delivered, nothing to redeliver)", got)
	}
	if got := a.IngestWebhook(chatEnvelope("u-new", "1234567890", "B", "2090000000000000031", "new")); got != XIngestNotRunning {
		t.Errorf("a new event after stop = %v, want not-running (X should redeliver it)", got)
	}
}

// TestXRunExitNeverDeletesWhatTheNextRunReuses: a reconnect starts the new Run while the old one is still
// finishing. Because no Run deletes on exit there is nothing for them to race over.
func TestXRunExitNeverDeletesWhatTheNextRunReuses(t *testing.T) {
	st := newXChatStub(t)
	old := st.adapter(t, time.Unix(1761904800, 0))
	stopOld := xRun(t, old, SinkFunc(func(Message) {}))
	fresh := st.adapter(t, time.Unix(1761904800, 0))
	stopFresh := xRun(t, fresh, SinkFunc(func(Message) {})) // reuses both
	if n := st.countCalls("POST /2/activity/subscriptions"); n != 2 {
		t.Fatalf("the new run created %d subscriptions in total, want it to reuse the 2 that exist", n)
	}
	_ = stopOld()
	_ = stopFresh()
	if n := st.countCalls("DELETE /2/activity/subscriptions"); n != 0 {
		t.Errorf("a Run deleted %d subscription(s) on exit: %v", n, st.recorded())
	}
	if got := st.subList(); len(got) != 2 {
		t.Errorf("subscriptions = %v, want both in place", got)
	}
}

// TestXRemoveSubscriptionsDeletesOnlyThisInstallsForThisAccount: the explicit disconnect. Only polyemesis-tagged
// subscriptions on OUR webhook, for THIS account, go; another user's, another install's, and an untagged one stay.
// The token is the one handed in (the account's own token is already gone when this runs).
func TestXRemoveSubscriptionsDeletesOnlyThisInstallsForThisAccount(t *testing.T) {
	st := newXChatStub(t)
	st.webhooks = []xStubWebhook{{ID: "56", URL: xStubHookURL, Valid: true}, {ID: "9999", URL: "https://other.example.org/api/v1/webhooks/x", Valid: true}}
	mk := func(id, ev, user, hook, tag string) map[string]any {
		return map[string]any{"subscription_id": id, "event_type": ev, "filter": map[string]any{"user_id": user}, "webhook_id": hook, "tag": tag}
	}
	st.subs = []map[string]any{
		mk("1", "broadcast.chat", "1234567890", "56", "polyemesis"),
		mk("2", "broadcast.chat.remove", "1234567890", "56", "polyemesis"),
		mk("3", "broadcast.chat", "42", "56", "polyemesis"),
		mk("4", "broadcast.chat", "1234567890", "9999", "polyemesis"),
		// not an event this adapter ever subscribes to: left alone even when tagged.
		mk("5", "like.create", "1234567890", "56", "polyemesis"),
		mk("6", "broadcast.chat", "1234567890", "56", "someone-else"),
		// the viewer-activity events are this install's too, and go with the account.
		mk("7", "follow.follow", "1234567890", "56", "polyemesis"),
		mk("8", "subscriptions.subscribe", "1234567890", "56", "polyemesis"),
		mk("9", "broadcast.start", "1234567890", "56", "polyemesis"),
		mk("10", "broadcast.end", "1234567890", "56", "polyemesis"),
		mk("11", "follow.follow", "1234567890", "56", "someone-else"),
	}
	a, _ := NewX(XConfig{AccountRef: "1234567890", Token: func(context.Context) (string, error) { return "", fmt.Errorf("account gone") },
		AppBearer: "app-bearer", APIBase: st.srv.URL, WebhookURL: xStubHookURL})
	if err := a.RemoveSubscriptions(context.Background(), "user-token"); err != nil {
		t.Fatalf("RemoveSubscriptions: %v", err)
	}
	var left []string
	for _, s := range st.subList() {
		left = append(left, fmt.Sprint(s["subscription_id"]))
	}
	if strings.Join(left, ",") != "3,4,5,6,11" {
		t.Errorf("subscriptions left = %v, want 3,4,5,6,11", left)
	}
}

// TestXRunDeliversWhatItAcceptedEvenWhenTheSecondSubscriptionFails: the inbox is open before the subscriptions are
// made, so an event can be accepted (and answered 200) and then Run can fail before its loop ever drains it.
func TestXRunDeliversWhatItAcceptedEvenWhenTheSecondSubscriptionFails(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Unix(1761904800, 0))
	var n int
	var verdict XIngest
	st.onSubscribe = func() {
		n++
		if n == 1 {
			verdict = a.IngestWebhook(chatEnvelope("early", "1234567890", "B", "2090000000000000060", "accepted early"))
		}
	}
	st.failSubscribeAt = 2
	sink := &recordingSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Run(ctx, sink); err == nil {
		t.Fatal("Run succeeded although the second subscription failed")
	}
	if verdict != XIngestQueued {
		t.Fatalf("the early event = %v, want queued", verdict)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.messages) != 1 || sink.messages[0].Text != "accepted early" {
		t.Errorf("delivered %+v; an event answered 200 was lost on Run's exit", sink.messages)
	}
}

func TestXRunRetriesARevalidationPUTThatFailed(t *testing.T) {
	st := newXChatStub(t)
	a, _ := xRunEvery(t, st, 10*time.Millisecond)
	_ = a
	id := st.webhookList()[0].ID
	st.mu.Lock()
	st.putFailures = 2
	st.mu.Unlock()
	st.setValid(id, false)
	xWaitFor(t, "the webhook valid again after failed PUTs", func() bool { return st.webhookList()[0].Valid })
	if n := st.countCalls("PUT /2/webhooks/" + id); n < 3 {
		t.Errorf("PUT = %d, want the 2 failures retried and a third that worked", n)
	}
}

// TestXIngestRetryOfAQueuedEventIsADuplicate: X retries an event it got no 200 for. One that is already in the inbox
// (not yet delivered) must answer as a duplicate, even when the inbox is full.
func TestXIngestRetryOfAQueuedEventIsADuplicate(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Unix(1761904800, 0))
	release := make(chan struct{})
	xRun(t, a, SinkFunc(func(Message) { <-release }))
	t.Cleanup(func() { close(release) }) // after xRun's own cleanup is registered, so it runs first
	for i := 0; i < xInboxSize+5; i++ {
		a.IngestWebhook(chatEnvelope(fmt.Sprint("q", i), "1234567890", "B", fmt.Sprint(3000+i), "m"))
	}
	if got := a.IngestWebhook(chatEnvelope("q10", "1234567890", "B", "3010", "m")); got != XIngestDuplicate {
		t.Errorf("retry of a queued event with a full inbox = %v, want duplicate", got)
	}
	if got := a.IngestWebhook(chatEnvelope("brand-new", "1234567890", "B", "9999", "m")); got != XIngestFull {
		t.Errorf("a new event with a full inbox = %v, want full", got)
	}
}

// TestXUnavailableIsTerminalOrLongNotLive: the route answers 503 only while chat is plausibly about to come up.
func TestXUnavailableIsTerminalOrLongNotLive(t *testing.T) {
	st := newXChatStub(t)
	now := time.Unix(1761904800, 0)
	clock := &now
	a, _ := NewX(XConfig{AccountRef: "1234567890", Token: StaticToken("t"), AppBearer: "b", APIBase: st.srv.URL,
		WebhookURL: xStubHookURL, Now: func() time.Time { return *clock }})
	if a.Unavailable(10 * time.Minute) {
		t.Error("a fresh adapter is not yet unavailable")
	}
	*clock = clock.Add(11 * time.Minute)
	if !a.Unavailable(10 * time.Minute) {
		t.Error("an adapter not live for longer than the grace is unavailable")
	}
	fatal, _ := NewX(XConfig{AccountRef: "1", Token: StaticToken("t"), AppBearer: "b", WebhookProblem: "none"})
	_ = xRunErr(t, fatal)
	if !fatal.Unavailable(time.Hour) {
		t.Error("an adapter whose Run ended with a Fatal error is unavailable at once")
	}
}
func TestXRunSaysSoWhenItReplacesAnotherInstallsSubscription(t *testing.T) {
	st := newXChatStub(t)
	st.webhooks = []xStubWebhook{
		{ID: "56", URL: xStubHookURL, Valid: true},
		{ID: "9999", URL: "https://other-install.example.org/api/v1/webhooks/x", Valid: true},
	}
	st.subs = []map[string]any{
		{"subscription_id": "7001", "event_type": "broadcast.chat", "filter": map[string]any{"user_id": "1234567890"}, "webhook_id": "9999", "tag": "polyemesis"},
	}
	var logs bytes.Buffer
	a, _ := NewX(XConfig{AccountRef: "1234567890", Token: StaticToken("user-token"), AppBearer: "app-bearer",
		APIBase: st.srv.URL, WebhookURL: xStubHookURL, Log: slog.New(slog.NewTextHandler(&logs, nil))})
	xRun(t, a, SinkFunc(func(Message) {}))
	d := a.Health().Detail
	for _, want := range []string{"replaced another polyemesis install's X chat subscription", "other-install.example.org"} {
		if !strings.Contains(d, want) {
			t.Errorf("status detail %q does not contain %q", d, want)
		}
	}
	if strings.Contains(d, "app-bearer") || strings.Contains(d, "/api/v1") {
		t.Errorf("status detail carries more than the host: %q", d)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "other-install.example.org") {
		t.Errorf("no WARN naming the other host in the log: %q", logs.String())
	}
}

func TestXRunMapsXsDocumentedWebhookErrors(t *testing.T) {
	for _, c := range []struct {
		name, body, want, notWant string
		fatal                     bool
	}{
		{"limit", `{"title":"Forbidden","detail":"WebhookLimitExceeded"}`, "maximum number of webhooks", "public HTTPS address", false},
		{"url", `{"title":"Invalid","detail":"UrlValidationFailed: not https"}`, "rejected the webhook address", "CRC", false},
		{"unknown", `{"title":"Invalid Request","detail":"something else"}`, "would not register the webhook", "public HTTPS address", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := newXChatStub(t)
			st.createStatus, st.createBody = 400, c.body
			err := xRunErr(t, st.adapter(t, time.Unix(1761904800, 0)))
			if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), c.notWant) {
				t.Errorf("error = %v, want it to contain %q and not %q", err, c.want, c.notWant)
			}
		})
	}
}

func TestXRunReusesTheWebhookWhenXSaysItIsADuplicate(t *testing.T) {
	st := newXChatStub(t)
	st.createStatus, st.createRaces = 400, true
	st.createBody = `{"title":"Invalid","detail":"DuplicateUrlFailed"}`
	xRun(t, st.adapter(t, time.Unix(1761904800, 0)), SinkFunc(func(Message) {}))
}

func TestXRunRevalidatesAtMostOncePerRegistration(t *testing.T) {
	st := newXChatStub(t)
	st.putFixes = false
	st.webhooks = []xStubWebhook{{ID: "56", URL: xStubHookURL, Valid: false}}
	_ = xRunErr(t, st.adapter(t, time.Unix(1761904800, 0)))
	if n := st.countCalls("PUT /2/webhooks/56"); n != 1 {
		t.Errorf("PUT = %d, want exactly 1", n)
	}
}

func xRunEvery(t *testing.T, st *xChatStub, every time.Duration) (*XAdapter, func() error) {
	t.Helper()
	a, err := NewX(XConfig{AccountRef: "1234567890", Token: StaticToken("user-token"), AppBearer: "app-bearer",
		APIBase: st.srv.URL, WebhookURL: xStubHookURL, WebhookRecheck: every})
	if err != nil {
		t.Fatal(err)
	}
	return a, xRun(t, a, SinkFunc(func(Message) {}))
}

// TestXRunRechecksTheWebhookWhileLive: X validates hourly and stops delivering to an invalid webhook, so a webhook
// that has gone invalid is re-validated once, without waiting for a reconnect.
func TestXRunRechecksTheWebhookWhileLive(t *testing.T) {
	st := newXChatStub(t)
	_, _ = xRunEvery(t, st, 10*time.Millisecond)
	id := st.webhookList()[0].ID
	st.setValid(id, false)
	xWaitFor(t, "a PUT after the webhook went invalid", func() bool { return st.countCalls("PUT /2/webhooks/"+id) >= 1 })
	xWaitFor(t, "the webhook valid again", func() bool { return st.webhookList()[0].Valid })
}

func TestXRunSurfacesAWebhookThatStaysInvalidAndDoesNotPUTAgain(t *testing.T) {
	st := newXChatStub(t)
	a, _ := xRunEvery(t, st, 10*time.Millisecond)
	id := st.webhookList()[0].ID
	st.putFixes = false
	st.setValid(id, false)
	xWaitFor(t, "a status for the invalid webhook", func() bool { return strings.Contains(a.Health().Detail, "invalid") })
	time.Sleep(80 * time.Millisecond)
	if n := st.countCalls("PUT /2/webhooks/" + id); n != 1 {
		t.Errorf("PUT = %d while the webhook stays invalid, want 1", n)
	}
	st.putFixes = true
	st.setValid(id, true)
	xWaitFor(t, "the status to clear", func() bool { return a.Health().Detail == "" })
}

// TestXRunExitDrainIsBounded: the exit drain hands accepted events to the sink, and a sink that blocks must not hang
// Run (and with it the Hub's shutdown). Past the bound the rest is abandoned and logged.
func TestXRunExitDrainIsBounded(t *testing.T) {
	old := xDrainLimit
	xDrainLimit = 100 * time.Millisecond
	t.Cleanup(func() { xDrainLimit = old })

	st := newXChatStub(t)
	var logs bytes.Buffer
	a, _ := NewX(XConfig{AccountRef: "1234567890", Token: StaticToken("user-token"), AppBearer: "app-bearer",
		APIBase: st.srv.URL, WebhookURL: xStubHookURL, Log: slog.New(slog.NewTextHandler(&logs, nil))})
	n := 0
	st.onSubscribe = func() {
		n++
		if n == 1 {
			for i := 0; i < 3; i++ {
				a.IngestWebhook(chatEnvelope(fmt.Sprint("d", i), "1234567890", "B", fmt.Sprint(4000+i), "m"))
			}
		}
	}
	st.failSubscribeAt = 2
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), SinkFunc(func(Message) { <-release })) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Run succeeded although the second subscription failed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run hung on a blocked sink during its exit drain")
	}
	if !strings.Contains(logs.String(), "already answered 200") {
		t.Errorf("the abandoned events were not logged as already 200'd: %q", logs.String())
	}
}

// TestXUnavailableResetsWhenANewRunStarts: a retry Run is "coming up", however long the outage before it was.
func TestXUnavailableResetsWhenANewRunStarts(t *testing.T) {
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-hang }))
	t.Cleanup(func() { close(hang); srv.Close() })
	var mu sync.Mutex
	now := time.Unix(1761904800, 0)
	a, _ := NewX(XConfig{AccountRef: "1234567890", Token: StaticToken("t"), AppBearer: "b", APIBase: srv.URL, WebhookURL: xStubHookURL,
		Now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now }})
	mu.Lock()
	now = now.Add(time.Hour)
	mu.Unlock()
	if !a.Unavailable(10 * time.Minute) {
		t.Fatal("setup: an hour-old outage should be unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = a.Run(ctx, SinkFunc(func(Message) {})); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	xWaitFor(t, "a starting Run to read as coming up", func() bool { return !a.Unavailable(10 * time.Minute) })
}

// The Hub never sets a runner live on its own; X has to say so through Health, or the pane reads "connecting"
// for as long as chat is arriving.
func TestXHealthReportsLiveOnceRegisteredAndDegradedWhileTheWebhookIsInvalid(t *testing.T) {
	st := newXChatStub(t)
	a, _ := NewX(XConfig{AccountRef: "1234567890", Token: StaticToken("user-token"), AppBearer: "app-bearer",
		APIBase: st.srv.URL, WebhookURL: xStubHookURL, WebhookRecheck: 10 * time.Millisecond})
	if got := a.Health().State; got != "" {
		t.Fatalf("State before Run = %q, want empty (the Hub's connecting)", got)
	}
	stop := xRun(t, a, SinkFunc(func(Message) {}))
	defer stop()
	if got := a.Health().State; got != StateLive {
		t.Fatalf("State once registered and subscribed = %q, want live", got)
	}
	id := st.webhookList()[0].ID
	st.putFixes = false
	st.setValid(id, false)
	xWaitFor(t, "degraded for the invalid webhook", func() bool { return a.Health().State == StateDegraded })
	st.putFixes = true
	st.setValid(id, true)
	xWaitFor(t, "live again", func() bool { return a.Health().State == StateLive })
}

// A 429 on the webhook create or the re-validation keeps its type, so Run waits out the reset (the Hub's own
// backoff is 1 to 30 seconds), does not ask X to list again, and does not blame the public address.
func TestXRunWaitsOutARateLimitedWebhookCreateAndRevalidate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(st *xChatStub)
		call  string
	}{
		{"create", func(st *xChatStub) {
			st.createStatus = http.StatusTooManyRequests
			st.createBody = `{"title":"Too Many Requests","detail":"Too Many Requests","type":"about:blank","status":429}`
		}, "POST /2/webhooks"},
		{"revalidate", func(st *xChatStub) {
			st.webhooks = []xStubWebhook{{ID: "56", URL: xStubHookURL, Valid: false}}
			st.putStatus = http.StatusTooManyRequests
		}, "PUT /2/webhooks/56"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newXChatStub(t)
			tc.setup(st)
			var waits []time.Duration
			a := st.adapter(t, time.Unix(1761904800, 0))
			a.cfg.Wait = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
			err := xRunErr(t, a)
			if err == nil || IsFatal(err) {
				t.Fatalf("Run = %v, want a retryable error", err)
			}
			var rl *xChatRateLimited
			if !errors.As(err, &rl) {
				t.Fatalf("Run = %v, want an *xChatRateLimited", err)
			}
			if strings.Contains(err.Error(), "CRC") || strings.Contains(err.Error(), "public HTTPS") {
				t.Errorf("a rate limit was reported as an address problem: %v", err)
			}
			if len(waits) != 1 || waits[0] <= 0 {
				t.Errorf("waits = %v, want one positive wait", waits)
			}
			if n := st.countCalls(tc.call); n != 1 {
				t.Errorf("%s = %d, want 1", tc.call, n)
			}
			if n := st.countCalls("GET /2/webhooks"); n != 1 {
				t.Errorf("GET /2/webhooks = %d, want 1 (no extra list after a 429)", n)
			}
		})
	}
}
