package chat

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// A deleted message is a tombstone in a list page: its id and publish time and
// nothing else. The sink hears it as a retraction, once per sighting, and the
// tombstone is not shown as a message. The removed messageDeletedEvent and
// messageRetractedEvent types are not read at all.
func TestAYouTubeTombstoneRetractsTheDeletedMessage(t *testing.T) {
	a := ytAdapter(t, "http://example.invalid", nil)
	sink := &recordingSink{}
	items := []ytChatItem{
		{ID: "gone-1"}, {ID: "kept"}, {ID: "legacy"},
	}
	items[0].Snippet.Type = "tombstone"
	items[0].Snippet.PublishedAt = "2026-03-01T19:59:00Z"
	items[1].Snippet.Type = "textMessageEvent"
	items[1].Snippet.DisplayMessage = "hello"
	items[1].Snippet.PublishedAt = "2026-03-01T19:59:30Z"
	items[2].Snippet.Type = "messageDeletedEvent" // removed from the docs; must be inert
	n := a.deliverPage(items, time.Time{}, sink)
	if n != 1 || len(sink.messages) != 1 || sink.messages[0].ID != "kept" {
		t.Fatalf("delivered %d %v, want only the live message", n, sink.messages)
	}
	if got, _ := sink.snapshot(); len(got) != 1 || got[0] != "gone-1" {
		t.Fatalf("retracted %v, want [gone-1]", got)
	}
	// A sink that cannot hear retractions is a legitimate answer.
	if n := a.deliverPage(items[:1], time.Time{}, SinkFunc(func(Message) {})); n != 0 {
		t.Errorf("tombstone counted as delivered: %d", n)
	}
}

func pollPage(id, status string, tallies ...string) ytChatItem {
	var it ytChatItem
	it.ID = id
	it.Snippet.Type = "pollDetails"
	it.Snippet.PollDetails = &struct {
		Metadata struct {
			Status  string `json:"status"`
			Options []struct {
				Tally string `json:"tally"`
			} `json:"options"`
		} `json:"metadata"`
	}{}
	md := &it.Snippet.PollDetails.Metadata
	md.Status = status
	for _, tl := range tallies {
		md.Options = append(md.Options, struct {
			Tally string `json:"tally"`
		}{Tally: tl})
	}
	return it
}

// The tally of a RUNNING poll is read from the list page that carries the poll
// message, without closing it.
func TestAYouTubeRunningPollTallyIsReadFromTheChatPage(t *testing.T) {
	a := ytAdapter(t, "http://example.invalid", nil)
	if _, _, ok := a.PollTally("p1"); ok {
		t.Fatal("a tally before any page")
	}
	a.deliverPage([]ytChatItem{pollPage("p1", "active", "4", "9")}, time.Time{}, SinkFunc(func(Message) {}))
	votes, live, ok := a.PollTally("p1")
	if !ok || !live || len(votes) != 2 || votes[0] != 4 || votes[1] != 9 {
		t.Fatalf("PollTally = %v %v %v", votes, live, ok)
	}
	votes[0] = 99 // a copy: the caller cannot rewrite the cache
	if v, _, _ := a.PollTally("p1"); v[0] != 4 {
		t.Error("PollTally returned its own storage")
	}
	// A later page moves it; a closed status ends the live reading.
	a.deliverPage([]ytChatItem{pollPage("p1", "closed", "5", "9")}, time.Time{}, SinkFunc(func(Message) {}))
	if v, live, _ := a.PollTally("p1"); live || v[0] != 5 {
		t.Errorf("after close: %v live=%v", v, live)
	}
	// Without a tally (a token that is not the channel owner's) nothing is
	// recorded, rather than a row of zeros.
	a.deliverPage([]ytChatItem{pollPage("p2", "active", "", "")}, time.Time{}, SinkFunc(func(Message) {}))
	if _, _, ok := a.PollTally("p2"); ok {
		t.Error("an absent tally was recorded")
	}
	a.deliverPage([]ytChatItem{pollPage("p3", "active")}, time.Time{}, SinkFunc(func(Message) {}))
	if _, _, ok := a.PollTally("p3"); ok {
		t.Error("a poll with no options was recorded")
	}
	// The cache is bounded.
	for i := 0; i <= ytMaxTallies; i++ {
		a.notePoll(pollPage("x"+string(rune('a'+i%26))+strings.Repeat("z", i), "active", "1"))
	}
	if len(a.tallies) > ytMaxTallies {
		t.Errorf("cache grew to %d", len(a.tallies))
	}
}

type modServer struct {
	mu     sync.Mutex
	calls  []string
	status int
	body   string
}

