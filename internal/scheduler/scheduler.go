// Package scheduler starts and stops destinations at a time the operator chose.
//
// It does not start or stop anything itself. A schedule flips the stored
// "enabled" intent through the same path the API uses and then asks for a
// reconcile, so the engine's existing loop does the real work and a scheduled
// start is indistinguishable from somebody clicking the switch. That is the
// whole design: there is exactly one way a destination comes up.
//
// Two rules about time, both learned the hard way by everything that has ever
// had a cron in it. Instants are stored in UTC and wall-clock times carry an
// explicit IANA zone, so a schedule does not silently move when the server's
// TZ does. And a window that was missed because the server was down does NOT
// fire when it comes back: an occurrence older than its grace period is marked
// as handled and skipped, because a stream that starts itself four hours late
// is worse than one that never started.
package scheduler

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Action is what a schedule does when it fires.
type Action string

const (
	// ActionStart enables its destinations.
	ActionStart Action = "start"
	// ActionStop disables them.
	ActionStop Action = "stop"
)

// Kind is how a schedule recurs.
type Kind string

const (
	// KindOnce fires at a single instant and never again.
	KindOnce Kind = "once"
	// KindDaily fires every day at a local wall-clock time.
	KindDaily Kind = "daily"
	// KindWeekly fires on chosen weekdays at a local wall-clock time.
	KindWeekly Kind = "weekly"
)

// Grace bounds. The default is generous enough to survive a restart and a slow
// boot, and short enough that nobody comes back to a stream that started itself
// in the middle of the night.
const (
	MinGraceSeconds     = 30
	MaxGraceSeconds     = 86400
	DefaultGraceSeconds = 300

	// MinutesPerDay is the exclusive upper bound on AtMinutes.
	MinutesPerDay = 24 * 60
	// MaxNameLen keeps a pasted mistake out of the database.
	MaxNameLen = 120
	// MaxTargets bounds one schedule's destination list.
	MaxTargets = 256
)

