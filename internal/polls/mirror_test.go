package polls

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// mirroredFixture starts a two-option poll mirrored on the Twitch chat
// "acct-twitch" (the account the fixture's vote helper uses), with a native
// count of [4, 1].
func mirroredFixture(t *testing.T, m *fakeMirror, opts ...Option) (*fixture, Result) {
	t.Helper()
	if m.start == nil {
		m.start = []Mirrored{{Result: NativeResult{Platform: "twitch", Status: NativeMirrored},
			Account: "acct-twitch", Ref: "np1"}}
	}
	if m.counts == nil {
		m.counts = []int{4, 1}
	}
	f := newFixture(t, append([]Option{WithMirror(m)}, opts...)...)
	r, err := f.e.Start(Spec{Question: "q", Options: []string{"a", "b"}, DurationSeconds: 60, MirrorTwitch: true})
	if err != nil {
		t.Fatal(err)
	}
	f.e.Wait()
	return f, r
}

func (f *fixture) readMirror(t *testing.T) {
	t.Helper()
	f.clk.add(mirrorEvery)
	f.e.Tick()
	f.e.Wait()
}

// TestAViewerWhoVotesInTheTwitchPollAndInTwitchChatCountsOnce: while the
// native poll runs, Twitch's share is the native count alone. A Twitch chat
// vote -- including one typed before the native poll existed -- is not added,
// so the viewer who did both is counted once. Other platforms' chat votes
// count as usual.
func TestAViewerWhoVotesInTheTwitchPollAndInTwitchChatCountsOnce(t *testing.T) {
	m := &fakeMirror{counts: []int{1, 0}} // the viewer's native vote
	f, _ := mirroredFixture(t, m)
	f.vote(db.PlatformTwitch, "t1", "u1", "1", chat.DisplayClear)   // same viewer, in chat
	f.vote(db.PlatformYouTube, "y1", "u9", "2", chat.DisplayClear)  // another platform
	f.vote(db.PlatformTwitch, "t2", "u2", "2", chat.DisplayPending) // held, then cleared
	f.e.Judge(db.PlatformTwitch, "acct-twitch", "t2", chat.DisplayClear)
	f.readMirror(t)
	r := f.result(t)
	if !eq(votes(r), []int{1, 1}) || r.ByPlatform["twitch"] != 1 || r.ByPlatform["youtube"] != 1 {
		t.Fatalf("votes = %v by %v, want Twitch from the native poll only", votes(r), r.ByPlatform)
	}
}

// TestTwitchChatVotesCountAgainFromTheMomentTheNativePollStopsEarly: the
// transition is explicit. A chat vote typed while the native poll ran stays
// uncounted (that viewer was told to vote natively, and the native count so
// far stands, frozen); a chat vote typed after it stopped counts.
func TestTwitchChatVotesCountAgainFromTheMomentTheNativePollStopsEarly(t *testing.T) {
	m := &fakeMirror{counts: []int{3, 0}}
	f, _ := mirroredFixture(t, m)
	f.vote(db.PlatformTwitch, "t1", "during", "2", chat.DisplayClear)
	f.readMirror(t)
	if r := f.result(t); !eq(votes(r), []int{3, 0}) {
		t.Fatalf("while mirrored = %v", votes(r))
	}

	m.mu.Lock()
	m.stopped = true // a streamer closed the poll on Twitch
	m.mu.Unlock()
	f.readMirror(t)
	cur, _ := f.e.Current()
	if cur.Native[0].Status != NativeStopped || !strings.Contains(cur.Native[0].Reason, "chat votes count again") {
		t.Fatalf("native = %+v", cur.Native)
	}
	f.vote(db.PlatformTwitch, "t2", "after", "2", chat.DisplayClear)
	if r := f.result(t); !eq(votes(r), []int{3, 1}) || r.ByPlatform["twitch"] != 4 {
		t.Errorf("after the stop = %v by %v, want the frozen native count plus only the later chat vote",
			votes(r), r.ByPlatform)
	}
	// A later native read changes nothing: the stopped poll is no longer read.
	m.mu.Lock()
	m.counts = []int{99, 99}
	m.mu.Unlock()
	f.readMirror(t)
	if r := f.result(t); r.Options[0].Votes != 3 {
		t.Errorf("a stopped native poll was read again: %v", votes(r))
	}
}

// TestAMirrorThatFailsToStartLeavesChatVotesCounting: no native poll, no
// change to how that chat votes.
func TestAMirrorThatFailsToStartLeavesChatVotesCounting(t *testing.T) {
	m := &fakeMirror{start: []Mirrored{{Result: NativeResult{Platform: "twitch", Status: NativeFailed,
		Reason: "not an Affiliate"}, Account: "acct-twitch"}}}
	f, _ := mirroredFixture(t, m)
	f.vote(db.PlatformTwitch, "t1", "u1", "1", chat.DisplayClear)
	if r := f.result(t); !eq(votes(r), []int{1, 0}) {
		t.Errorf("votes = %v, want the chat vote", votes(r))
	}
}

