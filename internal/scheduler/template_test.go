package scheduler

import (
	"strings"
	"testing"
	"time"
)

// A weekly Tuesday show at 19:00 New York, the shape a recurring broadcast
// template exists for. Built through Normalized so the tests read the same
// values the production path stores.
func weeklyTuesday(mut ...func(*Schedule)) Schedule {
	s := Schedule{
		ID: 7, Name: "tuesday show", Enabled: true,
		Action: ActionStart, Kind: KindWeekly,
		Days:      []time.Weekday{time.Tuesday},
		AtMinutes: 19 * 60, TZ: "America/New_York", GraceSeconds: 300,
		AllDestinations: true,
	}
	for _, m := range mut {
		m(&s)
	}
	return s.Normalized()
}

// The title pattern is the one place an operator's text becomes a public
// YouTube title, so its {date} and {time} tokens must render in the SHOW's zone,
// not in UTC and not in the server's zone. A 23:30 New York show is the 4th of
// the next month in UTC, and a title that said the 3rd would be wrong to every
// viewer.
func TestRenderTitleSubstitutesDateAndTimeInTheSchedulesZone(t *testing.T) {
	ny := mustLoad(t, "America/New_York")
	// 2026-11-03 23:30 New York is 2026-11-04 04:30 UTC.
	at := time.Date(2026, 11, 3, 23, 30, 0, 0, ny)

	got, err := RenderTitle("Late night {date} at {time}", at, ny)
	if err != nil {
		t.Fatalf("RenderTitle returned an error for known tokens: %v", err)
	}
	if want := "Late night 2026-11-03 at 23:30"; got != want {
		t.Fatalf("RenderTitle = %q, want %q: {date} and {time} must read in the show's zone", got, want)
	}
}

// A title is what a viewer sees on the event page, and YouTube refuses one
// past 100 characters. Validate must catch it before a broadcast is created
// with it, not after the platform has refused the write and left the event
// page half-built.
func TestTemplateValidateRefusesATitleThatWouldBeTooLongOnceRendered(t *testing.T) {
	long := strings.Repeat("x", 100-len("2026-11-03")+1) + "{date}"
	tpl := BroadcastTemplate{ScheduleID: 7, TitlePattern: long}.Normalized()
	if err := tpl.Validate(); err == nil {
		t.Fatalf("Validate accepted a title pattern whose rendered title is over 100 characters")
	}
}

// An unknown token is a typo, and a typo that reached YouTube would publish
// the literal brace text to viewers. Refused by name so the operator can fix it.
func TestTemplateValidateRefusesAnUnknownTokenByName(t *testing.T) {
	tpl := BroadcastTemplate{ScheduleID: 7, TitlePattern: "Show {dte}"}.Normalized()
	err := tpl.Validate()
	if err == nil {
		t.Fatalf("Validate accepted the unknown token {dte}")
	}
	if !strings.Contains(err.Error(), "{dte}") {
		t.Fatalf("the refusal %q does not name the token it refused", err)
	}
}

// Privacy is a three-value platform enum. An unknown value is refused, and an
// empty one defaults to public, because a broadcast left private announces the
// show to nobody and the operator asked for an announcement.
func TestTemplateNormalizedDefaultsPrivacyAndLookaheadAndValidatesThem(t *testing.T) {
	tpl := BroadcastTemplate{ScheduleID: 7, TitlePattern: "Show {date}"}.Normalized()
	if tpl.Privacy != "public" {
		t.Fatalf("empty privacy normalised to %q, want public", tpl.Privacy)
	}
	if tpl.Lookahead != DefaultLookahead {
		t.Fatalf("zero lookahead normalised to %d, want the default %d", tpl.Lookahead, DefaultLookahead)
	}
	if err := tpl.Validate(); err != nil {
		t.Fatalf("a plain template did not validate: %v", err)
	}

	bad := tpl
	bad.Privacy = "friends"
	if err := bad.Validate(); err == nil {
		t.Fatalf("Validate accepted privacy %q, which is not a YouTube privacy status", bad.Privacy)
	}

	over := tpl
	over.Lookahead = MaxLookahead + 1
	if err := over.Validate(); err == nil {
		t.Fatalf("Validate accepted a lookahead of %d, over the bound %d", over.Lookahead, MaxLookahead)
	}
}

