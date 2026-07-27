package scheduler

import (
	"strings"
	"testing"
	"time"
)

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("zone %s is unavailable on this host: %v", name, err)
	}
	return loc
}

func daily(mut ...func(*Schedule)) Schedule {
	s := Schedule{
		ID: 1, Name: "evening show", Enabled: true,
		Action: ActionStart, Kind: KindDaily,
		AtMinutes: 19 * 60, TZ: "America/New_York", GraceSeconds: 300,
	}
	for _, m := range mut {
		m(&s)
	}
	return s.Normalized()
}

// The rule the whole feature turns on: a window missed while the server was
// down must NOT fire when it comes back.
func TestEvaluateDoesNotFireAMissedWindowRetroactively(t *testing.T) {
	ny := mustLoad(t, "America/New_York")
	occurrence := time.Date(2026, 7, 27, 19, 0, 0, 0, ny).UTC()

	tests := []struct {
		name     string
		now      time.Time
		last     time.Time
		wantFire bool
		wantSkip bool
		wantAt   time.Time
	}{
		{
			name:     "on time",
			now:      occurrence.Add(10 * time.Second),
			wantFire: true,
			wantAt:   occurrence,
		},
		{
			name:     "late but inside the grace period",
			now:      occurrence.Add(4 * time.Minute),
			wantFire: true,
			wantAt:   occurrence,
		},
		{
			name:     "one second past the grace period",
			now:      occurrence.Add(5*time.Minute + time.Second),
			wantSkip: true,
			wantAt:   occurrence,
		},
		{
			name:     "four hours later, which is the server-was-down case",
			now:      occurrence.Add(4 * time.Hour),
			wantSkip: true,
			wantAt:   occurrence,
		},
		{
			name:   "already handled",
			now:    occurrence.Add(time.Minute),
			last:   occurrence,
			wantAt: occurrence,
		},
		{
			name: "before the first occurrence of the day",
			now:  time.Date(2026, 7, 27, 6, 0, 0, 0, ny).UTC(),
			// The previous day's 19:00 is the most recent occurrence, and it is
			// far outside the grace period.
			wantSkip: true,
			wantAt:   time.Date(2026, 7, 26, 19, 0, 0, 0, ny).UTC(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := daily(func(s *Schedule) { s.LastRunAt = tt.last })
			got := Evaluate(s, tt.now)
			if got.Fire != tt.wantFire {
				t.Errorf("Fire = %v, want %v (reason %q)", got.Fire, tt.wantFire, got.Reason)
			}
			if got.Skip != tt.wantSkip {
				t.Errorf("Skip = %v, want %v (reason %q)", got.Skip, tt.wantSkip, got.Reason)
			}
			if !tt.wantAt.IsZero() && !got.At.Equal(tt.wantAt) {
				t.Errorf("At = %s, want %s", got.At, tt.wantAt)
			}
		})
	}
}

func TestEvaluateRefusesWhatItCannotEvaluate(t *testing.T) {
	tests := []struct {
		name       string
		schedule   Schedule
		wantReason string
	}{
		{
			name:       "switched off",
			schedule:   daily(func(s *Schedule) { s.Enabled = false }),
			wantReason: ReasonDisabled,
		},
		{
			name:       "an unknown zone is not guessed at",
			schedule:   daily(func(s *Schedule) { s.TZ = "Mars/Olympus_Mons" }),
			wantReason: ReasonInvalid,
		},
		{
			name:       "a weekly schedule with no days",
			schedule:   daily(func(s *Schedule) { s.Kind = KindWeekly; s.Days = nil }),
			wantReason: ReasonInvalid,
		},
		{
			name:       "a one-shot with no instant",
			schedule:   daily(func(s *Schedule) { s.Kind = KindOnce }),
			wantReason: ReasonInvalid,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(tt.schedule, time.Date(2026, 7, 27, 20, 0, 0, 0, time.UTC))
			if got.Fire || got.Skip {
				t.Errorf("Evaluate acted on an unevaluable schedule: %+v", got)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
			}
		})
	}
}

