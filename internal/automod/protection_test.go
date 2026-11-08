package automod

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod/pack"
	"github.com/rainmanjam/polyemesis/internal/db"
)

func newProtection(t *testing.T, cfg ProtectionConfig) *Protection {
	t.Helper()
	p, err := NewProtection(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func protectedEngine(t *testing.T, m Matrix, p *Protection) *Engine {
	t.Helper()
	hl := DefaultHistoryLimits()
	return New(m, PlatformCaps{}, nil, NewHistory(hl), nil).WithProtection(p)
}

// firstSevere takes a severe hate term from the pack at run time, so no test
// spells a slur.
func firstSevere(t *testing.T) string {
	t.Helper()
	pk, err := pack.Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range pk.Entries {
		if e.Category == pack.CategoryHate && e.Tier == pack.TierSevere && !strings.Contains(e.Term, " ") {
			return e.Term
		}
	}
	t.Fatal("no severe term")
	return ""
}

func TestAPackHitFlagsAndHoldsButNeverActs(t *testing.T) {
	hl := DefaultHistoryLimits()
	e := protectedEngine(t, DefaultMatrix(), newProtection(t, ProtectionConfig{History: &hl}))
	v := e.CheckFast(db.PlatformTwitch, Subject{AuthorID: "u1", AuthorName: "viewer", Text: "you " + firstSevere(t)})
	if !v.Hold {
		t.Fatalf("a severe hate term did not hold: %+v", v)
	}
	if len(v.Findings) != 1 {
		t.Fatalf("findings: %+v", v.Findings)
	}
	f := v.Findings[0]
	if f.Checker != CheckerProtection || f.Category != CategoryHateSpeech || f.Action != ActionFlag || !f.Hold || f.Detector != pack.GroupHateSevere {
		t.Errorf("finding: %+v", f)
	}
	for _, a := range v.Act {
		if a.Action != ActionFlag {
			t.Errorf("a hold armed %s", a.Action)
		}
	}
}

// A hold never makes a platform call: whatever the category, platform or
// checker, with nothing but flags armed, no finding the engine returns for
// action is above a flag.
func TestNoPackCategoryActsOnAnyPlatformUnarmed(t *testing.T) {
	sev := firstSevere(t)
	texts := map[string]string{
		"hate":      sev,
		"doxx":      "call +44 20 7946 0958 or someone@example.com",
		"scam":      "free nitro https://bit.ly/x",
		"flood":     "wow " + strings.Repeat("#", 40),
		"mentions":  "@a1 @b2 @c3 @d4 @e5 @f6",
		"impostor":  "hi",
		"raid-text": "everyone type this exact message in chat right now",
	}
	for _, plat := range Platforms {
		for name, text := range texts {
			p := newProtection(t, ProtectionConfig{Pack: pack.Config{Names: pack.NewNameSet([]string{"kestrel_demo"})}})
			e := New(DefaultMatrix(), PlatformCaps{}, nil, NewHistory(DefaultHistoryLimits()), nil).WithProtection(p)
			v := e.CheckFast(plat, Subject{AuthorID: "u", AuthorName: "kestrel_demo_official", Text: text})
			if len(v.Findings) == 0 {
				t.Errorf("%s/%s: nothing found", plat, name)
			}
			for _, f := range v.Act {
				if f.Action != ActionFlag {
					t.Errorf("%s/%s: %s armed an action %s", plat, name, f.Checker, f.Action)
				}
			}
		}
	}
	// Armed rules and history cells do not arm the pack: the pack has its own
	// column, and an absent cell is off.
	m := DefaultMatrix()
	for _, p := range Platforms {
		for _, a := range []Action{ActionDelete, ActionTimeout, ActionBan} {
			m.Set(Key{Platform: p, Action: a, Checker: CheckerRules}, true)
			m.Set(Key{Platform: p, Action: a, Checker: CheckerHistory}, true)
		}
	}
	e := protectedEngine(t, m, newProtection(t, ProtectionConfig{}))
	v := e.CheckFast(db.PlatformTwitch, Subject{AuthorID: "u", Text: sev})
	for _, f := range v.Act {
		if f.Checker == CheckerProtection && f.Action != ActionFlag {
			t.Errorf("arming rules and history armed the pack: %+v", f)
		}
	}
}

// With no pack attached the engine is exactly what it was: history findings
// carry only the additive Detector name, and never hold.
func TestWithoutThePackNothingChanges(t *testing.T) {
	e := New(DefaultMatrix(), PlatformCaps{}, nil, NewHistory(DefaultHistoryLimits()), nil)
	v := e.CheckFast(db.PlatformTwitch, Subject{AuthorID: "u", Text: "@a1 @b2 @c3 @d4 @e5 @f6 AAAAAAAAAAAAAAAAAAAAAAAAA"})
	if v.Hold {
		t.Fatal("history alone held a message")
	}
	seen := map[string]bool{}
	for _, f := range v.Findings {
		if f.Hold || f.Checker != CheckerHistory || f.Category != CategoryFlood {
			t.Errorf("history finding changed: %+v", f)
		}
		seen[f.Detector] = true
	}
	if !seen[DetectorMentions] || !seen[DetectorCaps] {
		t.Errorf("detector names missing: %v", seen)
	}
	if e.Protection() != nil || (*Engine)(nil).Protection() != nil {
		t.Error("an engine without the pack reports one")
	}
	e.LearnProtectedName("anyone")           // a no-op, not a panic
	(*Engine)(nil).LearnProtectedName("any") // likewise
	if (*Engine)(nil).WithProtection(nil) != nil {
		t.Error("WithProtection on a nil engine returned one")
	}
}

func TestHistoryDetectorsHoldPerThePackPolicy(t *testing.T) {
	hl := DefaultHistoryLimits()
	for name, tc := range map[string]struct {
		cfg  ProtectionConfig
		text string
		det  string
		hold bool
	}{
		"mentions hold by default": {ProtectionConfig{History: &hl}, "@a1 @b2 @c3 @d4 @e5 @f6 hi", DetectorMentions, true},
		"caps flag only":           {ProtectionConfig{History: &hl}, "THIS IS A VERY LOUD MESSAGE", DetectorCaps, false},
		"caps override holds":      {ProtectionConfig{History: &hl, Hold: map[string]bool{"caps": true}}, "THIS IS A VERY LOUD MESSAGE", DetectorCaps, true},
		"group name override":      {ProtectionConfig{History: &hl, Hold: map[string]bool{"flood/mentions": false}}, "@a1 @b2 @c3 @d4 @e5 @f6 hi", DetectorMentions, false},
	} {
		t.Run(name, func(t *testing.T) {
			e := protectedEngine(t, DefaultMatrix(), newProtection(t, tc.cfg))
			v := e.CheckFast(db.PlatformTwitch, Subject{AuthorID: "u", Text: tc.text})
			var got *Finding
			for i := range v.Findings {
				if v.Findings[i].Detector == tc.det {
					got = &v.Findings[i]
				}
			}
			if got == nil {
				t.Fatalf("%s did not fire: %+v", tc.det, v.Findings)
			}
			if got.Hold != tc.hold || got.Checker != CheckerHistory {
				t.Errorf("hold=%v checker=%s, want hold=%v from history", got.Hold, got.Checker, tc.hold)
			}
			// And exactly once: the pack must not count it a second time.
			n := 0
			for _, f := range v.Findings {
				if strings.HasSuffix(f.Detector, "/"+tc.det) || f.Detector == tc.det {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%s reported %d times", tc.det, n)
			}
		})
	}
}

func TestNearDuplicateIsDroppedWhenHistoryAlreadySawTheExactRepeat(t *testing.T) {
	hl := DefaultHistoryLimits()
	raid, nd := pack.NewRaid(pack.RaidConfig{}), pack.NewNearDup(pack.NearDupConfig{})
	p := newProtection(t, ProtectionConfig{History: &hl, Pack: pack.Config{Raid: raid, NearDup: nd}})
	e := protectedEngine(t, DefaultMatrix(), p)
	var last Verdict
	for i := 0; i < 5; i++ {
		last = e.CheckFast(db.PlatformTwitch, Subject{AuthorID: "spam", Text: "buy my cheap followers now"})
	}
	var repeats, near int
	for _, f := range last.Findings {
		if f.Detector == DetectorRepeats {
			repeats++
		}
		if f.Detector == pack.GroupNearDuplicate {
			near++
		}
	}
	if repeats != 1 || near != 0 {
		t.Errorf("exact repeat: history %d, near-duplicate %d (want 1 and 0)", repeats, near)
	}
	// Numbered copies are not exact, so the near-duplicate finding stands,
	// flag-only.
	for i := 0; i < 5; i++ {
		last = e.CheckFast(db.PlatformTwitch, Subject{AuthorID: "numbered", Text: fmt.Sprintf("giveaway entry %d", i)})
	}
	found := false
	for _, f := range last.Findings {
		if f.Detector == pack.GroupNearDuplicate {
			found = true
			if f.Hold || f.Category != CategoryFlood {
				t.Errorf("near-duplicate finding: %+v", f)
			}
		}
	}
	if !found {
		t.Errorf("numbered copies not flagged: %+v", last.Findings)
	}
}

func TestARaidSurvivesAnEngineRebuild(t *testing.T) {
	raid := pack.NewRaid(pack.RaidConfig{})
	build := func() *Engine {
		return protectedEngine(t, DefaultMatrix(), newProtection(t, ProtectionConfig{Pack: pack.Config{Raid: raid}}))
	}
	var v Verdict
	for i := 0; i < 5; i++ {
		// A settings save between every message rebuilds the engine and the
		// protection; the tracker is the caller's, so the count carries over.
		v = build().CheckFast(db.PlatformTwitch, Subject{AuthorID: fmt.Sprint("u", i), Text: "everyone type this exact message in chat right now"})
	}
	if !v.Hold {
		t.Fatalf("a rebuild mid-raid reset the count: %+v", v)
	}
	raided := false
	for _, f := range v.Findings {
		if f.Category == CategoryRaid && f.Hold && f.Checker == CheckerProtection && strings.Contains(f.Reason, "5 accounts") {
			raided = true
		}
	}
	if !raided {
		t.Errorf("no raid finding: %+v", v.Findings)
	}
}

func TestLearnedModeratorNamesProtectAgainstImpostors(t *testing.T) {
	e := protectedEngine(t, DefaultMatrix(), newProtection(t, ProtectionConfig{}))
	imp := Subject{AuthorID: "x", AuthorName: "harbor_wren_official", Text: "hi chat"}
	if e.CheckFast(db.PlatformTwitch, imp).Hold {
		t.Fatal("held before the moderator was seen")
	}
	e.LearnProtectedName("harbor_wren")
	v := e.CheckFast(db.PlatformYouTube, imp)
	if !v.Hold {
		t.Fatalf("an impostor of a learned moderator was not held: %+v", v)
	}
	if f := v.Findings[0]; f.Category != CategoryImpersonation || f.Action != ActionFlag {
		t.Errorf("finding: %+v", f)
	}
}

func TestCheckTextRunsTheStatelessDetectorsOnly(t *testing.T) {
	raid, nd := pack.NewRaid(pack.RaidConfig{}), pack.NewNearDup(pack.NearDupConfig{})
	e := protectedEngine(t, DefaultMatrix(), newProtection(t, ProtectionConfig{Pack: pack.Config{Raid: raid, NearDup: nd}}))
	v := e.CheckText(db.PlatformTwitch, "email me at someone@example.com")
	if !v.Hold || len(v.Findings) != 1 || v.Findings[0].Detector != pack.GroupEmail {
		t.Errorf("CheckText: %+v", v)
	}
	if raid.Tracked() != 0 || nd.Tracked() != 0 {
		t.Error("CheckText recorded state")
	}
	var nilP *Protection
	if nilP.checkText(db.PlatformTwitch, "x") != nil || nilP.check(db.PlatformTwitch, Subject{}, time.Now(), nil) != nil {
		t.Error("a nil Protection produced findings")
	}
	if nilP.Checker() != nil || e.Protection().Checker() == nil {
		t.Error("Checker is wrong for a nil and a built Protection")
	}
}

func TestExplainLeavesNoTraceAndNamesDetectorsOnly(t *testing.T) {
	raid, nd := pack.NewRaid(pack.RaidConfig{}), pack.NewNearDup(pack.NearDupConfig{})
	hl := DefaultHistoryLimits()
	hist := NewHistory(hl)
	p := newProtection(t, ProtectionConfig{History: &hl, Pack: pack.Config{Raid: raid, NearDup: nd, Names: pack.NewNameSet([]string{"kestrel_demo"})}})
	rules, err := NewRuleSet([]Rule{{ID: 3, Name: "r", Enabled: true, Pattern: `forbidden`, Action: ActionFlag}})
	if err != nil {
		t.Fatal(err)
	}
	e := New(DefaultMatrix(), PlatformCaps{}, rules, hist, nil).WithProtection(p)

	secret := "someone.private@example.com"
	for i := 0; i < 30; i++ {
		ex := e.Explain(db.PlatformTwitch, Subject{AuthorID: "a", AuthorName: "kestrel_demo_official", Text: "forbidden " + secret + " @a1 @b2 @c3 @d4 @e5 @f6"})
		if !ex.Protection || !ex.Held || len(ex.Windowed) == 0 {
			t.Fatalf("explanation: %+v", ex)
		}
		b, _ := json.Marshal(ex)
		if strings.Contains(string(b), secret) || strings.Contains(ex.String(), secret) {
			t.Fatal("the explanation carries the matched value")
		}
		seen := map[string]Detection{}
		for _, d := range ex.Detections {
			seen[d.Detector] = d
		}
		for _, want := range []string{pack.GroupEmail, pack.GroupImpersonation, DetectorMentions} {
			if _, ok := seen[want]; !ok {
				t.Fatalf("missing %s in %v", want, seen)
			}
		}
		if d := seen[pack.GroupEmail]; d.Form != "nfkc" || !d.Hold || !d.Act || d.Action != ActionFlag {
			t.Errorf("email detection: %+v", d)
		}
	}
	if hist.Tracked() != 0 || raid.Tracked() != 0 || nd.Tracked() != 0 {
		t.Errorf("Explain recorded state: history %d, raid %d, near-dup %d", hist.Tracked(), raid.Tracked(), nd.Tracked())
	}
	if ex := (*Engine)(nil).Explain(db.PlatformTwitch, Subject{Text: "x"}); len(ex.Detections) != 0 || ex.Protection {
		t.Errorf("nil engine: %+v", ex)
	}
	plain := New(DefaultMatrix(), PlatformCaps{}, nil, nil, nil).Explain(db.PlatformTwitch, Subject{Text: secret})
	if plain.Protection || len(plain.Detections) != 0 {
		t.Errorf("an engine without the pack explained: %+v", plain)
	}
}

func TestEveryPackCategoryIsAKnownCategoryWithAShortReason(t *testing.T) {
	for _, c := range []string{pack.CategoryHate, pack.CategoryProfanity, pack.CategoryDoxxing, pack.CategoryScam,
		pack.CategoryFlood, pack.CategoryImpersonation, pack.CategoryRaid, "something-new"} {
		cat := categoryOf(c)
		if !KnownCategory(cat) {
			t.Errorf("%s maps to an unknown category %q", c, cat)
		}
		if r := cat.Reason(); len(r) == 0 || len(r) > 100 {
			t.Errorf("%s: reason %q", c, r)
		}
	}
}

// The history detectors the pack leaves to History are counted by History's own
// code. This runs the corpus's lines for them through a real Engine, so the
// delegation is measured, not assumed.
func TestHistoryOwnedFloodLinesThroughTheRealEngine(t *testing.T) {
	f, err := os.Open(filepath.Join("pack", "testdata", "corpus-en.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	hl := DefaultHistoryLimits()
	want := map[string]string{"flood/mentions": DetectorMentions, "flood/links": DetectorLinks, "flood/caps": DetectorCaps}
	var positives, traps int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		var l struct {
			Text   string `json:"text"`
			Group  string `json:"group"`
			Expect string `json:"expect"`
			Slice  string `json:"slice"`
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil || want[l.Group] == "" || l.Slice == "positive" && l.Expect == "" {
			continue
		}
		e := protectedEngine(t, DefaultMatrix(), newProtection(t, ProtectionConfig{History: &hl}))
		// A fresh author per line: only the per-message shape is under test.
		v := e.CheckFast(db.PlatformTwitch, Subject{AuthorID: fmt.Sprint("line", n), Text: l.Text})
		fired, held := 0, false
		for _, fd := range v.Findings {
			if fd.Detector == want[l.Group] {
				fired++
				held = held || fd.Hold
			}
		}
		if l.Expect == "pass" {
			traps++
			if fired != 0 {
				t.Errorf("corpus line %d (%s) is a trap and fired %s", n, l.Group, want[l.Group])
			}
			continue
		}
		positives++
		if fired != 1 {
			t.Errorf("corpus line %d (%s): %s fired %d times, want once", n, l.Group, want[l.Group], fired)
		}
		if wantHold := l.Expect == "hold"; held != wantHold {
			t.Errorf("corpus line %d (%s): hold=%v, want %v", n, l.Group, held, wantHold)
		}
	}
	if positives < 30 || traps < 20 {
		t.Errorf("only %d positives and %d traps were run", positives, traps)
	}
}

// self_harm is never acted against, and the rule editor is another way to try:
// a category an operator's rule may borrow must not let it climb.
func TestARuleCannotActOnSelfHarm(t *testing.T) {
	mk := func(a Action, hold bool) error {
		_, err := NewRuleSet([]Rule{{ID: 1, Name: "r", Enabled: true, Pattern: `x`, Action: a, TimeoutSeconds: 60, Category: CategorySelfHarm, HoldOverlay: hold}})
		return err
	}
	for _, a := range []Action{ActionHideLocal, ActionHide, ActionDelete, ActionTimeout, ActionBan} {
		if mk(a, false) == nil {
			t.Errorf("a self_harm rule with action %s was accepted", a)
		}
	}
	if err := mk(ActionFlag, false); err != nil {
		t.Errorf("a flag-only self_harm rule was refused: %v", err)
	}
	if err := mk(ActionFlag, true); err != nil {
		t.Errorf("a flag-and-hold self_harm rule was refused: %v", err)
	}
	// Other pack categories may be borrowed at any action.
	if _, err := NewRuleSet([]Rule{{ID: 1, Name: "r", Enabled: true, Pattern: `x`, Action: ActionBan, Category: CategoryScam}}); err != nil {
		t.Errorf("a scam rule that bans was refused: %v", err)
	}
}

// An Engine built as a struct literal has no clock. CheckFast must not panic on
// it, with or without the pack.
func TestCheckFastWithoutAClockDoesNotPanic(t *testing.T) {
	p := newProtection(t, ProtectionConfig{})
	for _, e := range []*Engine{(&Engine{}).WithProtection(p), {history: NewHistory(DefaultHistoryLimits())}} {
		if e.history == nil {
			e.history = NewHistory(DefaultHistoryLimits())
		}
		e.CheckFast(db.PlatformTwitch, Subject{AuthorID: "a", Text: "hello there everyone"})
	}
}

// ExplainAs with the pack off reports what chat would do: the pack's findings
// are listed, marked not live, and do not hold.
func TestExplainAsWithThePackOffDoesNotClaimAHold(t *testing.T) {
	hl := DefaultHistoryLimits()
	p := newProtection(t, ProtectionConfig{History: &hl})
	e := New(DefaultMatrix(), PlatformCaps{}, nil, NewHistory(hl), nil).WithProtection(p)
	sub := Subject{AuthorID: "a", Text: "mail me at someone.private@example.com"}

	on := e.ExplainAs(db.PlatformTwitch, sub, true)
	if !on.Protection || !on.Held || !on.WouldHoldIfOn {
		t.Fatalf("pack on: %+v", on)
	}
	off := e.ExplainAs(db.PlatformTwitch, sub, false)
	if off.Protection || off.Held || !off.WouldHoldIfOn {
		t.Fatalf("pack off still claims a live hold: %+v", off)
	}
	found := false
	for _, d := range off.Detections {
		if d.Checker == CheckerProtection {
			found = true
			if d.Live {
				t.Errorf("a pack detection is live while the pack is off: %+v", d)
			}
		}
	}
	if !found {
		t.Error("the pack's detections are not shown at all while it is off")
	}
}

// The test box must answer what the ladder would ask a platform to do at
// strikes 1, 2 and 3, from the arming and the ceiling, not from the finding's
// own flag action: with Ban armed on Twitch only, a Twitch hit reaches a ban at
// strike 3 and YouTube stays a flag.
func TestExplainAsPreviewsTheLadderPerStrikeWithoutCountingOne(t *testing.T) {
	hl := DefaultHistoryLimits()
	p := newProtection(t, ProtectionConfig{History: &hl})
	m := DefaultMatrix()
	m.Set(Key{Platform: db.PlatformTwitch, Action: ActionBan, Checker: CheckerProtection}, true)
	m.Set(Key{Platform: db.PlatformTwitch, Action: ActionDelete, Checker: CheckerProtection}, true)
	st := NewStrikes()
	e := New(m, PlatformCaps{}, nil, NewHistory(hl), nil).WithProtection(p).
		WithLadder(&Ladder{Strikes: st, Ceilings: map[string]Rung{string(CategoryDoxxing): RungBan}})
	sub := Subject{AuthorID: "a", Text: "mail me at someone.private@example.com"}

	steps := func(plat db.Platform) []Action {
		var out []Action
		for _, d := range e.ExplainAs(plat, sub, true).Detections {
			if d.Detector == pack.GroupEmail {
				for _, s := range d.Ladder {
					out = append(out, s.Action)
				}
			}
		}
		return out
	}
	// Delete armed, timeout not: strike 2 lowers to the delete, strike 3 bans.
	if got, want := steps(db.PlatformTwitch), []Action{ActionDelete, ActionDelete, ActionBan}; !slices.Equal(got, want) {
		t.Errorf("twitch ladder = %v, want %v", got, want)
	}
	if got, want := steps(db.PlatformYouTube), []Action{ActionFlag, ActionFlag, ActionFlag}; !slices.Equal(got, want) {
		t.Errorf("youtube ladder = %v, want %v", got, want)
	}
	if st.Tracked() != 0 {
		t.Errorf("the preview recorded a strike: %d tracked", st.Tracked())
	}
	// Without a ladder there is no preview to claim.
	bare := New(m, PlatformCaps{}, nil, NewHistory(hl), nil).WithProtection(p)
	for _, d := range bare.ExplainAs(db.PlatformTwitch, sub, true).Detections {
		if len(d.Ladder) != 0 {
			t.Errorf("a ladder preview without a ladder: %+v", d)
		}
	}
}

// The test box shows the delete a sanction rung keeps: a ban step with delete
// armed says AlsoDelete, a plain delete step and a platform with neither do not.
func TestLadderPreviewShowsTheDeleteASanctionKeeps(t *testing.T) {
	hl := DefaultHistoryLimits()
	m := DefaultMatrix()
	m.Set(Key{Platform: db.PlatformTwitch, Action: ActionBan, Checker: CheckerProtection}, true)
	m.Set(Key{Platform: db.PlatformTwitch, Action: ActionDelete, Checker: CheckerProtection}, true)
	m.Set(Key{Platform: db.PlatformKick, Action: ActionBan, Checker: CheckerProtection}, true)
	e := New(m, PlatformCaps{}, nil, NewHistory(hl), nil).WithProtection(newProtection(t, ProtectionConfig{History: &hl})).
		WithLadder(&Ladder{Strikes: NewStrikes(), Ceilings: map[string]Rung{string(CategoryDoxxing): RungBan}})
	sub := Subject{AuthorID: "a", Text: "mail me at someone.private@example.com"}
	steps := func(plat db.Platform) []LadderStep {
		for _, d := range e.ExplainAs(plat, sub, true).Detections {
			if d.Detector == pack.GroupEmail {
				return d.Ladder
			}
		}
		return nil
	}
	tw := steps(db.PlatformTwitch)
	if len(tw) != 3 || tw[0].AlsoDelete || tw[1].AlsoDelete || tw[2].Action != ActionBan || !tw[2].AlsoDelete {
		t.Errorf("twitch preview = %+v, want only the ban step to also delete", tw)
	}
	for _, s := range steps(db.PlatformKick) {
		if s.AlsoDelete {
			t.Errorf("kick has no delete armed but previews one: %+v", s)
		}
	}
}