func (f *modServer) handler(w http.ResponseWriter, r *http.Request, _ int64) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" "+string(b))
	f.mu.Unlock()
	if f.status != 0 {
		w.WriteHeader(f.status)
	}
	_, _ = w.Write([]byte(f.body))
}

const modItem = `{"id":"mod-1","snippet":{"moderatorDetails":{"channelId":"UC-m","displayName":"Mo","channelUrl":"https://youtube.com/c/m","profileImageUrl":"https://i/x.png"}}}`

func modAdapter(t *testing.T, f *modServer) *YouTubeAdapter {
	t.Helper()
	srv := newYTStub(t, f.handler)
	return ytAdapter(t, srv.URL, func(c *YouTubeConfig) { c.LiveChatID = "chat-1" })
}

func TestYouTubeListsAddsAndRemovesModerators(t *testing.T) {
	f := &modServer{body: `{"items":[` + modItem + `]}`}
	a := modAdapter(t, f)
	ctx := context.Background()

	mods, err := a.Moderators(ctx)
	if err != nil || len(mods) != 1 || mods[0].ID != "mod-1" || mods[0].ChannelID != "UC-m" || mods[0].DisplayName != "Mo" {
		t.Fatalf("Moderators = %+v, %v", mods, err)
	}
	if c := f.calls[0]; !strings.HasPrefix(c, "GET /liveChat/moderators?") || !strings.Contains(c, "liveChatId=chat-1") ||
		!strings.Contains(c, "part=snippet") {
		t.Errorf("list call = %q", c)
	}

	f.body = modItem
	m, err := a.AddModerator(ctx, " UC-m ")
	if err != nil || m.ID != "mod-1" {
		t.Fatalf("AddModerator = %+v, %v", m, err)
	}
	if c := f.calls[1]; !strings.HasPrefix(c, "POST /liveChat/moderators?part=snippet") ||
		!strings.Contains(c, `"liveChatId":"chat-1"`) || !strings.Contains(c, `"channelId":"UC-m"`) {
		t.Errorf("insert call = %q", c)
	}

	f.body = ""
	if err := a.RemoveModerator(ctx, "mod-1"); err != nil {
		t.Fatalf("RemoveModerator: %v", err)
	}
	if c := f.calls[2]; !strings.HasPrefix(c, "DELETE /liveChat/moderators?id=mod-1") {
		t.Errorf("delete call = %q", c)
	}
	// 50 + 200 + 200 at the quota table's prices.
	if got := a.budget.status().Used; got != 450 {
		t.Errorf("spent %d units, want 450", got)
	}
}

func TestYouTubeModeratorCallsRefuseBadInputWithoutCalling(t *testing.T) {
	f := &modServer{}
	a := modAdapter(t, f)
	ctx := context.Background()
	if _, err := a.AddModerator(ctx, "  "); err == nil {
		t.Error("blank channel accepted")
	}
	if err := a.RemoveModerator(ctx, ""); err == nil {
		t.Error("blank id accepted")
	}
	noChat := ytAdapter(t, "http://example.invalid", nil)
	if _, err := noChat.Moderators(ctx); err == nil || !strings.Contains(err.Error(), "no live chat") {
		t.Errorf("Moderators without a chat = %v", err)
	}
	if _, err := noChat.AddModerator(ctx, "UC"); err == nil {
		t.Error("AddModerator without a chat succeeded")
	}
	if len(f.calls) != 0 {
		t.Errorf("a refused call reached YouTube: %v", f.calls)
	}
}

func TestYouTubeModeratorRefusalsAreSaidInPlainWords(t *testing.T) {
	ctx := context.Background()
	f := &modServer{status: http.StatusForbidden, body: `{"error":{"code":403,"errors":[{"reason":"forbidden"}]}}`}
	a := modAdapter(t, f)
	if _, err := a.AddModerator(ctx, "UC"); err == nil || !strings.Contains(err.Error(), "Only the owner of the channel") {
		t.Errorf("add 403 = %v", err)
	}
	if _, err := a.Moderators(ctx); err == nil || !strings.Contains(err.Error(), "Only the owner of the channel") {
		t.Errorf("list 403 = %v", err)
	}
	if err := a.RemoveModerator(ctx, "m"); err == nil || !strings.Contains(err.Error(), "Only the owner of the channel") {
		t.Errorf("remove 403 = %v", err)
	}

	f.status, f.body = http.StatusNotFound, `{"error":{"code":404}}`
	if err := a.RemoveModerator(ctx, "m"); err == nil || !strings.Contains(err.Error(), "already be removed") {
		t.Errorf("remove 404 = %v", err)
	}

	// A spent quota is the quota's message, not an authority one.
	f.status, f.body = http.StatusForbidden, `{"error":{"code":403,"errors":[{"reason":"quotaExceeded"}]}}`
	if _, err := a.AddModerator(ctx, "UC"); err == nil || !strings.Contains(err.Error(), "quota is spent") {
		t.Errorf("add quota = %v", err)
	}
}