func TestOneShotFiresExactlyOnce(t *testing.T) {
	at := time.Date(2026, 7, 27, 14, 30, 0, 0, time.UTC)
	s := Schedule{
		ID: 2, Name: "the broadcast", Enabled: true, Action: ActionStart,
		Kind: KindOnce, RunAt: at, GraceSeconds: 300,
	}.Normalized()

	before := Evaluate(s, at.Add(-time.Minute))
	if before.Fire || before.Reason != ReasonPending {
		t.Errorf("before the instant = %+v, want pending", before)
	}
	on := Evaluate(s, at.Add(30*time.Second))
	if !on.Fire {
		t.Fatalf("at the instant = %+v, want a fire", on)
	}
	s.LastRunAt = on.At
	after := Evaluate(s, at.Add(2*time.Minute))
	if after.Fire || after.Reason != ReasonHandled {
		t.Errorf("after firing = %+v, want handled", after)
	}
}

func TestPreviousAndNextRespectTheStoredZoneNotTheServers(t *testing.T) {
	tokyo := mustLoad(t, "Asia/Tokyo")
	s := daily(func(s *Schedule) { s.TZ = "Asia/Tokyo"; s.AtMinutes = 9 * 60 })

	// 2026-07-27 09:00 Tokyo is 2026-07-27 00:00 UTC.
	want := time.Date(2026, 7, 27, 9, 0, 0, 0, tokyo).UTC()
	now := want.Add(time.Hour)

	prev, ok := Previous(s, now)
	if !ok || !prev.Equal(want) {
		t.Errorf("Previous = %s (%v), want %s", prev, ok, want)
	}
	next, ok := Next(s, now)
	if !ok || !next.Equal(want.Add(24*time.Hour)) {
		t.Errorf("Next = %s (%v), want %s", next, ok, want.Add(24*time.Hour))
	}
}

func TestDailyScheduleHoldsItsWallClockAcrossADaylightSavingChange(t *testing.T) {
	ny := mustLoad(t, "America/New_York")
	s := daily(func(s *Schedule) { s.AtMinutes = 19 * 60 })

	// The US autumn change is 2026-11-01: 19:00 local is UTC-4 the day before
	// and UTC-5 the day after, so the UTC instant moves while the wall clock
	// does not. That is the point of storing the zone.
	beforeChange := time.Date(2026, 10, 31, 19, 30, 0, 0, ny).UTC()
	afterChange := time.Date(2026, 11, 2, 19, 30, 0, 0, ny).UTC()

	p1, _ := Previous(s, beforeChange)
	p2, _ := Previous(s, afterChange)
	for _, p := range []time.Time{p1, p2} {
		if h, m := p.In(ny).Hour(), p.In(ny).Minute(); h != 19 || m != 0 {
			t.Errorf("occurrence %s is %02d:%02d local, want 19:00", p, h, m)
		}
	}
	if p1.In(ny).UTC().Hour() == p2.In(ny).UTC().Hour() {
		t.Errorf("the UTC hour did not move across the change (%s, %s); the test is not "+
			"exercising what it claims", p1, p2)
	}
}

func TestWeeklyOnlyFiresOnItsDays(t *testing.T) {
	utc := time.UTC
	s := Schedule{
		ID: 3, Name: "weekend show", Enabled: true, Action: ActionStart, Kind: KindWeekly,
		AtMinutes: 10 * 60, Days: []time.Weekday{time.Saturday, time.Sunday},
		GraceSeconds: 300,
	}.Normalized()

	tests := []struct {
		name     string
		now      time.Time
		wantFire bool
		wantDay  time.Weekday
	}{
		{
			name:     "Saturday at the hour",
			now:      time.Date(2026, 8, 1, 10, 1, 0, 0, utc),
			wantFire: true,
			wantDay:  time.Saturday,
		},
		{
			name:     "Sunday at the hour",
			now:      time.Date(2026, 8, 2, 10, 1, 0, 0, utc),
			wantFire: true,
			wantDay:  time.Sunday,
		},
		{
			// The most recent occurrence is Sunday, days ago, so it is skipped
			// rather than fired.
			name: "Wednesday",
			now:  time.Date(2026, 8, 5, 10, 1, 0, 0, utc),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(s, tt.now)
			if got.Fire != tt.wantFire {
				t.Fatalf("Fire = %v, want %v (at %s, reason %q)", got.Fire, tt.wantFire, got.At, got.Reason)
			}
			if tt.wantFire && got.At.Weekday() != tt.wantDay {
				t.Errorf("occurrence fell on %s, want %s", got.At.Weekday(), tt.wantDay)
			}
		})
	}
}

