package automod

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// THE test. With a cell off, nothing may be automatic -- and it has to hold for
// every action, on every platform, for every checker, because a single case
// passing proves only that one cell is wired.
//
// This is what stands between an operator and an accidental auto-ban.
func TestNothingIsAutomaticUnlessItsCellIsOn(t *testing.T) {
	caps := PlatformCaps{}
	m := Matrix{Enabled: true} // no cells on at all

	for _, p := range Platforms {
		for _, a := range Actions {
			for _, c := range Checkers {
				k := Key{Platform: p, Action: a, Checker: c}
				if m.Allows(caps, k) {
					t.Errorf("%s is automatic with no cell set", k)
				}
			}
		}
	}
}

// A fresh install must act on nothing except flagging, which changes nothing an
// audience can see. Automod that does something on first run is automod that
// surprises somebody mid-broadcast.
func TestDefaultsArmOnlyFlagging(t *testing.T) {
	caps := PlatformCaps{}
	m := DefaultMatrix()

	for _, p := range Platforms {
		for _, a := range Actions {
			for _, c := range Checkers {
				k := Key{Platform: p, Action: a, Checker: c}
				got := m.Allows(caps, k)
				want := a == ActionFlag
				if got != want {
					t.Errorf("%s allowed=%v, want %v", k, got, want)
				}
			}
		}
	}
	// And the collapsed view an operator reads must say "nothing automatic",
	// since flagging is not what they mean by that question.
	for p, n := range m.Summary(caps) {
		if n != 0 {
			t.Errorf("%s summary = %d automatic actions on a fresh install, want 0", p, n)
		}
	}
}

// Auto-ban is OFFERED rather than forbidden -- refusing to expose a capability
// takes the decision away from the person who knows their channel. What must
// hold is that it is off until switched on, per platform and per checker.
func TestAutoBanIsAvailableButOff(t *testing.T) {
	caps := PlatformCaps{}
	m := DefaultMatrix()

	k := Key{Platform: db.PlatformTwitch, Action: ActionBan, Checker: CheckerModel}
	if m.Allows(caps, k) {
		t.Fatal("model auto-ban is on by default")
	}
	if ok, _ := caps.Can(db.PlatformTwitch, ActionBan); !ok {
		t.Fatal("auto-ban is not offered at all; it should be available and off")
	}

	m.Set(k, true)
	if !m.Allows(caps, k) {
		t.Fatal("auto-ban did not switch on when asked")
	}
	// Turning it on for one platform must not arm it anywhere else.
	other := Key{Platform: db.PlatformKick, Action: ActionBan, Checker: CheckerModel}
	if m.Allows(caps, other) {
		t.Fatal("enabling auto-ban on Twitch also armed it on Kick")
	}
	// Nor for a different checker on the same platform.
	rules := Key{Platform: db.PlatformTwitch, Action: ActionBan, Checker: CheckerRules}
	if m.Allows(caps, rules) {
		t.Fatal("enabling the model checker also armed the rules checker")
	}
}

// The capability gate is not overridable. A stored setting written before a
// capability changed must not become an action nobody can explain.
func TestCapabilityGateOverridesAStoredCell(t *testing.T) {
	caps := PlatformCaps{}
	m := Matrix{Enabled: true}

	// Upstream hide on Twitch: not something the platform offers.
	k := Key{Platform: db.PlatformTwitch, Action: ActionHide, Checker: CheckerRules}
	m.Set(k, true)
	if m.Allows(caps, k) {
		t.Fatal("an unsupported action was allowed because a stored cell said so")
	}
	// And the UI must render it inert WITH a reason rather than as an unticked
	// box, which would read as a choice the operator simply has not made.
	for _, cell := range m.Cells(caps) {
		if cell.Platform == db.PlatformTwitch && cell.Action == ActionHide {
			if cell.Available {
				t.Fatal("upstream hide on Twitch is marked available")
			}
			if cell.Reason == "" {
				t.Fatal("an unavailable cell carries no reason")
			}
		}
	}
}

// Facebook is the one platform whose upstream hide is real. If this stops being
// true the matrix must stop offering it, which is what the gate is for.
func TestOnlyFacebookCanHideUpstream(t *testing.T) {
	caps := PlatformCaps{}
	for _, p := range Platforms {
		ok, reason := caps.Can(p, ActionHide)
		if p == db.PlatformFacebook {
			if !ok {
				t.Errorf("Facebook cannot hide upstream: %s", reason)
			}
			continue
		}
		if ok {
			t.Errorf("%s claims an upstream hide it does not have", p)
		}
		if reason == "" {
			t.Errorf("%s gives no reason for refusing an upstream hide", p)
		}
	}
}

