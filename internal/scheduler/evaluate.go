package scheduler

import "time"

// Reasons a Decision carries. They are compared in tests and shown to the
// operator, so they read as sentences rather than as codes.
const (
	ReasonDisabled = "the schedule is switched off"
	ReasonInvalid  = "the schedule cannot be evaluated"
	ReasonPending  = "no occurrence has come round yet"
	ReasonHandled  = "the most recent occurrence has already been handled"
	ReasonStale    = "the occurrence was missed while the server was not running"
	ReasonDue      = "the occurrence is due"
)

// Decision is what to do about one schedule at one moment.
type Decision struct {
	// Fire means act on it now.
	Fire bool
	// Skip means an occurrence came and went unhandled and is now too old to
	// act on. It is recorded as handled anyway — that is the entire point, so
	// it cannot fire retroactively hours later.
	Skip bool
	// At is the occurrence in question, in UTC.
	At     time.Time
	Reason string
}

// Previous is the most recent occurrence at or before now, in UTC.
//
// Recurring kinds are evaluated in the schedule's own zone, so "07:00 daily"
// stays 07:00 across a daylight-saving boundary rather than drifting an hour.
// time.Date resolves the gap hour of a spring-forward to a real instant, which
// is the behaviour anybody would want: the schedule fires once, slightly late,
// rather than being skipped for the year.
func Previous(s Schedule, now time.Time) (time.Time, bool) {
	loc, err := s.Location()
	if err != nil {
		return time.Time{}, false
	}
	now = now.UTC()

	switch s.Kind {
	case KindOnce:
		if s.RunAt.IsZero() || s.RunAt.After(now) {
			return time.Time{}, false
		}
		return s.RunAt.UTC(), true

	case KindDaily:
		local := now.In(loc)
		for back := 0; back <= 1; back++ {
			d := local.AddDate(0, 0, -back)
			at := atMinutes(d, s.AtMinutes, loc)
			if !at.After(now) {
				return at.UTC(), true
			}
		}
		return time.Time{}, false

	case KindWeekly:
		if len(s.Days) == 0 {
			return time.Time{}, false
		}
		want := map[time.Weekday]bool{}
		for _, d := range s.Days {
			want[d] = true
		}
		local := now.In(loc)
		// Eight days back, not seven: the local weekday of an occurrence is
		// read from the candidate day itself, and a zone change across the
		// boundary can otherwise put the only match one day out of reach.
		for back := 0; back <= 8; back++ {
			d := local.AddDate(0, 0, -back)
			at := atMinutes(d, s.AtMinutes, loc)
			if !want[at.In(loc).Weekday()] || at.After(now) {
				continue
			}
			return at.UTC(), true
		}
		return time.Time{}, false
	}
	return time.Time{}, false
}

// Next is the first occurrence strictly after now, for a UI that wants to say
// when something will happen. It returns false for a one-shot that has been and
// gone.
func Next(s Schedule, now time.Time) (time.Time, bool) {
	loc, err := s.Location()
	if err != nil {
		return time.Time{}, false
	}
	now = now.UTC()

	switch s.Kind {
	case KindOnce:
		if s.RunAt.IsZero() || !s.RunAt.After(now) {
			return time.Time{}, false
		}
		return s.RunAt.UTC(), true

	case KindDaily:
		local := now.In(loc)
		for fwd := 0; fwd <= 1; fwd++ {
			at := atMinutes(local.AddDate(0, 0, fwd), s.AtMinutes, loc)
			if at.After(now) {
				return at.UTC(), true
			}
		}
		return time.Time{}, false

	case KindWeekly:
		if len(s.Days) == 0 {
			return time.Time{}, false
		}
		want := map[time.Weekday]bool{}
		for _, d := range s.Days {
			want[d] = true
		}
		local := now.In(loc)
		for fwd := 0; fwd <= 8; fwd++ {
			at := atMinutes(local.AddDate(0, 0, fwd), s.AtMinutes, loc)
			if !want[at.In(loc).Weekday()] || !at.After(now) {
				continue
			}
			return at.UTC(), true
		}
		return time.Time{}, false
	}
	return time.Time{}, false
}

// atMinutes builds the instant for a local day at minutes past midnight.
func atMinutes(day time.Time, minutes int, loc *time.Location) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), minutes/60, minutes%60, 0, 0, loc)
}

// Evaluate decides what to do about one schedule right now.
//
// The missed-window rule lives here and nowhere else: an occurrence older than
// the grace period is returned as a Skip, which the runner records as handled
// without acting on it. A server that was off for four hours therefore comes
// back and does nothing, rather than starting a stream nobody is watching.
func Evaluate(s Schedule, now time.Time) Decision {
	if !s.Enabled {
		return Decision{Reason: ReasonDisabled}
	}
	if err := s.Validate(); err != nil {
		return Decision{Reason: ReasonInvalid}
	}
	at, ok := Previous(s, now)
	if !ok {
		return Decision{Reason: ReasonPending}
	}
	if !s.LastRunAt.IsZero() && !at.After(s.LastRunAt.UTC()) {
		return Decision{At: at, Reason: ReasonHandled}
	}
	if now.UTC().Sub(at) > s.Grace() {
		return Decision{Skip: true, At: at, Reason: ReasonStale}
	}
	return Decision{Fire: true, At: at, Reason: ReasonDue}
}
