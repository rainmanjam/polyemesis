package chat

// Tests for the Rumble adapter.
//
// WHAT THESE CAN AND CANNOT PROVE, said up front so nobody reads more into a
// green run than is there. Rumble's live-stream API needs a key issued from an
// account's own settings page, and no key was available while this was written.
// So nothing here proves polyemesis can talk to rumble.com. What is proved is
// everything that is ours: that Rumble's own published payload normalises into
// the shape the rest of polyemesis speaks, that a refused key stops instead of
// hammering, that the poll interval cannot be configured into a rate-limit
// ban, and that the key cannot reach a log. That is the same split the package
// doc describes for Twitch IRC.
//
// Every test below was mutation-verified: the behaviour was broken, the named
// test was watched to fail, and the tree was restored from a file backup. The
// exact mutation is recorded in each doc comment.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// rumbleNow is the clock every test here runs on. Fixed, because a backfill
// window compared against time.Now() is a test that fails at midnight.
var rumbleNow = time.Date(2026, 8, 13, 18, 0, 0, 0, time.UTC)

// rumbleSample is Rumble's own documented response, trimmed to the parts this
// adapter reads and with the fields it deliberately ignores LEFT IN.
//
// stream_key is in here on purpose. It is in Rumble's real response, it is the
// #310 shape -- a secret arriving in a payload nobody was thinking about -- and
// a fixture that quietly dropped it would make the test that checks it never
// escapes pass for the wrong reason.
const rumbleSample = `{
  "now": 1695059500,
  "type": "user",
  "user_id": "XXXXX",
  "livestreams": [
    {
      "id": "abc123",
      "title": "Title of Live Stream",
      "is_live": true,
      "stream_key": "SUPER-SECRET-STREAM-KEY",
      "watching_now": 19,
      "chat": {
        "latest_message": {
          "username": "UserNameH", "badges": ["admin"],
          "text": "test chat", "created_on": "2026-08-13T17:59:00+00:00"
        },
        "recent_messages": [
          {
            "username": "UserNameA",
            "badges": ["admin", "premium", "whale-blue"],
            "text": "This is my chat message",
            "created_on": "2026-08-13T17:59:00+00:00"
          }
        ],
        "recent_rants": [
          {
            "username": "UserNameB", "badges": ["premium"],
            "text": "Rant Message", "created_on": "2026-08-13T17:59:30+00:00",
            "expires_on": "2026-08-13T18:01:30+00:00",
            "amount_cents": 150, "amount_dollars": 1
          }
        ]
      }
    }
  ]
}`

type rumbleStub struct {
	*httptest.Server
	calls atomic.Int64
	// lastQuery is what the adapter actually put on the wire, so a test can
	// assert the key travelled rather than assuming it did.
	lastQuery atomic.Value
}

func newRumbleStub(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, call int64)) *rumbleStub {
	t.Helper()
	s := &rumbleStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.lastQuery.Store(r.URL.RawQuery)
		n := s.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		handler(w, r, n)
	}))
	t.Cleanup(s.Close)
	return s
}

// rumbleOK serves one fixed body on every poll, which is what Rumble does: the
// same recent-50 window, over and over.
func rumbleOK(t *testing.T, body string) *rumbleStub {
	return newRumbleStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
		_, _ = w.Write([]byte(body))
	})
}

func rumbleAdapter(t *testing.T, base string, opts func(*RumbleConfig)) *RumbleAdapter {
	t.Helper()
	cfg := RumbleConfig{
		AccountRef: "rumble-me",
		Channel:    "My Rumble Channel",
		Key:        "test-api-key-abc123",
		APIBase:    base,
		Now:        func() time.Time { return rumbleNow },
	}
	if opts != nil {
		opts(&cfg)
	}
	a, err := NewRumble(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// rumbleRun drives Run for a bounded number of polls and collects what reached
// the sink, NORMALISED -- because the Hub normalises before it dedupes, and the
// id this adapter relies on does not exist until it has.
func rumbleRun(t *testing.T, a *RumbleAdapter, polls int) ([]Message, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		mu  sync.Mutex
		got []Message
		n   int
	)
	a.cfg.Sleep = func(context.Context, time.Duration) bool {
		n++
		if n >= polls {
			cancel()
			return false
		}
		return true
	}
	err := a.Run(ctx, SinkFunc(func(m Message) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, m.Normalise(a.cfg.Now))
	}))
	mu.Lock()
	defer mu.Unlock()
	return got, err
}

