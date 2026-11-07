package chat

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

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
		if strings.HasPrefix(c, "POST /2/broadcasts/chat/mutes ") {
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
		if strings.HasPrefix(c, "POST /2/broadcasts/chat/mutes ") && strings.Contains(c, "end_at_ms") {
			t.Errorf("a permanent mute carried end_at_ms: %q", c)
		}
	}
}

// TestXMutesAreTheBroadcastersOwnAndNeedNoBroadcast: X's mutes belong to the broadcaster and apply to every broadcast
// they host (docs.x.com/livestream-api/chat/mute-user), so the routes are POST /2/broadcasts/chat/mutes and
// DELETE /2/broadcasts/chat/mutes/{user_id} with no broadcast id, and no live broadcast is needed to mute. The body
// carries no broadcast_id: that is for a moderator acting in someone else's chats.
func TestXMutesAreTheBroadcastersOwnAndNeedNoBroadcast(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Unix(1761904800, 0))
	a.told = ""
	if err := a.Ban(context.Background(), "555", 0, ""); err != nil {
		t.Fatalf("Ban: %v", err)
	}
	if err := a.Unban(context.Background(), "555"); err != nil {
		t.Fatalf("Unban: %v", err)
	}
	for _, c := range st.recorded() {
		if strings.Contains(c, "broadcast_id") {
			t.Errorf("a broadcaster's own mute carried a broadcast_id: %q", c)
		}
		if strings.HasPrefix(c, "GET /2/broadcasts") {
			t.Errorf("a mute asked X for a running broadcast it does not need: %q", c)
		}
	}
}

// TestXUnbanDeletesTheMute maps Unban onto DELETE /2/broadcasts/chat/mutes/{user_id}.
func TestXUnbanDeletesTheMute(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Unix(1761904800, 0))
	if err := a.Unban(context.Background(), "555"); err != nil {
		t.Fatalf("Unban: %v", err)
	}
	found := false
	for _, c := range st.recorded() {
		if strings.HasPrefix(c, "DELETE /2/broadcasts/chat/mutes/555 ") {
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

// TestXSendUsesTheToldBroadcastAndFallsBackToTheListWhenCleared: Send, Delete and the mutes address the broadcast
// the coordinator named, which saves the list read on every call. Clearing the tell (the broadcast ended) goes back
// to asking X which broadcast is running.
func TestXSendUsesTheToldBroadcastAndFallsBackToTheListWhenCleared(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, time.Unix(1761904800, 0))
	ctx := context.Background()

	a.SetBroadcast("B7told")
	if _, err := a.Send(ctx, "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if n := st.countCalls("POST /2/broadcasts/B7told/chat"); n != 1 {
		t.Errorf("sends to the told broadcast = %d, want 1; calls %v", n, st.recorded())
	}
	if n := st.countCalls("GET /2/broadcasts"); n != 0 {
		t.Errorf("broadcast reads = %d, want 0 while a broadcast is told", n)
	}

	a.SetBroadcast("")
	if _, err := a.Send(ctx, "again"); err != nil {
		t.Fatalf("Send after clearing: %v", err)
	}
	if n := st.countCalls("POST /2/broadcasts/B1run/chat"); n != 1 {
		t.Errorf("sends to the listed running broadcast = %d, want 1; calls %v", n, st.recorded())
	}
}
