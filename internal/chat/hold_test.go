package chat

import (
	"fmt"
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
)

var heldFinding = automod.Finding{Checker: automod.CheckerRules, Action: automod.ActionFlag, Hold: true}

// A held verdict is offered as Held, not Blocked and not Clear, and nothing is
// retracted: the console keeps the line.
func TestAHeldVerdictIsOfferedHeldAndNothingIsRetracted(t *testing.T) {
	mod := &stubModerator{verdict: automod.Verdict{
		Findings: []automod.Finding{heldFinding},
		Act:      []automod.Finding{{Checker: automod.CheckerRules, Action: automod.ActionFlag}},
		Hold:     true,
	}}
	h, sink, _ := displayHub(t, mod, msg("m1", "u1", "hello"))
	sink.waitFor(t, fmt.Sprintf("offer m1 %d", DisplayHeld))
	for _, c := range sink.got() {
		if c == "retract [m1]" {
			t.Fatalf("a hold retracted the message: %v", sink.got())
		}
	}
	waitFor(t, "flag marked held", func() bool {
		got := h.Flagged("", 0)
		return len(got) == 1 && got[0].Held
	})
	if got := h.Flagged("", 0); !got[0].Findings[0].Hold {
		t.Fatalf("the finding lost its hold: %+v", got)
	}
}

// Removal outranks hold: a message being deleted is Blocked, not releasable.
func TestRemovalOutranksHold(t *testing.T) {
	del := automod.Finding{Checker: automod.CheckerRules, Action: automod.ActionHideLocal, Hold: true}
	mod := &stubModerator{verdict: automod.Verdict{Findings: []automod.Finding{del}, Act: []automod.Finding{del}, Hold: true}}
	_, sink, _ := displayHub(t, mod, msg("m1", "u1", "bad"))
	sink.waitFor(t, "retract [m1]")
	if got := sink.got(); got[0] != fmt.Sprintf("offer m1 %d", DisplayBlocked) {
		t.Fatalf("calls %v: want Blocked first", got)
	}
}

// The review list's Held marker is the MESSAGE's, set only where the line was
// really offered Held. A finding that asks for a hold does not make it so.
func TestTheHeldMarkIsOnlySetWhereTheLineWasReallyHeld(t *testing.T) {
	del := automod.Finding{Checker: automod.CheckerRules, Action: automod.ActionHideLocal, Hold: true}
	for name, v := range map[string]automod.Verdict{
		// Removal outranks hold: the line is Blocked and is not offered back.
		"removed": {Findings: []automod.Finding{del}, Act: []automod.Finding{del}, Hold: true},
		// A kill switch off: the engine sets no Verdict.Hold, the line goes out,
		// and the finding still says hold.
		"switch off": {Findings: []automod.Finding{heldFinding}},
	} {
		t.Run(name, func(t *testing.T) {
			h, sink, _ := displayHub(t, &stubModerator{verdict: v}, msg("m1", "u1", "x"))
			sink.waitFor(t, "offer m1 "+fmt.Sprint(map[bool]Display{true: DisplayBlocked, false: DisplayClear}[name == "removed"]))
			waitFor(t, "flagged", func() bool { return len(h.Flagged("", 0)) == 1 })
			if got := h.Flagged("", 0)[0]; got.Held {
				t.Fatalf("marked held though the line was not: %+v", got)
			}
		})
	}
}

// A model hold marks the flag too.
func TestAModelHoldMarksTheFlagHeld(t *testing.T) {
	mod := &stubModerator{modelOn: true, modelVerdict: automod.Verdict{Findings: []automod.Finding{heldFinding}, Hold: true}}
	h, sink, _ := displayHub(t, mod, msg("m1", "u1", "hello"))
	sink.waitFor(t, fmt.Sprintf("judge m1 %d", DisplayHeld))
	waitFor(t, "flag marked held", func() bool {
		got := h.Flagged("", 0)
		return len(got) == 1 && got[0].Held
	})
}

// With the model on, a hold set by the fast pass wins over Pending: the line is
// never in doubt, so it is never held pending a paid answer.
func TestAHeldVerdictBeatsPendingWhenTheModelIsOn(t *testing.T) {
	mod := &stubModerator{modelOn: true, verdict: automod.Verdict{Findings: []automod.Finding{heldFinding}, Hold: true}}
	_, sink, _ := displayHub(t, mod, msg("m1", "u1", "hello"))
	sink.waitFor(t, fmt.Sprintf("offer m1 %d", DisplayHeld))
	if got := sink.got(); got[0] != fmt.Sprintf("offer m1 %d", DisplayHeld) {
		t.Fatalf("calls %v", got)
	}
}

