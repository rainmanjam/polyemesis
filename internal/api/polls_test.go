package api

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/eventstream/streamtest"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/polls"
)

// pollServer is a test server with a poll engine on its own store.
func pollServer(t *testing.T, o Options) (*Server, http.Handler, *db.DB) {
	t.Helper()
	s, h, store := testServerWith(t, o)
	s.polls = polls.New(store, s.stream)
	t.Cleanup(s.polls.Wait)
	return s, h, store
}

func pollSpec() polls.Spec {
	return polls.Spec{Question: "Next map?", Options: []string{"Dust", "Mirage"}, DurationSeconds: 60}
}

var pollBody = map[string]any{"question": "Next map?", "options": []string{"Dust", "=HYPERLINK(\"x\")"}, "durationSeconds": 60}

func TestPollRoutesAnswer503WithoutAPollEngine(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	send(t, h, sign, http.MethodGet, "/api/v1/polls", nil, http.StatusServiceUnavailable)
	send(t, h, sign, http.MethodPost, "/api/v1/polls", pollBody, http.StatusServiceUnavailable)
	send(t, h, sign, http.MethodGet, "/api/v1/polls/1", nil, http.StatusServiceUnavailable)
	send(t, h, sign, http.MethodPost, "/api/v1/polls/1/end", nil, http.StatusServiceUnavailable)
	send(t, h, sign, http.MethodGet, "/api/v1/polls/export", nil, http.StatusServiceUnavailable)
}

func TestPollRoutesRequireAuthentication(t *testing.T) {
	_, h, _ := pollServer(t, Options{})
	for _, p := range []string{"/api/v1/polls", "/api/v1/polls/1", "/api/v1/polls/export"} {
		if w := do(t, h, jsonRequest(t, http.MethodGet, p, nil)); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s anonymously = %d", p, w.Code)
		}
	}
}

