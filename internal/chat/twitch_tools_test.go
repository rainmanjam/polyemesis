package chat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// helixFake answers Helix paths from a table and records every request.
type helixFake struct {
	mu    sync.Mutex
	calls []fakeCall
	reply map[string]fakeReply
}

type fakeCall struct {
	Method, Path string
	Query        url.Values
	Body         map[string]any
}

type fakeReply struct {
	Status int
	Body   string
}

func newHelixFake(t *testing.T, reply map[string]fakeReply) (*helixFake, *TwitchAdapter) {
	t.Helper()
	f := &helixFake{reply: reply}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.calls = append(f.calls, fakeCall{r.Method, r.URL.Path, r.URL.Query(), body})
		rep, ok := f.reply[r.Method+" "+r.URL.Path]
		f.mu.Unlock()
		if !ok {
			rep = fakeReply{http.StatusNoContent, ""}
		}
		w.WriteHeader(rep.Status)
		_, _ = w.Write([]byte(rep.Body))
	}))
	t.Cleanup(srv.Close)
	return f, twitchForDelete(t, srv.URL)
}

func (f *helixFake) last() fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func (f *helixFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

const errBody = `{"error":"x","status":0,"message":"twitch words"}`

func TestAnnounceSendsMessageAndColour(t *testing.T) {
	f, a := newHelixFake(t, nil)
	if err := a.Announce(context.Background(), " hello ", "Purple"); err != nil {
		t.Fatal(err)
	}
	c := f.last()
	if c.Method != "POST" || c.Path != "/chat/announcements" || c.Body["message"] != "hello" || c.Body["color"] != "purple" {
		t.Fatalf("call = %+v", c)
	}
	if c.Query.Get("broadcaster_id") != "42" || c.Query.Get("moderator_id") != "42" {
		t.Fatalf("ids = %v", c.Query)
	}
	if err := a.Announce(context.Background(), "x", ""); err != nil || f.last().Body["color"] != "primary" {
		t.Fatalf("default colour: %v %+v", err, f.last())
	}
}

func TestAnnounceRefusesBadInputWithoutCalling(t *testing.T) {
	f, a := newHelixFake(t, nil)
	for _, tc := range []struct{ msg, color string }{
		{"", "blue"}, {strings.Repeat("a", 501), "blue"}, {"ok", "pink"},
	} {
		if err := a.Announce(context.Background(), tc.msg, tc.color); err == nil {
			t.Errorf("Announce(%q,%q) = nil", tc.msg[:min(len(tc.msg), 5)], tc.color)
		}
	}
	if f.count() != 0 {
		t.Fatal("a refused announcement reached Twitch")
	}
}

func TestToolErrorsSayWhatToDo(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{401, "reconnect"}, {403, "does not moderate"}, {429, "wait"},
	}
	for _, tc := range cases {
		_, a := newHelixFake(t, map[string]fakeReply{"POST /chat/announcements": {tc.status, errBody}})
		err := a.Announce(context.Background(), "hi", "blue")
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
			t.Errorf("status %d: err = %v, want it to contain %q", tc.status, err, tc.want)
		}
		if tc.status == 401 && !strings.Contains(err.Error(), "twitch words") {
			t.Errorf("Twitch's own message was dropped: %v", err)
		}
	}
	_, a := newHelixFake(t, map[string]fakeReply{"POST /chat/announcements": {429, errBody}})
	if err := a.Announce(context.Background(), "hi", "blue"); !strings.Contains(err.Error(), "announcements") {
		// 429 has a call-specific hint, which wins over the default.
		t.Errorf("announcement 429 lost its hint: %v", err)
	}
	_, a = newHelixFake(t, map[string]fakeReply{"PATCH /chat/pins": {429, errBody}})
	if err := a.UpdatePin(context.Background(), "m", 60); err == nil || !strings.Contains(err.Error(), "rate-limiting") {
		t.Errorf("default 429 text: %v", err)
	}
	_, a = newHelixFake(t, map[string]fakeReply{"POST /chat/announcements": {500, errBody}})
	if err := a.Announce(context.Background(), "hi", "blue"); err == nil || statusOf(err) != 500 {
		t.Errorf("a 500 should pass through unmapped, got %v", err)
	}
}

