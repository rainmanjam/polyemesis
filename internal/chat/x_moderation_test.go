package chat

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

var xNow = time.Unix(1761904800, 0)

// Send hands back X's own timestamp as the message id, so Delete takes it as is and a reply can name it.
func TestXSendEchoCarriesTheTimestampAsItsID(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, xNow)
	m, err := a.Send(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if m.ID != "1761904800000000000" || !m.Echo {
		t.Errorf("echo id = %q echo=%v, want X's timestamp", m.ID, m.Echo)
	}
	if err := a.Delete(context.Background(), m.ID); err != nil {
		t.Errorf("Delete of the echo's own id: %v", err)
	}
	if got := st.countCalls("DELETE /2/broadcasts/B1run/chat/1761904800000000000"); got != 1 {
		t.Errorf("delete calls = %d, want 1 for the timestamp id", got)
	}
}

// A timestamp that is not digits falls back to a synthesised id rather than an unaddressable one.
func TestXSendFallsBackWhenTheTimestampIsNotDigits(t *testing.T) {
	st := newXChatStub(t)
	st.sendBody = `{"data":{"success":true,"timestamp":"soon"}}`
	a := st.adapter(t, xNow)
	m, err := a.Send(context.Background(), "hello")
	if err != nil || !strings.HasPrefix(m.ID, "syn-x-") {
		t.Errorf("Send = %+v, %v; want a syn-x- id", m, err)
	}
}

func TestXListMutesReadsExpiryUsersAndPages(t *testing.T) {
	st := newXChatStub(t)
	st.mutePages = map[string]string{
		"": `{"data":[{"user_id":"11","end_at_ms":"1761905400000"},{"user_id":"22"}],` +
			`"includes":{"users":[{"id":"11","username":"loud","name":"Loud One"}]},"meta":{"next_token":"p2"}}`,
		"p2": `{"data":[{"user_id":"33"}],"meta":{"next_token":"p3"}}`,
		"p3": `{"data":[],"meta":{"next_token":"p3"}}`,
	}
	a := st.adapter(t, xNow)
	got, truncated, err := a.ListMutes(context.Background())
	if err != nil || truncated {
		t.Fatalf("ListMutes: truncated %v, err %v", truncated, err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d mutes, want 3 across the pages: %+v", len(got), got)
	}
	if got[0].Username != "loud" || got[0].EndsAt == nil || got[0].EndsAt.UnixMilli() != 1761905400000 {
		t.Errorf("timed mute = %+v, want name and expiry", got[0])
	}
	if got[1].EndsAt != nil || got[2].EndsAt != nil {
		t.Errorf("indefinite mutes carry an expiry: %+v", got[1:])
	}
	q := st.queries[0]
	for _, want := range []string{"broadcast_chat_mute.fields=end_at_ms", "expansions=user_id", "user.fields="} {
		if !strings.Contains(q, want) {
			t.Errorf("first query %q lacks %s", q, want)
		}
	}
	if n := st.countCalls("GET /2/broadcasts/chat/mutes"); n != 3 {
		t.Errorf("mute list calls = %d, want 3 (the empty page ends it)", n)
	}
}

// A list X still has more of after the pages polyemesis will read is returned with truncated set, not as if complete.
func TestXListMutesSaysWhenTheListWasCutOff(t *testing.T) {
	st := newXChatStub(t)
	st.mutePages = map[string]string{}
	for i := 0; i < xMuteListPages; i++ {
		key := ""
		if i > 0 {
			key = fmt.Sprintf("t%d", i)
		}
		st.mutePages[key] = fmt.Sprintf(`{"data":[{"user_id":"%d"}],"meta":{"next_token":"t%d"}}`, i+1, i+1)
	}
	got, truncated, err := st.adapter(t, xNow).ListMutes(context.Background())
	if err != nil || !truncated || len(got) != xMuteListPages {
		t.Fatalf("ListMutes = %d mutes, truncated %v, err %v; want %d, true", len(got), truncated, err, xMuteListPages)
	}
}

// A moderator's token is refused the owner's list, and the sentence says so in X's words.
func TestXListMutesRefusalIsReadable(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, xNow)
	_, _, err := a.ListMutes(context.Background())
	if err == nil || !strings.Contains(err.Error(), "only the broadcast owner") || !strings.Contains(err.Error(), "moderator") {
		t.Errorf("err = %v, want X's words and the moderator hint", err)
	}
}