// A hold the MODEL's verdict sets is judged Held, so the pending line stays
// withheld and releasable.
func TestAModelHoldIsJudgedHeld(t *testing.T) {
	mod := &stubModerator{modelOn: true, modelVerdict: automod.Verdict{Findings: []automod.Finding{heldFinding}, Hold: true}}
	_, sink, _ := displayHub(t, mod, msg("m1", "u1", "hello"))
	sink.waitFor(t, fmt.Sprintf("judge m1 %d", DisplayHeld))
	if got := sink.got(); got[0] != fmt.Sprintf("offer m1 %d", DisplayPending) {
		t.Fatalf("calls %v: want Pending first", got)
	}
}

// Hold on alert text: Blocked, from the fast checkers and from the model, and
// with no history, flag or action.
func TestScreenTextHoldKeepsAlertTextOffAir(t *testing.T) {
	mod := &stubModerator{modelOn: true, verdict: automod.Verdict{Findings: []automod.Finding{heldFinding}, Hold: true}}
	h := screenHub(t, mod)
	if v := h.ScreenText(db.PlatformTwitch, "words", func(Display) { t.Error("done called") }); v != DisplayBlocked {
		t.Fatalf("verdict %v, want Blocked", v)
	}
	if n, _ := mod.modelObserved(); n != 0 {
		t.Errorf("the paid model was asked about text already held (%d calls)", n)
	}
	if len(h.Flagged("", 0)) != 0 || len(h.History(10)) != 0 {
		t.Error("screening wrote a flag or history")
	}
}

func TestScreenModelHoldKeepsAlertTextOffAir(t *testing.T) {
	mod := &stubModerator{modelOn: true, modelVerdict: automod.Verdict{Findings: []automod.Finding{heldFinding}, Hold: true}}
	h := screenHub(t, mod)
	ch := make(chan Display, 1)
	if v := h.ScreenText(db.PlatformTwitch, "words", func(d Display) { ch <- d }); v != DisplayPending {
		t.Fatalf("immediate verdict %v", v)
	}
	if got := awaitVerdict(t, ch); got != DisplayBlocked {
		t.Fatalf("verdict %v, want Blocked", got)
	}
}

// subjectRecorder records the Subject CheckFast was given.
type subjectRecorder struct {
	stubModerator
	smu  sync.Mutex
	subs []automod.Subject
}

func (s *subjectRecorder) CheckFast(p db.Platform, sub automod.Subject) automod.Verdict {
	s.smu.Lock()
	s.subs = append(s.subs, sub)
	s.smu.Unlock()
	return s.stubModerator.CheckFast(p, sub)
}

func (s *subjectRecorder) got() []automod.Subject {
	s.smu.Lock()
	defer s.smu.Unlock()
	return append([]automod.Subject(nil), s.subs...)
}

func TestCheckFastGetsTheWholeMessage(t *testing.T) {
	mod := &subjectRecorder{}
	m := Message{
		ID: "m1", Author: Author{ID: "u1", Name: "Ana"}, Text: "hi Kappa Kappa",
		Emotes: []Emote{{ID: "25", Start: 3, End: 8}, {ID: "25", Start: 9, End: 14}}, ReplyToID: "parent",
	}
	displayHub(t, mod, m)
	waitFor(t, "CheckFast called", func() bool { return len(mod.got()) == 1 })
	want := automod.Subject{AuthorID: "u1", AuthorName: "Ana", Text: "hi Kappa Kappa", EmoteCount: 2, ReplyToID: "parent"}
	if got := mod.got()[0]; got != want {
		t.Fatalf("Subject = %+v, want %+v", got, want)
	}
}

// A moderator without CheckText falls back to CheckFast with no author, and no
// name: alert text has neither.
func TestTheTextFallbackSendsOnlyText(t *testing.T) {
	mod := &subjectRecorder{}
	h := screenHub(t, mod)
	h.ScreenText(db.PlatformTwitch, "alert words", nil)
	h.ScreenName(db.PlatformTwitch, "Ana")
	got := mod.got()
	if len(got) != 2 || got[0] != (automod.Subject{Text: "alert words"}) || got[1] != (automod.Subject{Text: "Ana"}) {
		t.Fatalf("subjects = %+v", got)
	}
}