// Schedule is one stored rule about when destinations should be live.
type Schedule struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Action  Action `json:"action"`
	Kind    Kind   `json:"kind"`
	// DestinationIDs is what this schedule acts on. Empty means every
	// destination, which is what "start the show" usually means.
	DestinationIDs []int64 `json:"destinationIds"`
	// TZ is the IANA zone the wall-clock fields are read in. Empty means UTC —
	// explicit, not "whatever the server is set to", because the server's zone
	// is not a thing the operator chose.
	TZ string `json:"tz"`
	// AtMinutes is minutes past local midnight, for the recurring kinds.
	AtMinutes int `json:"atMinutes"`
	// Days are the weekdays a weekly schedule fires on.
	Days []time.Weekday `json:"days"`
	// RunAt is the instant a one-shot fires, stored and compared in UTC.
	RunAt time.Time `json:"runAt"`
	// GraceSeconds is how late an occurrence may still be acted on. Past it the
	// occurrence is marked handled and skipped.
	GraceSeconds int `json:"graceSeconds"`
	// LastRunAt is the newest occurrence already handled, fired or skipped. It
	// is what stops a restart from replaying the morning.
	LastRunAt time.Time `json:"lastRunAt"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Grace is the lateness allowance, defaulted and clamped.
func (s Schedule) Grace() time.Duration {
	g := s.GraceSeconds
	if g <= 0 {
		g = DefaultGraceSeconds
	}
	if g < MinGraceSeconds {
		g = MinGraceSeconds
	}
	if g > MaxGraceSeconds {
		g = MaxGraceSeconds
	}
	return time.Duration(g) * time.Second
}

// Location resolves TZ. An empty zone is UTC, never the server's local time:
// "the machine happens to be in Denver" is not a scheduling decision anybody
// made.
func (s Schedule) Location() (*time.Location, error) {
	if strings.TrimSpace(s.TZ) == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(s.TZ)
	if err != nil {
		return nil, fmt.Errorf("schedule %q has an unknown time zone %q", s.Name, s.TZ)
	}
	return loc, nil
}

// Enables reports the enabled value this schedule writes when it fires.
func (s Schedule) Enables() bool { return s.Action == ActionStart }

// Normalized fills the defaults and puts the collections in a stable order, so
// two schedules that mean the same thing store the same bytes.
func (s Schedule) Normalized() Schedule {
	s.Name = strings.TrimSpace(s.Name)
	s.TZ = strings.TrimSpace(s.TZ)
	if s.Kind == "" {
		s.Kind = KindOnce
	}
	if s.Action == "" {
		s.Action = ActionStart
	}
	if s.GraceSeconds <= 0 {
		s.GraceSeconds = DefaultGraceSeconds
	}
	if s.GraceSeconds < MinGraceSeconds {
		s.GraceSeconds = MinGraceSeconds
	}
	if s.GraceSeconds > MaxGraceSeconds {
		s.GraceSeconds = MaxGraceSeconds
	}
	if !s.RunAt.IsZero() {
		// Stored UTC, always. A one-shot that round-tripped through a zone
		// would be a different instant on the way back.
		s.RunAt = s.RunAt.UTC().Truncate(time.Second)
	}
	if !s.LastRunAt.IsZero() {
		s.LastRunAt = s.LastRunAt.UTC().Truncate(time.Second)
	}

	seenID := map[int64]bool{}
	ids := make([]int64, 0, len(s.DestinationIDs))
	for _, id := range s.DestinationIDs {
		if id <= 0 || seenID[id] {
			continue
		}
		seenID[id] = true
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	s.DestinationIDs = ids

	seenDay := map[time.Weekday]bool{}
	days := make([]time.Weekday, 0, len(s.Days))
	for _, d := range s.Days {
		if d < time.Sunday || d > time.Saturday || seenDay[d] {
			continue
		}
		seenDay[d] = true
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })
	s.Days = days
	return s
}

// Validate rejects a schedule that could not be evaluated.
func (s Schedule) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("schedule needs a name")
	}
	if len(s.Name) > MaxNameLen {
		return fmt.Errorf("schedule name is longer than %d characters", MaxNameLen)
	}
	switch s.Action {
	case ActionStart, ActionStop:
	default:
		return fmt.Errorf("schedule %q has an unknown action %q", s.Name, s.Action)
	}
	if len(s.DestinationIDs) > MaxTargets {
		return fmt.Errorf("schedule %q targets more than %d destinations", s.Name, MaxTargets)
	}
	if _, err := s.Location(); err != nil {
		return err
	}
	switch s.Kind {
	case KindOnce:
		if s.RunAt.IsZero() {
			return fmt.Errorf("schedule %q needs a date and time to run at", s.Name)
		}
	case KindDaily, KindWeekly:
		if s.AtMinutes < 0 || s.AtMinutes >= MinutesPerDay {
			return fmt.Errorf("schedule %q has a time of day outside 00:00-23:59", s.Name)
		}
		if s.Kind == KindWeekly && len(s.Days) == 0 {
			return fmt.Errorf("schedule %q needs at least one weekday", s.Name)
		}
	default:
		return fmt.Errorf("schedule %q has an unknown kind %q", s.Name, s.Kind)
	}
	if s.GraceSeconds != 0 && (s.GraceSeconds < MinGraceSeconds || s.GraceSeconds > MaxGraceSeconds) {
		return fmt.Errorf("schedule %q needs a grace between %d and %d seconds",
			s.Name, MinGraceSeconds, MaxGraceSeconds)
	}
	return nil
}

// LocalTime renders AtMinutes as HH:MM, for a UI that displays local while the
// database keeps UTC.
func (s Schedule) LocalTime() string {
	return fmt.Sprintf("%02d:%02d", s.AtMinutes/60, s.AtMinutes%60)
}
