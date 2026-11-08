package automod

import (
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod/pack"
	"github.com/rainmanjam/polyemesis/internal/db"
)

func firstContextual(t *testing.T) string {
	t.Helper()
	pk, err := pack.Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range pk.Entries {
		if e.Category == pack.CategoryHate && e.Tier == pack.TierContextual && !strings.Contains(e.Term, " ") {
			return e.Term
		}
	}
	t.Fatal("pack has no single-word contextual term")
	return ""
}

// ladderEngine is a protected engine on a fake clock, with the given rungs armed
// on Twitch for the protection checker.
func ladderEngine(t *testing.T, st *Strikes, l *Ladder, rungs ...Action) (*Engine, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
	hl := DefaultHistoryLimits()
	m := DefaultMatrix()
	for _, r := range rungs {
		m.Set(Key{Platform: db.PlatformTwitch, Action: r, Checker: CheckerProtection}, true)
	}
	e := New(m, PlatformCaps{}, nil, NewHistory(hl), nil).WithProtection(newProtection(t, ProtectionConfig{History: &hl}))
	e.now = clk.now
	if l == nil {
		l = &Ladder{}
	}
	l.Strikes = st
	return e.WithLadder(l), clk
}

func slur(t *testing.T) string { return "you " + firstSevere(t) }

// hit sends one severe-hate message from u1 and returns the resolved action.
func hit(e *Engine, text string) Finding {
	v := e.CheckFast(db.PlatformTwitch, Subject{AuthorID: "u1", AuthorName: "viewer", Text: text})
	for _, f := range v.Findings {
		if f.Checker == CheckerProtection {
			return f
		}
	}
	return Finding{}
}

func TestLadderClimbsWithEveryRungArmed(t *testing.T) {
	st := NewStrikes()
	e, clk := ladderEngine(t, st, nil, ActionDelete, ActionTimeout, ActionBan)
	want := []Action{ActionDelete, ActionTimeout, ActionBan, ActionBan}
	for i, w := range want {
		f := hit(e, slur(t))
		if f.Action != w {
			t.Fatalf("strike %d: action %q, want %q", i+1, f.Action, w)
		}
		if w == ActionTimeout && f.TimeoutSeconds != DefaultLadderTimeoutSeconds {
			t.Fatalf("timeout rung length %d, want %d", f.TimeoutSeconds, DefaultLadderTimeoutSeconds)
		}
		if !f.Hold {
			t.Fatalf("strike %d: a severe hit stopped holding", i+1)
		}
		clk.advance(time.Second)
	}
}

func TestLadderActsOnlyOnArmedRungsAtOrBelowTheStrike(t *testing.T) {
	cases := []struct {
		name  string
		armed []Action
		want  []Action // strikes 1, 2, 3
	}{
		{"nothing armed", nil, []Action{ActionFlag, ActionFlag, ActionFlag}},
		{"only ban", []Action{ActionBan}, []Action{ActionFlag, ActionFlag, ActionBan}},
		{"only timeout", []Action{ActionTimeout}, []Action{ActionFlag, ActionTimeout, ActionTimeout}},
		{"only delete", []Action{ActionDelete}, []Action{ActionDelete, ActionDelete, ActionDelete}},
		{"delete and ban", []Action{ActionDelete, ActionBan}, []Action{ActionDelete, ActionDelete, ActionBan}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, clk := ladderEngine(t, NewStrikes(), nil, tc.armed...)
			for i, w := range tc.want {
				if got := hit(e, slur(t)).Action; got != w {
					t.Fatalf("strike %d: %q, want %q", i+1, got, w)
				}
				clk.advance(time.Second)
			}
		})
	}
}

func TestAnUnarmedLadderStillCountsAndArmingStartsTheCountAgain(t *testing.T) {
	st := NewStrikes()
	e, clk := ladderEngine(t, st, nil)
	for i := 0; i < 3; i++ {
		hit(e, slur(t))
		clk.advance(time.Second)
	}
	// The operator arms ban. Three prior strikes must not make the next message a
	// ban: arming only affects what happens afterwards.
	m := e.Matrix()
	m.Set(Key{Platform: db.PlatformTwitch, Action: ActionBan, Checker: CheckerProtection}, true)
	before := st.Epoch(db.PlatformTwitch)
	e.SetMatrix(m)
	if st.Epoch(db.PlatformTwitch) == before {
		t.Fatal("arming a rung did not start a new epoch")
	}
	if got := hit(e, slur(t)).Action; got != ActionFlag {
		t.Fatalf("first hit after arming ban: %q, want flag", got)
	}
	clk.advance(time.Second)
	hit(e, slur(t))
	clk.advance(time.Second)
	if got := hit(e, slur(t)).Action; got != ActionBan {
		t.Fatalf("third hit after arming ban: %q, want ban", got)
	}
}