// TestAPollIsStartedListedEndedAndExported walks the whole REST surface as
// the dashboard uses it.
func TestAPollIsStartedListedEndedAndExported(t *testing.T) {
	s, h, _ := pollServer(t, Options{})
	sign := login(t, h)

	var started polls.Result
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/polls", pollBody, http.StatusCreated), &started)
	if started.ID == 0 || started.State != polls.StateOpen || len(started.Options) != 2 {
		t.Fatalf("started = %+v", started)
	}
	// One at a time; an invalid one is a 400.
	send(t, h, sign, http.MethodPost, "/api/v1/polls", pollBody, http.StatusConflict)
	send(t, h, sign, http.MethodPost, "/api/v1/polls",
		map[string]any{"question": "q", "options": []string{"a"}, "durationSeconds": 60}, http.StatusBadRequest)

	s.polls.Offer(chat.Message{ID: "m1", Platform: db.PlatformTwitch, Account: "a",
		Author: chat.Author{ID: "u1", Name: "u1"}, Text: "1"}, chat.DisplayClear)

	var list pollsResponse
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/polls", nil, http.StatusOK), &list)
	if list.Current == nil || list.Current.ID != started.ID || list.Current.Total != 1 {
		t.Fatalf("current = %+v", list.Current)
	}
	if len(list.History) != 1 || list.History[0].Total != 1 {
		t.Errorf("the running poll's history row is not the live one: %+v", list.History)
	}
	var one polls.Result
	decodeInto(t, send(t, h, sign, http.MethodGet, fmt.Sprintf("/api/v1/polls/%d", started.ID), nil, http.StatusOK), &one)
	if one.Total != 1 {
		t.Errorf("GET one = %+v", one)
	}

	send(t, h, sign, http.MethodPost, fmt.Sprintf("/api/v1/polls/%d/end", started.ID+1), nil, http.StatusConflict)
	send(t, h, sign, http.MethodPost, "/api/v1/polls/x/end", nil, http.StatusBadRequest)
	var ended polls.Result
	decodeInto(t, send(t, h, sign, http.MethodPost, fmt.Sprintf("/api/v1/polls/%d/end", started.ID), nil, http.StatusOK), &ended)
	if ended.State != polls.StateEnded || !ended.EndedEarly || ended.Total != 1 {
		t.Fatalf("ended = %+v", ended)
	}

	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/polls", nil, http.StatusOK), &list)
	if list.Current != nil || len(list.History) != 1 || list.History[0].State != polls.StateEnded || list.History[0].Total != 1 {
		t.Fatalf("after the end = %+v", list)
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, fmt.Sprintf("/api/v1/polls/%d", started.ID), nil, http.StatusOK), &one)
	if one.State != polls.StateEnded || one.Options[0].ByPlatform["twitch"] != 1 {
		t.Errorf("stored = %+v", one)
	}
	send(t, h, sign, http.MethodGet, "/api/v1/polls/999", nil, http.StatusNotFound)
	send(t, h, sign, http.MethodGet, "/api/v1/polls/x", nil, http.StatusBadRequest)
	send(t, h, sign, http.MethodGet, "/api/v1/polls?since=yesterday", nil, http.StatusBadRequest)
	send(t, h, sign, http.MethodGet, "/api/v1/polls?limit=0", nil, http.StatusBadRequest)

	raw := send(t, h, sign, http.MethodGet, "/api/v1/polls/export", nil, http.StatusOK)
	rows, err := csv.NewReader(strings.NewReader(string(raw))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	// Header, then option 1 (twitch, total) and option 2 (total).
	if len(rows) != 4 || rows[0][0] != "poll_id" || rows[1][7] != "twitch" || rows[1][8] != "1" || rows[2][7] != "total" {
		t.Fatalf("csv = %q", rows)
	}
	if !strings.HasPrefix(rows[3][6], "'=") {
		t.Errorf("a formula-looking label was exported live: %q", rows[3][6])
	}
	var asJSON []polls.Result
	decodeInto(t, send(t, h, sign, http.MethodGet, fmt.Sprintf("/api/v1/polls/export?format=json&id=%d", started.ID), nil, http.StatusOK), &asJSON)
	if len(asJSON) != 1 || asJSON[0].ID != started.ID {
		t.Errorf("json export = %+v", asJSON)
	}
	send(t, h, sign, http.MethodGet, "/api/v1/polls/export?format=xml", nil, http.StatusBadRequest)
	send(t, h, sign, http.MethodGet, "/api/v1/polls/export?id=999", nil, http.StatusNotFound)
	send(t, h, sign, http.MethodGet, "/api/v1/polls/export?id=x", nil, http.StatusBadRequest)
	send(t, h, sign, http.MethodGet, "/api/v1/polls/export?until=nope", nil, http.StatusBadRequest)
}

