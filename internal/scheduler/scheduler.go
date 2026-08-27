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
	// THE TIME ZONE DATABASE, COMPILED IN, AND IT BELONGS HERE.
	//
	// A schedule carries an IANA zone -- "Europe/London" -- and LoadLocation
	// resolves it against /usr/share/zoneinfo. The shipped image is Alpine with
	// ffmpeg and nothing else: that directory does not exist. Every zone but UTC
	// was refused at save time there, which is merely annoying. What is not
	// annoying is a schedule saved where the database DOES exist and then run
	// here: Previous() returns (zero, false) when Location() errors, which is
	// the identical answer it gives for "nothing is due". The broadcast that
	// should have gone on air at 19:00 does not, the runs page shows no
	// failure because there was no run, and nothing anywhere says why.
	//
	// In THIS package rather than in cmd/, so the guarantee travels with the
	// code that depends on it: any binary that links the scheduler gets the
	// database, and the test beside this is testing the real thing rather than
	// whatever the developer's laptop happens to have in /usr/share.
	_ "time/tzdata"

	"fmt"
	"slices"
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

	// ActionPlaylistStart enables the failover playlist. INSTALL-WIDE: settings
	// are global (GetSettings takes no source id), so this is not per-programme.
	//
	// It means "filler from now if nothing is live", NOT "filler now regardless".
	// The playlist ranks below both ingests and a live encoder pre-empts it
	// immediately, deliberately. Forcing filler over a live encoder needs a pin,
	// and a pin is in-memory only, so it cannot be scheduled without breaking
	// this package's invariant.
	ActionPlaylistStart Action = "playlist.start"
	// ActionPlaylistStop disables it.
	ActionPlaylistStop Action = "playlist.stop"
)

// AllActions is every action a schedule may carry, in the order the operator's
// dropdown offers them.
//
// Validate ranges over this rather than repeating the four names in a switch,
// which makes it the single place an action becomes real. That matters because
// of what the alternative cost: the test that used to guard this list read
// scheduler.go with a regexp and compared what it found against a list written
// out again in the test file. It could not see an action declared in a sibling
// file, it could not see one written `Action("pause")`, and it never executed
// Validate at all -- so it would have passed on a fifth action that Validate
// rejected, and passed again on one Validate accepted but nothing implemented.
//
// A variable the production path consumes has none of those holes: a test that
// ranges over AllActions and calls Validate is asking the real question, and
// an action added to the const block but not to this slice fails Validate the
// first time anybody uses it rather than silently taking the destination path.
var AllActions = []Action{
	ActionStart,
	ActionStop,
	ActionPlaylistStart,
	ActionPlaylistStop,
}

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

// TargetsPlaylist reports whether this schedule acts on the playlist rather
// than on destinations.
//
// A predicate rather than a comparison at each call site, because Enables()
// answers Action == ActionStart and the destination path reads it: route
// playlist.stop by that boolean and it disables every destination. One place
// decides which half of the runner a schedule belongs to.
func (s Schedule) TargetsPlaylist() bool {
	return s.Action == ActionPlaylistStart || s.Action == ActionPlaylistStop
}

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
	if !slices.Contains(AllActions, s.Action) {
		return fmt.Errorf("schedule %q has an unknown action %q", s.Name, s.Action)
	}
	if len(s.DestinationIDs) > MaxTargets {
		return fmt.Errorf("schedule %q targets more than %d destinations", s.Name, MaxTargets)
	}
	if s.TargetsPlaylist() && len(s.DestinationIDs) > 0 {
		return fmt.Errorf("schedule %q acts on the playlist, so it cannot also name "+
			"%d destination(s); use a second schedule for those", s.Name, len(s.DestinationIDs))
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