func TestToolsRefuseWithoutHelixCredentials(t *testing.T) {
	a, err := NewTwitch(TwitchConfig{Nick: "n", Channel: "n", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Announce(context.Background(), "x", ""); err == nil || !strings.Contains(err.Error(), "cannot moderate") {
		t.Fatalf("err = %v", err)
	}
}

func TestShoutoutResolvesLoginAndTracksCooldowns(t *testing.T) {
	f, a := newHelixFake(t, map[string]fakeReply{
		"GET /users": {200, `{"data":[{"id":"777"}]}`},
	})
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	a.tools.now = func() time.Time { return now }
	res, err := a.Shoutout(context.Background(), UserRef{Login: "@Friend"})
	if err != nil {
		t.Fatal(err)
	}
	c := f.last()
	if c.Path != "/chat/shoutouts" || c.Query.Get("to_broadcaster_id") != "777" || c.Query.Get("from_broadcaster_id") != "42" {
		t.Fatalf("call = %+v", c)
	}
	if got := f.calls[0].Query.Get("login"); got != "friend" {
		t.Fatalf("login lookup = %q", got)
	}
	if res.NextAnyAt == nil || !res.NextAnyAt.Equal(now.Add(2*time.Minute)) || !res.Targets["777"].Equal(now.Add(time.Hour)) {
		t.Fatalf("cooldowns = %+v", res)
	}

	n := f.count()
	if _, err := a.Shoutout(context.Background(), UserRef{ID: "888"}); err == nil || !strings.Contains(err.Error(), "wait 2m0s") {
		t.Fatalf("second shoutout inside 2 minutes: %v", err)
	}
	if f.count() != n {
		t.Fatal("a cooled-down shoutout reached Twitch")
	}
	now = now.Add(3 * time.Minute)
	if _, err := a.Shoutout(context.Background(), UserRef{ID: "777"}); err == nil || !strings.Contains(err.Error(), "57m") {
		t.Fatalf("same target inside 60 minutes: %v", err)
	}
	if _, err := a.Shoutout(context.Background(), UserRef{ID: "888"}); err != nil {
		t.Fatalf("a different target after 2 minutes: %v", err)
	}
	now = now.Add(2 * time.Hour)
	if cd := a.ShoutoutCooldowns(); cd.NextAnyAt != nil || len(cd.Targets) != 0 {
		t.Fatalf("cooldowns did not expire: %+v", cd)
	}
}

func TestShoutoutRefusals(t *testing.T) {
	_, a := newHelixFake(t, map[string]fakeReply{
		"GET /users":           {200, `{"data":[]}`},
		"POST /chat/shoutouts": {400, errBody},
	})
	if _, err := a.Shoutout(context.Background(), UserRef{Login: "ghost"}); err == nil || !strings.Contains(err.Error(), "no user named") {
		t.Fatalf("unknown login: %v", err)
	}
	if _, err := a.Shoutout(context.Background(), UserRef{}); err == nil {
		t.Fatal("empty user accepted")
	}
	if _, err := a.Shoutout(context.Background(), UserRef{ID: "42"}); err == nil || !strings.Contains(err.Error(), "themselves") {
		t.Fatalf("self shoutout: %v", err)
	}
	_, err := a.Shoutout(context.Background(), UserRef{ID: "9"})
	if err == nil || !strings.Contains(err.Error(), "live") {
		t.Fatalf("400 should explain the live requirement: %v", err)
	}
	// A refused shoutout must not start a cooldown.
	if cd := a.ShoutoutCooldowns(); cd.NextAnyAt != nil {
		t.Fatal("a refused shoutout started a cooldown")
	}
	_, a = newHelixFake(t, map[string]fakeReply{"GET /users": {500, errBody}})
	if _, err := a.Shoutout(context.Background(), UserRef{Login: "x"}); err == nil {
		t.Fatal("lookup failure swallowed")
	}
}

func TestPinCalls(t *testing.T) {
	f, a := newHelixFake(t, map[string]fakeReply{
		"GET /chat/pins": {200, `{"data":[{"message_id":"m1","sender_user_login":"bob","sender_user_name":"Bob","pinned_by_user_name":"Me","message":{"text":"hi"}}]}`},
	})
	ctx := context.Background()
	if err := a.PinMessage(ctx, "m1", 300); err != nil {
		t.Fatal(err)
	}
	if c := f.last(); c.Method != "PUT" || c.Query.Get("message_id") != "m1" || c.Query.Get("duration_seconds") != "300" {
		t.Fatalf("pin = %+v", c)
	}
	if err := a.PinMessage(ctx, "m1", 0); err != nil || f.last().Query.Get("duration_seconds") != "" {
		t.Fatalf("until stream end must omit duration: %v %+v", err, f.last())
	}
	if err := a.UpdatePin(ctx, "m1", 60); err != nil || f.last().Method != "PATCH" {
		t.Fatalf("update: %v %+v", err, f.last())
	}
	if err := a.Unpin(ctx, "m1"); err != nil || f.last().Method != "DELETE" {
		t.Fatalf("unpin: %v %+v", err, f.last())
	}
	p, err := a.GetPin(ctx)
	if err != nil || p == nil || p.MessageID != "m1" || p.Text != "hi" || p.SenderLogin != "bob" {
		t.Fatalf("GetPin = %+v, %v", p, err)
	}
	for _, bad := range []int{10, 1801} {
		if err := a.PinMessage(ctx, "m1", bad); err == nil {
			t.Errorf("duration %d accepted", bad)
		}
	}
	if err := a.PinMessage(ctx, " ", 30); err == nil {
		t.Error("blank message id accepted")
	}
}

func TestPinRefusals(t *testing.T) {
	for status, want := range map[int]string{409: "already pinned", 404: "no longer has"} {
		_, a := newHelixFake(t, map[string]fakeReply{"PUT /chat/pins": {status, errBody}})
		if err := a.PinMessage(context.Background(), "m", 0); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("status %d: %v", status, err)
		}
	}
	_, a := newHelixFake(t, map[string]fakeReply{"GET /chat/pins": {200, `{"data":[]}`}})
	if p, err := a.GetPin(context.Background()); err != nil || p != nil {
		t.Fatalf("no pin should be nil, got %+v %v", p, err)
	}
	_, a = newHelixFake(t, map[string]fakeReply{"GET /chat/pins": {401, errBody}})
	if _, err := a.GetPin(context.Background()); err == nil {
		t.Fatal("401 swallowed")
	}
}

func TestShield(t *testing.T) {
	f, a := newHelixFake(t, map[string]fakeReply{
		"GET /moderation/shield_mode": {200, `{"data":[{"is_active":true,"moderator_login":"mod","last_activated_at":"2022-01-01T00:00:00Z"}]}`},
		"PUT /moderation/shield_mode": {200, `{"data":[{"is_active":false}]}`},
	})
	st, err := a.GetShield(context.Background())
	if err != nil || !st.Active || st.ModeratorLogin != "mod" {
		t.Fatalf("GetShield = %+v, %v", st, err)
	}
	st, err = a.SetShield(context.Background(), false)
	if err != nil || st.Active || f.last().Body["is_active"] != false {
		t.Fatalf("SetShield = %+v, %v, %+v", st, err, f.last())
	}
	_, a = newHelixFake(t, map[string]fakeReply{"PUT /moderation/shield_mode": {200, `{}`}})
	if st, _ := a.SetShield(context.Background(), true); !st.Active {
		t.Fatal("an empty echo should fall back to the requested state")
	}
	_, a = newHelixFake(t, map[string]fakeReply{"PUT /moderation/shield_mode": {403, errBody}, "GET /moderation/shield_mode": {403, errBody}})
	if _, err := a.SetShield(context.Background(), true); err == nil {
		t.Fatal("403 swallowed")
	}
	if _, err := a.GetShield(context.Background()); err == nil {
		t.Fatal("403 swallowed on read")
	}
	if s := (shieldWire{}).state(); s.Active {
		t.Fatal("empty wire reads as active")
	}
}

func TestWarn(t *testing.T) {
	f, a := newHelixFake(t, nil)
	if err := a.Warn(context.Background(), UserRef{ID: "9"}, " stop "); err != nil {
		t.Fatal(err)
	}
	c := f.last()
	data, _ := c.Body["data"].(map[string]any)
	if c.Path != "/moderation/warnings" || data["user_id"] != "9" || data["reason"] != "stop" {
		t.Fatalf("warn = %+v", c)
	}
	if err := a.Warn(context.Background(), UserRef{ID: "9"}, ""); err == nil {
		t.Error("empty reason accepted")
	}
	if err := a.Warn(context.Background(), UserRef{ID: "9"}, strings.Repeat("é", 501)); err == nil {
		t.Error("501-character reason accepted")
	}
	_, a = newHelixFake(t, map[string]fakeReply{"POST /moderation/warnings": {409, errBody}})
	if err := a.Warn(context.Background(), UserRef{ID: "9"}, "x"); err == nil || !strings.Contains(err.Error(), "try again") {
		t.Fatalf("409: %v", err)
	}
}

func TestBlockedTermsListAddDelete(t *testing.T) {
	f, a := newHelixFake(t, map[string]fakeReply{
		"GET /moderation/blocked_terms":  {200, `{"data":[{"id":"i1","text":"foo","created_at":"c"}],"pagination":{}}`},
		"POST /moderation/blocked_terms": {200, `{"data":[{"id":"i2","text":"bar"}]}`},
	})
	ts, err := a.ListBlockedTerms(context.Background())
	if err != nil || len(ts) != 1 || ts[0].ID != "i1" {
		t.Fatalf("list = %+v, %v", ts, err)
	}
	bt, err := a.AddBlockedTerm(context.Background(), "bar")
	if err != nil || bt.ID != "i2" || f.last().Body["text"] != "bar" {
		t.Fatalf("add = %+v, %v", bt, err)
	}
	if err := a.DeleteBlockedTerm(context.Background(), "i2"); err != nil || f.last().Method != "DELETE" || f.last().Query.Get("id") != "i2" {
		t.Fatalf("delete: %v %+v", err, f.last())
	}
	for _, bad := range []string{"a", strings.Repeat("z", 501)} {
		if _, err := a.AddBlockedTerm(context.Background(), bad); err == nil {
			t.Errorf("term of %d chars accepted", len(bad))
		}
	}
	if err := a.DeleteBlockedTerm(context.Background(), ""); err == nil {
		t.Error("empty id accepted")
	}
	_, a = newHelixFake(t, map[string]fakeReply{"POST /moderation/blocked_terms": {200, `{"data":[]}`}, "GET /moderation/blocked_terms": {401, errBody}})
	if bt, err := a.AddBlockedTerm(context.Background(), "zz"); err != nil || bt.Text != "zz" {
		t.Fatalf("empty echo: %+v %v", bt, err)
	}
	if _, err := a.ListBlockedTerms(context.Background()); err == nil {
		t.Fatal("401 swallowed")
	}
}

func TestBlockedTermsPageThroughCursor(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		after := r.URL.Query().Get("after")
		seen = append(seen, after)
		if after == "" {
			io.WriteString(w, `{"data":[{"id":"1","text":"aa"}],"pagination":{"cursor":"c2"}}`)
			return
		}
		io.WriteString(w, `{"data":[{"id":"2","text":"bb"}],"pagination":{}}`)
	}))
	defer srv.Close()
	a := twitchForDelete(t, srv.URL)
	ts, err := a.ListBlockedTerms(context.Background())
	if err != nil || len(ts) != 2 || len(seen) != 2 || seen[1] != "c2" {
		t.Fatalf("ts=%+v seen=%v err=%v", ts, seen, err)
	}
}

