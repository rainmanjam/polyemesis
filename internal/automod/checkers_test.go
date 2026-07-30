package automod

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// ------------------------------------------------------------------- rules

func TestRuleMatchesAndTheNegativeCase(t *testing.T) {
	set, err := NewRuleSet([]Rule{
		{ID: 1, Name: "no casino spam", Enabled: true, Pattern: `free\s*robux`, Action: ActionDelete},
	})
	if err != nil {
		t.Fatalf("NewRuleSet: %v", err)
	}
	if f := set.Check("get FREE ROBUX here"); len(f) != 1 {
		t.Fatalf("no finding on an obvious match: %+v", f)
	}
	// The negative case carries as much weight: a filter that fires on
	// everything passes the positive test just as happily.
	if f := set.Check("free advice: robux is a scam"); len(f) != 0 {
		t.Fatalf("fired on unrelated text: %+v", f)
	}
}

func TestDisabledRuleDoesNotFire(t *testing.T) {
	set, _ := NewRuleSet([]Rule{
		{ID: 1, Name: "off", Enabled: false, Pattern: `spam`, Action: ActionDelete},
	})
	if f := set.Check("spam spam spam"); len(f) != 0 {
		t.Fatalf("a disabled rule fired: %+v", f)
	}
}

// A bad pattern must be refused when the set is built, not silently dropped.
// Dropping it leaves an operator believing a protection exists when it does not
// -- the same silent-failure shape the capability gate exists to prevent.
func TestABadPatternRefusesTheWholeSet(t *testing.T) {
	_, err := NewRuleSet([]Rule{
		{ID: 1, Name: "ok", Enabled: true, Pattern: `fine`, Action: ActionDelete},
		{ID: 2, Name: "broken", Enabled: true, Pattern: `([unclosed`, Action: ActionDelete},
	})
	if err == nil {
		t.Fatal("a set with an uncompilable pattern was accepted")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Fatalf("the error does not name the offending rule: %v", err)
	}
}

func TestARuleAskingForAnUnknownActionIsRefused(t *testing.T) {
	if _, err := NewRuleSet([]Rule{
		{ID: 1, Name: "weird", Enabled: true, Pattern: `x`, Action: Action("teleport")},
	}); err == nil {
		t.Fatal("a rule asking for an unknown action was accepted")
	}
}

// Normalisation exists to defeat the specific tricks used to slip a term past a
// filter. Each case here is one of them.
func TestNormaliseDefeatsEvasion(t *testing.T) {
	set, _ := NewRuleSet([]Rule{
		{ID: 1, Name: "term", Enabled: true, Pattern: `badword`, Action: ActionDelete},
	})
	evasions := []string{
		"badword",
		"BaDwOrD",
		"b a d w o r d",
		"bbbaaaddddwooord",
		"b​adword", // zero-width space
		"b4dword",  // leetspeak
		"bаdword",  // Cyrillic 'а'
	}
	for _, s := range evasions {
		t.Run(s, func(t *testing.T) {
			if f := set.Check(s); len(f) == 0 {
				t.Fatalf("evasion got through: %q normalised to %q", s, Normalise(s))
			}
		})
	}
	// And it must not fold so hard that unrelated words collide.
	for _, s := range []string{"goodword", "a very ordinary sentence"} {
		if f := set.Check(s); len(f) != 0 {
			t.Fatalf("normalisation caused a false positive on %q", s)
		}
	}
}

// ----------------------------------------------------------------- history

// A fake clock, because a rate detector tested with real sleeps is a slow test
// that is also flaky on a loaded machine.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestHistory(limits HistoryLimits) (*History, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)}
	h := NewHistory(limits)
	h.now = clk.now
	return h, clk
}

// Fires at N in the window and NOT at N-1. Both halves are the test.
func TestRateDetectorFiresAtTheThresholdAndNotBelow(t *testing.T) {
	limits := DefaultHistoryLimits()
	limits.MaxMessages = 5
	h, _ := newTestHistory(limits)

	for i := 0; i < limits.MaxMessages; i++ {
		if f := h.Observe(db.PlatformTwitch, "u1", fmt.Sprintf("message %d", i)); len(f) != 0 {
			t.Fatalf("fired at %d messages, threshold is %d: %+v", i+1, limits.MaxMessages, f)
		}
	}
	f := h.Observe(db.PlatformTwitch, "u1", "one too many")
	if len(f) == 0 {
		t.Fatal("did not fire when the threshold was exceeded")
	}
	if f[0].Checker != CheckerHistory {
		t.Fatalf("checker = %q", f[0].Checker)
	}
}