// TestAReadTokenSeesPollResultsButNeverAVoterAndCannotStartOne: the result is
// counts, so a read token gets it -- over REST and on the events stream --
// and nothing a read token receives names the viewer who voted. Starting and
// ending post into every chat, so they are admin.
func TestAReadTokenSeesPollResultsButNeverAVoterAndCannotStartOne(t *testing.T) {
	stream := eventstream.NewHub()
	s, h, _ := pollServer(t, Options{Stream: stream})
	sign := login(t, h)
	read := bearer(createScopedToken(t, h, sign, "monitor", db.ScopeRead))

	var started polls.Result
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/polls", pollBody, http.StatusCreated), &started)
	s.polls.Offer(chat.Message{ID: "msg-77", Platform: db.PlatformYouTube, Account: "acct-ref-9",
		Author: chat.Author{ID: "UCvoterChannelId", Name: "Voter McVoteface"}, Text: "2"}, chat.DisplayClear)

	send(t, h, read, http.MethodPost, "/api/v1/polls", pollBody, http.StatusForbidden)
	send(t, h, read, http.MethodPost, fmt.Sprintf("/api/v1/polls/%d/end", started.ID), nil, http.StatusForbidden)

	var bodies []string
	for _, p := range []string{"/api/v1/polls", fmt.Sprintf("/api/v1/polls/%d", started.ID), "/api/v1/polls/export",
		"/api/v1/polls/export?format=json"} {
		bodies = append(bodies, string(send(t, h, read, http.MethodGet, p, nil, http.StatusOK)))
	}
	if !strings.Contains(bodies[0], `"total":1`) {
		t.Fatalf("the read token did not see the vote count: %s", bodies[0])
	}
	// The events stream, as a read token: the poll topic is granted, and the
	// final result arrives on it.
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/events?topics=poll", nil)
	read(req)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events as a read token = %d", resp.StatusCode)
	}
	if _, err := s.polls.End(started.ID); err != nil {
		t.Fatal(err)
	}
	f := streamtest.ExpectSSE(t, bufio.NewReader(resp.Body), eventstream.TypePollEnded)
	raw, _ := json.Marshal(f.Env)
	if !strings.Contains(string(raw), `"total":1`) {
		t.Errorf("poll.ended to a read token = %s", raw)
	}
	bodies = append(bodies, string(raw))
	bodies = append(bodies, string(send(t, h, read, http.MethodGet, "/api/v1/polls/export", nil, http.StatusOK)))

	for _, b := range bodies {
		for _, secret := range []string{"UCvoterChannelId", "Voter McVoteface", "msg-77", "acct-ref-9"} {
			if strings.Contains(b, secret) {
				t.Errorf("%q reached a read token: %.300s", secret, b)
			}
		}
	}
}

// ---------------------------------------------------------------- mirror

// helixPolls is a fake Helix: users, token refresh and /polls.
type helixPolls struct {
	mu      sync.Mutex
	created []map[string]any
	ended   int
	status  int
}

func (f *helixPolls) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/polls" {
			t.Errorf("unexpected Helix call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if f.status != 0 {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(`{"status":403,"message":"not an affiliate"}`))
			return
		}
		body := `{"data":[{"id":"np1","status":"ACTIVE","choices":[{"votes":4},{"votes":1}]}]}`
		switch r.Method {
		case http.MethodPost:
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			f.created = append(f.created, m)
			body = `{"data":[{"id":"np1","status":"ACTIVE","choices":[{"votes":0},{"votes":0}]}]}`
		case http.MethodPatch:
			f.ended++
			body = `{"data":[{"id":"np1","status":"TERMINATED","choices":[{"votes":9},{"votes":2}]}]}`
		}
		_, _ = w.Write([]byte(body))
	}
}

func seedTwitch(t *testing.T, s *Server, store *db.DB, ref, scopes string) {
	t.Helper()
	if _, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformTwitch, AccountName: ref, AccountRef: ref,
		AccessToken: "tok-" + ref, RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour),
		Scopes: scopes, ScopeVer: 5,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestAPollIsMirroredToTwitchAndItsVotesMerged: the native poll is created
