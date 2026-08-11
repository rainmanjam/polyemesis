package api

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// These two tests are about the TRANSLATION the chat handlers perform before
// delegating to the Hub -- the default scope, and the wire's seconds becoming a
// time.Duration -- because that translation is the handler's own work and lives
// nowhere else. What the Hub then does with a Duration, and what each adapter
// converts it into, belong to internal/chat and are pinned there.
//
// Every assertion is a RECORDING made by the fake adapter. A status code would
// prove only that something answered.

// moderatableAdapter is a chat.Adapter that also implements Hider and Banner,
// and records every call it receives.
type moderatableAdapter struct {
	platform db.Platform
	account  string

	mu sync.Mutex

	hideCalls []hideCall
	banCalls  []banCall
}

type hideCall struct {
	messageID string
	hidden    bool
}

type banCall struct {
	userID string
	d      time.Duration
	reason string
}

func (a *moderatableAdapter) Platform() db.Platform { return a.platform }
func (a *moderatableAdapter) Account() string       { return a.account }

func (a *moderatableAdapter) Run(ctx context.Context, sink chat.Sink) error {
	<-ctx.Done()
	return nil
}

func (a *moderatableAdapter) Hide(_ context.Context, messageID string, hidden bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hideCalls = append(a.hideCalls, hideCall{messageID: messageID, hidden: hidden})
	return nil
}

func (a *moderatableAdapter) Ban(_ context.Context, userID string, d time.Duration, reason string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.banCalls = append(a.banCalls, banCall{userID: userID, d: d, reason: reason})
	return nil
}

func (a *moderatableAdapter) Unban(_ context.Context, userID string) error { return nil }

func (a *moderatableAdapter) hides() []hideCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]hideCall(nil), a.hideCalls...)
}

func (a *moderatableAdapter) bans() []banCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]banCall(nil), a.banCalls...)
}

// moderationFixture is a server with a Hub that has one recording adapter
// attached and the test's own store behind it, so a local hide is observable in
// the stored scrollback as well as in the adapter's silence.
func moderationFixture(t *testing.T) (http.Handler, func(*http.Request), *db.DB, *moderatableAdapter) {
	t.Helper()
	s, h, store := testServer(t, config.Config{})

	hub := chat.New(chat.WithStore(store))
	t.Cleanup(hub.Close)
	s.chat = hub

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	a := &moderatableAdapter{platform: db.PlatformTwitch, account: "acct"}
	if err := hub.Attach(ctx, a); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return h, login(t, h), store, a
}

// seedChatMessage writes one message into the durable scrollback GET
// /chat/messages reads from.
func seedChatMessage(t *testing.T, store *db.DB, id string) {
	t.Helper()
	n, err := store.AppendChatMessages([]db.ChatMessage{{
		Platform:   db.PlatformTwitch,
		Account:    "acct",
		MessageID:  id,
		AuthorID:   "u-1",
		AuthorName: "someone",
		Text:       "a message",
		At:         time.Now(),
	}})
	if err != nil || n != 1 {
		t.Fatalf("AppendChatMessages: %d, %v", n, err)
	}
}

// storedIDs is the message ids GET /chat/messages currently reports.
func storedIDs(t *testing.T, h http.Handler, sign func(*http.Request)) []string {
	t.Helper()
	var out struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/chat/messages",
		nil, http.StatusOK), &out)
	ids := make([]string, 0, len(out.Messages))
	for _, m := range out.Messages {
		ids = append(ids, m.ID)
	}
	return ids
}

