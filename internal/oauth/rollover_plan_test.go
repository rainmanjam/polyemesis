package oauth

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The planner decides WHEN a rollover is due and says which trigger made it so. It is a pure function of
// the inputs, so every rule is tested here as a table, with the clock passed in and never read. The rules
// that matter most on a live show are the ones that stop a rollover from firing when it should not: a
// destination that is not live, a trigger that has already fired, a scene switch that happened before the
// session began, and a second trigger landing on top of a rollover that has just run.

var planLondon = mustLoadPlanZone("Europe/London")

func mustLoadPlanZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// planBase is a session that went live at 09:00 UTC on 31 October 2026, on Twitch: a 48-hour cap, so the
// cutoff is 11:00 UTC on 2 November.
func planBase() RolloverInput {
	start := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	return RolloverInput{
		Live:             true,
		Limit:            mustLimit(db.PlatformTwitch),
		SessionStartedAt: start,
		MarginMinutes:    30,
		Now:              start.Add(time.Hour),
	}
}

func mustLimit(p db.Platform) PlatformLimit {
	l, ok := RolloverLimitFor(p)
	if !ok {
		panic("no limit for " + string(p))
	}
	return l
}

// TestNothingIsDueForADestinationThatIsNotLive keeps a planner from rolling over something that is not on air.
// A switched-off destination has no broadcast to roll, so a due answer here would be a call against nothing.
func TestNothingIsDueForADestinationThatIsNotLive(t *testing.T) {
	in := planBase()
	in.Live = false
	in.Now = in.SessionStartedAt.Add(48 * time.Hour)
	if d := PlanRollover(in); d.Due {
		t.Errorf("a destination that is not live was marked due by %q", d.Trigger)
	}
}

// TestTheSafetyMarginFiresExactlyAtCutoffMinusMargin pins the margin trigger to the instant it is named for:
// the rollover is due at the cutoff less the margin, not a moment after, and not before.
func TestTheSafetyMarginFiresExactlyAtCutoffMinusMargin(t *testing.T) {
	in := planBase()
	cutoff := in.SessionStartedAt.Add(48 * time.Hour)
	in.Now = cutoff.Add(-30*time.Minute - time.Second)
	if d := PlanRollover(in); d.Due {
		t.Fatalf("due one second before cutoff minus margin: %+v", d)
	}
	in.Now = cutoff.Add(-30 * time.Minute)
	d := PlanRollover(in)
	if !d.Due || d.Trigger != TriggerSafetyMargin {
		t.Fatalf("at cutoff minus margin: due=%v trigger=%q, want due with %q", d.Due, d.Trigger, TriggerSafetyMargin)
	}
	if want := cutoff.Add(-30 * time.Minute); !d.At.Equal(want) {
		t.Errorf("planned at %s, want %s", d.At, want)
	}
}

// TestAStatedLimitIsTheOnlyThingThatArmsTheMarginTrigger covers the platform with no number. Kick has no limit
// row with a figure, so a margin trigger must never run there, however late the session is.
func TestAStatedLimitIsTheOnlyThingThatArmsTheMarginTrigger(t *testing.T) {
	in := planBase()
	in.Limit = PlatformLimit{PresetID: "kick", Platform: db.PlatformKick, Kind: LimitKindBroadcastDuration, Status: LimitUnverified}
	in.Now = in.SessionStartedAt.Add(400 * time.Hour)
	if d := PlanRollover(in); d.Due {
		t.Errorf("an unverified limit armed a margin trigger: %+v", d)
	}
}

// TestTheQuietWindowFiresInsideTheWindowForASessionThatHasRunLongEnough checks the daily quiet window. It must
// fire inside the window and not before the session is an hour old, so a stream started at 03:10 is not
// rolled over at 03:11.
func TestTheQuietWindowFiresInsideTheWindowForASessionThatHasRunLongEnough(t *testing.T) {
	in := planBase()
	in.Limit = PlatformLimit{}
	in.QuietStart, in.QuietEnd, in.QuietZone = "03:00", "05:00", "Europe/London"
	in.SessionStartedAt = time.Date(2026, 10, 31, 1, 0, 0, 0, planLondon)
	in.Now = time.Date(2026, 10, 31, 3, 30, 0, 0, planLondon)
	d := PlanRollover(in)
	if !d.Due || d.Trigger != TriggerQuietWindow {
		t.Fatalf("inside the window, session 2.5 h old: due=%v trigger=%q", d.Due, d.Trigger)
	}
	in.SessionStartedAt = in.Now.Add(-20 * time.Minute)
	if d := PlanRollover(in); d.Due {
		t.Errorf("a session twenty minutes old was rolled over by the quiet window: %+v", d)
	}
}