// TestRumbleNormalisesItsDocumentedPayload pins the field mapping against
// Rumble's own published example.
//
// This is the test that would catch Rumble renaming a field, and the only
// place the payload shape is asserted rather than assumed. It checks the
// author, the text, the timestamp and the platform stamp together, because a
// message that arrives with the right text under the wrong platform is not a
// message the pane can render.
//
// Proven able to fail against the committed tree by changing the `text` json
// tag on rumbleChatEntry.Text to `message`, which makes every Text empty and
// the entry drop out of deliver as blank.
func TestRumbleNormalisesItsDocumentedPayload(t *testing.T) {
	s := rumbleOK(t, rumbleSample)
	a := rumbleAdapter(t, s.URL, nil)

	got, err := rumbleRun(t, a, 1)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}

	var chat *Message
	for i := range got {
		if got[i].Author.Name == "UserNameA" {
			chat = &got[i]
		}
	}
	if chat == nil {
		t.Fatalf("the documented chat message never reached the sink; got %d messages: %+v", len(got), got)
	}

	if chat.Text != "This is my chat message" {
		t.Errorf("text = %q", chat.Text)
	}
	if chat.Platform != db.PlatformRumble {
		t.Errorf("platform = %q, want %q", chat.Platform, db.PlatformRumble)
	}
	if chat.Account != "rumble-me" {
		t.Errorf("account = %q", chat.Account)
	}
	// Rumble sends no numeric id, so the username is the identity. Asserted
	// rather than left implicit because it is weaker than every other adapter
	// here and a future change that "fixes" it should have to come through this
	// line.
	if chat.Author.ID != "UserNameA" {
		t.Errorf("author id = %q, want the username", chat.Author.ID)
	}
	want := time.Date(2026, 8, 13, 17, 59, 0, 0, time.UTC)
	if !chat.At.Equal(want) {
		t.Errorf("timestamp = %v, want %v", chat.At, want)
	}
	// "admin" must survive as a badge AND be promoted to the moderator flag by
	// applyBadgeRoles, which is the whole reason this adapter passes Rumble's
	// own token through as the badge id instead of a prettified label.
	if !chat.Author.Moderator {
		t.Errorf("an admin badge did not become the moderator flag: %+v", chat.Author)
	}
	if len(chat.Author.Badges) != 3 {
		t.Errorf("badges = %+v, want admin, premium and whale-blue", chat.Author.Badges)
	}
	// An unrecognised badge must render as itself rather than vanish.
	var sawWhale bool
	for _, b := range chat.Author.Badges {
		if b.ID == "whale-blue" && b.Label == "whale-blue" {
			sawWhale = true
		}
	}
	if !sawWhale {
		t.Errorf("the unrecognised badge was dropped or relabelled: %+v", chat.Author.Badges)
	}
}

// TestRumbleMarksARantWithItsAmount proves a paid message is delivered and
// distinguishable.
//
// Rants arrive in a separate list from ordinary chat, so "we read
// recent_messages" would silently lose every paid message -- the ones an
// operator most wants to see. The amount is checked in cents-derived form
// because Rumble's own amount_dollars field rounds $1.50 down to 1.
//
// Proven able to fail against the committed tree by deleting the
// `for _, e := range ls.Chat.RecentRants` loop from deliver: the rant never
// reaches the sink and this test reports the message missing. Also fails if the
// label is built from amount_dollars, which reads "Rant $1.00".
func TestRumbleMarksARantWithItsAmount(t *testing.T) {
	s := rumbleOK(t, rumbleSample)
	a := rumbleAdapter(t, s.URL, nil)

	got, err := rumbleRun(t, a, 1)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}

	var rant *Message
	for i := range got {
		if got[i].Author.Name == "UserNameB" {
			rant = &got[i]
		}
	}
	if rant == nil {
		t.Fatalf("the rant never reached the sink; got %+v", got)
	}
	var label string
	for _, b := range rant.Author.Badges {
		if b.ID == "rant" {
			label = b.Label
		}
	}
	if label != "Rant $1.50" {
		t.Fatalf("rant badge = %q, want %q — a paid message with no figure on it is just a message", label, "Rant $1.50")
	}
}