// The look-ahead is the feature's promise: the next N occurrences exist before
// the show, not just the next one. Each must be strictly after the one before
// it, in the schedule's own zone, and the count must be exactly N.
func TestOccurrencesReturnsTheNextNStrictlyIncreasingOccurrences(t *testing.T) {
	ny := mustLoad(t, "America/New_York")
	s := weeklyTuesday()
	// Monday 2026-11-02, so the next Tuesday is the 3rd.
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, ny)

	got := Occurrences(s, now, 3)
	if len(got) != 3 {
		t.Fatalf("Occurrences returned %d instants, want 3", len(got))
	}
	want := []time.Time{
		time.Date(2026, 11, 3, 19, 0, 0, 0, ny),
		time.Date(2026, 11, 10, 19, 0, 0, 0, ny),
		time.Date(2026, 11, 17, 19, 0, 0, 0, ny),
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("occurrence %d = %s, want %s", i, got[i], want[i])
		}
	}
}

// A one-shot has exactly one occurrence, however large N is. Asking it for
// more must not invent a recurrence the operator never scheduled.
func TestOccurrencesOfAOneShotIsAtMostOneInstant(t *testing.T) {
	at := time.Date(2026, 11, 3, 19, 0, 0, 0, time.UTC)
	s := Schedule{ID: 9, Name: "once", Enabled: true, Action: ActionStart, Kind: KindOnce,
		RunAt: at, AllDestinations: true}.Normalized()

	got := Occurrences(s, at.Add(-time.Hour), 5)
	if len(got) != 1 || !got[0].Equal(at) {
		t.Fatalf("Occurrences of a one-shot = %v, want exactly [%s]", got, at)
	}
	if after := Occurrences(s, at, 5); len(after) != 0 {
		t.Fatalf("a one-shot already past returned %v, want nothing", after)
	}
}

// A disabled-or-unevaluable schedule plans nothing: the planner must not pre-
// create events for a show whose schedule is switched off or whose zone is
// unknown, because an event for a show that will not run is a public page for
// nothing.
func TestOccurrencesPlansNothingForAScheduleItCannotEvaluate(t *testing.T) {
	s := weeklyTuesday(func(s *Schedule) { s.TZ = "Mars/Olympus" })
	if got := Occurrences(s, time.Now(), 2); len(got) != 0 {
		t.Fatalf("Occurrences for an unknown zone returned %v, want none", got)
	}
}

// A template is shaped by the same rule as every other schedule-derived value:
// an empty pattern is refused, because it would publish a broadcast with no
// name, and the platform refuses that anyway.
func TestTemplateValidateNeedsATitlePattern(t *testing.T) {
	tpl := BroadcastTemplate{ScheduleID: 7, TitlePattern: "   "}.Normalized()
	if err := tpl.Validate(); err == nil {
		t.Fatalf("Validate accepted a blank title pattern")
	}
}

func TestTemplateLimitsAreYouTubes(t *testing.T) {
	if MaxTitleLen != 100 || MaxDescriptionLen != 5000 {
		t.Errorf("limits = %d/%d, want 100/5000", MaxTitleLen, MaxDescriptionLen)
	}
	ok := BroadcastTemplate{ScheduleID: 7, TitlePattern: "x", Description: strings.Repeat("d", 5000)}.Normalized()
	if err := ok.Validate(); err != nil {
		t.Errorf("5000-byte description refused: %v", err)
	}
	bad := BroadcastTemplate{ScheduleID: 7, TitlePattern: "x", Description: strings.Repeat("d", 5001)}.Normalized()
	if bad.Validate() == nil {
		t.Error("5001-byte description accepted")
	}
}

func TestTemplateTitleIsCountedInUTF16(t *testing.T) {
	ok := BroadcastTemplate{ScheduleID: 7, TitlePattern: strings.Repeat("😀", 50)}.Normalized()
	if err := ok.Validate(); err != nil {
		t.Errorf("50 emoji (100 UTF-16 units) refused: %v", err)
	}
	bad := BroadcastTemplate{ScheduleID: 7, TitlePattern: strings.Repeat("😀", 51)}.Normalized()
	if bad.Validate() == nil {
		t.Error("51 emoji (102 UTF-16 units) accepted")
	}
}