// The kill switches have to work from any state, because they are what an
// operator reaches for mid-incident.
func TestKillSwitches(t *testing.T) {
	caps := PlatformCaps{}
	k := Key{Platform: db.PlatformTwitch, Action: ActionDelete, Checker: CheckerRules}

	m := Matrix{Enabled: true}
	m.Set(k, true)
	if !m.Allows(caps, k) {
		t.Fatal("precondition: the cell should be on")
	}

	m.PlatformEnabled = map[db.Platform]bool{db.PlatformTwitch: false}
	if m.Allows(caps, k) {
		t.Fatal("the per-platform kill switch did not stop the action")
	}
	// Other platforms keep working: killing one channel is not killing all.
	other := Key{Platform: db.PlatformKick, Action: ActionDelete, Checker: CheckerRules}
	m.Set(other, true)
	if !m.Allows(caps, other) {
		t.Fatal("killing Twitch also stopped Kick")
	}

	m.Enabled = false
	if m.Allows(caps, other) {
		t.Fatal("the global kill switch did not stop everything")
	}
}

// An absent platform in PlatformEnabled means enabled, so adding a platform
// does not silently disable it. The global switch is the one that fails closed.
func TestAnAbsentPlatformSwitchMeansEnabled(t *testing.T) {
	caps := PlatformCaps{}
	m := Matrix{Enabled: true, PlatformEnabled: map[db.Platform]bool{db.PlatformTwitch: false}}
	k := Key{Platform: db.PlatformYouTube, Action: ActionDelete, Checker: CheckerRules}
	m.Set(k, true)
	if !m.Allows(caps, k) {
		t.Fatal("a platform with no explicit switch was treated as disabled")
	}
}

// Off is stored as absence, so the persisted form stays sparse and "absent
// means off" remains the only rule a reader needs.
func TestSetOffRemovesTheKeyRatherThanStoringFalse(t *testing.T) {
	m := Matrix{Enabled: true}
	k := Key{Platform: db.PlatformKick, Action: ActionTimeout, Checker: CheckerHistory}
	m.Set(k, true)
	m.Set(k, false)
	if _, present := m.On[k.String()]; present {
		t.Fatalf("off was stored as a key: %v", m.On)
	}
}

func TestCellsCoverEveryCombinationExactlyOnce(t *testing.T) {
	caps := PlatformCaps{}
	cells := DefaultMatrix().Cells(caps)
	want := len(Platforms) * len(Actions) * len(Checkers)
	if len(cells) != want {
		t.Fatalf("got %d cells, want %d", len(cells), want)
	}
	seen := map[string]bool{}
	for _, c := range cells {
		k := Key{Platform: c.Platform, Action: c.Action, Checker: c.Checker}.String()
		if seen[k] {
			t.Fatalf("duplicate cell %s", k)
		}
		seen[k] = true
	}
}

// Actions are rendered in ascending consequence, so the most destructive row is
// last and never the one ticked by muscle memory.
func TestActionsAreOrderedByConsequence(t *testing.T) {
	if Actions[0] != ActionFlag {
		t.Fatalf("first action is %q, want flag", Actions[0])
	}
	if Actions[len(Actions)-1] != ActionBan {
		t.Fatalf("last action is %q, want ban", Actions[len(Actions)-1])
	}
	if actionRank(ActionDelete) <= actionRank(ActionHideLocal) {
		t.Fatal("delete is ranked no worse than a local hide")
	}
}

func TestParseKeyRoundTrips(t *testing.T) {
	k := Key{Platform: db.PlatformKick, Action: ActionTimeout, Checker: CheckerModel}
	got, err := ParseKey(k.String())
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}
	if got != k {
		t.Fatalf("round trip gave %+v, want %+v", got, k)
	}
	if _, err := ParseKey("nonsense"); err == nil {
		t.Fatal("a malformed key parsed successfully")
	}
}

// A key from a newer version must be ignorable rather than misread.
func TestUnknownPartsAreRecognisableAsUnknown(t *testing.T) {
	if KnownActions("teleport") {
		t.Fatal("an unknown action was accepted")
	}
	if KnownChecker("astrology") {
		t.Fatal("an unknown checker was accepted")
	}
	if KnownPlatform(db.Platform("myspace")) {
		t.Fatal("an unknown platform was accepted")
	}
	for _, a := range Actions {
		if !KnownActions(a) {
			t.Fatalf("%q is in Actions but not recognised", a)
		}
	}
}