// TestHidingWithoutAScopeStaysLocal is the overreach guard.
//
// The scope switch reads `case "", "local"`: a caller who omits the parameter
// gets the half that cannot fail and cannot reach the platform. That default is
// the handler's own decision, and getting it wrong means a moderator's
// accidental click writes to Twitch.
//
// The platform half is asserted FIRST-CLASS rather than as an afterthought.
// Without it, "the adapter was not called" would also pass with the adapter
// unreachable, the hub misattached or the route gone -- the test would be
// vacuous in both directions at once.
func TestHidingWithoutAScopeStaysLocal(t *testing.T) {
	h, sign, store, adapter := moderationFixture(t)
	seedChatMessage(t, store, "msg-local")
	seedChatMessage(t, store, "msg-platform")

	var out map[string]string
	decodeInto(t, send(t, h, sign, http.MethodPost,
		"/api/v1/chat/messages/hide?platform=twitch&account=acct&id=msg-local",
		nil, http.StatusOK), &out)

	if out["scope"] != "local" {
		t.Errorf("an omitted scope answered scope %q, want %q", out["scope"], "local")
	}
	if calls := adapter.hides(); len(calls) != 0 {
		t.Fatalf("an omitted scope reached the platform: the adapter recorded %v. "+
			"A caller who did not ask for a platform write must not get one", calls)
	}
	// The local half still has to DO something, or "did not call the platform"
	// is satisfied by a handler that does nothing at all.
	if got := storedIDs(t, h, sign); contains(got, "msg-local") {
		t.Errorf("msg-local is still in the stored scrollback after a local hide: %v", got)
	}

	decodeInto(t, send(t, h, sign, http.MethodPost,
		"/api/v1/chat/messages/hide?platform=twitch&account=acct&id=msg-platform&scope=platform",
		nil, http.StatusOK), &out)

	if out["scope"] != "platform" {
		t.Errorf("scope=platform answered scope %q", out["scope"])
	}
	calls := adapter.hides()
	if len(calls) != 1 {
		t.Fatalf("scope=platform recorded %d adapter calls, want exactly 1: %v", len(calls), calls)
	}
	if calls[0] != (hideCall{messageID: "msg-platform", hidden: true}) {
		t.Errorf("the platform hide reached the adapter as %+v, want message "+
			"msg-platform hidden=true", calls[0])
	}
}

// TestChatBanPassesTheExactDurationAndReportsTheVerb pins the unit conversion
// and the verb, both of which are the handler's.
//
// The wire carries SECONDS and exactly one unit exists in this API on purpose:
// the platforms disagree (Kick counts minutes) and each adapter converts at the
// last moment. The handler is where seconds become a time.Duration, so a
// dropped `* time.Second` would turn a ten-minute timeout into ten
// microseconds, silently, on every platform at once.
//
// The duration is compared as a time.Duration recorded AT THE ADAPTER -- never
// as elapsed wall-clock time, which would make this a flaky test about the
// machine rather than a test about arithmetic.
func TestChatBanPassesTheExactDurationAndReportsTheVerb(t *testing.T) {
	h, sign, _, adapter := moderationFixture(t)

	var out map[string]string
	decodeInto(t, send(t, h, sign, http.MethodPost,
		"/api/v1/chat/bans?platform=twitch&account=acct&userId=u-1&seconds=600&reason=spam",
		nil, http.StatusOK), &out)

	calls := adapter.bans()
	if len(calls) != 1 {
		t.Fatalf("recorded %d adapter bans, want 1: %v", len(calls), calls)
	}
	if calls[0].d != 10*time.Minute {
		t.Errorf("seconds=600 reached the adapter as %v, want %v -- the wire is in "+
			"seconds and the adapter takes a Duration", calls[0].d, 10*time.Minute)
	}
	if calls[0].userID != "u-1" || calls[0].reason != "spam" {
		t.Errorf("the ban reached the adapter as user %q reason %q, want u-1/spam",
			calls[0].userID, calls[0].reason)
	}
	if out["status"] != "timed out" || out["scope"] != "10m0s" {
		t.Errorf("a 600-second ban was reported as %v, want status \"timed out\" and "+
			"scope \"10m0s\": a caller must not have to infer which of the two things "+
			"it just did", out)
	}

	// Omitted duration is a PERMANENT ban on all three platforms, so it stays
	// a zero Duration rather than becoming some default.
	decodeInto(t, send(t, h, sign, http.MethodPost,
		"/api/v1/chat/bans?platform=twitch&account=acct&userId=u-2",
		nil, http.StatusOK), &out)

	calls = adapter.bans()
	if len(calls) != 2 {
		t.Fatalf("recorded %d adapter bans, want 2: %v", len(calls), calls)
	}
	if calls[1].d != 0 {
		t.Errorf("an omitted duration reached the adapter as %v, want 0 (permanent)",
			calls[1].d)
	}
	if out["status"] != "banned" || out["scope"] != "permanent" {
		t.Errorf("a ban with no duration was reported as %v, want status \"banned\" "+
			"and scope \"permanent\"", out)
	}
}