func TestXBanDeletingSendsBroadcastAndMessageInOneCall(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, xNow)
	if err := a.BanDeleting(context.Background(), "555", 10*time.Minute, "spam", "1761904800123456789"); err != nil {
		t.Fatalf("BanDeleting: %v", err)
	}
	var body string
	for _, c := range st.recorded() {
		if strings.HasPrefix(c, "POST /2/broadcasts/chat/mutes ") {
			body = c
		}
	}
	for _, want := range []string{`"broadcast_id":"B1run"`, `"message_id":"1761904800123456789"`, `"user_id":"555"`, `"end_at_ms":"1761905400000"`} {
		if !strings.Contains(body, want) {
			t.Errorf("mute body %q lacks %s", body, want)
		}
	}
}

func TestXBanDeletingRefusesBadIDsWithoutARequest(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, xNow)
	for _, c := range []struct{ user, msg string }{{"0555", "1"}, {"5", "0123"}, {"5", "syn-x-1"}, {"x", "1"}} {
		if err := a.BanDeleting(context.Background(), c.user, 0, "", c.msg); err == nil {
			t.Errorf("user %q message %q was accepted", c.user, c.msg)
		}
	}
	if n := len(st.recorded()); n != 0 {
		t.Errorf("%d request(s) for refused ids", n)
	}
}

// With nothing live the call is refused in words before the mute, because message_id needs a live broadcast.
func TestXBanDeletingNeedsALiveBroadcast(t *testing.T) {
	st := newXChatStub(t)
	st.broadcasts = `{"data":[{"id":"B0done","state":"ENDED"}]}`
	a := st.adapter(t, xNow)
	if err := a.BanDeleting(context.Background(), "555", 0, "", "1"); err == nil {
		t.Error("BanDeleting with no running broadcast succeeded")
	}
	if st.countCalls("POST /2/broadcasts/chat/mutes") != 0 {
		t.Error("a mute was sent with no live broadcast")
	}
}

func TestXModeratorsListAddRemove(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, xNow)
	ctx := context.Background()
	mods, err := a.ListModerators(ctx)
	if err != nil || len(mods) != 1 || mods[0].Username != "helper" {
		t.Fatalf("ListModerators = %+v, %v", mods, err)
	}
	ids, err := a.AddModerator(ctx, "888")
	if err != nil || len(ids) != 2 {
		t.Fatalf("AddModerator = %v, %v", ids, err)
	}
	if !strings.Contains(strings.Join(st.recorded(), "\n"), `POST /2/broadcasts/chat/moderators {"user_id":"888"}`) {
		t.Errorf("add body wrong: %v", st.recorded())
	}
	ids, err = a.RemoveModerator(ctx, "888")
	if err != nil || len(ids) != 1 || st.countCalls("DELETE /2/broadcasts/chat/moderators/888") != 1 {
		t.Fatalf("RemoveModerator = %v, %v; calls %v", ids, err, st.recorded())
	}
}

func TestXModeratorRefusals(t *testing.T) {
	st := newXChatStub(t)
	a := st.adapter(t, xNow)
	ctx := context.Background()
	if _, err := a.AddModerator(ctx, "not-an-id"); err == nil {
		t.Error("a non-numeric moderator id was accepted")
	}
	if _, err := a.RemoveModerator(ctx, "0"); err == nil {
		t.Error("a zero moderator id was accepted")
	}
	if n := len(st.recorded()); n != 0 {
		t.Errorf("%d request(s) for refused ids", n)
	}
	st.modStatus = 400
	st.modBody = `{"title":"Invalid Request","detail":"too many moderators"}`
	_, err := a.AddModerator(ctx, "999")
	if err == nil || !strings.Contains(err.Error(), "too many moderators") || !strings.Contains(err.Error(), "at most 20") {
		t.Errorf("21st moderator err = %v, want X's words and the limit", err)
	}
	st.modStatus, st.modBody = 403, `{"title":"Forbidden"}`
	if _, err := a.AddModerator(ctx, "999"); err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("403 err = %v", err)
	}
}

func TestXRefusalWording(t *testing.T) {
	for status, want := range map[int]string{401: "reconnect", 429: "rate limiting", 500: "X answered 500"} {
		err := &apiError{Status: status, Body: ""}
		if got := xRefusal(err); !strings.Contains(got, want) {
			t.Errorf("status %d -> %q, want %q", status, got, want)
		}
	}
	if got := xRefusal(&apiError{Status: 400, Body: `{"errors":[{"message":"bad field"}]}`}); got != "bad field" {
		t.Errorf("errors[] message = %q", got)
	}
}
