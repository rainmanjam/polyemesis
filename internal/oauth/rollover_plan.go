package oauth

import (
	"fmt"
	"strings"
	"time"
	// The zone database is compiled in. A quiet window names an IANA zone, and a container without
	// /usr/share/zoneinfo would otherwise refuse every zone but UTC, the failure internal/scheduler
	// records at length. The scheduler imports it too; importing it here keeps this package honest alone.
	_ "time/tzdata"
)

// Planned rollover (#19): the planner decides WHEN a rollover is due, and says which trigger made it so.
//
// It is a pure function of its input. The clock arrives as Now and nothing here reads time.Now, so every rule
// is a table test. It decides nothing about HOW: the executors (youtube_rollover.go for YouTube, the
// reconnect for Twitch) do the platform work, and the sweep in internal/api runs them. Keeping the decision
// apart means the rule that matters most on a live show, "do not fire twice", is checked without a platform
// or a database in the loop.

// RolloverTrigger names what made a rollover due. The strings are what the timeline and the webhook carry,
// so they are as stable as an event type.
type RolloverTrigger string

const (
	// TriggerSafetyMargin fires at the platform's cap less the operator's margin.
	TriggerSafetyMargin RolloverTrigger = "margin"
	// TriggerQuietWindow fires once a daily quiet window is open and the session has run long enough that a
	// fresh start is not rolled over straight away.
	TriggerQuietWindow RolloverTrigger = "quiet"
	// TriggerOBSScene fires when the operator's OBS scene becomes active during the current session. The
	// caller must pass only live switches; a switch OBS reported as a reconnect snapshot is not a cue.
	TriggerOBSScene RolloverTrigger = "obsScene"
)

const (
	// rolloverMinInterval is the debounce. After a rollover runs, no trigger fires for this long, so a quiet
	// window, a scene and the margin landing in the same minute produce one rollover, not three.
	rolloverMinInterval = 10 * time.Minute
	// rolloverMinQuietSession is how long a session must have run before a quiet window may roll it over. A
	// stream started at 03:10 inside a 03:00 window is not rolled over at 03:11.
	rolloverMinQuietSession = time.Hour
	// MinRolloverMarginMinutes and MaxRolloverMarginMinutes bound the safety margin. Under five minutes is not
	// enough time to create and start a replacement; over two hours rolls over a stream that is not near its cap.
	MinRolloverMarginMinutes = 5
	MaxRolloverMarginMinutes = 120
)

// RolloverInput is everything the planner may look at. Each field is the caller's reading of the platform
// or of the operator's settings at Now.
type RolloverInput struct {
	Now time.Time
	// Live is whether the platform reports the destination on air. Nothing is due for a destination that is not.
	Live bool
	// Limit is the cap the margin counts to. A zero Limit arms no margin trigger.
	Limit PlatformLimit
	// SessionStartedAt is the platform's live start for the broadcast being counted. Zero means unknown, which
	// arms no margin trigger and no quiet trigger.
	SessionStartedAt time.Time
	MarginMinutes    int
	QuietStart       string
	QuietEnd         string
	QuietZone        string
	OBSScene         string
	// SceneSwitchedAt is when the configured scene last became active, live switches only. Zero means never.
	SceneSwitchedAt time.Time
	// LastRolloverAt is when a rollover last ran for this destination. Zero means never.
	LastRolloverAt time.Time
}

// RolloverDecision is the planner's answer. Due and Trigger say whether to act now. At is the moment the due
// trigger became due, which is the one the timeline shows. Next is the earliest future moment an enabled trigger
// would fire, for the countdown card's "planned for" line; zero when no trigger can fire.
type RolloverDecision struct {
	Due     bool
	Trigger RolloverTrigger
	At      time.Time
	Next    time.Time
	// Reason says why nothing is due, in words the card can show, so an operator asking "why has it not rolled
	// over" gets an answer from the planner rather than from a guess.
	Reason string
}

type candidate struct {
	trigger RolloverTrigger
	at      time.Time
}