// TestRumbleGivesAnOverlappingPollTheSameIDSoTheHubDeduplicates is the test
// that the whole polling design rests on.
//
// Rumble has no cursor and no since-parameter that was verified to work: every
// response is the same recent-50 window. At a ten-second poll the overlap is
// near total, so if two polls of the same message produced two different keys
// the pane would repeat the entire chat every ten seconds. Nothing else in this
// file would catch that.
//
// Proven able to fail against the committed tree by setting
// `ID: fmt.Sprintf("rumble-%d", time.Now().UnixNano())` in messageFrom, which
// gives each poll's copy a fresh id and makes the two keys differ.
func TestRumbleGivesAnOverlappingPollTheSameIDSoTheHubDeduplicates(t *testing.T) {
	s := rumbleOK(t, rumbleSample)
	a := rumbleAdapter(t, s.URL, nil)

	// Two polls of an unchanging window, exactly as the real API serves it.
	got, err := rumbleRun(t, a, 2)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if s.calls.Load() < 2 {
		t.Fatalf("the adapter polled %d times; this test is meaningless without two", s.calls.Load())
	}

	keys := map[string]int{}
	for _, m := range got {
		if m.ID == "" {
			t.Fatalf("a message reached the sink with no id, so the Hub cannot dedupe it: %+v", m)
		}
		keys[m.Key()]++
	}
	// Two polls of one message must collapse to ONE key seen twice, not two
	// keys seen once each.
	for k, n := range keys {
		if n != 2 {
			t.Errorf("key %q appeared %d times across two identical polls, want 2 — "+
				"a key that changes between polls makes the pane repeat the whole chat", k, n)
		}
	}
	if len(keys) != 2 {
		t.Fatalf("two identical polls produced %d distinct keys, want 2 (one chat, one rant): %v", len(keys), keys)
	}
}

// TestRumbleBoundsTheFirstPollToTheBackfillWindow stops the pane opening with a
// replay.
//
// Every response carries the most recent 50 messages, which on a slow chat can
// span hours. Without a window the pane opens by rendering all of it as if it
// had just arrived.
//
// Proven able to fail against the committed tree by deleting the
// `if first { cutoff = ... }` block from Run, after which the hour-old message
// is delivered and the count is 2 rather than 1.
func TestRumbleBoundsTheFirstPollToTheBackfillWindow(t *testing.T) {
	// One message inside a five-minute window, one an hour old.
	body := `{"livestreams":[{"is_live":true,"title":"t","chat":{"recent_messages":[
	  {"username":"Old","badges":[],"text":"an hour ago","created_on":"2026-08-13T17:00:00+00:00"},
	  {"username":"New","badges":[],"text":"just now","created_on":"2026-08-13T17:59:00+00:00"}
	]}}]}`
	s := rumbleOK(t, body)
	a := rumbleAdapter(t, s.URL, func(c *RumbleConfig) { c.Backfill = 5 * time.Minute })

	got, err := rumbleRun(t, a, 1)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("first poll delivered %d messages, want 1 — the backfill window did not bound it: %+v", len(got), got)
	}
	if got[0].Text != "just now" {
		t.Fatalf("delivered %q, want the recent message", got[0].Text)
	}
}

// TestRumbleIgnoresAStreamThatIsNotLive proves the is_live flag is honoured
// rather than the first array entry taken.
//
// The array can hold a finished broadcast whose chat block is stale. Reading it
// would report the pane as connected while replaying an old show's chat, which
// looks exactly like the platform being slow.
//
// The fixture puts the DEAD stream first on purpose: a version of this adapter
// that took livestreams[0] would pass a test where the live one happened to be
// first, and that is the vacuous shape this guards against.
//
// Proven able to fail against the committed tree by changing liveStream's loop
// body to `return resp.Livestreams[0], true`, which delivers "yesterday" and
// fails on both the count and the text.
func TestRumbleIgnoresAStreamThatIsNotLive(t *testing.T) {
	body := `{"livestreams":[
	  {"is_live":false,"title":"finished","chat":{"recent_messages":[
	    {"username":"Ghost","badges":[],"text":"yesterday","created_on":"2026-08-13T17:59:00+00:00"}]}},
	  {"is_live":true,"title":"on air","chat":{"recent_messages":[
	    {"username":"Live","badges":[],"text":"today","created_on":"2026-08-13T17:59:00+00:00"}]}}
	]}`
	s := rumbleOK(t, body)
	a := rumbleAdapter(t, s.URL, nil)

	got, err := rumbleRun(t, a, 1)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if len(got) != 1 || got[0].Text != "today" {
		t.Fatalf("delivered %+v, want only the live stream's message", got)
	}
}