func TestScheduleValidateAndNormalize(t *testing.T) {
	tests := []struct {
		name    string
		in      Schedule
		wantErr string
	}{
		{name: "a complete daily schedule", in: daily()},
		{
			name:    "no name",
			in:      daily(func(s *Schedule) { s.Name = "" }),
			wantErr: "needs a name",
		},
		{
			name:    "an unknown action",
			in:      daily(func(s *Schedule) { s.Action = "pause" }),
			wantErr: "unknown action",
		},
		{
			name:    "an unknown kind",
			in:      daily(func(s *Schedule) { s.Kind = "fortnightly" }),
			wantErr: "unknown kind",
		},
		{
			name:    "a time of day off the clock",
			in:      daily(func(s *Schedule) { s.AtMinutes = 1441 }),
			wantErr: "outside 00:00-23:59",
		},
		{
			name:    "an unknown zone",
			in:      daily(func(s *Schedule) { s.TZ = "Middle/Earth" }),
			wantErr: "unknown time zone",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate = %v, want an error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestNormalizedSortsAndDeduplicatesAndStoresUTC(t *testing.T) {
	ny := mustLoad(t, "America/New_York")
	local := time.Date(2026, 7, 27, 19, 0, 0, 0, ny)

	got := Schedule{
		Name: " show ", Kind: KindWeekly, AtMinutes: 60,
		DestinationIDs: []int64{5, 2, 5, 0, -1, 9},
		Days:           []time.Weekday{time.Friday, time.Monday, time.Friday, time.Weekday(9)},
		RunAt:          local,
	}.Normalized()

	if got.Name != "show" {
		t.Errorf("Name = %q, want it trimmed", got.Name)
	}
	wantIDs := []int64{2, 5, 9}
	if len(got.DestinationIDs) != len(wantIDs) {
		t.Fatalf("DestinationIDs = %v, want %v", got.DestinationIDs, wantIDs)
	}
	for i, w := range wantIDs {
		if got.DestinationIDs[i] != w {
			t.Errorf("DestinationIDs[%d] = %d, want %d", i, got.DestinationIDs[i], w)
		}
	}
	wantDays := []time.Weekday{time.Monday, time.Friday}
	if len(got.Days) != len(wantDays) {
		t.Fatalf("Days = %v, want %v", got.Days, wantDays)
	}
	if got.RunAt.Location() != time.UTC {
		t.Errorf("RunAt is in %s, want it stored in UTC", got.RunAt.Location())
	}
	if !got.RunAt.Equal(local) {
		t.Errorf("RunAt = %s, want the same instant as %s", got.RunAt, local)
	}
	if got.Action != ActionStart || got.GraceSeconds != DefaultGraceSeconds {
		t.Errorf("defaults were not filled: action %q grace %d", got.Action, got.GraceSeconds)
	}
}

func TestGraceIsClampedRatherThanTrusted(t *testing.T) {
	tests := []struct {
		in   int
		want time.Duration
	}{
		{in: 0, want: DefaultGraceSeconds * time.Second},
		{in: -5, want: DefaultGraceSeconds * time.Second},
		{in: 1, want: MinGraceSeconds * time.Second},
		{in: 600, want: 600 * time.Second},
		{in: 1 << 30, want: MaxGraceSeconds * time.Second},
	}
	for _, tt := range tests {
		if got := (Schedule{GraceSeconds: tt.in}).Grace(); got != tt.want {
			t.Errorf("Grace(%d) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestLocalTimeRendersTheStoredMinute(t *testing.T) {
	tests := []struct {
		minutes int
		want    string
	}{
		{minutes: 0, want: "00:00"},
		{minutes: 9 * 60, want: "09:00"},
		{minutes: 19*60 + 30, want: "19:30"},
		{minutes: 23*60 + 59, want: "23:59"},
	}
	for _, tt := range tests {
		if got := (Schedule{AtMinutes: tt.minutes}).LocalTime(); got != tt.want {
			t.Errorf("LocalTime(%d) = %q, want %q", tt.minutes, got, tt.want)
		}
	}
}