// PlanRollover decides whether a rollover is due at in.Now.
func PlanRollover(in RolloverInput) RolloverDecision {
	if !in.Live {
		return RolloverDecision{Reason: "the destination is not live"}
	}
	if !in.LastRolloverAt.IsZero() && in.Now.Sub(in.LastRolloverAt) < rolloverMinInterval {
		return RolloverDecision{
			Reason: fmt.Sprintf("a rollover ran less than %s ago", rolloverMinInterval),
			Next:   in.LastRolloverAt.Add(rolloverMinInterval),
		}
	}

	cands := rolloverCandidates(in)
	dec := decideFromCandidates(cands, in.Now)
	if dec.Due {
		dec.Reason = ""
		return dec
	}
	if len(cands) == 0 {
		dec.Reason = "no trigger is configured: set a quiet window, an OBS scene or a safety margin against a stated limit"
	} else {
		dec.Reason = "no trigger has come due yet"
	}
	return dec
}

// rolloverCandidates lists every trigger that is configured for this input.
func rolloverCandidates(in RolloverInput) []candidate {
	var cands []candidate
	if c, ok := marginCandidate(in); ok {
		cands = append(cands, c)
	}
	if c, ok := quietCandidate(in); ok {
		cands = append(cands, c)
	}
	if c, ok := sceneCandidate(in); ok {
		cands = append(cands, c)
	}
	return cands
}

// decideFromCandidates picks the earliest due trigger, or else the next one to
// come due.
func decideFromCandidates(cands []candidate, now time.Time) RolloverDecision {
	var dec RolloverDecision
	for _, c := range cands {
		if !c.at.After(now) {
			// Among the triggers that are due, the earliest moment wins, so the timeline names the cue that
			// really came first.
			if !dec.Due || c.at.Before(dec.At) {
				dec = RolloverDecision{Due: true, Trigger: c.trigger, At: c.at}
			}
			continue
		}
		if dec.Next.IsZero() || c.at.Before(dec.Next) {
			dec.Next = c.at
		}
	}
	return dec
}

// marginCandidate is the safety margin: the stated cap less the operator's margin. It needs both a stated limit
// and a known session start, which is why an unverified platform never gets a margin trigger.
func marginCandidate(in RolloverInput) (candidate, bool) {
	cutoff, ok := in.Limit.CutoffFor(in.SessionStartedAt)
	if !ok {
		return candidate{}, false
	}
	return candidate{trigger: TriggerSafetyMargin, at: cutoff.Add(-time.Duration(in.MarginMinutes) * time.Minute)}, true
}

// quietCandidate is the daily quiet window. The window's opening, or the end of the minimum session length if that
// is later, is when it becomes due. A window already acted on, one that a rollover ran inside, is spent, and its
// next occurrence is what is planned instead.
func quietCandidate(in RolloverInput) (candidate, bool) {
	if in.QuietStart == "" || in.QuietEnd == "" || in.QuietZone == "" || in.SessionStartedAt.IsZero() {
		return candidate{}, false
	}
	loc, err := time.LoadLocation(in.QuietZone)
	if err != nil {
		return candidate{}, false
	}
	start, okS := parseClock(in.QuietStart)
	end, okE := parseClock(in.QuietEnd)
	if !okS || !okE || start == end {
		return candidate{}, false
	}
	opened, inside := windowOpening(in.Now.In(loc), start, end, loc)
	floor := in.SessionStartedAt.Add(rolloverMinQuietSession)
	if inside && !in.LastRolloverAt.Before(opened) {
		// Already rolled over in this window: spent, so plan the next opening.
		opened = nextWindowOpening(in.Now.In(loc), start, loc)
		return candidate{trigger: TriggerQuietWindow, at: laterOf(opened, floor)}, true
	}
	if !inside {
		opened = nextWindowOpening(in.Now.In(loc), start, loc)
	}
	return candidate{trigger: TriggerQuietWindow, at: laterOf(opened, floor)}, true
}

// sceneCandidate is the OBS trigger. It counts only a switch that happened during this session and after the
// last rollover, so a scene left over from before the stream began, or one already acted on, does nothing.
func sceneCandidate(in RolloverInput) (candidate, bool) {
	if in.OBSScene == "" || in.SceneSwitchedAt.IsZero() || in.SessionStartedAt.IsZero() {
		return candidate{}, false
	}
	if !in.SceneSwitchedAt.After(in.SessionStartedAt) || !in.SceneSwitchedAt.After(in.LastRolloverAt) {
		return candidate{}, false
	}
	return candidate{trigger: TriggerOBSScene, at: in.SceneSwitchedAt}, true
}

