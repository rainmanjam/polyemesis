package chat

import (
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// perTextModerator gives each message text its own fast verdict, so one test
// can send a message the fast checkers remove beside one they do not.
type perTextModerator struct {
	stubModerator
	fast map[string]automod.Verdict
}

func (p *perTextModerator) CheckFast(pl db.Platform, authorID, text string) automod.Verdict {
	p.stubModerator.CheckFast(pl, authorID, text)
	return p.fast[text]
}

// THE SPEND FIX. The model used to be asked about EVERY non-empty message
// while it was enabled -- including one a rule had just deleted, which is a
// paid call whose answer can change nothing -- while model.go's comment said
// it was asked "only about what the first two checkers could not settle". A
// message the fast verdict REMOVES is now not sent to the model at all.
//
// The other direction matters as much: a fast finding that only FLAGS removes
// nothing, so that message is still worth the model's opinion, and a clean
// message is too. Both are sent.
func TestTheModelIsNotAskedAboutAMessageTheFastCheckersAlreadyRemoved(t *testing.T) {
	h := testHub(t)
	mod := &perTextModerator{
		stubModerator: stubModerator{modelOn: true},
		fast: map[string]automod.Verdict{
			"removed by a rule": {
				Findings: []automod.Finding{{Checker: automod.CheckerRules, Action: automod.ActionDelete}},
				Act:      []automod.Finding{{Checker: automod.CheckerRules, Action: automod.ActionDelete}},
			},
			"flagged by history": {
				Findings: []automod.Finding{{Checker: automod.CheckerHistory, Action: automod.ActionFlag}},
				Act:      []automod.Finding{{Checker: automod.CheckerHistory, Action: automod.ActionFlag}},
			},
		},
	}
	h.SetModerator(mod)
	attach(t, h, &fakeAdapter{
		platform: db.PlatformTwitch, account: "acct",
		messages: []Message{
			{ID: "m1", Author: Author{ID: "u1"}, Text: "removed by a rule"},
			{ID: "m2", Author: Author{ID: "u2"}, Text: "flagged by history"},
			{ID: "m3", Author: Author{ID: "u3"}, Text: "clean"},
		},
	})

	// The worker is one goroutine and the jobs are queued in message order,
	// so once the LAST message's model call has happened, the first one's
	// would have too had it been queued.
	waitFor(t, "the model to be asked about the clean message", func() bool {
		_, seen := mod.modelObserved()
		return len(seen) > 0 && seen[len(seen)-1] == "clean"
	})
	n, seen := mod.modelObserved()
	if n != 2 || seen[0] != "flagged by history" || seen[1] != "clean" {
		t.Fatalf("the model was asked about %v; want exactly [flagged by history, clean] -- "+
			"never about a message a rule already removed", seen)
	}
}

// verdictTap records what the poll counter's tap (#14) is told, by message id.
type verdictTap struct {
	mu   sync.Mutex
	seen map[string]Display
}

func (v *verdictTap) Offer(m Message, d Display) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.seen[m.ID] = d
}
func (v *verdictTap) Judge(_ db.Platform, _, id string, d Display) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.seen[id] = d
}
func (*verdictTap) RetractMessage(db.Platform, string, string) {}
func (*verdictTap) RetractAuthor(db.Platform, string, string)  {}
func (*verdictTap) ClearRoom(db.Platform, string)              {}

func (v *verdictTap) get(id string) (Display, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	d, ok := v.seen[id]
	return d, ok
}

// #21's spend fix meets #14's poll tap. The model is skipped for a message
// the fast verdict removes -- and on that skip path the poll counter must
// STILL be told the message was blocked, or a vote a rule deleted would be
// counted (the counter waits for a verdict on a pending vote and counts a
// clear one). Written when the two branches were integrated: neither branch's
// tests had the other's half.
//
// Mutation: in checkAutomod, offer DisplayClear instead of DisplayBlocked when
// removes(v). Observed FAIL.
func TestTheModelSkipStillTellsThePollTapTheMessageWasBlocked(t *testing.T) {
	h := testHub(t)
	tap := &verdictTap{seen: map[string]Display{}}
	h.SetTap(tap)
	mod := &perTextModerator{
		stubModerator: stubModerator{modelOn: true},
		fast: map[string]automod.Verdict{
			"!vote 1 removed by a rule": {
				Findings: []automod.Finding{{Checker: automod.CheckerRules, Action: automod.ActionDelete}},
				Act:      []automod.Finding{{Checker: automod.CheckerRules, Action: automod.ActionDelete}},
			},
		},
	}
	h.SetModerator(mod)
	attach(t, h, &fakeAdapter{
		platform: db.PlatformTwitch, account: "acct",
		messages: []Message{
			{ID: "m1", Author: Author{ID: "u1"}, Text: "!vote 1 removed by a rule"},
			{ID: "m2", Author: Author{ID: "u2"}, Text: "!vote 2"},
		},
	})
	waitFor(t, "the model to be asked about the clean vote", func() bool {
		_, seen := mod.modelObserved()
		return len(seen) > 0 && seen[len(seen)-1] == "!vote 2"
	})
	if n, _ := mod.modelObserved(); n != 1 {
		t.Fatalf("the model was asked %d times; want once, never about the removed vote", n)
	}
	if d, ok := tap.get("m1"); !ok || d != DisplayBlocked {
		t.Errorf("the poll tap was told %v (seen=%v) for a vote a rule removed; want DisplayBlocked", d, ok)
	}
	if d, ok := tap.get("m2"); !ok || d == DisplayBlocked {
		t.Errorf("the poll tap was told %v (seen=%v) for a clean vote", d, ok)
	}
}