// with the poll's own question and options on every Twitch account that
// granted the scope, its votes are merged into Twitch's share, and it is
// ended -- not left running -- when the chat poll ends. An account connected
// before the scope existed is skipped with a reason.
func TestAPollIsMirroredToTwitchAndItsVotesMerged(t *testing.T) {
	fake := &helixPolls{}
	helix := httptest.NewServer(fake.handler(t))
	t.Cleanup(helix.Close)
	s, h, store := pollServer(t, Options{Providers: oauth.NewSet(oauth.WithBaseURL(helix.URL))})
	s.polls.SetMirror(s.PollMirror())
	sign := login(t, h)
	if err := store.PutPlatformCreds(s.box, db.PlatformTwitch, "cid", "secret"); err != nil {
		t.Fatal(err)
	}
	seedTwitch(t, s, store, "affiliate", "chat:read channel:manage:polls")
	seedTwitch(t, s, store, "oldtoken", "chat:read")

	body := map[string]any{"question": "Heads or tails?", "options": []string{"Heads", "Tails"}, "durationSeconds": 60, "mirrorTwitch": true}
	var started polls.Result
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/polls", body, http.StatusCreated), &started)
	s.polls.Wait()

	fake.mu.Lock()
	if len(fake.created) != 1 || fake.created[0]["title"] != "Heads or tails?" || fake.created[0]["broadcaster_id"] != "affiliate" {
		t.Errorf("created = %v", fake.created)
	}
	fake.mu.Unlock()
	cur, _ := s.polls.Current()
	if len(cur.Native) != 2 {
		t.Fatalf("native = %+v", cur.Native)
	}
	var mirrored, skippedOne bool
	for _, n := range cur.Native {
		mirrored = mirrored || n.Status == polls.NativeMirrored
		skippedOne = skippedOne || (n.Status == polls.NativeSkipped && strings.Contains(n.Reason, "reconnect"))
	}
	if !mirrored || !skippedOne {
		t.Errorf("native = %+v", cur.Native)
	}

	// A vote in the mirrored channel's chat is not counted: that channel's
	// share is the native poll's. One in the channel that could not be
	// mirrored is.
	s.polls.Offer(chat.Message{ID: "c1", Platform: db.PlatformTwitch, Account: "affiliate",
		Author: chat.Author{ID: "viewer-a"}, Text: "1"}, chat.DisplayClear)
	s.polls.Offer(chat.Message{ID: "c2", Platform: db.PlatformTwitch, Account: "oldtoken",
		Author: chat.Author{ID: "viewer-b"}, Text: "2"}, chat.DisplayClear)

	if _, err := s.polls.End(started.ID); err != nil {
		t.Fatal(err)
	}
	s.polls.Wait()
	s.polls.Tick()
	final := storedPoll(t, store, started.ID)
	if final.Options[0].Votes != 9 || final.Options[1].Votes != 3 || final.ByPlatform["twitch"] != 12 {
		t.Errorf("final = %+v", final)
	}
	if fake.ended != 1 {
		t.Errorf("the native poll was ended %d times", fake.ended)
	}
}

func storedPoll(t *testing.T, store *db.DB, id int64) polls.Result {
	t.Helper()
	p, err := store.Poll(id)
	if err != nil {
		t.Fatal(err)
	}
	return storedResult(*p)
}

func TestAPollThatTwitchCannotRunIsNotMirrored(t *testing.T) {
	fake := &helixPolls{}
	helix := httptest.NewServer(fake.handler(t))
	t.Cleanup(helix.Close)
	s, _, store := pollServer(t, Options{Providers: oauth.NewSet(oauth.WithBaseURL(helix.URL))})
	m := s.PollMirror()

	// No Twitch account at all.
	if got := m.Start(t.Context(), polls.Spec{Question: "q", Options: []string{"a", "b"}, DurationSeconds: 60, MirrorTwitch: true}); len(got) != 1 ||
		got[0].Result.Status != polls.NativeSkipped {
		t.Errorf("no account = %+v", got)
	}
	_ = store.PutPlatformCreds(s.box, db.PlatformTwitch, "cid", "secret")
	seedTwitch(t, s, store, "chan", "channel:manage:polls")
	// Six options: Twitch allows five. Skipped, not shortened.
	got := m.Start(t.Context(), polls.Spec{Question: "q", Options: []string{"a", "b", "c", "d", "e", "f"}, DurationSeconds: 60, MirrorTwitch: true})
	if len(got) != 1 || got[0].Result.Status != polls.NativeSkipped || !strings.Contains(got[0].Result.Reason, "5") {
		t.Errorf("six options = %+v", got)
	}
	// Twitch refuses (not an Affiliate): reported, not fatal.
	fake.status = http.StatusForbidden
	got = m.Start(t.Context(), polls.Spec{Question: "q", Options: []string{"a", "b"}, DurationSeconds: 60, MirrorTwitch: true})
	if len(got) != 1 || got[0].Result.Status != polls.NativeFailed || strings.Contains(got[0].Result.Reason, "tok-chan") {
		t.Errorf("refused = %+v", got)
	}
	if _, _, err := m.Read(t.Context(), polls.Mirrored{}, false); err == nil {
		t.Error("a mirror with no handle was read")
	}
	fake.mu.Lock()
	if len(fake.created) != 0 {
		t.Errorf("a native poll was created: %v", fake.created)
	}
	fake.mu.Unlock()
}