func TestEpochIsPerPlatformAndAnUnchangedSetIsANoOp(t *testing.T) {
	st := NewStrikes()
	e, _ := ladderEngine(t, st, nil, ActionDelete)
	tw := st.Epoch(db.PlatformTwitch)
	other := st.Epoch(db.PlatformYouTube)
	e.SetMatrix(e.Matrix()) // same armed set
	if st.Epoch(db.PlatformTwitch) != tw {
		t.Fatal("re-applying an identical matrix reset the strikes")
	}
	if tw == 0 {
		t.Fatal("arming at build did not start an epoch")
	}
	m := e.Matrix()
	m.Set(Key{Platform: db.PlatformTwitch, Action: ActionTimeout, Checker: CheckerProtection}, true)
	e.SetMatrix(m)
	if st.Epoch(db.PlatformYouTube) != other {
		t.Fatal("arming Twitch moved YouTube's epoch")
	}
}

func TestStrikesSurviveAnEngineRebuild(t *testing.T) {
	st := NewStrikes()
	e1, clk := ladderEngine(t, st, nil, ActionDelete, ActionTimeout, ActionBan)
	hit(e1, slur(t))
	clk.advance(time.Second)
	// A settings save rebuilds the engine with the same Strikes and the same
	// armed set: the count carries on (strike 2 is a timeout).
	e2, _ := ladderEngine(t, st, nil, ActionDelete, ActionTimeout, ActionBan)
	e2.now = clk.now
	if got := hit(e2, slur(t)).Action; got != ActionTimeout {
		t.Fatalf("strike 2 after a rebuild: %q, want timeout", got)
	}
}

func TestStrikesFallOutOfTheWindow(t *testing.T) {
	e, clk := ladderEngine(t, NewStrikes(), &Ladder{Window: time.Minute}, ActionDelete, ActionTimeout, ActionBan)
	hit(e, slur(t))
	clk.advance(2 * time.Minute)
	if got := hit(e, slur(t)).Action; got != ActionDelete {
		t.Fatalf("a hit after the window: %q, want delete (strike 1)", got)
	}
}

func TestCeilingsCapTheClimb(t *testing.T) {
	st := NewStrikes()
	e, clk := ladderEngine(t, st, nil, ActionDelete, ActionTimeout, ActionBan)
	email := "mail me at someone@gmail.com"
	var last Finding
	for i := 0; i < 4; i++ {
		last = hit(e, email)
		clk.advance(time.Second)
	}
	if last.Category != CategoryDoxxing || last.Action != ActionTimeout {
		t.Fatalf("doxxing at strike 4: %q/%q, want timeout (its ceiling)", last.Category, last.Action)
	}
}

func TestAnOverrideMovesACeiling(t *testing.T) {
	l := &Ladder{Ceilings: map[string]Rung{"doxxing": RungDelete}}
	e, clk := ladderEngine(t, NewStrikes(), l, ActionDelete, ActionTimeout, ActionBan)
	var last Finding
	for i := 0; i < 3; i++ {
		last = hit(e, "mail me at someone@gmail.com")
		clk.advance(time.Second)
	}
	if last.Action != ActionDelete {
		t.Fatalf("doxxing with a delete ceiling at strike 3: %q", last.Action)
	}
}

func TestContextualHateAndSelfHarmNeverClimb(t *testing.T) {
	l := &Ladder{Ceilings: map[string]Rung{"hate_speech": RungBan, "self_harm": RungBan}}
	if got := l.ceilingOf(Finding{Category: CategorySelfHarm}); got != RungHold {
		t.Fatalf("self_harm ceiling %v, want hold whatever is stored", got)
	}
	if got := l.ceilingOf(Finding{Category: CategoryHateSpeech, Detector: pack.GroupHateContextual}); got != RungHold {
		t.Fatalf("contextual hate ceiling %v, want hold", got)
	}
	e, clk := ladderEngine(t, NewStrikes(), nil, ActionDelete, ActionTimeout, ActionBan)
	for i := 0; i < 3; i++ {
		f := hit(e, "you "+firstContextual(t))
		if f.Action != ActionFlag || f.Hold {
			t.Fatalf("contextual hate: action %q hold %v, want a plain flag", f.Action, f.Hold)
		}
		clk.advance(time.Second)
	}
}

func TestImpersonationStaysAtHold(t *testing.T) {
	hl := DefaultHistoryLimits()
	names := pack.NewNameSet([]string{"alphastreamer"})
	p := newProtection(t, ProtectionConfig{Pack: pack.Config{Names: names}, History: &hl})
	m := DefaultMatrix()
	for _, a := range []Action{ActionDelete, ActionTimeout, ActionBan} {
		m.Set(Key{Platform: db.PlatformTwitch, Action: a, Checker: CheckerProtection}, true)
	}
	e := New(m, PlatformCaps{}, nil, NewHistory(hl), nil).WithProtection(p).WithLadder(&Ladder{Strikes: NewStrikes()})
	for i := 0; i < 3; i++ {
		v := e.CheckFast(db.PlatformTwitch, Subject{AuthorID: "x1", AuthorName: "Alphastreamer_", Text: "hello"})
		if len(v.Findings) != 1 {
			t.Fatalf("impersonation findings: %+v", v.Findings)
		}
		if v.Findings[0].Action != ActionFlag || len(v.Act) != 1 || v.Act[0].Action != ActionFlag {
			t.Fatalf("impersonation acted: %+v", v)
		}
	}
}