// TestRumbleWaitsRatherThanFailingWhenNothingIsLive checks the normal
// overnight state.
//
// Rumble chat cannot exist without a broadcast. Reporting "no broadcast" as a
// failure would make the account list read as broken every morning, and would
// put the Hub into a reconnect loop over a condition reconnecting cannot change.
//
// Proven able to fail against the committed tree by changing the `!ok` branch
// in Run to `return fmt.Errorf("no live stream")`, which returns an error where
// this test requires nil and leaves the health state failed rather than degraded.
func TestRumbleWaitsRatherThanFailingWhenNothingIsLive(t *testing.T) {
	s := rumbleOK(t, `{"livestreams":[]}`)
	a := rumbleAdapter(t, s.URL, nil)

	got, err := rumbleRun(t, a, 1)
	if err != nil {
		t.Fatalf("Run = %v, want nil — waiting for a broadcast is not a failure", err)
	}
	if len(got) != 0 {
		t.Fatalf("delivered %d messages with nothing live", len(got))
	}
	h := a.Health()
	if h.State != StateDegraded {
		t.Errorf("state = %q, want %q", h.State, StateDegraded)
	}
	if !strings.Contains(strings.ToLower(h.Detail), "waiting") {
		t.Errorf("detail = %q, want a sentence saying it is waiting for a broadcast", h.Detail)
	}
}

// TestRumbleStopsRatherThanRetryingACredentialItCannotFix is the ban-avoidance
// test, and the reason classify has a fatal set at all.
//
// A key the operator reset answers 403 forever. On the Hub's schedule that is a
// request every thirty seconds from a home IP, at an endpoint whose rate limit
// Rumble does not publish. twitch.go's fatalNotice carries the same reasoning
// for the same reason.
//
// The 500 case is in the same test on purpose: a fatal set that swallowed
// everything would pass a test that only checked 403, and would take the chat
// pane down for the rest of the day over a bad afternoon at Rumble.
//
// Proven able to fail against the committed tree by deleting the
// `case http.StatusUnauthorized, http.StatusForbidden:` arm from classify (the
// 403 subtest then reports not-fatal), and separately by adding
// `case http.StatusInternalServerError:` to the fatal switch (the 500 subtest
// then reports fatal).
func TestRumbleStopsRatherThanRetryingACredentialItCannotFix(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		wantFatal bool
		because   string
	}{
		{
			name:   "a refused key is fatal",
			status: http.StatusForbidden,
			// The body Rumble actually returned to a probe with an invalid key.
			body:      `{"user":{"id":null,"logged_in":false},"errors":[{"code":"403","message":"Forbidden","type":"generic"}]}`,
			wantFatal: true,
			because:   "retrying a rejected key every thirty seconds is how an IP gets banned",
		},
		{
			name:   "a missing key is fatal",
			status: http.StatusBadRequest,
			// Also measured: this is what no key at all returns.
			body:      `{"user":{"id":null,"logged_in":false},"errors":[{"code":"invalid_request","message":"No access token found in the request","type":"generic"}]}`,
			wantFatal: true,
			because:   "retrying an absent credential is retrying nothing",
		},
		{
			name:      "a server error is retryable",
			status:    http.StatusInternalServerError,
			body:      `{"errors":[{"code":"500","message":"Internal Server Error"}]}`,
			wantFatal: false,
			because:   "a bad afternoon at Rumble must not take chat down until somebody restarts polyemesis",
		},
		{
			name:      "being rate limited is retryable",
			status:    http.StatusTooManyRequests,
			body:      `{"errors":[{"code":"429","message":"Too Many Requests"}]}`,
			wantFatal: false,
			because:   "backing off is the correct response to a rate limit; giving up is not",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newRumbleStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			a := rumbleAdapter(t, s.URL, nil)

			_, err := rumbleRun(t, a, 1)
			if err == nil {
				t.Fatalf("Run returned nil for HTTP %d; the failure was swallowed entirely", tc.status)
			}
			if IsFatal(err) != tc.wantFatal {
				t.Fatalf("IsFatal(%v) = %v, want %v (%s)", err, IsFatal(err), tc.wantFatal, tc.because)
			}
		})
	}
}

