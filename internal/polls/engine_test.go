package polls

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
)

// ---------------------------------------------------------------- fakes

type memStore struct {
	mu    sync.Mutex
	rows  map[int64]db.Poll
	next  int64
	fails bool
}

func (s *memStore) CreatePoll(p db.Poll) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fails {
		return 0, errors.New("disk full")
	}
	if s.rows == nil {
		s.rows = map[int64]db.Poll{}
	}
	s.next++
	p.ID = s.next
	s.rows[p.ID] = p
	return p.ID, nil
}

func (s *memStore) UpdatePoll(p db.Poll) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[p.ID]
	if !ok {
		return db.ErrPollNotFound
	}
	r.State, r.EndedAt, r.Result = p.State, p.EndedAt, p.Result
	s.rows[p.ID] = r
	return nil
}

func (s *memStore) row(id int64) db.Poll {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[id]
}

type recorder struct {
	mu   sync.Mutex
	envs []eventstream.Envelope
}

func (r *recorder) Publish(env eventstream.Envelope) {
	r.mu.Lock()
	r.envs = append(r.envs, env)
	r.mu.Unlock()
}

func (r *recorder) of(t eventstream.Type) []eventstream.Envelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []eventstream.Envelope
	for _, e := range r.envs {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fakeSender struct {
	mu    sync.Mutex
	texts []string
	res   []chat.SendResult
	err   error
}

func (f *fakeSender) Send(_ context.Context, text string) ([]chat.SendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texts = append(f.texts, text)
	return f.res, f.err
}

func (f *fakeSender) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

type fixture struct {
	e     *Engine
	store *memStore
	pub   *recorder
	clk   *clock
}

func newFixture(t *testing.T, opts ...Option) *fixture {
	t.Helper()
	f := &fixture{store: &memStore{}, pub: &recorder{}, clk: &clock{t: time.Unix(1_800_000_000, 0)}}
	base := []Option{WithClock(f.clk.now), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))}
	f.e = New(f.store, f.pub, append(base, opts...)...)
	t.Cleanup(f.e.Wait)
	return f
}

