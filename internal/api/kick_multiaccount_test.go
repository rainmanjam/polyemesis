package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
)

func kickDelivery(t *testing.T, s *Server, body string) int {
	t.Helper()
	r := chi.NewRouter()
	r.Post("/api/v1/chat/kick/{secret}", s.handleKickChatWebhook)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/kick/"+s.kickCallbackSecret(), strings.NewReader(body))
	req.Header.Set("Kick-Event-Type", "chat.message.sent")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code
}

// Kick has one webhook URL per app, so every connected channel's chat arrives
// at the same route. Each delivery has to reach the adapter for the channel it
// names, or the second channel's chat is credited to the first account.
func TestKickWebhookReachesTheAdapterForItsBroadcaster(t *testing.T) {
	s, _, _ := testServer(t, config.Config{})
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	permissive := func(*http.Request, []byte) error { return nil }
	for _, c := range []struct {
		id   int
		slug string
	}{{111, "first"}, {222, "second"}} {
		a, err := chat.NewKick(chat.KickConfig{
			BroadcasterUserID: c.id,
			Channel:           c.slug,
			Token:             chat.StaticToken("t"),
			CallbackSecret:    s.kickCallbackSecret(),
			Verify:            permissive,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.chat.Attach(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		// A delivery that beats the adapter's own Run is counted, not shown.
		for deadline := time.Now().Add(3 * time.Second); a.Health().State == chat.StateConnecting; {
			if time.Now().After(deadline) {
				t.Fatal("adapter never left connecting")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	ev := func(id, bid, text string) string {
		return `{"message_id":"` + id + `","broadcaster":{"user_id":` + bid +
			`,"channel_slug":"x"},"sender":{"user_id":7,"username":"v"},"content":"` + text + `"}`
	}
	if code := kickDelivery(t, s, ev("m1", "222", "hello second")); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if code := kickDelivery(t, s, ev("m2", "111", "hello first")); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	// A channel nobody here is attached to is acknowledged and dropped, not
	// credited to whichever account happens to be first.
	if code := kickDelivery(t, s, ev("m3", "333", "stranger")); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}

	want := map[string]string{"hello second": "222", "hello first": "111"}
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := map[string]string{}
		for _, m := range s.chat.History(0) {
			got[m.Text] = m.Account
		}
		if len(got) == len(want) {
			for text, acct := range want {
				if got[text] != acct {
					t.Fatalf("%q credited to account %q, want %q", text, got[text], acct)
				}
			}
			if _, bad := got["stranger"]; bad {
				t.Fatal("a delivery for an unattached channel was credited to an account")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("messages = %v, want %v", got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// One connected account is no exception: a delivery for a channel that is not
// attached here is dropped, not credited to the only account there is.
func TestKickWebhookForAForeignChannelIsDroppedEvenWithOneAdapter(t *testing.T) {
	s, _, _ := testServer(t, config.Config{})
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	a, err := chat.NewKick(chat.KickConfig{
		BroadcasterUserID: 111, Channel: "only", Token: chat.StaticToken("t"),
		CallbackSecret: s.kickCallbackSecret(),
		Verify:         func(*http.Request, []byte) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.chat.Attach(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Second); a.Health().State == chat.StateConnecting; {
		if time.Now().After(deadline) {
			t.Fatal("adapter never left connecting")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ev := func(id, bid, text string) string {
		return `{"message_id":"` + id + `","broadcaster":{"user_id":` + bid +
			`,"channel_slug":"x"},"sender":{"user_id":7,"username":"v"},"content":"` + text + `"}`
	}
	if code := kickDelivery(t, s, ev("f1", "999", "foreign")); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if code := kickDelivery(t, s, ev("f2", "111", "mine")); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		hist := s.chat.History(0)
		if len(hist) >= 1 {
			for _, m := range hist {
				if m.Text == "foreign" {
					t.Fatal("a foreign channel's chat was credited to the only account")
				}
			}
			if len(hist) == 1 && hist[0].Text == "mine" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("history = %v, want only 'mine'", hist)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A delivery for a channel nobody here listens to is still signature-checked:
// a forgery earns the same 401 whether or not its channel is connected.
func TestKickWebhookForAnUnconnectedChannelStillRejectsABadSignature(t *testing.T) {
	s, _, _ := testServer(t, config.Config{})
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	for _, id := range []int{111, 222} {
		a, err := chat.NewKick(chat.KickConfig{
			BroadcasterUserID: id, Token: chat.StaticToken("t"),
			CallbackSecret: s.kickCallbackSecret(),
			Verify:         func(*http.Request, []byte) error { return errors.New("bad signature") },
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.chat.Attach(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	body := `{"message_id":"z","broadcaster":{"user_id":999},"sender":{"user_id":7},"content":"forged"}`
	if code := kickDelivery(t, s, body); code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a bad signature on an unconnected channel", code)
	}
}