func TestYouTubeModeratorCallsStopWhenTheBudgetCannotPayForThem(t *testing.T) {
	f := &modServer{body: `{"items":[]}`}
	a := modAdapter(t, f)
	a.budget.spend(a.budget.status().Limit - 10)
	if _, err := a.Moderators(context.Background()); err == nil || !strings.Contains(err.Error(), "quota is spent") {
		t.Errorf("list = %v", err)
	}
	if _, err := a.AddModerator(context.Background(), "UC"); err == nil || !strings.Contains(err.Error(), "quota is spent") {
		t.Errorf("add = %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("calls reached YouTube: %v", f.calls)
	}
	// Removing is never refused for budget.
	if err := a.RemoveModerator(context.Background(), "m"); err != nil {
		t.Errorf("remove = %v", err)
	}
}

type chatModeratorAdapter struct {
	*fakeAdapter
	removed string
}

func (m *chatModeratorAdapter) Moderators(context.Context) ([]ChatModerator, error) {
	return []ChatModerator{{ID: "m1", ChannelID: "UC1"}}, nil
}
func (m *chatModeratorAdapter) AddModerator(_ context.Context, id string) (ChatModerator, error) {
	return ChatModerator{ID: "m2", ChannelID: id}, nil
}
func (m *chatModeratorAdapter) RemoveModerator(_ context.Context, id string) error {
	m.removed = id
	return nil
}

func TestTheHubRoutesModeratorCallsToTheRightAdapter(t *testing.T) {
	h := testHub(t)
	ctx := context.Background()
	ad := &chatModeratorAdapter{fakeAdapter: &fakeAdapter{platform: db.PlatformYouTube, account: "acct"}}
	if err := h.Attach(ctx, ad); err != nil {
		t.Fatal(err)
	}
	if mods, err := h.Moderators(ctx, db.PlatformYouTube, "acct"); err != nil || len(mods) != 1 {
		t.Fatalf("Moderators = %v, %v", mods, err)
	}
	if m, err := h.AddModerator(ctx, db.PlatformYouTube, "acct", "UC9"); err != nil || m.ChannelID != "UC9" {
		t.Fatalf("AddModerator = %v, %v", m, err)
	}
	if err := h.RemoveModerator(ctx, db.PlatformYouTube, "acct", "m1"); err != nil || ad.removed != "m1" {
		t.Fatalf("RemoveModerator = %v (%q)", err, ad.removed)
	}
	// Not connected, and connected but incapable: both say so.
	if _, err := h.Moderators(ctx, db.PlatformTwitch, "x"); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Errorf("unconnected = %v", err)
	}
	plain := &fakeAdapter{platform: db.PlatformKick, account: "k"}
	if err := h.Attach(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if _, err := h.AddModerator(ctx, db.PlatformKick, "k", "u"); err == nil || !strings.Contains(err.Error(), "cannot manage moderators") {
		t.Errorf("incapable = %v", err)
	}
	if err := h.RemoveModerator(ctx, db.PlatformKick, "k", "u"); err == nil {
		t.Error("incapable remove succeeded")
	}
}

// liveChatBans.insert takes snippet.type permanent or temporary; only a
// temporary ban carries banDurationSeconds, and a sub-second timeout rounds UP
// rather than becoming a permanent ban.
func TestYouTubeBanSendsPermanentOrTemporaryAsAsked(t *testing.T) {
	f := &modServer{body: `{}`}
	a := modAdapter(t, f)
	ctx := context.Background()
	if err := a.Ban(ctx, "UC-bad", 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := a.Ban(ctx, "UC-bad", 600*time.Second, ""); err != nil {
		t.Fatal(err)
	}
	if err := a.Ban(ctx, "UC-bad", 500*time.Millisecond, ""); err != nil {
		t.Fatal(err)
	}
	perm, temp, sub := f.calls[0], f.calls[1], f.calls[2]
	if !strings.Contains(perm, `"type":"permanent"`) || strings.Contains(perm, "banDurationSeconds") {
		t.Errorf("permanent ban = %q", perm)
	}
	if !strings.Contains(temp, `"type":"temporary"`) || !strings.Contains(temp, `"banDurationSeconds":600`) {
		t.Errorf("temporary ban = %q", temp)
	}
	if !strings.Contains(sub, `"type":"temporary"`) || !strings.Contains(sub, `"banDurationSeconds":1`) {
		t.Errorf("sub-second ban = %q", sub)
	}
	// 200 units each at the quota table's price.
	if got := a.budget.status().Used; got != 3*200 {
		t.Errorf("spent %d, want 600", got)
	}
}
