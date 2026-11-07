package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func TestKickReplyCarriesTheMessageIDAndSendsAsTheUser(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"data":{"is_sent":true,"message_id":"sent-9"}}`)
	}))
	defer srv.Close()

	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL })
	m, err := a.Reply(context.Background(), " thanks ", " parent-1 ", false)
	if err != nil {
		t.Fatal(err)
	}
	if body["reply_to_message_id"] != "parent-1" || body["type"] != "user" || body["content"] != "thanks" {
		t.Fatalf("body = %+v", body)
	}
	if fmt.Sprint(body["broadcaster_user_id"]) != "99" {
		t.Fatalf("broadcaster id = %v", body["broadcaster_user_id"])
	}
	if m.ID != "sent-9" || m.ReplyToID != "parent-1" || !m.Author.Broadcaster {
		t.Fatalf("echo = %+v", m)
	}
}

func TestKickReplyAsBotOmitsTheBroadcasterID(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"data":{"is_sent":true,"message_id":"b-1"}}`)
	}))
	defer srv.Close()

	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL })
	m, err := a.Reply(context.Background(), "beep", "p", true)
	if err != nil {
		t.Fatal(err)
	}
	if body["type"] != "bot" {
		t.Fatalf("type = %v", body["type"])
	}
	if _, ok := body["broadcaster_user_id"]; ok {
		t.Fatalf("a bot post must not name a broadcaster: %+v", body)
	}
	if m.Author.Broadcaster {
		t.Fatal("a bot message was attributed to the broadcaster")
	}
}

func TestKickReplyRefusalsAreActionable(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		asBot   bool
		reply   string
		wantSub string
		fatal   bool
	}{
		{"missing parent id", 0, false, "  ", "no message to reply to", false},
		{"401 asks to reconnect", 401, false, "p", "reconnect", true},
		{"403 names chat:write", 403, false, "p", "chat:write", false},
		{"403 as bot explains the channel", 403, true, "p", "channel the token belongs to", false},
		{"400 says the parent may be gone", 400, false, "p", "may have been deleted", false},
		{"404 says the parent may be gone", 404, false, "p", "may have been deleted", false},
		{"429 says to wait", 429, false, "p", "rate-limiting", false},
		{"500 passes through", 500, false, "p", "500", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"message":"nope"}`)
			}))
			defer srv.Close()
			a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL })
			_, err := a.Reply(context.Background(), "hi", tc.reply, tc.asBot)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantSub)
			}
			if tc.fatal && !IsFatal(err) {
				t.Fatalf("error %v should be fatal", err)
			}
		})
	}
}

// A plain send that Kick refuses with 400 keeps the generic error: the
// reply-specific sentence would blame a message that was never involved.
func TestKickSendBadRequestIsNotBlamedOnAReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL })
	_, err := a.Send(context.Background(), "hi")
	if err == nil || strings.Contains(err.Error(), "deleted") {
		t.Fatalf("error = %v", err)
	}
}

type replyingAdapter struct {
	sendingAdapter
	gotReply string
	gotBot   bool
}

func (r *replyingAdapter) Reply(_ context.Context, text, id string, asBot bool) (Message, error) {
	r.gotReply, r.gotBot = id, asBot
	if r.sendErr != nil {
		return Message{}, r.sendErr
	}
	return Message{}, nil
}

func TestHubReply(t *testing.T) {
	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rep := &replyingAdapter{sendingAdapter: sendingAdapter{&fakeAdapter{platform: db.PlatformKick, account: "kk"}}}
	plain := sendingAdapter{&fakeAdapter{platform: db.PlatformTwitch, account: "tw"}}
	readOnly := &fakeAdapter{platform: db.PlatformFacebook, account: "fb"}
	for _, a := range []Adapter{rep, plain, readOnly} {
		if err := h.Attach(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	res, err := h.Reply(ctx, db.PlatformKick, "kk", "yo", "m1", true)
	if err != nil || !res.OK || rep.gotReply != "m1" || !rep.gotBot {
		t.Fatalf("res=%+v err=%v adapter=%+v", res, err, rep)
	}

	res, err = h.Reply(ctx, db.PlatformTwitch, "tw", "yo", "m1", false)
	if err != nil || !res.Skipped || !strings.Contains(res.Detail, "cannot reply") {
		t.Fatalf("a platform without replies must be refused, not sent plain: %+v %v", res, err)
	}
	if plain.sends != 0 {
		t.Fatal("the reply was posted as a plain message")
	}

	res, err = h.Reply(ctx, db.PlatformFacebook, "fb", "yo", "m1", false)
	if err != nil || !res.Skipped || !strings.Contains(res.Detail, "cannot post") {
		t.Fatalf("receive-only: %+v %v", res, err)
	}

	rep.sendErr = fmt.Errorf("kick said no")
	res, err = h.Reply(ctx, db.PlatformKick, "kk", "yo", "m1", false)
	if err != nil || res.OK || res.Detail != "kick said no" {
		t.Fatalf("failure not reported: %+v %v", res, err)
	}

	for name, call := range map[string]func() error{
		"empty text": func() error { _, e := h.Reply(ctx, db.PlatformKick, "kk", " ", "m1", false); return e },
		"no id":      func() error { _, e := h.Reply(ctx, db.PlatformKick, "kk", "x", " ", false); return e },
		"not connected": func() error {
			_, e := h.Reply(ctx, db.PlatformYouTube, "yt", "x", "m1", false)
			return e
		},
	} {
		if call() == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