// windowOpening reports whether local time t is inside the window [start, end) in minutes past midnight, and
// the moment the window that contains t opened (or, when outside, the last opening before t).
func windowOpening(t time.Time, start, end int, loc *time.Location) (time.Time, bool) {
	m := t.Hour()*60 + t.Minute()
	y, mo, d := t.Date()
	if start < end {
		opened := time.Date(y, mo, d, start/60, start%60, 0, 0, loc)
		return opened, m >= start && m < end
	}
	// A window that wraps midnight: 23:00-01:00 is open from 23:00 until 01:00 the next day.
	if m >= start {
		return time.Date(y, mo, d, start/60, start%60, 0, 0, loc), true
	}
	yesterday := t.AddDate(0, 0, -1)
	y2, mo2, d2 := yesterday.Date()
	opened := time.Date(y2, mo2, d2, start/60, start%60, 0, 0, loc)
	return opened, m < end
}

// nextWindowOpening is the first opening of the window strictly after local time t.
func nextWindowOpening(t time.Time, start int, loc *time.Location) time.Time {
	y, mo, d := t.Date()
	today := time.Date(y, mo, d, start/60, start%60, 0, 0, loc)
	if today.After(t) {
		return today
	}
	return today.AddDate(0, 0, 1)
}

func laterOf(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// parseClock reads "HH:MM" in 24-hour time and returns minutes past midnight. The format is strict: the
// operator's window is a daily fact, and a loose parse would move it silently.
func parseClock(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// RolloverDebounceEnds is the moment the debounce after a rollover ends, given when that rollover ran. The manual run
// uses it too, so a click made just after a rollover is refused rather than rolling the new broadcast over again.
func RolloverDebounceEnds(last time.Time) time.Time {
	return last.Add(rolloverMinInterval)
}

// RolloverConfig is the operator's settings as the planner and the API accept them.
type RolloverConfig struct {
	MarginMinutes int
	QuietStart    string
	QuietEnd      string
	QuietZone     string
	OBSScene      string
}

// maxOBSSceneLength bounds a scene name the operator types. OBS allows longer names; this is a field a human
// types into a settings screen, and a bound keeps it out of every log and event line.
const maxOBSSceneLength = 200

// Validate refuses a configuration the planner could only act on by guessing. The errors name the field, so the
// settings screen can say what to change.
// validateQuietWindow checks the quiet window, if the operator set any of it.
func (c RolloverConfig) validateQuietWindow() error {
	anyWindow := c.QuietStart != "" || c.QuietEnd != "" || c.QuietZone != ""
	if !anyWindow {
		return nil
	}
	if c.QuietStart == "" || c.QuietEnd == "" || c.QuietZone == "" {
		return fmt.Errorf("a quiet window needs a start, an end and a time zone, or none of them")
	}
	start, okS := parseClock(c.QuietStart)
	end, okE := parseClock(c.QuietEnd)
	if !okS || !okE {
		return fmt.Errorf("the quiet window times must be HH:MM in 24-hour time")
	}
	if start == end {
		return fmt.Errorf("the quiet window cannot start and end at the same time")
	}
	if _, err := time.LoadLocation(c.QuietZone); err != nil {
		return fmt.Errorf("the quiet window time zone %q is not a known IANA zone", c.QuietZone)
	}
	return nil
}

func (c RolloverConfig) Validate() error {
	if c.MarginMinutes < MinRolloverMarginMinutes || c.MarginMinutes > MaxRolloverMarginMinutes {
		return fmt.Errorf("the safety margin must be between %d and %d minutes", MinRolloverMarginMinutes, MaxRolloverMarginMinutes)
	}
	if err := c.validateQuietWindow(); err != nil {
		return err
	}
	if len(c.OBSScene) > maxOBSSceneLength || strings.ContainsAny(c.OBSScene, "\x00\n\r\t") {
		return fmt.Errorf("the OBS scene name must be at most %d characters with no control characters", maxOBSSceneLength)
	}
	return nil
}