// TestRumbleKeepsTheAPIKeyOutOfEveryStringItProduces is #310 as a test.
//
// There, a refused destination wrote its stream key to server.log on every
// retry because one sink was covered and its sibling was not. Rumble is the
// worst shape for that in this package: it is the only adapter whose credential
// travels in a URL, and the only one whose SUCCESS body contains a stream key.
//
// The stub echoes the key back in its error body deliberately. Rumble does not
// do that today, and the point is that it would not matter if it started: the
// guarantee has to be a property of our code, not of the platform's goodwill.
//
// Proven able to fail against the committed tree two ways:
//   - replacing `r.scrub(err.Error())` with `err.Error()` in classify (the
//     error-text assertion fails)
//   - replacing `r.scrub(detail)` with `detail` in setHealth (the health
//     assertion fails)
//
// WHAT THIS TEST CANNOT SEE, recorded so nobody reads it as covering more than
// it does. httpx.go's `URL: stripQuery(endpoint)` is a third layer keeping the
// key out of the error, and changing it to `URL: endpoint` does NOT fail this
// test -- scrub catches the key first. The two layers are therefore not
// independently pinned here, and an earlier draft of this comment claimed they
// were. That claim was wrong and the mutation run is what found it. stripQuery
// is owned by httpx.go and shared by three other adapters; this file relies on
// it but does not test it.
func TestRumbleKeepsTheAPIKeyOutOfEveryStringItProduces(t *testing.T) {
	const key = "rumble-live-key-do-not-log-me"

	s := newRumbleStub(t, func(w http.ResponseWriter, r *http.Request, _ int64) {
		w.WriteHeader(http.StatusInternalServerError)
		// The platform handing our own credential back to us, which is exactly
		// how a secret ends up in a log line built from an error body.
		_, _ = w.Write([]byte(`{"errors":[{"message":"failed for key ` + r.URL.Query().Get("key") + `"}]}`))
	})
	a := rumbleAdapter(t, s.URL, func(c *RumbleConfig) { c.Key = key })

	_, err := rumbleRun(t, a, 1)
	if err == nil {
		t.Fatal("Run returned nil; this test needs the error path it is checking")
	}

	// First, prove the key really did travel — otherwise every assertion below
	// passes because there was nothing to leak, which is the vacuous shape this
	// whole file is written against.
	sent, _ := s.lastQuery.Load().(string)
	if !strings.Contains(sent, "rumble-live-key") {
		t.Fatalf("the key never reached the wire (query %q), so this test proves nothing", sent)
	}

	if strings.Contains(err.Error(), key) {
		t.Errorf("the API key is in the error text, which is the one string most likely to be logged:\n%s", err)
	}

	// The health detail is rendered in the UI and copied into logs.
	if d := a.Health().Detail; strings.Contains(d, key) {
		t.Errorf("the API key is in the health detail: %q", d)
	}
	// And the chokepoint itself, with a string no production path builds today.
	// Asserted directly because "no caller passes a secret through here" is the
	// assumption that caused #310, and it stops being true silently.
	a.setHealth(StateFailed, "rumble said: "+key)
	if d := a.Health().Detail; strings.Contains(d, key) {
		t.Errorf("setHealth did not scrub its input: %q", d)
	}
}

// TestRumbleNeverDecodesTheStreamKeyOutOfTheChatPayload guards the omission in
// rumbleLivestream.
//
// Rumble's response carries livestreams[].stream_key in the same JSON as the
// chat. The defence is that the struct has no field for it, so it cannot be
// logged, serialised or put in an error by a later change. That defence is
// invisible in the source -- it is an absence -- so this test is what makes it
// deliberate rather than accidental.
//
// Proven able to fail against the committed tree by adding
// `StreamKey string `+"`json:\"stream_key\"`"+` to rumbleLivestream and using it
// in the Channel field of messageFrom: the key then rides on every message.
func TestRumbleNeverDecodesTheStreamKeyOutOfTheChatPayload(t *testing.T) {
	const streamKey = "SUPER-SECRET-STREAM-KEY"

	s := rumbleOK(t, rumbleSample)
	a := rumbleAdapter(t, s.URL, nil)

	got, err := rumbleRun(t, a, 1)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no messages delivered, so this test would pass for the wrong reason")
	}
	// The fixture must actually contain the secret, or the assertion is empty.
	if !strings.Contains(rumbleSample, streamKey) {
		t.Fatal("the fixture no longer carries a stream key; this test proves nothing")
	}

	// Everything the adapter emits, serialised the way the API layer and the
	// event bus would emit it.
	blob, merr := json.Marshal(struct {
		Messages []Message `json:"messages"`
		Health   Health    `json:"health"`
	}{got, a.Health()})
	if merr != nil {
		t.Fatal(merr)
	}
	if strings.Contains(string(blob), streamKey) {
		t.Fatalf("the stream key from Rumble's chat payload reached a delivered message or the health "+
			"report:\n%s", blob)
	}
}

