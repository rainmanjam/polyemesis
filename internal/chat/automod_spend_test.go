package chat

import (
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