func TestSyncBlockedTermsAddsOnlyAndExplainsSkips(t *testing.T) {
	f, a := newHelixFake(t, map[string]fakeReply{
		"GET /moderation/blocked_terms":  {200, `{"data":[{"id":"i1","text":"Already"}],"pagination":{}}`},
		"POST /moderation/blocked_terms": {200, `{"data":[{"id":"n","text":"x"}]}`},
	})
	res, err := a.SyncBlockedTerms(context.Background(),
		[]string{"fresh", "ALREADY", "wild*", "x", "fresh", " other one "})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 2 || res.Added[0] != "fresh" || res.Added[1] != "other one" {
		t.Fatalf("added = %v", res.Added)
	}
	if len(res.Present) != 1 || len(res.Skipped) != 2 {
		t.Fatalf("present=%v skipped=%+v", res.Present, res.Skipped)
	}
	for _, c := range f.calls {
		if c.Method == "DELETE" {
			t.Fatal("sync removed a Twitch term")
		}
	}
	// A failure mid-way reports how far it got.
	_, a = newHelixFake(t, map[string]fakeReply{
		"GET /moderation/blocked_terms":  {200, `{"data":[]}`},
		"POST /moderation/blocked_terms": {429, errBody},
	})
	res, err = a.SyncBlockedTerms(context.Background(), []string{"aa", "bb"})
	if err == nil || !strings.Contains(err.Error(), "stopped after adding 0 of 2") {
		t.Fatalf("err = %v", err)
	}
	_, a = newHelixFake(t, map[string]fakeReply{"GET /moderation/blocked_terms": {401, errBody}})
	if _, err := a.SyncBlockedTerms(context.Background(), []string{"aa"}); err == nil {
		t.Fatal("list failure swallowed")
	}
}