// TestTheQuietWindowWrapsPastMidnight checks a window that starts one day and ends the next. A 23:00-01:00
// window must be open at 00:30, and closed at 12:00.
func TestTheQuietWindowWrapsPastMidnight(t *testing.T) {
	in := planBase()
	in.Limit = PlatformLimit{}
	in.QuietStart, in.QuietEnd, in.QuietZone = "23:00", "01:00", "Europe/London"
	in.SessionStartedAt = time.Date(2026, 10, 30, 10, 0, 0, 0, planLondon)
	in.Now = time.Date(2026, 10, 31, 0, 30, 0, 0, planLondon)
	if d := PlanRollover(in); !d.Due || d.Trigger != TriggerQuietWindow {
		t.Errorf("00:30 inside a 23:00-01:00 window: due=%v trigger=%q", d.Due, d.Trigger)
	}
	in.Now = time.Date(2026, 10, 31, 12, 0, 0, 0, planLondon)
	if d := PlanRollover(in); d.Due {
		t.Errorf("12:00 outside a 23:00-01:00 window was due: %+v", d)
	}
}

// TestASceneSwitchFiresOnlyForTheCurrentSession checks the OBS trigger. A switch counts when it lands after the
// session began and after the last rollover. A switch from before the session is an old cue, and must not roll
// a fresh session over.
func TestASceneSwitchFiresOnlyForTheCurrentSession(t *testing.T) {
	in := planBase()
	in.Limit = PlatformLimit{}
	in.OBSScene = "Be Right Back"
	in.SceneSwitchedAt = in.SessionStartedAt.Add(time.Hour)
	in.Now = in.SceneSwitchedAt.Add(time.Minute)
	if d := PlanRollover(in); !d.Due || d.Trigger != TriggerOBSScene {
		t.Fatalf("a switch inside the session: due=%v trigger=%q", d.Due, d.Trigger)
	}
	in.SceneSwitchedAt = in.SessionStartedAt.Add(-time.Hour)
	if d := PlanRollover(in); d.Due {
		t.Errorf("a switch from before the session was due: %+v", d)
	}
}

// TestASceneSwitchThatAlreadyRolledOverDoesNotFireAgain checks the debounce for the scene trigger. A switch that
// was already acted on, because a rollover ran after it, is spent.
func TestASceneSwitchThatAlreadyRolledOverDoesNotFireAgain(t *testing.T) {
	in := planBase()
	in.Limit = PlatformLimit{}
	in.OBSScene = "BRB"
	in.SceneSwitchedAt = in.SessionStartedAt.Add(time.Hour)
	in.LastRolloverAt = in.SceneSwitchedAt.Add(time.Minute)
	in.Now = in.LastRolloverAt.Add(30 * time.Minute)
	if d := PlanRollover(in); d.Due {
		t.Errorf("a scene switch that a later rollover already acted on fired again: %+v", d)
	}
}

// TestARecentRolloverSuppressesEveryTriggerForAShortInterval is the repeat-firing guard. A quiet window, a scene
// and the margin can all be true in the same minute; the first rollover must be the only one.
func TestARecentRolloverSuppressesEveryTriggerForAShortInterval(t *testing.T) {
	in := planBase()
	cutoff := in.SessionStartedAt.Add(48 * time.Hour)
	in.Now = cutoff.Add(-30 * time.Minute)
	in.LastRolloverAt = in.Now.Add(-rolloverMinInterval / 2)
	if d := PlanRollover(in); d.Due {
		t.Errorf("a rollover fired %s after the last one; the debounce is %s", rolloverMinInterval/2, rolloverMinInterval)
	}
	in.LastRolloverAt = in.Now.Add(-rolloverMinInterval - time.Second)
	if d := PlanRollover(in); !d.Due {
		t.Errorf("a rollover just past the debounce interval was still suppressed")
	}
}

