package chat

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// displayRecorder writes down every call, in order, as one line each.
type displayRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (s *displayRecorder) Offer(m Message, d Display) {
	s.add(fmt.Sprintf("offer %s %d", m.ID, d))
}
func (s *displayRecorder) Judge(p db.Platform, account, id string, d Display) {
	s.add(fmt.Sprintf("judge %s %d", id, d))
}
func (s *displayRecorder) Retract(p db.Platform, account string, ids []string) {
	s.add(fmt.Sprintf("retract %v", ids))
}
func (s *displayRecorder) add(l string) {
	s.mu.Lock()
	s.calls = append(s.calls, l)
	s.mu.Unlock()
}
func (s *displayRecorder) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *displayRecorder) waitFor(t *testing.T, line string) {
	t.Helper()
	waitFor(t, line, func() bool {
		for _, c := range s.got() {
			if c == line {
				return true
			}
		}
		return false
	})
}

func displayHub(t *testing.T, mod Moderator, msgs ...Message) (*Hub, *displayRecorder, *fakeAdapter) {
	t.Helper()
	h := testHub(t)
	sink := &displayRecorder{}
	h.SetDisplay(sink)
	if mod != nil {
		h.SetModerator(mod)
	}
	a := &fakeAdapter{platform: db.PlatformTwitch, account: "acct", messages: msgs}
	attach(t, h, a)
	return h, sink, a
}

func msg(id, author, text string) Message {
	return Message{ID: id, Author: Author{ID: author, Name: author}, Text: text}
}

func TestWithNoAutomodEveryMessageIsClearForDisplayAtOnce(t *testing.T) {
	_, sink, _ := displayHub(t, nil, msg("m1", "u1", "hello"))
	sink.waitFor(t, fmt.Sprintf("offer m1 %d", DisplayClear))
}

func TestAFlagIsNotARemoval(t *testing.T) {
	mod := &stubModerator{verdict: automod.Verdict{
		Findings: []automod.Finding{{Checker: automod.CheckerRules, Action: automod.ActionFlag}},
		Act:      []automod.Finding{{Checker: automod.CheckerRules, Action: automod.ActionFlag}},
	}}
	_, sink, _ := displayHub(t, mod, msg("m1", "u1", "hello"))
	sink.waitFor(t, fmt.Sprintf("offer m1 %d", DisplayClear))
}

// A message the fast checkers will remove is offered as Blocked, and the offer
// arrives BEFORE the retraction the removal causes -- so a sink can never be
// told "show m1" after "m1 is gone".
func TestAFastRemovalIsBlockedBeforeItsRetraction(t *testing.T) {
	f := automod.Finding{Checker: automod.CheckerRules, Action: automod.ActionHideLocal}
	mod := &stubModerator{verdict: automod.Verdict{Findings: []automod.Finding{f}, Act: []automod.Finding{f}}}
	_, sink, _ := displayHub(t, mod, msg("m1", "u1", "bad"))
	sink.waitFor(t, "retract [m1]")
	got := sink.got()
	if got[0] != fmt.Sprintf("offer m1 %d", DisplayBlocked) {
		t.Fatalf("calls %v: want the Blocked offer first", got)
	}
}

func TestWithTheModelOnAMessageIsPendingUntilJudged(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verdict automod.Verdict
		err     error
		want    Display
	}{
		{"clean", automod.Verdict{}, nil, DisplayClear},
		{"removed", automod.Verdict{Act: []automod.Finding{{Checker: automod.CheckerModel, Action: automod.ActionDelete}}}, nil, DisplayBlocked},
		// Fail open, like the console: an outage of the model endpoint must not
		// silently empty every overlay.
		{"model error", automod.Verdict{}, errors.New("503"), DisplayClear},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mod := &stubModerator{modelOn: true, modelVerdict: tc.verdict, modelErr: tc.err}
			_, sink, _ := displayHub(t, mod, msg("m1", "u1", "hello"))
			sink.waitFor(t, fmt.Sprintf("judge m1 %d", tc.want))
			if got := sink.got(); got[0] != fmt.Sprintf("offer m1 %d", DisplayPending) {
				t.Fatalf("calls %v: want the Pending offer first", got)
			}
		})
	}
}

func TestEveryOperatorRemovalReachesTheDisplay(t *testing.T) {
	h, sink, _ := displayHub(t, nil, msg("m1", "u1", "a"), msg("m2", "u2", "b"), msg("m3", "u2", "c"))
	sink.waitFor(t, fmt.Sprintf("offer m3 %d", DisplayClear))

	if err := h.HideLocally(db.PlatformTwitch, "acct", "m1"); err != nil {
		t.Fatal(err)
	}
	sink.waitFor(t, "retract [m1]")

	// A ban sweeps the person's lines.
	h.mu.Lock()
	r := h.runners[runnerKey(db.PlatformTwitch, "acct")]
	h.mu.Unlock()
	h.retractUser(r, "u2")
	sink.waitFor(t, "retract [m2 m3]")
}

func TestADetachedSinkIsNotCalled(t *testing.T) {
	h := testHub(t)
	sink := &displayRecorder{}
	h.SetDisplay(sink)
	h.SetDisplay(nil)
	attach(t, h, &fakeAdapter{platform: db.PlatformTwitch, account: "acct", messages: []Message{msg("m1", "u1", "x")}})
	waitFor(t, "history", func() bool { return len(h.History(5)) == 1 })
	if got := sink.got(); len(got) != 0 {
		t.Fatalf("detached sink got %v", got)
	}
}