// Messages outside the window must not count, or the detector is a lifetime
// counter that eventually fires on everyone.
func TestRateDetectorForgetsOutsideTheWindow(t *testing.T) {
	limits := DefaultHistoryLimits()
	limits.MaxMessages = 3
	limits.Window = 10 * time.Second
	h, clk := newTestHistory(limits)

	for i := 0; i < 3; i++ {
		h.Observe(db.PlatformTwitch, "u1", fmt.Sprintf("m%d", i))
	}
	clk.advance(11 * time.Second)
	if f := h.Observe(db.PlatformTwitch, "u1", "much later"); len(f) != 0 {
		t.Fatalf("counted messages from outside the window: %+v", f)
	}
}

// The same phrase repeated fires; three DIFFERENT messages do not. The second
// half is what proves it is repetition being detected and not merely volume.
func TestRepeatDetectorNeedsActualRepetition(t *testing.T) {
	limits := DefaultHistoryLimits()
	limits.MaxRepeats = 2
	limits.MaxMessages = 100 // take rate out of the picture
	h, _ := newTestHistory(limits)

	for i := 0; i < 2; i++ {
		if f := h.Observe(db.PlatformKick, "u1", "buy my thing"); len(f) != 0 {
			t.Fatalf("fired at repeat %d: %+v", i+1, f)
		}
	}
	if f := h.Observe(db.PlatformKick, "u1", "buy my thing"); len(f) == 0 {
		t.Fatal("did not fire on a repeated phrase")
	}

	h2, _ := newTestHistory(limits)
	for _, m := range []string{"hello", "how is everyone", "nice stream"} {
		if f := h2.Observe(db.PlatformKick, "u2", m); len(f) != 0 {
			t.Fatalf("fired on three different messages: %+v", f)
		}
	}
}

// Near-duplicates are the actual technique -- nobody paste-spams byte-identical
// text once they have been caught once.
func TestRepeatDetectorSeesThroughSmallVariations(t *testing.T) {
	limits := DefaultHistoryLimits()
	limits.MaxRepeats = 2
	limits.MaxMessages = 100
	h, _ := newTestHistory(limits)

	for _, m := range []string{"BUY MY THING", "buy   my thing", "buyyy my thing"} {
		h.Observe(db.PlatformKick, "u1", m)
	}
	if f := h.Observe(db.PlatformKick, "u1", "Buy My Thing"); len(f) == 0 {
		t.Fatal("did not see through case, spacing and doubled letters")
	}
}

// Two people saying the same thing is a conversation, not spam.
func TestAuthorsAreTrackedSeparately(t *testing.T) {
	limits := DefaultHistoryLimits()
	limits.MaxRepeats = 1
	limits.MaxMessages = 100
	h, _ := newTestHistory(limits)

	h.Observe(db.PlatformTwitch, "u1", "same phrase")
	if f := h.Observe(db.PlatformTwitch, "u2", "same phrase"); len(f) != 0 {
		t.Fatalf("one author's message counted against another: %+v", f)
	}
}

// The same author_id on two platforms is not one person's history.
func TestPlatformsAreTrackedSeparately(t *testing.T) {
	limits := DefaultHistoryLimits()
	limits.MaxMessages = 2
	h, _ := newTestHistory(limits)

	h.Observe(db.PlatformTwitch, "shared", "a")
	h.Observe(db.PlatformTwitch, "shared", "b")
	if f := h.Observe(db.PlatformKick, "shared", "c"); len(f) != 0 {
		t.Fatalf("history leaked across platforms: %+v", f)
	}
}

func TestShoutingNeedsLengthAndRatio(t *testing.T) {
	limits := DefaultHistoryLimits()
	limits.MaxMessages = 100
	h, _ := newTestHistory(limits)

	// Short shouts are not shouting. "OK" and "WHAT" are ordinary.
	if f := h.Observe(db.PlatformTwitch, "u1", "WHAT"); len(f) != 0 {
		t.Fatalf("fired on a short exclamation: %+v", f)
	}
	if f := h.Observe(db.PlatformTwitch, "u2", "THIS IS COMPLETELY UNREASONABLE"); len(f) == 0 {
		t.Fatal("did not fire on sustained upper case")
	}
	if f := h.Observe(db.PlatformTwitch, "u3", "this is a perfectly normal sentence"); len(f) != 0 {
		t.Fatalf("fired on ordinary text: %+v", f)
	}
}

