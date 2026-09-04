package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// Three functions on the wiring path had no coverage at all:
// facebookLiveVideoID, setFacebookBroadcast and kickChatHandler. Between them
// they are how a running adapter learns something it could not know at attach
// time -- which broadcast to read, and where a webhook should land -- so the
// failure they hide is a chat pane that stays empty and blames nothing.

func fbDestination(t *testing.T, store *db.DB, acct *int64, name, key string) {
	t.Helper()
	if _, err := store.CreateDestination(&db.Destination{
		Name: name, Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmps://live-api.example/rtmp", StreamKey: key, AccountID: acct,
	}); err != nil {
		t.Fatalf("create destination %q: %v", name, err)
	}
}

func TestTheBroadcastIDIsReadFromThisAccountsOwnDestination(t *testing.T) {
	s, _, store := testServer(t, config.Config{})

	mine := connectAccount(t, store, s.box, db.PlatformFacebook, "page-a")
	theirs := connectAccount(t, store, s.box, db.PlatformFacebook, "page-b")

	// A second Page's broadcast, and an unlinked destination. Neither may be
	// answered for "mine": comments would be read from somebody else's live
	// video and shown under this account's tab, which is a cross-account leak
	// dressed up as a working feature.
	fbDestination(t, store, &theirs, "Page B", "555000555")
	fbDestination(t, store, nil, "hand-rolled", "777000777")
	fbDestination(t, store, &mine, "Page A", "123456789")

	if got := s.facebookLiveVideoID(mine); got != "123456789" {
		t.Fatalf("facebookLiveVideoID(mine) = %q, want this account's own broadcast", got)
	}
}

func TestAKeyPastedByHandCarriesNoBroadcastAndSaysSoWithSilence(t *testing.T) {
	s, _, store := testServer(t, config.Config{})
	acct := connectAccount(t, store, s.box, db.PlatformFacebook, "page-a")

	// The supported degradation: an operator pasted a stream key rather than
	// connecting the account, so there is no live video object polyemesis
	// created. Empty is the answer, and Run turns it into one explained
	// sentence -- a guessed id would poll a video that is not theirs.
	fbDestination(t, store, &acct, "hand-pasted", "FB-x1?s=bl")

	if got := s.facebookLiveVideoID(acct); got != "" {
		t.Fatalf("facebookLiveVideoID = %q, want empty for a hand-pasted key", got)
	}
}

func TestAnAccountWithNoDestinationAtAllHasNoBroadcast(t *testing.T) {
	s, _, store := testServer(t, config.Config{})
	acct := connectAccount(t, store, s.box, db.PlatformFacebook, "page-a")
	if got := s.facebookLiveVideoID(acct); got != "" {
		t.Fatalf("facebookLiveVideoID = %q with nothing linked, want empty", got)
	}
}

// paths records what a stub API was asked for, so a test can assert that a
// running adapter changed which resource it polls.
type paths struct {
	mu   sync.Mutex
	seen []string
}

func (p *paths) add(s string) {
	p.mu.Lock()
	p.seen = append(p.seen, s)
	p.mu.Unlock()
}

func (p *paths) sawContaining(sub string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.seen {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestSettingTheBroadcastReachesAnAdapterAlreadyRunning(t *testing.T) {
	// This is the claim in setFacebookBroadcast's own comment: the key-refresh
	// path calls it after creating a live video, "so comments start arriving
	// without waiting for a restart". Asserted by watching what the adapter
	// asks the API for, because an implementation that stored the id somewhere
	// the poll loop never reads would satisfy every weaker assertion.
	var got paths
	fb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.add(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	t.Cleanup(fb.Close)

	s, _, _ := testServer(t, config.Config{})
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)

	adapter, err := chat.NewFacebook(chat.FacebookConfig{
		AccountRef: "page-a-ref",
		Channel:    "Page A",
		Token:      func(context.Context) (string, error) { return "at", nil },
		APIBase:    fb.URL,
		// No LiveVideoID yet: exactly the state the refresh path exists to fix.
		Interval: time.Millisecond,
		Sleep: func(ctx context.Context, _ time.Duration) bool {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(time.Millisecond):
				return true
			}
		},
	})
	if err != nil {
		t.Fatalf("NewFacebook: %v", err)
	}
	if err := s.chat.Attach(context.Background(), adapter); err != nil {
		t.Fatalf("attach: %v", err)
	}

	s.setFacebookBroadcast("page-a-ref", "998877")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got.sawContaining("998877") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the running adapter never polled the broadcast it was pointed at, so " +
		"comments would not arrive until the next restart")
}