func TestChatters(t *testing.T) {
	f, a := newHelixFake(t, map[string]fakeReply{
		"GET /chat/chatters": {200, `{"data":[{"user_id":"1","user_login":"a","user_name":"A"}],"total":812,"pagination":{}}`},
	})
	c, err := a.Chatters(context.Background(), false)
	if err != nil || c.Total != 812 || len(c.Chatters) != 0 || f.last().Query.Get("first") != "1" {
		t.Fatalf("count = %+v, %v, %v", c, err, f.last().Query)
	}
	c, err = a.Chatters(context.Background(), true)
	if err != nil || len(c.Chatters) != 1 || c.Chatters[0].Login != "a" || c.Truncated {
		t.Fatalf("list = %+v, %v", c, err)
	}
	if f.last().Query.Get("first") != "1000" {
		t.Fatalf("page size = %v", f.last().Query)
	}
	_, a = newHelixFake(t, map[string]fakeReply{"GET /chat/chatters": {401, errBody}})
	if _, err := a.Chatters(context.Background(), false); err == nil || !strings.Contains(err.Error(), "reconnect") {
		t.Fatalf("old-scope token: %v", err)
	}
}

func TestChattersTruncatesAtThePageCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"user_id":"1","user_login":"a","user_name":"A"}],"total":9999,"pagination":{"cursor":"more"}}`)
	}))
	defer srv.Close()
	c, err := twitchForDelete(t, srv.URL).Chatters(context.Background(), true)
	if err != nil || !c.Truncated || len(c.Chatters) != twitchChatterPageLimit || c.Total != 9999 {
		t.Fatalf("c = %+v err = %v", c, err)
	}
}

func intp(n int) *int { return &n }

func TestAutoModPutReplacesSoItSendsOverallAloneOrEveryCategory(t *testing.T) {
	f, a := newHelixFake(t, map[string]fakeReply{
		"PUT /moderation/automod/settings": {200, `{"data":[{"overall_level":3,"swearing":3}]}`},
	})
	got, err := a.SetAutoMod(context.Background(), AutoModSettings{OverallLevel: intp(3), Swearing: 1})
	if err != nil || got.OverallLevel == nil || *got.OverallLevel != 3 {
		t.Fatalf("got %+v, %v", got, err)
	}
	b := f.last().Body
	if len(b) != 1 || b["overall_level"] != float64(3) {
		t.Fatalf("overall level must go alone (both is a 400), body = %v", b)
	}

	if _, err := a.SetAutoMod(context.Background(), AutoModSettings{Swearing: 2}); err != nil {
		t.Fatal(err)
	}
	b = f.last().Body
	if _, has := b["overall_level"]; has || len(b) != 8 {
		t.Fatalf("a category change must send all eight categories and no overall, body = %v", b)
	}
	if v, ok := b["bullying"]; !ok || v != float64(0) {
		t.Fatalf("a zero category must still be sent, or Twitch zeroes it by omission: %v", b)
	}
	if b["swearing"] != float64(2) {
		t.Fatalf("swearing = %v", b["swearing"])
	}
}

func TestAutoModValidationAndReads(t *testing.T) {
	_, a := newHelixFake(t, map[string]fakeReply{
		"GET /moderation/automod/settings": {200, `{"data":[{"overall_level":null,"aggression":2,"race_ethnicity_or_religion":4}]}`},
	})
	if _, err := a.SetAutoMod(context.Background(), AutoModSettings{OverallLevel: intp(5)}); err == nil {
		t.Error("overall 5 accepted")
	}
	if _, err := a.SetAutoMod(context.Background(), AutoModSettings{Bullying: -1}); err == nil {
		t.Error("category -1 accepted")
	}
	s, err := a.GetAutoMod(context.Background())
	if err != nil || s.OverallLevel != nil || s.Aggression != 2 || s.RaceEthnicityReligion != 4 {
		t.Fatalf("read = %+v, %v", s, err)
	}
	_, a = newHelixFake(t, map[string]fakeReply{
		"GET /moderation/automod/settings": {200, `{"data":[]}`},
		"PUT /moderation/automod/settings": {200, `{"data":[]}`},
	})
	if _, err := a.GetAutoMod(context.Background()); err == nil {
		t.Error("an empty read must be an error, not zero levels")
	}
	if got, err := a.SetAutoMod(context.Background(), AutoModSettings{Misogyny: 1}); err != nil || got.Misogyny != 1 {
		t.Errorf("empty echo should fall back to the request: %+v %v", got, err)
	}
	_, a = newHelixFake(t, map[string]fakeReply{"GET /moderation/automod/settings": {403, errBody}, "PUT /moderation/automod/settings": {401, errBody}})
	if _, err := a.GetAutoMod(context.Background()); err == nil {
		t.Error("403 swallowed")
	}
	if _, err := a.SetAutoMod(context.Background(), AutoModSettings{}); err == nil {
		t.Error("401 swallowed")
	}
}

func TestResolveHeldMessage(t *testing.T) {
	f, a := newHelixFake(t, nil)
	if err := a.ResolveHeldMessage(context.Background(), "h1", true); err != nil {
		t.Fatal(err)
	}
	b := f.last().Body
	if f.last().Path != "/moderation/automod/message" || b["user_id"] != "42" || b["msg_id"] != "h1" || b["action"] != "ALLOW" {
		t.Fatalf("allow = %+v", f.last())
	}
	if err := a.ResolveHeldMessage(context.Background(), "h1", false); err != nil || f.last().Body["action"] != "DENY" {
		t.Fatalf("deny: %v %+v", err, f.last())
	}
	if err := a.ResolveHeldMessage(context.Background(), " ", true); err == nil {
		t.Error("blank id accepted")
	}
	_, a = newHelixFake(t, map[string]fakeReply{"POST /moderation/automod/message": {404, errBody}})
	if err := a.ResolveHeldMessage(context.Background(), "h1", true); err == nil || !strings.Contains(err.Error(), "expires") {
		t.Fatalf("404: %v", err)
	}
}

func TestRoles(t *testing.T) {
	f, a := newHelixFake(t, map[string]fakeReply{
		"GET /moderation/moderators": {200, `{"data":[{"user_id":"1","user_login":"m","user_name":"M"}],"pagination":{}}`},
		"GET /channels/vips":         {200, `{"data":[{"user_id":"2","user_login":"v","user_name":"V"}]}`},
		"GET /users":                 {200, `{"data":[{"id":"55"}]}`},
	})
	ctx := context.Background()
	mods, err := a.ListRole(ctx, "moderator")
	if err != nil || len(mods) != 1 || mods[0].Login != "m" {
		t.Fatalf("mods = %+v %v", mods, err)
	}
	vips, err := a.ListRole(ctx, "vip")
	if err != nil || len(vips) != 1 || vips[0].UserID != "2" {
		t.Fatalf("vips = %+v %v", vips, err)
	}
	if err := a.AddRole(ctx, "vip", UserRef{Login: "friend"}); err != nil {
		t.Fatal(err)
	}
	if c := f.last(); c.Method != "POST" || c.Path != "/channels/vips" || c.Query.Get("user_id") != "55" {
		t.Fatalf("add vip = %+v", c)
	}
	if err := a.RemoveRole(ctx, "moderator", UserRef{ID: "7"}); err != nil {
		t.Fatal(err)
	}
	if c := f.last(); c.Method != "DELETE" || c.Path != "/moderation/moderators" || c.Query.Get("user_id") != "7" {
		t.Fatalf("remove mod = %+v", c)
	}
	if _, err := a.ListRole(ctx, "admin"); err == nil {
		t.Error("unknown role accepted")
	}
	if err := a.AddRole(ctx, "admin", UserRef{ID: "1"}); err == nil {
		t.Error("unknown role accepted on add")
	}
}

func TestRoleRefusalsIncludeTheTierGate(t *testing.T) {
	for status, want := range map[int]string{
		425: "Build a Community", 409: "VIP slot", 422: "moderator and a VIP",
	} {
		_, a := newHelixFake(t, map[string]fakeReply{"POST /channels/vips": {status, errBody}})
		err := a.AddRole(context.Background(), "vip", UserRef{ID: "9"})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("status %d: err = %v, want %q", status, err, want)
		}
	}
	_, a := newHelixFake(t, map[string]fakeReply{"GET /channels/vips": {401, errBody}})
	if _, err := a.ListRole(context.Background(), "vip"); err == nil {
		t.Error("401 swallowed")
	}
}

func TestBroadcasterOnlyCallsRefuseForAModerator(t *testing.T) {
	f, a := newHelixFake(t, nil)
	a.cfg.ModeratorID = "99"
	ctx := context.Background()
	if err := a.AddRole(ctx, "vip", UserRef{ID: "1"}); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Errorf("add role as moderator: %v", err)
	}
	if _, err := a.ListRole(ctx, "moderator"); err == nil {
		t.Error("list role as moderator")
	}
	if _, err := a.ListBanned(ctx); err == nil {
		t.Error("banned list as moderator")
	}
	if f.count() != 0 {
		t.Fatal("a broadcaster-only call reached Twitch from a moderator")
	}
}

func TestListBanned(t *testing.T) {
	_, a := newHelixFake(t, map[string]fakeReply{
		"GET /moderation/banned": {200, `{"data":[{"user_id":"1","user_login":"x","user_name":"X","reason":"r","created_at":"c","expires_at":""}],"pagination":{}}`},
	})
	bs, err := a.ListBanned(context.Background())
	if err != nil || len(bs) != 1 || bs[0].Reason != "r" {
		t.Fatalf("banned = %+v %v", bs, err)
	}
	_, a = newHelixFake(t, map[string]fakeReply{"GET /moderation/banned": {401, errBody}})
	if _, err := a.ListBanned(context.Background()); err == nil {
		t.Fatal("401 swallowed")
	}
}

func TestUnbanRequests(t *testing.T) {
	f, a := newHelixFake(t, map[string]fakeReply{
		"GET /moderation/unban_requests":   {200, `{"data":[{"id":"u1","user_id":"1","user_login":"x","text":"sorry","status":"pending"}],"pagination":{}}`},
		"PATCH /moderation/unban_requests": {200, `{"data":[{"id":"u1","status":"approved","resolution_text":"ok"}]}`},
	})
	ctx := context.Background()
	rs, err := a.ListUnbanRequests(ctx, "")
	if err != nil || len(rs) != 1 || rs[0].Text != "sorry" || f.last().Query.Get("status") != "pending" {
		t.Fatalf("list = %+v %v %v", rs, err, f.last().Query)
	}
	r, err := a.ResolveUnbanRequest(ctx, "u1", "Approved", " ok ")
	if err != nil || r.Status != "approved" {
		t.Fatalf("resolve = %+v %v", r, err)
	}
	q := f.last().Query
	if f.last().Method != "PATCH" || q.Get("unban_request_id") != "u1" || q.Get("status") != "approved" || q.Get("resolution_text") != "ok" {
		t.Fatalf("resolve call = %+v", f.last())
	}
	if _, err := a.ListUnbanRequests(ctx, "bogus"); err == nil {
		t.Error("bogus status accepted")
	}
	for _, tc := range []struct{ id, st, text string }{
		{"", "approved", ""}, {"u", "pending", ""}, {"u", "approved", strings.Repeat("x", 501)},
	} {
		if _, err := a.ResolveUnbanRequest(ctx, tc.id, tc.st, tc.text); err == nil {
			t.Errorf("resolve(%q,%q) accepted", tc.id, tc.st)
		}
	}
}

func TestUnbanRequestRefusals(t *testing.T) {
	_, a := newHelixFake(t, map[string]fakeReply{
		"GET /moderation/unban_requests":   {400, errBody},
		"PATCH /moderation/unban_requests": {404, errBody},
	})
	if _, err := a.ListUnbanRequests(context.Background(), "pending"); err == nil || !strings.Contains(err.Error(), "not be receiving") {
		t.Fatalf("400: %v", err)
	}
	if _, err := a.ResolveUnbanRequest(context.Background(), "u", "denied", ""); err == nil || !strings.Contains(err.Error(), "deny that unban request") || !strings.Contains(err.Error(), "no such unban") {
		t.Fatalf("404: %v", err)
	}
	_, a = newHelixFake(t, map[string]fakeReply{"PATCH /moderation/unban_requests": {200, `{"data":[]}`}})
	if r, err := a.ResolveUnbanRequest(context.Background(), "u", "denied", ""); err != nil || r.Status != "denied" {
		t.Fatalf("empty echo: %+v %v", r, err)
	}
}

func TestSuspicious(t *testing.T) {
	f, a := newHelixFake(t, nil)
	ctx := context.Background()
	if err := a.SetSuspicious(ctx, UserRef{ID: "9"}, "restricted"); err != nil {
		t.Fatal(err)
	}
	if c := f.last(); c.Method != "POST" || c.Body["status"] != "RESTRICTED" || c.Body["user_id"] != "9" {
		t.Fatalf("set = %+v", c)
	}
	if err := a.ClearSuspicious(ctx, UserRef{ID: "9"}); err != nil || f.last().Method != "DELETE" || f.last().Query.Get("user_id") != "9" {
		t.Fatalf("clear: %v %+v", err, f.last())
	}
	if err := a.SetSuspicious(ctx, UserRef{ID: "9"}, "MAYBE"); err == nil {
		t.Error("bad status accepted")
	}
	if err := a.ClearSuspicious(ctx, UserRef{}); err == nil {
		t.Error("blank user accepted")
	}
	_, a = newHelixFake(t, map[string]fakeReply{"POST /moderation/suspicious_users": {400, errBody}})
	if err := a.SetSuspicious(ctx, UserRef{ID: "9"}, "ACTIVE_MONITORING"); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("400: %v", err)
	}
}