func TestExplainAndCheckTextRecordNoStrikes(t *testing.T) {
	st := NewStrikes()
	e, _ := ladderEngine(t, st, nil, ActionDelete, ActionTimeout, ActionBan)
	e.Explain(db.PlatformTwitch, Subject{AuthorID: "u1", Text: slur(t)})
	e.CheckText(db.PlatformTwitch, slur(t))
	if st.Tracked() != 0 {
		t.Fatalf("a pretend message recorded a strike (%d authors tracked)", st.Tracked())
	}
}

func TestOnlyPackHitsWithAnAuthorCount(t *testing.T) {
	st := NewStrikes()
	e, _ := ladderEngine(t, st, nil, ActionDelete)
	e.CheckFast(db.PlatformTwitch, Subject{AuthorID: "u1", Text: "hello there"})
	e.CheckFast(db.PlatformTwitch, Subject{AuthorID: "", Text: slur(t)})
	if st.Tracked() != 0 {
		t.Fatalf("tracked %d authors for a clean message and an anonymous hit", st.Tracked())
	}
}

func TestOneStrikePerMessageNotPerFinding(t *testing.T) {
	e, _ := ladderEngine(t, NewStrikes(), nil, ActionDelete, ActionTimeout, ActionBan)
	// An email and a severe term in one message: two findings, one strike.
	f := hit(e, slur(t)+" someone@gmail.com")
	if f.Action != ActionDelete {
		t.Fatalf("first message with two findings: %q, want delete (strike 1)", f.Action)
	}
}

func TestStrikesAreBoundedAndIdleOnesEvicted(t *testing.T) {
	st := NewStrikes()
	st.max = 50
	at := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 500; i++ {
		st.Record(db.PlatformTwitch, "a"+string(rune('A'+i%26))+strings.Repeat("x", i/26), at, time.Minute)
	}
	if st.Tracked() > 50 {
		t.Fatalf("tracked %d authors over a ceiling of 50", st.Tracked())
	}
	st.Record(db.PlatformTwitch, "late", at.Add(10*time.Minute), time.Minute)
	if st.Tracked() != 1 {
		t.Fatalf("idle authors were not evicted: %d tracked", st.Tracked())
	}
	var nilStrikes *Strikes
	if nilStrikes.Record(db.PlatformTwitch, "u", at, time.Minute) != 0 {
		t.Fatal("nil Strikes counted")
	}
	nilStrikes.Arm(db.PlatformTwitch, 1)
}

func TestRungsAndTheirNames(t *testing.T) {
	for s, a := range map[string]Action{"hold": "", "delete": ActionDelete, "timeout": ActionTimeout, "ban": ActionBan} {
		r, ok := ParseRung(s)
		if !ok || r.Action() != a {
			t.Fatalf("%s parsed to %v/%v", s, r, ok)
		}
	}
	if _, ok := ParseRung("kick"); ok {
		t.Fatal("an unknown rung parsed")
	}
	if (Rung(9)).Action() != "" {
		t.Fatal("an out-of-range rung has an action")
	}
	if (&Ladder{TimeoutSeconds: -5}).timeoutSeconds() != DefaultLadderTimeoutSeconds || (&Ladder{}).window() != DefaultStrikeWindow {
		t.Fatal("ladder defaults")
	}
}

func TestPersistentIsUsableAndLadderWithoutStrikesIsIgnored(t *testing.T) {
	ps := NewPersistent()
	if ps.Strikes == nil || ps.Raid == nil || ps.NearDup == nil || ps.Names == nil {
		t.Fatalf("%+v", ps)
	}
	e := New(DefaultMatrix(), PlatformCaps{}, nil, nil, nil)
	if e.WithLadder(&Ladder{}).ladder != nil || e.WithLadder(nil).ladder != nil {
		t.Fatal("a ladder with no Strikes was attached")
	}
	var ne *Engine
	if ne.WithLadder(&Ladder{Strikes: ps.Strikes}) != nil {
		t.Fatal("nil engine")
	}
	if got := CeilingKeys(); len(got) != 7 {
		t.Fatalf("ceiling keys: %v", got)
	}
}

func TestPolicyKeyNamesProfanityByItsGroup(t *testing.T) {
	if policyKey(Finding{Category: CategoryOther, Detector: pack.GroupProfanityMild}) != "profanity" ||
		policyKey(Finding{Category: CategoryScam, Detector: pack.GroupBio}) != "scam" {
		t.Fatal("policyKey")
	}
}