func TestMentionSpam(t *testing.T) {
	limits := DefaultHistoryLimits()
	limits.MaxMessages = 100
	limits.MaxMentionsPerMessage = 3
	h, _ := newTestHistory(limits)

	if f := h.Observe(db.PlatformTwitch, "u1", "@a @b hello"); len(f) != 0 {
		t.Fatalf("fired on ordinary mentions: %+v", f)
	}
	if f := h.Observe(db.PlatformTwitch, "u2", "@a @b @c @d @e @f pile on"); len(f) == 0 {
		t.Fatal("did not fire on mention spam")
	}
}

// A raid is thousands of new authors in a minute. The ring must forget, or the
// defence becomes the denial of service.
func TestMemoryIsBoundedUnderARaid(t *testing.T) {
	limits := DefaultHistoryLimits()
	limits.IdleEviction = 30 * time.Second
	h, clk := newTestHistory(limits)

	for i := 0; i < 5000; i++ {
		h.Observe(db.PlatformTwitch, fmt.Sprintf("raider-%d", i), "hello")
	}
	if got := h.Tracked(); got != 5000 {
		t.Fatalf("tracked %d authors during the raid, want 5000", got)
	}
	clk.advance(2 * time.Minute)
	h.Observe(db.PlatformTwitch, "someone-later", "hi")
	if got := h.Tracked(); got > 1 {
		t.Fatalf("still tracking %d authors after they went idle", got)
	}
}

// Retain caps per-author storage regardless of how much one person says.
func TestPerAuthorStorageIsCapped(t *testing.T) {
	limits := DefaultHistoryLimits()
	limits.Retain = 10
	limits.MaxMessages = 10000
	h, _ := newTestHistory(limits)

	for i := 0; i < 500; i++ {
		h.Observe(db.PlatformTwitch, "chatty", fmt.Sprintf("m%d", i))
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if n := len(h.authors[authorKey(db.PlatformTwitch, "chatty")].entries); n > limits.Retain {
		t.Fatalf("kept %d entries for one author, cap is %d", n, limits.Retain)
	}
}

// Durations are seconds everywhere above the adapters. Kick counts minutes and
// converts at the last moment; a mixed unit here is how "600" becomes ten
// minutes on two platforms and seven days on a third.
func TestHistoryFindingsCarrySeconds(t *testing.T) {
	limits := DefaultHistoryLimits()
	limits.MaxMessages = 1
	limits.TimeoutSeconds = 90
	h, _ := newTestHistory(limits)

	h.Observe(db.PlatformKick, "u1", "a")
	f := h.Observe(db.PlatformKick, "u1", "b")
	if len(f) == 0 {
		t.Fatal("expected a finding")
	}
	if f[0].TimeoutSeconds != 90 {
		t.Fatalf("TimeoutSeconds = %d, want 90", f[0].TimeoutSeconds)
	}
}

// Worst-first, so a caller taking only the first finding acts on the most
// serious thing rather than the first one noticed.
func TestFindingsAreOrderedWorstFirst(t *testing.T) {
	got := sortByConsequence([]Finding{
		{Action: ActionFlag},
		{Action: ActionBan},
		{Action: ActionDelete},
	})
	if got[0].Action != ActionBan {
		t.Fatalf("first finding is %q, want ban", got[0].Action)
	}
	if got[len(got)-1].Action != ActionFlag {
		t.Fatalf("last finding is %q, want flag", got[len(got)-1].Action)
	}
}

// The despacing heuristic must not reintroduce the Scunthorpe problem. Removing
// spaces unconditionally makes "a bad wordsmith" a match for "badword"; the
// heuristic exists precisely so that it does not.
func TestDespacingDoesNotFireOnOrdinaryShortWords(t *testing.T) {
	set, _ := NewRuleSet([]Rule{
		{ID: 1, Name: "term", Enabled: true, Pattern: `badword`, Action: ActionDelete},
	})
	ordinary := []string{
		"a bad wordsmith is at work",
		"that is a bad word to use",
		"I am a big fan of it",
		"is it on or is it off",
	}
	for _, s := range ordinary {
		if f := set.Check(s); len(f) != 0 {
			t.Fatalf("false positive on ordinary text %q (despaced form matched)", s)
		}
	}
	// And the evasion it exists for still gets caught.
	if f := set.Check("b a d w o r d"); len(f) == 0 {
		t.Fatal("the letter-spaced evasion got through")
	}
}