// pollerAdapter is a YouTube chat that can run a native poll, without a
// network: the real request shapes are tested in internal/chat.
type pollerAdapter struct {
	account string
	mu      sync.Mutex
	created []string
	closeV  []int
	err     error
	closeE  error
}

func (a *pollerAdapter) Platform() db.Platform { return db.PlatformYouTube }
func (a *pollerAdapter) Account() string       { return a.account }
func (a *pollerAdapter) Run(ctx context.Context, _ chat.Sink) error {
	<-ctx.Done()
	return nil
}
func (a *pollerAdapter) CreatePoll(_ context.Context, q string, _ []string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return "", a.err
	}
	a.created = append(a.created, q)
	return "ytp-1", nil
}
func (a *pollerAdapter) ClosePoll(context.Context, string) ([]int, error) { return a.closeV, a.closeE }

// TestAPollIsMirroredToYouTubeAndCountedAtClose: the YouTube chat's live poll
// is created through the chat adapter, YouTube chat votes are not counted
// while it runs, and its tally is merged when it closes. A quota refusal is a
// skip and leaves that chat voting by number.
func TestAPollIsMirroredToYouTubeAndCountedAtClose(t *testing.T) {
	hub := chat.New(chat.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(hub.Close)
	yt := &pollerAdapter{account: "UC-me", closeV: []int{6, 1}}
	if err := hub.Attach(context.Background(), yt); err != nil {
		t.Fatal(err)
	}
	s, _, store := pollServer(t, Options{Chat: hub})
	s.polls.SetMirror(s.PollMirror())

	r, err := s.polls.Start(polls.Spec{Question: "q", Options: []string{"a", "b"}, DurationSeconds: 60, MirrorYouTube: true})
	if err != nil {
		t.Fatal(err)
	}
	s.polls.Wait()
	s.polls.Offer(chat.Message{ID: "y1", Platform: db.PlatformYouTube, Account: "UC-me",
		Author: chat.Author{ID: "UCviewer"}, Text: "2"}, chat.DisplayClear)
	cur, _ := s.polls.Current()
	if len(cur.Native) != 1 || cur.Native[0].Status != polls.NativeMirrored || cur.Total != 0 {
		t.Fatalf("running = %+v", cur)
	}
	if _, err := s.polls.End(r.ID); err != nil {
		t.Fatal(err)
	}
	s.polls.Wait()
	s.polls.Tick()
	final := storedPoll(t, store, r.ID)
	if final.Options[0].Votes != 6 || final.Options[1].Votes != 1 || final.ByPlatform["youtube"] != 7 {
		t.Errorf("final = %+v", final)
	}

	// No quota above the reserve: skipped, and the chat vote counts.
	yt.err = chat.ErrPollQuota
	r, _ = s.polls.Start(polls.Spec{Question: "q2", Options: []string{"a", "b"}, DurationSeconds: 60, MirrorYouTube: true})
	s.polls.Wait()
	s.polls.Offer(chat.Message{ID: "y2", Platform: db.PlatformYouTube, Account: "UC-me",
		Author: chat.Author{ID: "UCviewer"}, Text: "2"}, chat.DisplayClear)
	cur, _ = s.polls.Current()
	if len(cur.Native) != 1 || cur.Native[0].Status != polls.NativeSkipped || cur.Total != 1 {
		t.Errorf("no quota = %+v", cur)
	}
	// Five options: YouTube allows four.
	m := s.PollMirror()
	got := m.Start(t.Context(), polls.Spec{Question: "q", Options: []string{"a", "b", "c", "d", "e"}, DurationSeconds: 60, MirrorYouTube: true})
	if len(got) != 1 || got[0].Result.Status != polls.NativeSkipped {
		t.Errorf("five options = %+v", got)
	}
	_, _ = s.polls.End(r.ID)
}