// TestRumbleClampsThePollIntervalAwayFromARateLimitBan pins the floor.
//
// Rumble publishes no rate limit, so the only safe assumption is that one
// exists and we do not know where it is. A misconfigured 100ms poll is ten
// requests a second from an operator's home IP for hours; the clamp costs
// nothing and finding the limit by crossing it costs the account.
//
// Proven able to fail against the committed tree by deleting the
// `if d < rumbleMinPoll { return rumbleMinPoll }` branch from pollInterval,
// after which the 100ms case returns 100ms.
func TestRumbleClampsThePollIntervalAwayFromARateLimitBan(t *testing.T) {
	tests := []struct {
		name string
		set  time.Duration
		want time.Duration
	}{
		{"an unset interval takes the conservative default", 0, rumbleDefaultPoll},
		{"an aggressive interval is floored", 100 * time.Millisecond, rumbleMinPoll},
		{"a negative interval cannot become a busy loop", -time.Second, rumbleDefaultPoll},
		{"an interval inside the range is honoured", 15 * time.Second, 15 * time.Second},
		{"a glacial interval is capped so a live chat is still live", time.Hour, rumbleMaxPoll},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := rumbleAdapter(t, "http://example.invalid", func(c *RumbleConfig) { c.Poll = tc.set })
			if got := a.pollInterval(); got != tc.want {
				t.Fatalf("pollInterval() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRumbleRefusesToStartWithNoKey checks the "not configured" path names the
// variable an operator has to set.
//
// A constructor that returned a working adapter with an empty key would send an
// unauthenticated request every ten seconds forever, and the 400 it gets back
// would be reported as a platform problem.
//
// Proven able to fail against the committed tree by deleting the
// `if strings.TrimSpace(cfg.Key) == ""` guard from NewRumble, after which the
// constructor returns a nil error.
func TestRumbleRefusesToStartWithNoKey(t *testing.T) {
	for _, key := range []string{"", "   "} {
		_, err := NewRumble(RumbleConfig{AccountRef: "r", Key: key})
		if err == nil {
			t.Fatalf("NewRumble with key %q returned no error", key)
		}
		// The message has to say WHERE to put the key, or it is just a refusal.
		if !strings.Contains(err.Error(), RumbleChatKeyEnv) {
			t.Errorf("the error does not name %s, so it does not tell the operator what to do: %v",
				RumbleChatKeyEnv, err)
		}
	}
}

// TestRumbleBacksOffWhileNobodyIsTalking checks the idle schedule.
//
// A silent chat polled every ten seconds for an eight-hour broadcast is 2,880
// requests that told us nothing, against an endpoint with an unpublished limit.
//
// The second half matters as much as the first: an adapter that only ever
// backed off would take a minute to show the message that ends the silence.
//
// Proven able to fail against the committed tree by deleting the
// `if delivered == 0 { wait *= 2 ... }` branch from Run, which leaves every
// wait at the base interval and fails the "grew" assertion.
func TestRumbleBacksOffWhileNobodyIsTalking(t *testing.T) {
	// A live stream whose chat is empty until the fourth poll.
	s := newRumbleStub(t, func(w http.ResponseWriter, _ *http.Request, call int64) {
		if call < 4 {
			_, _ = w.Write([]byte(`{"livestreams":[{"is_live":true,"title":"t","chat":{"recent_messages":[]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"livestreams":[{"is_live":true,"title":"t","chat":{"recent_messages":[
		  {"username":"Someone","badges":[],"text":"finally","created_on":"2026-08-13T17:59:00+00:00"}]}}]}`))
	})
	a := rumbleAdapter(t, s.URL, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var waits []time.Duration
	a.cfg.Sleep = func(_ context.Context, d time.Duration) bool {
		waits = append(waits, d)
		if len(waits) >= 4 {
			cancel()
			return false
		}
		return true
	}
	if err := a.Run(ctx, SinkFunc(func(Message) {})); err != nil {
		t.Fatalf("Run = %v", err)
	}

	if len(waits) < 4 {
		t.Fatalf("only %d polls happened; this test needs four", len(waits))
	}
	if !(waits[0] < waits[1] && waits[1] < waits[2]) {
		t.Errorf("the idle interval did not grow across silent polls: %v", waits)
	}
	// The fourth poll delivered, so the interval must snap back to the base.
	if waits[3] != a.pollInterval() {
		t.Errorf("after a message arrived the interval was %v, want the base %v — "+
			"a chat that woke up must not stay on the idle schedule", waits[3], a.pollInterval())
	}
}

// TestRumbleReArmsTheBackfillWindowForASecondBroadcast covers the
// stream-again case.
//
// The first draft of this test asserted the OVERNIGHT case -- attached at 9pm,
// live at 8am -- and it was vacuous: a poll that finds nothing live never
// consumes the window, so the `first` flag is still set when the broadcast
// arrives whether or not anything re-arms it. Removing the line under test
// changed nothing and the test stayed green. It is recorded here because that
// is the exact shape this file is written against, and because rediscovering
// which line a test actually pins is worth more than the test was.
//
// What the line really buys: an operator who ends a stream and starts another
// gets the second broadcast bounded the same way the first was. Without it the
// second stream's first poll has no cutoff and the pane opens by replaying that
// whole recent-50 window.
//
// Proven able to fail against the committed tree by deleting the `first = true`
// line from the not-live branch of Run, after which the second broadcast's
// stale message is delivered and the count is 2 rather than 1.
func TestRumbleReArmsTheBackfillWindowForASecondBroadcast(t *testing.T) {
	// Poll 1: live, one fresh message -- this consumes the backfill window.
	// Poll 2: the stream ended.
	// Poll 3: a NEW stream, whose window carries a message from an hour ago.
	s := newRumbleStub(t, func(w http.ResponseWriter, _ *http.Request, call int64) {
		switch call {
		case 1:
			_, _ = w.Write([]byte(`{"livestreams":[{"is_live":true,"title":"first","chat":{"recent_messages":[
			  {"username":"A","badges":[],"text":"first show","created_on":"2026-08-13T17:59:00+00:00"}]}}]}`))
		case 2:
			_, _ = w.Write([]byte(`{"livestreams":[{"is_live":false,"title":"first","chat":{}}]}`))
		default:
			_, _ = w.Write([]byte(`{"livestreams":[{"is_live":true,"title":"second","chat":{"recent_messages":[
			  {"username":"B","badges":[],"text":"an hour stale","created_on":"2026-08-13T17:00:00+00:00"}]}}]}`))
		}
	})
	a := rumbleAdapter(t, s.URL, func(c *RumbleConfig) { c.Backfill = 5 * time.Minute })

	got, err := rumbleRun(t, a, 3)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if s.calls.Load() < 3 {
		t.Fatalf("only %d polls happened; this test needs three", s.calls.Load())
	}
	if len(got) != 1 {
		t.Fatalf("delivered %d messages, want 1 — the second broadcast replayed its stale window: %+v",
			len(got), got)
	}
	if got[0].Text != "first show" {
		t.Fatalf("delivered %q, want only the first show's fresh message", got[0].Text)
	}
}

// TestRumbleReportsTheViewerCountItWasAlreadyBeingSent pins the field read.
//
// watching_now is one of the 15 fields Rumble publishes, and it rides on the
// SAME get-data response this adapter has always polled for chat -- so the
// count costs no extra request against an endpoint whose rate limit Rumble
// declines to state. That is also why it is read here rather than through
// internal/oauth: there is no Rumble provider to hang a Stats method off,
// because this API has no sign-in at all.
//
// Proven able to fail against the committed tree by changing the `watching_now`
// json tag on rumbleLivestream.WatchingNow to `watching`, after which the
// documented payload decodes to nil and the count never appears.
func TestRumbleReportsTheViewerCountItWasAlreadyBeingSent(t *testing.T) {
	s := rumbleOK(t, rumbleSample)
	a := rumbleAdapter(t, s.URL, nil)

	if _, err := rumbleRun(t, a, 1); err != nil {
		t.Fatalf("Run = %v", err)
	}
	h := a.Health()
	if h.State != StateLive {
		t.Fatalf("state = %q, want %q -- the count is only meaningful on a live poll", h.State, StateLive)
	}
	if h.Viewers == nil {
		t.Fatal("Viewers = nil, want 19 -- Rumble's documented payload carries watching_now")
	}
	if *h.Viewers != 19 {
		t.Errorf("Viewers = %d, want 19 (the value in Rumble's own example payload)", *h.Viewers)
	}
}

// TestRumbleReportsNoCountRatherThanAnAudienceOfNone is the absent-is-not-zero
// test, and Rumble is where that rule stops being pedantry.
//
// Rumble's article is explicit that everything under livestreams is "only
// populated during a live stream". A plain int would therefore decode every
// offline, ended and not-yet-started broadcast into a viewer count of exactly
// zero -- a number polyemesis would render as fact and which Rumble never sent.
// The pointer is what keeps "no answer" and "an audience of none" apart, and
// the last case is what stops the pointer being decoration: a live stream that
// really does say 0 must still report 0.
//
// Proven able to fail against the committed tree by changing WatchingNow from
// *int to int and setLive to take an int, after which the FIRST case reports a
// count of 0 rather than no count at all. Only the first, and the reason is
// worth recording: the offline and ended cases never reach setLive at all, so
// they survive that mutation on Health's zero value rather than on the decision
// this test is about. They are here because they pin the other half of the
// contract -- that a stale or unmeasured count does not leak out of a poll that
// found nothing live -- not because they would catch a plain int.
func TestRumbleReportsNoCountRatherThanAnAudienceOfNone(t *testing.T) {
	tests := []struct {
		name string
		body string
		want *int // nil means "no count reported at all"
	}{
		{
			name: "live but Rumble sent no watching_now",
			body: `{"livestreams":[{"is_live":true,"title":"on air","chat":{"recent_messages":[
			  {"username":"A","badges":[],"text":"hello","created_on":"2026-08-13T17:59:00+00:00"}]}}]}`,
			want: nil,
		},
		{
			name: "nothing is live, so the field is unpopulated",
			body: `{"livestreams":[]}`,
			want: nil,
		},
		{
			// is_live is false, so liveStream skips the entry and the stale 400
			// never reaches Health. A count from a finished broadcast is not a
			// smaller audience; it is no audience being measured.
			name: "an ended broadcast still carrying its last count",
			body: `{"livestreams":[{"is_live":false,"title":"finished","watching_now":400,"chat":{}}]}`,
			want: nil,
		},
		{
			// Rumble SAID zero, so zero is the honest report.
			name: "live and genuinely nobody watching",
			body: `{"livestreams":[{"is_live":true,"title":"on air","watching_now":0,"chat":{"recent_messages":[
			  {"username":"A","badges":[],"text":"hello","created_on":"2026-08-13T17:59:00+00:00"}]}}]}`,
			want: rumbleViewers(0),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := rumbleOK(t, tc.body)
			a := rumbleAdapter(t, s.URL, nil)

			if _, err := rumbleRun(t, a, 1); err != nil {
				t.Fatalf("Run = %v", err)
			}
			got := a.Health().Viewers

			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("Viewers = %d, want no count at all -- Rumble reported none, and a zero "+
					"here is polyemesis inventing an audience of none", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("Viewers = nil, want %d -- Rumble sent a real number and it was dropped", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("Viewers = %d, want %d", *got, *tc.want)
			}

			// The wire form must say what the struct says: a nil count leaves
			// the key OUT, so no client can read a default zero back out of it.
			blob, err := json.Marshal(a.Health())
			if err != nil {
				t.Fatal(err)
			}
			if hasKey := strings.Contains(string(blob), `"viewers"`); hasKey != (tc.want != nil) {
				t.Errorf("health JSON = %s; viewers key present = %v, want %v",
					blob, hasKey, tc.want != nil)
			}
		})
	}
}

// TestRumbleReadsTheViewerCountWithoutDecodingTheStreamKeyBesideIt guards the
// decision that reading one field out of this payload is not a licence to read
// the secret next to it.
//
// watching_now and stream_key arrive in the same object from the same
// unauthenticated GET. The defence for the key is still an ABSENCE -- no field
// on rumbleLivestream -- and an absence is exactly what a later change adding
// "just one more field" erodes without noticing.
//
// IT ASSERTS AT THE STRUCT LEVEL BECAUSE THE MESSAGE LEVEL HAS A HOLE.
// TestRumbleNeverDecodesTheStreamKeyOutOfTheChatPayload checks what the adapter
// EMITS, so it only fires once a decoded key is also used in a message or a
// health detail. Measured while writing this: adding
// `StreamKey string `+"`json:\"stream_key\"`"+` to rumbleLivestream and
// touching nothing else leaves that test PASSING, with the key sitting decoded
// in memory one field away from every error string. This one fails on the field
// itself, which is where the #310 defence actually lives.
//
// Proven able to fail against the committed tree by adding
// `StreamKey string `+"`json:\"stream_key\"`"+` to rumbleLivestream, which is
// precisely the change this test exists to stop.
func TestRumbleReadsTheViewerCountWithoutDecodingTheStreamKeyBesideIt(t *testing.T) {
	const streamKey = "SUPER-SECRET-STREAM-KEY"
	if !strings.Contains(rumbleSample, streamKey) {
		t.Fatal("the fixture no longer carries a stream key; this test proves nothing")
	}

	var decoded rumbleResponse
	if err := json.Unmarshal([]byte(rumbleSample), &decoded); err != nil {
		t.Fatal(err)
	}

	// The count must actually have been read, or the assertion below would
	// pass against a struct that decodes nothing at all.
	live, ok := liveStream(decoded)
	if !ok || live.WatchingNow == nil || *live.WatchingNow != 19 {
		t.Fatalf("the viewer count was not decoded (%+v), so this test would pass for the wrong reason", live)
	}

	blob, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), streamKey) {
		t.Fatalf("rumbleLivestream now decodes the stream key that rides beside watching_now:\n%s", blob)
	}
}

// rumbleViewers is the "Rumble said this number" spelling for the table above.
// Package-local because internal/chat has no pointer helper, and the
// alternative at each call site is a named variable per case.
func rumbleViewers(v int) *int { return &v }
