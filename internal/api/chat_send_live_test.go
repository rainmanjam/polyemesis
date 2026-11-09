package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// recAdapter is a chat that can send, and remembers what it was asked to post.
type recAdapter struct {
	platform db.Platform
	account  string
	live     bool // LiveChat answer, for the adapters that have one

	mu   sync.Mutex
	sent []string
}

func (a *recAdapter) Platform() db.Platform { return a.platform }
func (a *recAdapter) Account() string       { return a.account }
func (a *recAdapter) Run(ctx context.Context, _ chat.Sink) error {
	<-ctx.Done()
	return nil
}
func (a *recAdapter) Send(_ context.Context, text string) (chat.Message, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, text)
	return chat.Message{}, nil
}
func (a *recAdapter) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.sent)
}

// liveRec is a recAdapter that also answers chat.LiveChatter, as YouTube does.
type liveRec struct{ *recAdapter }

func (l liveRec) LiveChat() bool { return l.live }

type liveFixture struct {
	t    *testing.T
	s    *Server
	h    http.Handler
	sign func(*http.Request)

	twitch, kick, youtube *recAdapter
}

// newLiveFixture connects twitch, kick and youtube chats, with a stored account
// for each, and nothing on air. onAir installs what the engine reports.
func newLiveFixture(t *testing.T) *liveFixture {
	t.Helper()
	s, h, store := testServer(t, config.Config{})
	hub := chat.New()
	t.Cleanup(hub.Close)
	s.chat = hub
	f := &liveFixture{t: t, s: s, h: h, sign: login(t, h)}
	f.twitch = &recAdapter{platform: db.PlatformTwitch, account: "tw-ref"}
	f.kick = &recAdapter{platform: db.PlatformKick, account: "kk-ref"}
	f.youtube = &recAdapter{platform: db.PlatformYouTube, account: "yt-ref"}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for _, a := range []*recAdapter{f.twitch, f.kick} {
		if err := hub.Attach(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := hub.Attach(ctx, liveRec{f.youtube}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*recAdapter{f.twitch, f.kick, f.youtube} {
		if _, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
			Platform: a.platform, AccountName: string(a.platform), AccountRef: a.account,
			AccessToken: "t", ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	f.onAir() // nothing on air by default
	return f
}

// onAir sets which destinations the engine reports as on air.
func (f *liveFixture) onAir(dests ...onAirDest) {
	old := onAirDestinations
	onAirDestinations = func(*Server) []onAirDest { return dests }
	f.t.Cleanup(func() { onAirDestinations = old })
}

// accountID is the stored id of one of the fixture's accounts.
func (f *liveFixture) accountID(a *recAdapter) *int64 {
	f.t.Helper()
	accts, err := f.s.store.ListPlatformAccounts()
	if err != nil {
		f.t.Fatal(err)
	}
	for _, x := range accts {
		if x.Platform == a.platform && x.AccountRef == a.account {
			id := x.ID
			return &id
		}
	}
	f.t.Fatalf("no account for %s", a.platform)
	return nil
}

func (f *liveFixture) send(body map[string]any) (int, chatSendResponse, string) {
	f.t.Helper()
	r := jsonRequest(f.t, http.MethodPost, "/api/v1/chat/send", body)
	f.sign(r)
	w := do(f.t, f.h, r)
	var out chatSendResponse
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

func resultFor(out chatSendResponse, p db.Platform) (chat.SendResult, bool) {
	for _, r := range out.Results {
		if r.Platform == p {
			return r, true
		}
	}
	return chat.SendResult{}, false
}

// The staging bug: a message meant for the YouTube-only stream was posted in
// Kick and Twitch chats that were not streaming. By default it goes only to
// the platform with something on air, and the others are reported not live.
func TestChatSendDefaultsToTheLivePlatformsOnly(t *testing.T) {
	f := newLiveFixture(t)
	f.onAir(onAirDest{Platform: db.PlatformYouTube, AccountID: f.accountID(f.youtube)})

	code, out, raw := f.send(map[string]any{"text": "test"})
	if code != http.StatusOK {
		t.Fatalf("code = %d: %s", code, raw)
	}
	if f.youtube.count() != 1 || f.kick.count() != 0 || f.twitch.count() != 0 {
		t.Fatalf("posted youtube=%d kick=%d twitch=%d, want 1/0/0", f.youtube.count(), f.kick.count(), f.twitch.count())
	}
	if out.Sent != 1 || out.Skipped != 2 || out.Failed != 0 {
		t.Fatalf("tally = %+v", out)
	}
	for _, p := range []db.Platform{db.PlatformKick, db.PlatformTwitch} {
		r, ok := resultFor(out, p)
		if !ok || !r.Skipped || r.OK || r.Reason != "notLive" || r.Detail != "not live" {
			t.Errorf("%s = %+v, want skipped, reason notLive, detail %q", p, r, "not live")
		}
	}
}

// A destination linked to ANOTHER account on the same platform does not make
// this account's chat live.
func TestChatSendLiveIsPerAccount(t *testing.T) {
	f := newLiveFixture(t)
	other := int64(9999)
	f.onAir(onAirDest{Platform: db.PlatformTwitch, AccountID: &other})
	code, _, raw := f.send(map[string]any{"text": "x"})
	if code != http.StatusConflict {
		t.Fatalf("code = %d, want 409 (a destination on an unknown account is not this chat): %s", code, raw)
	}
}

// YouTube can be live without a destination of ours carrying it; the adapter
// says so, and that counts.
func TestChatSendTreatsAnAdapterReportedLiveChatAsLive(t *testing.T) {
	f := newLiveFixture(t)
	f.youtube.live = true
	code, out, raw := f.send(map[string]any{"text": "x"})
	if code != http.StatusOK || f.youtube.count() != 1 || f.kick.count() != 0 || out.Sent != 1 {
		t.Fatalf("code=%d youtube=%d kick=%d: %s", code, f.youtube.count(), f.kick.count(), raw)
	}
}

// A pasted stream key has no linked account: it counts for its platform when
// that platform has exactly one chat connected.
func TestChatSendUnlinkedDestinationCountsForTheOnlyChatOnItsPlatform(t *testing.T) {
	f := newLiveFixture(t)
	f.onAir(onAirDest{Platform: db.PlatformKick})
	code, out, raw := f.send(map[string]any{"text": "x"})
	if code != http.StatusOK || f.kick.count() != 1 || f.twitch.count() != 0 || out.Sent != 1 {
		t.Fatalf("code=%d kick=%d twitch=%d: %s", code, f.kick.count(), f.twitch.count(), raw)
	}
}

func TestChatSendWithNothingLiveAsksTheOperatorToChoose(t *testing.T) {
	f := newLiveFixture(t)
	code, _, raw := f.send(map[string]any{"text": "test"})
	if code != http.StatusConflict {
		t.Fatalf("code = %d, want 409: %s", code, raw)
	}
	if !strings.Contains(raw, "no platform is live; send ") {
		t.Errorf("body = %s, want the message that names the choice", raw)
	}
	if f.youtube.count()+f.kick.count()+f.twitch.count() != 0 {
		t.Fatal("something was posted although nothing is live")
	}
}

func TestChatSendToAllReachesEveryConnectedChat(t *testing.T) {
	f := newLiveFixture(t)
	code, out, raw := f.send(map[string]any{"text": "hi", "to": "all"})
	if code != http.StatusOK || out.Sent != 3 || f.kick.count() != 1 || f.twitch.count() != 1 || f.youtube.count() != 1 {
		t.Fatalf("code=%d out=%+v: %s", code, out, raw)
	}
}

func TestChatSendToLiveIsTheDefaultSpelledOut(t *testing.T) {
	f := newLiveFixture(t)
	f.onAir(onAirDest{Platform: db.PlatformYouTube, AccountID: f.accountID(f.youtube)})
	code, out, _ := f.send(map[string]any{"text": "hi", "to": "live"})
	if code != http.StatusOK || out.Sent != 1 || f.kick.count() != 0 {
		t.Fatalf("code=%d out=%+v", code, out)
	}
}

// An explicit list is sent to whether or not those platforms are live, and the
// platforms it leaves out are not mentioned: the operator named the targets.
func TestChatSendToAnExplicitListIgnoresLiveness(t *testing.T) {
	f := newLiveFixture(t)
	code, out, raw := f.send(map[string]any{"text": "hi", "platforms": []string{"kick", "twitch"}})
	if code != http.StatusOK || f.kick.count() != 1 || f.twitch.count() != 1 || f.youtube.count() != 0 {
		t.Fatalf("code=%d kick=%d twitch=%d youtube=%d: %s", code, f.kick.count(), f.twitch.count(), f.youtube.count(), raw)
	}
	if len(out.Results) != 2 || out.Sent != 2 || out.Skipped != 0 {
		t.Fatalf("out = %+v, want exactly the two named", out)
	}
}

func TestChatSendToAListedPlatformThatIsNotConnectedIsReportedSkipped(t *testing.T) {
	f := newLiveFixture(t)
	code, out, raw := f.send(map[string]any{"text": "hi", "platforms": []string{"kick", "rumble"}})
	if code != http.StatusOK {
		t.Fatalf("code=%d: %s", code, raw)
	}
	r, ok := resultFor(out, db.PlatformRumble)
	if !ok || !r.Skipped || r.Reason != "notConnected" || r.Detail != "not connected" {
		t.Fatalf("rumble = %+v, want skipped notConnected", r)
	}
	if out.Sent != 1 || out.Skipped != 1 {
		t.Fatalf("tally = %+v", out)
	}
}

func TestChatSendRefusesAnAmbiguousOrUnknownTarget(t *testing.T) {
	f := newLiveFixture(t)
	for name, body := range map[string]map[string]any{
		"to and platforms": {"text": "x", "to": "all", "platforms": []string{"kick"}},
		"unknown to":       {"text": "x", "to": "everyone"},
		"unknown platform": {"text": "x", "platforms": []string{"myspace"}},
		"to platforms":     {"text": "x", "to": "platforms"},
	} {
		if code, _, raw := f.send(body); code != http.StatusBadRequest {
			t.Errorf("%s: code=%d want 400 (%s)", name, code, raw)
		}
	}
	if f.youtube.count()+f.kick.count()+f.twitch.count() != 0 {
		t.Fatal("a refused request posted something")
	}
}

// An old client sends only {"text"}. It gets the live default, and when
// nothing is live the 409 says why rather than posting nowhere in silence.
func TestChatSendAnOldClientBodyIsTheLiveDefault(t *testing.T) {
	f := newLiveFixture(t)
	f.onAir(onAirDest{Platform: db.PlatformTwitch, AccountID: f.accountID(f.twitch)})
	code, out, _ := f.send(map[string]any{"text": "x"})
	if code != http.StatusOK || out.Sent != 1 || f.twitch.count() != 1 {
		t.Fatalf("code=%d out=%+v", code, out)
	}
}

// GET /chat/live is the composer's chips: the same reading a default send uses.
func TestChatLiveReportsTheSameSetADefaultSendUses(t *testing.T) {
	f := newLiveFixture(t)
	f.onAir(onAirDest{Platform: db.PlatformKick, AccountID: f.accountID(f.kick)})

	r := jsonRequest(t, http.MethodGet, "/api/v1/chat/live", nil)
	f.sign(r)
	w := do(t, f.h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", w.Code, w.Body.String())
	}
	var v chatLiveView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	live := map[db.Platform]bool{}
	for _, tg := range v.Targets {
		live[tg.Platform] = tg.Live
	}
	if len(v.Targets) != 3 || !v.AnyLive || !live[db.PlatformKick] || live[db.PlatformTwitch] || live[db.PlatformYouTube] {
		t.Fatalf("view = %+v", v)
	}

	// And with nothing on air the chips say so, which is when a send answers 409.
	f.onAir()
	r = jsonRequest(t, http.MethodGet, "/api/v1/chat/live", nil)
	f.sign(r)
	w = do(t, f.h, r)
	v = chatLiveView{}
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	if v.AnyLive || len(v.Targets) != 3 {
		t.Fatalf("view = %+v, want three chats, none live", v)
	}
}

// Polls follow the same live default: the announcement goes to the live chats,
// the others come back as "not live" in the poll's own record. Votes are still
// counted from every chat, because the hub taps them all.
func TestPollAnnouncementFollowsTheLiveDefault(t *testing.T) {
	f := newLiveFixture(t)
	f.onAir(onAirDest{Platform: db.PlatformTwitch, AccountID: f.accountID(f.twitch)})

	res, err := f.s.PollSender().SendEach(context.Background(), func(p db.Platform, _ string) string {
		if p == db.PlatformKick {
			return "" // a chat the poll has nothing to say to is simply absent
		}
		return "vote 1 or 2"
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.twitch.count() != 1 || f.youtube.count() != 0 || f.kick.count() != 0 {
		t.Fatalf("posted twitch=%d youtube=%d kick=%d, want 1/0/0", f.twitch.count(), f.youtube.count(), f.kick.count())
	}
	got := map[db.Platform]chat.SendResult{}
	for _, r := range res {
		got[r.Platform] = r
	}
	if !got[db.PlatformTwitch].OK {
		t.Errorf("twitch = %+v, want sent", got[db.PlatformTwitch])
	}
	if r := got[db.PlatformYouTube]; !r.Skipped || r.Reason != "notLive" {
		t.Errorf("youtube = %+v, want skipped notLive", r)
	}
	if _, ok := got[db.PlatformKick]; ok {
		t.Errorf("kick has a result although the poll had no text for it: %+v", got[db.PlatformKick])
	}
}

// Liveness is per account, so an operator with two channels on one platform can
// name the one they mean: "accounts" sends to exactly those chats.
func TestChatSendToNamedAccountsReachesOnlyThoseChats(t *testing.T) {
	f := newLiveFixture(t)
	second := &recAdapter{platform: db.PlatformTwitch, account: "tw-two"}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := f.s.chat.Attach(ctx, second); err != nil {
		t.Fatal(err)
	}
	code, out, raw := f.send(map[string]any{"text": "hi", "accounts": []map[string]string{
		{"platform": "twitch", "account": "tw-two"},
		{"platform": "kick", "account": "no-such"},
	}})
	if code != http.StatusOK {
		t.Fatalf("code=%d: %s", code, raw)
	}
	if second.count() != 1 || f.twitch.count() != 0 || f.kick.count() != 0 {
		t.Fatalf("posted second=%d twitch=%d kick=%d, want 1/0/0", second.count(), f.twitch.count(), f.kick.count())
	}
	if out.Sent != 1 || out.Skipped != 1 {
		t.Fatalf("out = %+v", out)
	}
	var missing chat.SendResult
	for _, r := range out.Results {
		if r.Platform == db.PlatformKick {
			missing = r
		}
	}
	if missing.Reason != "notConnected" || missing.Account != "no-such" {
		t.Fatalf("missing = %+v, want notConnected for the named account", missing)
	}
	// Alternatives, not a mix.
	if code, _, _ := f.send(map[string]any{"text": "x", "accounts": []map[string]string{{"platform": "kick", "account": "k"}}, "platforms": []string{"kick"}}); code != http.StatusBadRequest {
		t.Errorf("accounts with platforms: code = %d, want 400", code)
	}
}

// The 409 carries a code the UI translates by, and names the fields to send.
func TestChatSendNothingLiveIs409WithACodeAndTheFieldsToChooseWith(t *testing.T) {
	f := newLiveFixture(t)
	code, _, raw := f.send(map[string]any{"text": "test"})
	var e apiError
	_ = json.Unmarshal([]byte(raw), &e)
	if code != http.StatusConflict || e.Code != "no_platform_live" ||
		!strings.Contains(e.Error, `"to":"all"`) || !strings.Contains(e.Error, `"platforms"`) {
		t.Fatalf("code=%d body=%s", code, raw)
	}
}
