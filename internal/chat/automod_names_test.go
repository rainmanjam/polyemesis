package chat

import (
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// learnerModerator is a stubModerator that also hears protected names.
type learnerModerator struct {
	*stubModerator
	mu    sync.Mutex
	names []string
}

func (l *learnerModerator) LearnProtectedName(name string) {
	l.mu.Lock()
	l.names = append(l.names, name)
	l.mu.Unlock()
}

func (l *learnerModerator) learned() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.names...)
}

// The broadcaster's and moderators' messages are exempt from automod, and are
// also the only messages that say who they are: the pack learns their names from
// exactly those, and from no one else.
func TestOnlyBroadcasterAndModeratorNamesAreLearned(t *testing.T) {
	h := testHub(t)
	mod := &learnerModerator{stubModerator: &stubModerator{}}
	h.SetModerator(mod)
	attach(t, h, &fakeAdapter{
		platform: db.PlatformTwitch, account: "acct",
		messages: []Message{
			{ID: "m1", Author: Author{ID: "b", Name: "the_streamer", Broadcaster: true}, Text: "hello"},
			{ID: "m2", Author: Author{ID: "m", Name: "a_moderator", Moderator: true}, Text: "hi"},
			{ID: "m3", Author: Author{ID: "v", Name: "a_viewer"}, Text: "hey"},
		},
	})
	waitFor(t, "three messages", func() bool { return len(h.History(10)) == 3 })
	waitFor(t, "both names learned", func() bool { return len(mod.learned()) >= 2 })
	got := map[string]bool{}
	for _, n := range mod.learned() {
		got[n] = true
	}
	if !got["the_streamer"] || !got["a_moderator"] || got["a_viewer"] || len(got) != 2 {
		t.Errorf("learned %v", mod.learned())
	}
	// The viewer, and only the viewer, was judged.
	waitFor(t, "the viewer to be checked", func() bool { n, _ := mod.observed(); return n == 1 })
}

// A moderator that does not implement the optional half is unaffected.
func TestAModeratorWithoutNameLearningIsUnaffected(t *testing.T) {
	h := testHub(t)
	mod := &stubModerator{}
	h.SetModerator(mod)
	attach(t, h, &fakeAdapter{
		platform: db.PlatformTwitch, account: "acct",
		messages: []Message{{ID: "m1", Author: Author{ID: "b", Name: "the_streamer", Broadcaster: true}, Text: "hello"}},
	})
	waitFor(t, "the message", func() bool { return len(h.History(10)) == 1 })
	if n, _ := mod.observed(); n != 0 {
		t.Errorf("a broadcaster message was judged %d times", n)
	}
}