func (f *fixture) start(t *testing.T, opts ...string) Result {
	t.Helper()
	if len(opts) == 0 {
		opts = []string{"Dust", "Mirage", "Nuke"}
	}
	r, err := f.e.Start(Spec{Question: "Next map?", Options: opts, DurationSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// vote offers a message as the hub would: author id, platform, account.
func (f *fixture) vote(p db.Platform, id, author, text string, d chat.Display) {
	f.e.Offer(chat.Message{ID: id, Platform: p, Account: "acct-" + string(p),
		Author: chat.Author{ID: author, Name: "Name-" + author}, Text: text}, d)
}

func (f *fixture) result(t *testing.T) Result {
	t.Helper()
	f.e.mu.Lock()
	defer f.e.mu.Unlock()
	if f.e.cur == nil {
		t.Fatal("no poll is running")
	}
	recount(f.e.cur)
	return snapshot(f.e.cur)
}

func votes(r Result) []int {
	out := make([]int, len(r.Options))
	for i, o := range r.Options {
		out[i] = o.Votes
	}
	return out
}

func eq(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- counting

// TestOneVotePerViewerPerPlatformAndTheLastOneCounts: a viewer who changes
// their mind moves their vote rather than adding one, and a viewer spamming
// the same number is still one vote.
func TestOneVotePerViewerPerPlatformAndTheLastOneCounts(t *testing.T) {
	f := newFixture(t)
	f.start(t)
	f.vote(db.PlatformTwitch, "m1", "u1", "1", chat.DisplayClear)
	f.vote(db.PlatformTwitch, "m2", "u1", "1", chat.DisplayClear)
	f.vote(db.PlatformTwitch, "m3", "u1", "1", chat.DisplayClear)
	if got := votes(f.result(t)); !eq(got, []int{1, 0, 0}) {
		t.Fatalf("three identical votes from one viewer = %v, want one", got)
	}
	f.vote(db.PlatformTwitch, "m4", "u1", "!vote 3", chat.DisplayClear)
	r := f.result(t)
	if got := votes(r); !eq(got, []int{0, 0, 1}) || r.Total != 1 {
		t.Fatalf("after changing their mind = %v (total %d), want only option 3", got, r.Total)
	}
}

// TestVotesAcrossPlatformsAreCountedTogetherWithABreakdown: the same user id
// on two platforms is two different people as far as anyone can prove, and
// each platform's share is reported.
func TestVotesAcrossPlatformsAreCountedTogetherWithABreakdown(t *testing.T) {
	f := newFixture(t)
	f.start(t)
	f.vote(db.PlatformTwitch, "t1", "42", "1", chat.DisplayClear)
	f.vote(db.PlatformTwitch, "t2", "43", "2", chat.DisplayClear)
	// Same id, different platform: a different voter.
	f.vote(db.PlatformYouTube, "y1", "42", "1", chat.DisplayClear)
	f.vote(db.PlatformKick, "k1", "k9", "!vote 1", chat.DisplayClear)
	r := f.result(t)
	if got := votes(r); !eq(got, []int{3, 1, 0}) || r.Total != 4 {
		t.Fatalf("votes = %v (total %d), want [3 1 0]", got, r.Total)
	}
	if r.ByPlatform["twitch"] != 2 || r.ByPlatform["youtube"] != 1 || r.ByPlatform["kick"] != 1 {
		t.Errorf("by platform = %v", r.ByPlatform)
	}
	if b := r.Options[0].ByPlatform; b["twitch"] != 1 || b["youtube"] != 1 || b["kick"] != 1 {
		t.Errorf("option 1 by platform = %v", b)
	}
}

// TestTwoChannelsOnOnePlatformStillGiveAViewerOneVote: two connected Twitch
// channels are two accounts, but a viewer in both is one Twitch user.
func TestTwoChannelsOnOnePlatformStillGiveAViewerOneVote(t *testing.T) {
	f := newFixture(t)
	f.start(t)
	f.e.Offer(chat.Message{ID: "a", Platform: db.PlatformTwitch, Account: "chan-a", Author: chat.Author{ID: "u1"}, Text: "1"}, chat.DisplayClear)
	f.e.Offer(chat.Message{ID: "b", Platform: db.PlatformTwitch, Account: "chan-b", Author: chat.Author{ID: "u1"}, Text: "2"}, chat.DisplayClear)
	if got := votes(f.result(t)); !eq(got, []int{0, 1, 0}) {
		t.Errorf("votes = %v, want the later vote only", got)
	}
}

func TestOwnAnnouncementsAndAuthorlessMessagesAreNotVotes(t *testing.T) {
	f := newFixture(t)
	f.start(t)
	f.e.Offer(chat.Message{ID: "e", Platform: db.PlatformTwitch, Author: chat.Author{ID: "me"}, Text: "1", Echo: true}, chat.DisplayClear)
	f.e.Offer(chat.Message{ID: "n", Platform: db.PlatformTwitch, Author: chat.Author{Name: "no id"}, Text: "1"}, chat.DisplayClear)
	f.vote(db.PlatformTwitch, "c", "u1", "great stream", chat.DisplayClear)
	if r := f.result(t); r.Total != 0 {
		t.Errorf("total = %d, want 0", r.Total)
	}
}

// ---------------------------------------------------------------- automod

func TestAMessageAutomodBlocksIsNotAVote(t *testing.T) {
	f := newFixture(t)
	f.start(t)
	f.vote(db.PlatformTwitch, "m1", "u1", "1", chat.DisplayBlocked)
	if r := f.result(t); r.Total != 0 {
		t.Errorf("a blocked message counted: total %d", r.Total)
	}
}

// TestAVoteTheModelIsJudgingCountsOnlyOnceCleared: the same bar as the
// overlay -- a vote counts once automod has let the message stand.
func TestAVoteTheModelIsJudgingCountsOnlyOnceCleared(t *testing.T) {
	f := newFixture(t)
	f.start(t)
	f.vote(db.PlatformTwitch, "ok", "u1", "1", chat.DisplayPending)
	f.vote(db.PlatformTwitch, "bad", "u2", "2", chat.DisplayPending)
	f.vote(db.PlatformTwitch, "slow", "u3", "3", chat.DisplayPending)
	if r := f.result(t); r.Total != 0 {
		t.Fatalf("held votes counted before a verdict: %d", r.Total)
	}
	f.e.Judge(db.PlatformTwitch, "acct-twitch", "ok", chat.DisplayClear)
	f.e.Judge(db.PlatformTwitch, "acct-twitch", "bad", chat.DisplayBlocked)
	// No verdict within the grace: dropped, and a late one changes nothing.
	f.clk.add(PendingGrace + time.Millisecond)
	f.e.Tick()
	f.e.Judge(db.PlatformTwitch, "acct-twitch", "slow", chat.DisplayClear)
	if got := votes(f.result(t)); !eq(got, []int{1, 0, 0}) {
		t.Errorf("votes = %v, want only the cleared one", got)
	}
}

// TestALateVerdictDoesNotOverwriteALaterVote: a viewer's held vote, cleared
// after they typed a newer one, must not replace the newer one.
func TestALateVerdictDoesNotOverwriteALaterVote(t *testing.T) {
	f := newFixture(t)
	f.start(t)
	f.vote(db.PlatformTwitch, "first", "u1", "1", chat.DisplayPending)
	f.vote(db.PlatformTwitch, "second", "u1", "2", chat.DisplayClear)
	f.e.Judge(db.PlatformTwitch, "acct-twitch", "first", chat.DisplayClear)
	if got := votes(f.result(t)); !eq(got, []int{0, 1, 0}) {
		t.Errorf("votes = %v, want the later vote to stand", got)
	}
}

// ---------------------------------------------------------------- moderation

// TestADeletedVoteMessageVoidsThatVote: and only that vote. The viewer is not
// banned, so they may vote again; the vote they cast before the deleted one
// does not come back.
func TestADeletedVoteMessageVoidsThatVote(t *testing.T) {
	f := newFixture(t)
	f.start(t)
	f.vote(db.PlatformTwitch, "m1", "u1", "1", chat.DisplayClear)
	f.vote(db.PlatformTwitch, "m2", "u1", "2", chat.DisplayClear)
	f.vote(db.PlatformTwitch, "m3", "u2", "2", chat.DisplayClear)
	f.e.RetractMessage(db.PlatformTwitch, "acct-twitch", "m2")
	if got := votes(f.result(t)); !eq(got, []int{0, 1, 0}) {
		t.Fatalf("after the delete = %v, want only u2's vote", got)
	}
	// Deleting the superseded message changes nothing.
	f.e.RetractMessage(db.PlatformTwitch, "acct-twitch", "m1")
	f.vote(db.PlatformTwitch, "m4", "u1", "3", chat.DisplayClear)
	if got := votes(f.result(t)); !eq(got, []int{0, 1, 1}) {
		t.Errorf("after voting again = %v", got)
	}
	// A held vote whose message is deleted before the verdict never counts.
	f.vote(db.PlatformTwitch, "m5", "u3", "1", chat.DisplayPending)
	f.e.RetractMessage(db.PlatformTwitch, "acct-twitch", "m5")
	f.e.Judge(db.PlatformTwitch, "acct-twitch", "m5", chat.DisplayClear)
	if got := votes(f.result(t)); !eq(got, []int{0, 1, 1}) {
		t.Errorf("a deleted held vote counted: %v", got)
	}
}

// TestABannedViewerIsIgnoredForTheRestOfThePoll: a ban or timeout voids the
// vote they cast, drops one still being judged, and refuses every later one --
// on that platform only.
func TestABannedViewerIsIgnoredForTheRestOfThePoll(t *testing.T) {
	f := newFixture(t)
	f.start(t)
	f.vote(db.PlatformTwitch, "m1", "troll", "1", chat.DisplayClear)
	f.vote(db.PlatformTwitch, "m2", "troll", "2", chat.DisplayPending)
	f.vote(db.PlatformYouTube, "y1", "troll", "1", chat.DisplayClear)
	f.e.RetractAuthor(db.PlatformTwitch, "acct-twitch", "troll")
	f.e.Judge(db.PlatformTwitch, "acct-twitch", "m2", chat.DisplayClear)
	f.vote(db.PlatformTwitch, "m3", "troll", "3", chat.DisplayClear)
	r := f.result(t)
	if got := votes(r); !eq(got, []int{1, 0, 0}) || r.ByPlatform["twitch"] != 0 {
		t.Errorf("votes = %v by %v, want only the YouTube user with the same id", got, r.ByPlatform)
	}
}

// TestAClearedRoomKeepsEveryVote: clearing chat is housekeeping. Wiping every
// vote on a platform because the streamer tidied the room would be a silent
// recount.
func TestAClearedRoomKeepsEveryVote(t *testing.T) {
	f := newFixture(t)
	f.start(t)
	f.vote(db.PlatformTwitch, "m1", "u1", "1", chat.DisplayClear)
	f.vote(db.PlatformTwitch, "m2", "u2", "2", chat.DisplayClear)
	f.e.ClearRoom(db.PlatformTwitch, "acct-twitch")
	if r := f.result(t); r.Total != 2 {
		t.Errorf("total after a room clear = %d, want 2", r.Total)
	}
}

// ---------------------------------------------------------------- lifecycle

func TestOnePollAtATime(t *testing.T) {
	f := newFixture(t)
	r := f.start(t)
	if _, err := f.e.Start(Spec{Question: "q", Options: []string{"a", "b"}, DurationSeconds: 60}); !errors.Is(err, ErrRunning) {
		t.Errorf("a second poll = %v, want ErrRunning", err)
	}
	if _, err := f.e.End(r.ID + 1); !errors.Is(err, ErrNoPoll) {
		t.Errorf("ending another id = %v, want ErrNoPoll", err)
	}
	if _, err := f.e.Start(Spec{Question: "", Options: []string{"a", "b"}, DurationSeconds: 60}); !errors.Is(err, ErrInvalid) {
		t.Errorf("an invalid spec = %v", err)
	}
}

func TestAPollThatCannotBeStoredDoesNotStart(t *testing.T) {
	f := newFixture(t)
	f.store.fails = true
	if _, err := f.e.Start(Spec{Question: "q", Options: []string{"a", "b"}, DurationSeconds: 60}); err == nil {
		t.Fatal("started without being stored")
	}
	if _, ok := f.e.Current(); ok {
		t.Error("a poll is running that was never stored")
	}
}

// TestAPollClosesAtItsTimeAndWaitsForVotesStillBeingJudged: a vote typed in
// the last second while the model is judging it is a vote cast in time. The
// poll waits for it -- at most PendingGrace -- and then is final.
func TestAPollClosesAtItsTimeAndWaitsForVotesStillBeingJudged(t *testing.T) {
	f := newFixture(t)
	r := f.start(t)
	f.vote(db.PlatformTwitch, "m1", "u1", "1", chat.DisplayClear)
	f.clk.add(59 * time.Second)
	f.vote(db.PlatformTwitch, "late", "u2", "2", chat.DisplayPending)
	f.clk.add(time.Second)
	f.e.Tick()
	cur, ok := f.e.Current()
	if !ok || cur.State != StateClosing {
		t.Fatalf("at its time with a vote being judged: %+v, want closing", cur)
	}
	// A new vote after the time is not counted.
	f.vote(db.PlatformTwitch, "after", "u3", "3", chat.DisplayClear)
	f.e.Judge(db.PlatformTwitch, "acct-twitch", "late", chat.DisplayClear)
	f.e.Tick()
	if _, ok := f.e.Current(); ok {
		t.Fatal("the poll did not finish once the last vote was judged")
	}
	ended := f.pub.of(eventstream.TypePollEnded)
	if len(ended) != 1 {
		t.Fatalf("%d poll.ended events, want 1", len(ended))
	}
	final := ended[0].Data.(Result)
	if !eq(votes(final), []int{1, 1, 0}) || final.State != StateEnded || final.EndedAt == nil {
		t.Errorf("final = %+v", final)
	}
	row := f.store.row(r.ID)
	if row.State != "ended" || !strings.Contains(string(row.Result), `"total":2`) {
		t.Errorf("stored = %+v", row)
	}
}

func TestAPollWithNothingToWaitForEndsAtOnceWhenEndedEarly(t *testing.T) {
	f := newFixture(t)
	r := f.start(t)
	f.vote(db.PlatformYouTube, "m1", "u1", "2", chat.DisplayClear)
	got, err := f.e.End(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateEnded || !got.EndedEarly || got.Total != 1 {
		t.Errorf("ended early = %+v", got)
	}
	if _, ok := f.e.Current(); ok {
		t.Error("still running")
	}
	// A vote after the end goes nowhere, and the next poll starts clean.
	f.vote(db.PlatformYouTube, "m2", "u2", "1", chat.DisplayClear)
	f.start(t)
	if r := f.result(t); r.Total != 0 {
		t.Errorf("the next poll started with %d votes", r.Total)
	}
}

// TestVoteEventsAreCoalesced: a raid voting at once is at most one poll.vote a
// second, and nothing at all is sent while nothing changes -- except the
// display heartbeat for overlays that connect mid-poll.
func TestVoteEventsAreCoalesced(t *testing.T) {
	f := newFixture(t)
	f.start(t)
	for i := 0; i < 50; i++ {
		f.vote(db.PlatformTwitch, "m"+string(rune('A'+i)), "u"+string(rune('A'+i)), "1", chat.DisplayClear)
	}
	f.e.Tick()
	if n := len(f.pub.of(eventstream.TypePollVote)); n != 0 {
		t.Fatalf("%d poll.vote within the first second", n)
	}
	f.clk.add(time.Second)
	f.e.Tick()
	f.e.Tick()
	vs := f.pub.of(eventstream.TypePollVote)
	if len(vs) != 1 || vs[0].Data.(Result).Total != 50 {
		t.Fatalf("poll.vote = %d events, want 1 carrying all 50 votes", len(vs))
	}
	before := len(f.pub.of(eventstream.TypePollDisplay))
	f.clk.add(displayEvery)
	f.e.Tick()
	if len(f.pub.of(eventstream.TypePollVote)) != 1 {
		t.Error("a poll.vote was sent with nothing changed")
	}
	if len(f.pub.of(eventstream.TypePollDisplay)) != before+1 {
		t.Error("no display heartbeat while the poll was open")
	}
}

// ---------------------------------------------------------------- announcing

func TestThePollIsAnnouncedInEveryChatAndFailuresAreReported(t *testing.T) {
	s := &fakeSender{res: []chat.SendResult{
		{Platform: db.PlatformTwitch, Account: "acct-ref-1", OK: true},
		{Platform: db.PlatformYouTube, OK: false, Detail: "quota exceeded"},
		{Platform: db.PlatformRumble, Skipped: true, Detail: "receive-only"},
	}}
	f := newFixture(t, WithSender(s))
	r := f.start(t, "Dust", "Mirage")
	f.e.Wait()
	cur, _ := f.e.Current()
	if len(cur.Announced) != 3 || !cur.Announced[0].OK || cur.Announced[1].OK || !cur.Announced[2].Skipped {
		t.Errorf("announced = %+v", cur.Announced)
	}
	b, _ := json.Marshal(cur.Announced)
	if strings.Contains(string(b), "acct-ref-1") {
		t.Error("the announcement report carries the operator's account ref")
	}
	if _, err := f.e.End(r.ID); err != nil {
		t.Fatal(err)
	}
	f.e.Wait()
	sent := s.sent()
	if len(sent) != 2 || !strings.HasPrefix(sent[0], "POLL: Next map?") || !strings.HasPrefix(sent[1], "POLL CLOSED: Next map?") {
		t.Errorf("sent = %q", sent)
	}
}

func TestAFailedAnnouncementDoesNotFailThePoll(t *testing.T) {
	f := newFixture(t, WithSender(&fakeSender{err: errors.New("chat: no platform is connected")}))
	f.start(t)
	f.e.Wait()
	cur, ok := f.e.Current()
	if !ok || cur.State != StateOpen || len(cur.Announced) != 1 || cur.Announced[0].OK {
		t.Errorf("poll = %+v", cur)
	}
}

// ---------------------------------------------------------------- mirror

type fakeMirror struct {
	mu     sync.Mutex
	start  []Mirrored
	counts []int
	ended  int
	reads  int
}

func (m *fakeMirror) Start(context.Context, Spec) []Mirrored { return m.start }
func (m *fakeMirror) Read(_ context.Context, _ Mirrored, end bool) ([]int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	if end {
		m.ended++
	}
	return append([]int(nil), m.counts...), nil
}

// TestANativeTwitchPollsVotesAreMergedIn: the native poll's anonymous totals
// are added to the option counts and to Twitch's share, and its final count
// is read -- after ending it -- before the result is final.
func TestANativeTwitchPollsVotesAreMergedIn(t *testing.T) {
	m := &fakeMirror{
		start: []Mirrored{
			{Result: NativeResult{Platform: "twitch", Status: NativeMirrored}, Ref: "poll-1"},
			{Result: NativeResult{Platform: "twitch", Status: NativeSkipped, Reason: "not an Affiliate"}},
		},
		counts: []int{5, 2},
	}
	f := newFixture(t, WithMirror(m))
	r, err := f.e.Start(Spec{Question: "q", Options: []string{"a", "b"}, DurationSeconds: 60, MirrorTwitch: true})
	if err != nil {
		t.Fatal(err)
	}
	f.e.Wait()
	f.vote(db.PlatformYouTube, "y1", "u1", "2", chat.DisplayClear)
	f.e.Tick() // reads the mirror
	f.e.Wait()
	cur := f.result(t)
	if !eq(votes(cur), []int{5, 3}) || cur.ByPlatform["twitch"] != 7 || cur.ByPlatform["youtube"] != 1 || cur.Total != 8 {
		t.Fatalf("merged = %v by %v (total %d)", votes(cur), cur.ByPlatform, cur.Total)
	}
	if len(cur.Native) != 2 || cur.Native[1].Reason != "not an Affiliate" {
		t.Errorf("native = %+v", cur.Native)
	}

	m.mu.Lock()
	m.counts = []int{6, 2}
	m.mu.Unlock()
	if _, err := f.e.End(r.ID); err != nil {
		t.Fatal(err)
	}
	f.e.Wait()
	f.e.Tick()
	ended := f.pub.of(eventstream.TypePollEnded)
	if len(ended) != 1 || !eq(votes(ended[0].Data.(Result)), []int{6, 3}) {
		t.Fatalf("final = %+v", ended)
	}
	if m.ended != 1 {
		t.Errorf("the native poll was ended %d times, want 1", m.ended)
	}
}

// TestAMirrorThatNeverAnswersDoesNotHoldTheResultForever: the final count is
// waited for for mirrorGrace, then the poll is final without it.
func TestAMirrorThatNeverAnswersDoesNotHoldTheResultForever(t *testing.T) {
	block := make(chan struct{})
	m := &blockingMirror{release: block}
	f := newFixture(t, WithMirror(m))
	r, _ := f.e.Start(Spec{Question: "q", Options: []string{"a", "b"}, DurationSeconds: 60, MirrorTwitch: true})
	if _, err := f.e.End(r.ID); err != nil {
		t.Fatal(err)
	}
	if cur, ok := f.e.Current(); !ok || cur.State != StateClosing {
		t.Fatalf("with the mirror still starting: %+v", cur)
	}
	f.clk.add(mirrorGrace + time.Millisecond)
	f.e.Tick()
	if _, ok := f.e.Current(); ok {
		t.Error("still waiting on a mirror past its grace")
	}
	close(block)
}

type blockingMirror struct{ release chan struct{} }

func (m *blockingMirror) Start(context.Context, Spec) []Mirrored {
	<-m.release
	return nil
}
func (m *blockingMirror) Read(context.Context, Mirrored, bool) ([]int, error) { return nil, nil }

// ---------------------------------------------------------------- identity

// TestNoSurfaceCarriesAVoter: every serialised form of a poll -- the result
// the API and events send, the overlay projection, the stored row and the
// announcements -- is checked for the voter's id and name. They exist only in
// the counter's memory.
func TestNoSurfaceCarriesAVoter(t *testing.T) {
	s := &fakeSender{}
	f := newFixture(t, WithSender(s))
	r := f.start(t)
	f.e.Offer(chat.Message{ID: "msg-secret-id", Platform: db.PlatformTwitch, Account: "acct-secret-ref",
		Author: chat.Author{ID: "voter-id-31337", Name: "VoterNameXyz"}, Text: "1"}, chat.DisplayClear)
	f.clk.add(time.Second)
	f.e.Tick()
	if _, err := f.e.End(r.ID); err != nil {
		t.Fatal(err)
	}
	f.e.Wait()
	var blobs []string
	for _, env := range f.pub.envs {
		b, _ := json.Marshal(env)
		blobs = append(blobs, string(b))
	}
	blobs = append(blobs, string(f.store.row(r.ID).Result))
	blobs = append(blobs, s.sent()...)
	if len(blobs) < 6 {
		t.Fatalf("only %d surfaces captured", len(blobs))
	}
	for _, b := range blobs {
		for _, secret := range []string{"voter-id-31337", "VoterNameXyz", "msg-secret-id", "acct-secret-ref"} {
			if strings.Contains(b, secret) {
				t.Errorf("%q leaked into %s", secret, b)
			}
		}
	}
}
