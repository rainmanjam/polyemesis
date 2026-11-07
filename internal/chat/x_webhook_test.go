package chat

import (
	"context"
	"fmt"
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
	for a.WebhookUserID() == "" {
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
	if left := st.subList(); len(left) != 0 {
		t.Errorf("subscriptions left on X's side after a clean stop: %+v", left)
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
		"an error from X":           func(st *xChatStub) { st.createStatus = 400 },
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

func TestXRunReusesMatchingSubscriptionsAndRemovesOwnOnStop(t *testing.T) {
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
	left := st.subList()
	if len(left) != 1 || left[0]["subscription_id"] != "7003" {
		t.Errorf("after stop, subscriptions = %+v; want only the other user's 7003", left)
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