// TestWhenTheFinalNativeCountCannotBeReadTheChatsOwnVotesAreUsed: a close that
// fails leaves no native count at all. Rather than a share of zero, the chat
// votes that chat's viewers typed are counted -- the only count there is.
func TestWhenTheFinalNativeCountCannotBeReadTheChatsOwnVotesAreUsed(t *testing.T) {
	m := &fakeMirror{counts: []int{5, 5}, endErr: errors.New("modificationNotAllowed")}
	f, r := mirroredFixture(t, m)
	f.vote(db.PlatformTwitch, "t1", "u1", "2", chat.DisplayClear)
	f.readMirror(t)
	if _, err := f.e.End(r.ID); err != nil {
		t.Fatal(err)
	}
	f.e.Wait()
	f.e.Tick()
	final := f.pub.of("poll.ended")
	if len(final) != 1 {
		t.Fatalf("%d poll.ended", len(final))
	}
	res := final[0].Data.(Result)
	if !eq(votes(res), []int{0, 1}) || res.Native[0].Status != NativeFailed {
		t.Errorf("final = %v native %+v, want the chat vote instead of the unreadable native count", votes(res), res.Native)
	}
}

// TestATransientReadErrorChangesNothing: one failed read while the poll runs
// is retried, not taken as the native poll stopping.
func TestATransientReadErrorChangesNothing(t *testing.T) {
	m := &fakeMirror{counts: []int{2, 0}}
	f, _ := mirroredFixture(t, m)
	f.readMirror(t)
	m.mu.Lock()
	m.readErr = errors.New("503")
	m.mu.Unlock()
	f.readMirror(t)
	cur, _ := f.e.Current()
	if cur.Native[0].Status != NativeMirrored || !eq(votes(cur), []int{2, 0}) {
		t.Errorf("after a failed read = %+v %v", cur.Native, votes(cur))
	}
}

// TestAYouTubeMirrorIsCountedAtClose: YouTube cannot report a running poll's
// count, so its share is empty until close and the native count is merged
// then; YouTube chat votes are not counted meanwhile.
func TestAYouTubeMirrorIsCountedAtClose(t *testing.T) {
	m := &ytFakeMirror{final: []int{7, 2}}
	f := newFixture(t, WithMirror(m))
	r, err := f.e.Start(Spec{Question: "q", Options: []string{"a", "b"}, DurationSeconds: 60, MirrorYouTube: true})
	if err != nil {
		t.Fatal(err)
	}
	f.e.Wait()
	f.vote(db.PlatformYouTube, "y1", "u1", "1", chat.DisplayClear)
	f.readMirror(t)
	if cur := f.result(t); cur.Total != 0 {
		t.Fatalf("a running YouTube mirror counted %v", votes(cur))
	}
	if _, err := f.e.End(r.ID); err != nil {
		t.Fatal(err)
	}
	f.e.Wait()
	f.e.Tick()
	res := f.pub.of("poll.ended")[0].Data.(Result)
	if !eq(votes(res), []int{7, 2}) || res.ByPlatform["youtube"] != 9 {
		t.Errorf("final = %v by %v", votes(res), res.ByPlatform)
	}
}

type ytFakeMirror struct{ final []int }

func (m *ytFakeMirror) Start(_ context.Context, _ Spec) []Mirrored {
	return []Mirrored{{Result: NativeResult{Platform: "youtube", Status: NativeMirrored}, Account: "acct-youtube", Ref: "yp"}}
}
func (m *ytFakeMirror) Read(_ context.Context, _ Mirrored, end bool) ([]int, bool, error) {
	if !end {
		return nil, true, nil
	}
	return m.final, false, nil
}

// TestAMirroredChatIsToldToVoteInTheNativePoll: the announcement goes out
// after the native polls exist, and each chat gets its own: vote in the
// platform's poll where one runs, type a number everywhere else.
func TestAMirroredChatIsToldToVoteInTheNativePoll(t *testing.T) {
	s := &fakeSender{res: []chat.SendResult{
		{Platform: db.PlatformTwitch, Account: "acct-twitch", OK: true},
		{Platform: db.PlatformKick, Account: "acct-kick", OK: true},
	}}
	mirroredFixture(t, &fakeMirror{}, WithSender(s))
	s.mu.Lock()
	defer s.mu.Unlock()
	tw, kk := s.byChat["twitch/acct-twitch"], s.byChat["kick/acct-kick"]
	if len(tw) != 1 || !strings.Contains(tw[0], "vote in the Twitch poll") || strings.Contains(tw[0], "!vote") {
		t.Errorf("twitch was told %q", tw)
	}
	if len(kk) != 1 || !strings.Contains(kk[0], "!vote N") || strings.Contains(strings.ToLower(kk[0]), "twitch") {
		t.Errorf("kick was told %q", kk)
	}
}