func TestPointingAtABroadcastIsANoOpWhenThereIsNothingToPoint(t *testing.T) {
	s, _, _ := testServer(t, config.Config{})

	// Chat switched off entirely. The refresh path calls this unconditionally,
	// so a nil hub here has to be a quiet no-op rather than a panic that takes
	// out the key rotation the operator actually asked for.
	s.setFacebookBroadcast("page-a-ref", "998877")

	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	// Nothing attached under that ref, and an empty id. Neither may panic.
	s.setFacebookBroadcast("page-a-ref", "998877")
	s.setFacebookBroadcast("page-a-ref", "")
}

// ------------------------------------------------------------- kick webhook

func attachKick(t *testing.T, s *Server, ref string) {
	t.Helper()
	kick := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	t.Cleanup(kick.Close)

	adapter, err := chat.NewKick(chat.KickConfig{
		AccountRef:        ref,
		BroadcasterUserID: 12345,
		Token:             func(context.Context) (string, error) { return "at", nil },
		APIBase:           kick.URL,
	})
	if err != nil {
		t.Fatalf("NewKick: %v", err)
	}
	if err := s.chat.Attach(context.Background(), adapter); err != nil {
		t.Fatalf("attach kick: %v", err)
	}
}

func TestTheKickWebhookReachesTheAttachedAdaptersReceiver(t *testing.T) {
	s, h, _ := testServer(t, config.Config{})
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	attachKick(t, s, "12345")

	secret := s.kickCallbackSecret()
	if secret == "" {
		t.Fatal("no callback secret derived, so this route cannot be addressed at all")
	}

	// The adapter's receiver answers a GET by echoing ?probe=, which is how
	// Kick's console verifies the endpoint. Getting the echo back through the
	// real router is the proof that the path found the RIGHT adapter: the
	// route, the secret comparison and kickChatHandler's search all have to
	// hold for this byte to arrive.
	r := httptest.NewRequest(http.MethodGet, "/api/v1/chat/kick/"+secret+"?probe=alive", nil)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}
	if body := w.Body.String(); body != "alive" {
		t.Fatalf("body %q, want the probe echoed back by the Kick adapter", body)
	}
}

func TestAWebhookWithNoKickAttachedIs200RatherThanARetryStorm(t *testing.T) {
	s, h, _ := testServer(t, config.Config{})
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)

	// The secret is right -- this is Kick talking to the install it was told
	// to talk to -- but no Kick adapter is attached, which is the ordinary
	// state of a server whose operator disconnected the account. Kick retries
	// anything that is not 2xx, so a 404 or 500 here buys an ever-growing
	// retry storm for events nobody is ever going to want.
	secret := s.kickCallbackSecret()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/chat/kick/"+secret,
		strings.NewReader(`{"event":"chat.message.sent"}`))
	r.Header.Set("Content-Type", "application/json")
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("status %d with no Kick attached, want 200 so Kick stops retrying", w.Code)
	}

	// Same argument with chat switched off altogether.
	s.chat = nil
	r = httptest.NewRequest(http.MethodPost, "/api/v1/chat/kick/"+secret,
		strings.NewReader(`{"event":"chat.message.sent"}`))
	r.Header.Set("Content-Type", "application/json")
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("status %d with chat off, want 200 so Kick stops retrying", w.Code)
	}
}