// TestTheEarliestDueTriggerIsTheOneReported checks that when several triggers are due at once, the one whose
// moment came first is named. That is the one an operator will look for on the timeline.
func TestTheEarliestDueTriggerIsTheOneReported(t *testing.T) {
	in := planBase()
	in.QuietStart, in.QuietEnd, in.QuietZone = "03:00", "05:00", "Europe/London"
	in.SessionStartedAt = time.Date(2026, 10, 31, 1, 0, 0, 0, planLondon)
	in.SceneSwitchedAt = time.Date(2026, 10, 31, 2, 30, 0, 0, planLondon)
	in.OBSScene = "BRB"
	in.Now = time.Date(2026, 10, 31, 3, 30, 0, 0, planLondon)
	in.Limit = PlatformLimit{}
	d := PlanRollover(in)
	if d.Trigger != TriggerOBSScene {
		t.Errorf("trigger = %q, want the earlier scene switch (%q) rather than the window opening", d.Trigger, TriggerOBSScene)
	}
}

// TestANextPlannedTimeIsReportedWhenNothingIsDue gives the countdown card its "planned for" moment. It is the
// earliest future moment among the enabled triggers, and it is not due yet.
func TestANextPlannedTimeIsReportedWhenNothingIsDue(t *testing.T) {
	in := planBase()
	cutoff := in.SessionStartedAt.Add(48 * time.Hour)
	d := PlanRollover(in)
	if d.Due {
		t.Fatalf("nothing should be due one hour in: %+v", d)
	}
	if want := cutoff.Add(-30 * time.Minute); !d.Next.Equal(want) {
		t.Errorf("next planned = %s, want %s", d.Next, want)
	}
}

// TestTheConfigurationIsValidatedBeforeItIsSaved refuses the settings that would make the planner guess. A margin
// outside the sane range, a window with one end missing, an unknown zone, and a window that begins and ends at the
// same minute are all refused with a reason the operator can act on.
func TestTheConfigurationIsValidatedBeforeItIsSaved(t *testing.T) {
	cases := []struct {
		name string
		in   RolloverConfig
		ok   bool
	}{
		{"defaults are valid", RolloverConfig{MarginMinutes: 30}, true},
		{"margin below the floor", RolloverConfig{MarginMinutes: 1}, false},
		{"margin above the ceiling", RolloverConfig{MarginMinutes: 500}, false},
		{"window with no end", RolloverConfig{MarginMinutes: 30, QuietStart: "03:00", QuietZone: "UTC"}, false},
		{"window with no zone", RolloverConfig{MarginMinutes: 30, QuietStart: "03:00", QuietEnd: "05:00"}, false},
		{"window with an unknown zone", RolloverConfig{MarginMinutes: 30, QuietStart: "03:00", QuietEnd: "05:00", QuietZone: "Mars/Olympus"}, false},
		{"window that starts and ends together", RolloverConfig{MarginMinutes: 30, QuietStart: "03:00", QuietEnd: "03:00", QuietZone: "UTC"}, false},
		{"a clock time out of range", RolloverConfig{MarginMinutes: 30, QuietStart: "25:00", QuietEnd: "05:00", QuietZone: "UTC"}, false},
		{"a valid window", RolloverConfig{MarginMinutes: 30, QuietStart: "03:00", QuietEnd: "05:00", QuietZone: "Europe/London"}, true},
	}
	for _, c := range cases {
		err := c.in.Validate()
		if c.ok && err != nil {
			t.Errorf("%s: refused: %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

// TestTheQuietWindowDoesNotFireTwiceInTheSameWindow checks that a window already acted on stays spent. A rollover
// that ran at 03:10 inside a 03:00-05:00 window must not fire again at 03:40 in that same window; the next
// quiet-window rollover belongs to the next day's opening.
func TestTheQuietWindowDoesNotFireTwiceInTheSameWindow(t *testing.T) {
	in := planBase()
	in.Limit = PlatformLimit{}
	in.QuietStart, in.QuietEnd, in.QuietZone = "03:00", "05:00", "Europe/London"
	in.SessionStartedAt = time.Date(2026, 10, 31, 1, 0, 0, 0, planLondon)
	in.LastRolloverAt = time.Date(2026, 10, 31, 3, 10, 0, 0, planLondon)
	in.Now = time.Date(2026, 10, 31, 3, 40, 0, 0, planLondon)
	d := PlanRollover(in)
	if d.Due {
		t.Fatalf("the quiet window fired again inside the window it had already rolled over: %+v", d)
	}
	want := time.Date(2026, 11, 1, 3, 0, 0, 0, planLondon)
	if !d.Next.Equal(want) {
		t.Errorf("next quiet rollover planned for %s, want the next day's opening %s", d.Next, want)
	}
}
