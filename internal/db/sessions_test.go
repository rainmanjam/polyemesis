package db

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// base is a fixed instant so a failing test reads the same on every machine.
var base = time.Unix(1_700_000_000, 0)

// rec builds a Recording without touching the database, for the pure
// grouping tests.
func rec(id int64, startOffset time.Duration, dur time.Duration) Recording {
	return Recording{
		ID:         id,
		Filename:   "rec.mkv",
		StartedAt:  base.Add(startOffset),
		FinishedAt: base.Add(startOffset + dur),
		DurationMS: dur.Milliseconds(),
	}
}

func TestGroupRecordingsHeuristic(t *testing.T) {
	hour := time.Hour
	tests := []struct {
		name  string
		recs  []Recording
		rules SessionRules
		want  []int // group sizes, in time order
	}{
		{
			name: "a four hour broadcast in hour segments is one session",
			recs: []Recording{
				rec(1, 0, hour), rec(2, hour, hour), rec(3, 2*hour, hour), rec(4, 3*hour, hour),
			},
			want: []int{4},
		},
		{
			name: "two broadcasts a day apart are two sessions",
			recs: []Recording{
				rec(1, 0, hour), rec(2, hour, hour),
				rec(3, 24*hour, hour), rec(4, 25*hour, hour),
			},
			want: []int{2, 2},
		},
		{
			// The case the feature exists for: the encoder fell over and the
			// recorder came back forty seconds later. Same broadcast.
			name: "a short gap because the encoder dropped stays one session",
			recs: []Recording{
				rec(1, 0, hour),
				rec(2, hour+40*time.Second, hour),
				rec(3, 2*hour+40*time.Second, hour),
			},
			want: []int{3},
		},
		{
			name: "a gap longer than MaxGap splits",
			recs: []Recording{
				rec(1, 0, hour),
				rec(2, hour+20*time.Minute, hour),
			},
			want: []int{1, 1},
		},
		{
			// The case a start-to-start rule gets wrong: the operator drops
			// the segment length from an hour to ten minutes mid-show. Every
			// pair still chains end to start, so nothing splits.
			name: "a segment length change mid-session does not split it",
			recs: []Recording{
				rec(1, 0, hour),
				rec(2, hour, 10*time.Minute),
				rec(3, hour+10*time.Minute, 10*time.Minute),
				rec(4, hour+20*time.Minute, 10*time.Minute),
			},
			want: []int{4},
		},
		{
			// And the reverse: short segments growing into long ones.
			name: "a segment length increase mid-session does not split it",
			recs: []Recording{
				rec(1, 0, 10*time.Minute),
				rec(2, 10*time.Minute, 10*time.Minute),
				rec(3, 20*time.Minute, hour),
				rec(4, 80*time.Minute, hour),
			},
			want: []int{4},
		},
		{
			// An unmeasured recording has no end. Assuming a typical segment
			// keeps the chain intact; assuming zero would break it.
			name: "an unmeasured duration falls back to the segment hint",
			recs: []Recording{
				{ID: 1, StartedAt: base},
				{ID: 2, StartedAt: base.Add(hour), DurationMS: hour.Milliseconds()},
			},
			rules: SessionRules{MaxGap: 5 * time.Minute, SegmentHint: hour},
			want:  []int{2},
		},
		{
			name: "an unmeasured duration falls back to finished_at when it has one",
			recs: []Recording{
				{ID: 1, StartedAt: base, FinishedAt: base.Add(30 * time.Minute)},
				{ID: 2, StartedAt: base.Add(31 * time.Minute), DurationMS: hour.Milliseconds()},
			},
			want: []int{2},
		},
		{
			// Two writers overlapping, or a duration that over-measured. Not a
			// reason to call them different broadcasts.
			name: "overlapping recordings are one session",
			recs: []Recording{
				rec(1, 0, 2*hour),
				rec(2, hour, hour),
			},
			want: []int{2},
		},
		{
			name: "input order does not matter",
			recs: []Recording{
				rec(3, 2*hour, hour), rec(1, 0, hour), rec(2, hour, hour),
			},
			want: []int{3},
		},
		{
			name: "a single recording is a session of one",
			recs: []Recording{rec(1, 0, hour)},
			want: []int{1},
		},
		{
			name: "no recordings, no sessions",
			recs: nil,
			want: nil,
		},
		{
			// A recorder left running for a week is not one broadcast, however
			// perfectly its segments chain.
			name: "MaxSpan caps a session that never stops",
			recs: []Recording{
				rec(1, 0, hour), rec(2, hour, hour), rec(3, 2*hour, hour), rec(4, 3*hour, hour),
			},
			rules: SessionRules{MaxGap: 5 * time.Minute, SegmentHint: hour, MaxSpan: 2 * hour},
			want:  []int{3, 1},
		},
		{
			name: "MaxSpan of zero means no cap",
			recs: []Recording{
				rec(1, 0, hour), rec(2, hour, hour), rec(3, 2*hour, hour),
			},
			rules: SessionRules{MaxGap: 5 * time.Minute, SegmentHint: hour},
			want:  []int{3},
		},
		{
			name: "simultaneous starts group together and sort by id",
			recs: []Recording{
				rec(2, 0, hour), rec(1, 0, hour),
			},
			want: []int{2},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rules := tc.rules
			if rules == (SessionRules{}) {
				rules = DefaultSessionRules()
			}
			got := GroupRecordings(tc.recs, rules)
			if len(got) != len(tc.want) {
				t.Fatalf("groups = %d, want %d (%v)", len(got), len(tc.want), groupSizes(got))
			}
			for i, want := range tc.want {
				if len(got[i]) != want {
					t.Errorf("group %d size = %d, want %d (%v)", i, len(got[i]), want, groupSizes(got))
				}
			}
			// Whatever the grouping, every recording lands in exactly one.
			total := 0
			for _, g := range got {
				total += len(g)
			}
			if total != len(tc.recs) {
				t.Errorf("grouping lost or duplicated recordings: %d of %d", total, len(tc.recs))
			}
		})
	}
}

func groupSizes(gs [][]Recording) []int {
	out := make([]int, len(gs))
	for i, g := range gs {
		out[i] = len(g)
	}
	return out
}

func TestGroupRecordingsDoesNotMutateItsInput(t *testing.T) {
	in := []Recording{rec(3, 2*time.Hour, time.Hour), rec(1, 0, time.Hour), rec(2, time.Hour, time.Hour)}
	GroupRecordings(in, DefaultSessionRules())
	if in[0].ID != 3 || in[1].ID != 1 || in[2].ID != 2 {
		t.Errorf("caller's slice was reordered: %d %d %d", in[0].ID, in[1].ID, in[2].ID)
	}
}

func TestSessionRulesZeroValueFallsBackToDefaults(t *testing.T) {
	// A zero MaxGap would split every broadcast into single recordings, which
	// is the restrictive failure this repo has learned to avoid.
	got := GroupRecordings([]Recording{
		rec(1, 0, time.Hour), rec(2, time.Hour, time.Hour),
	}, SessionRules{})
	if len(got) != 1 {
		t.Errorf("zero rules produced %d groups, want 1", len(got))
	}
}

func TestBackfillGroupsExistingHistory(t *testing.T) {
	d := testDB(t)
	hour := time.Hour
	// Four segments of one show, then a second show the next day.
	for i, off := range []time.Duration{0, hour, 2 * hour, 3 * hour, 24 * hour, 25 * hour} {
		seedRecordingAt(t, d, "rec-"+itoa(i)+".mkv", base.Add(off), hour)
	}

	res, err := d.BackfillSessions(DefaultSessionRules())
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if res.Created != 2 || res.Assigned != 6 {
		t.Fatalf("result = %+v, want 2 created / 6 assigned", res)
	}
	sessions, err := d.ListSessions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(sessions))
	}
	// Newest first.
	if !sessions[0].StartedAt.After(sessions[1].StartedAt) {
		t.Errorf("sessions are not newest first")
	}
	if sessions[1].Recordings != 4 {
		t.Errorf("first show has %d recordings, want 4", sessions[1].Recordings)
	}
	if want := 4 * hour; sessions[1].EndedAt.Sub(sessions[1].StartedAt) != want {
		t.Errorf("span = %v, want %v", sessions[1].EndedAt.Sub(sessions[1].StartedAt), want)
	}
	if sessions[1].DurationMS != (4 * hour).Milliseconds() {
		t.Errorf("duration = %d", sessions[1].DurationMS)
	}
	if !sessions[1].Auto {
		t.Errorf("an inferred session should be marked automatic")
	}
}

func TestBackfillIsIdempotent(t *testing.T) {
	d := testDB(t)
	for i, off := range []time.Duration{0, time.Hour, 2 * time.Hour} {
		seedRecordingAt(t, d, "rec-"+itoa(i)+".mkv", base.Add(off), time.Hour)
	}
	first, err := d.BackfillSessions(DefaultSessionRules())
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	second, err := d.BackfillSessions(DefaultSessionRules())
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if second.Created != 0 || second.Assigned != 0 {
		t.Errorf("second pass changed things: %+v (first %+v)", second, first)
	}
	sessions, _ := d.ListSessions()
	if len(sessions) != 1 {
		t.Errorf("sessions = %d, want 1", len(sessions))
	}
}

func TestBackfillExtendsAnExistingSessionRatherThanCreatingASecond(t *testing.T) {
	d := testDB(t)
	a := seedRecordingAt(t, d, "rec-0.mkv", base, time.Hour)
	if _, err := d.BackfillSessions(DefaultSessionRules()); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	before, err := d.SessionForRecording(a)
	if err != nil {
		t.Fatalf("session: %v", err)
	}

	// The next hour of the same show arrives.
	b := seedRecordingAt(t, d, "rec-1.mkv", base.Add(time.Hour), time.Hour)
	res, err := d.BackfillSessions(DefaultSessionRules())
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if res.Created != 0 || res.Extended != 1 || res.Assigned != 1 {
		t.Fatalf("result = %+v, want 0 created / 1 extended / 1 assigned", res)
	}
	after, err := d.SessionForRecording(b)
	if err != nil {
		t.Fatalf("session for b: %v", err)
	}
	if after.ID != before.ID {
		t.Errorf("new recording joined session %d, want %d", after.ID, before.ID)
	}
	if after.Recordings != 2 {
		t.Errorf("session has %d recordings, want 2", after.Recordings)
	}
}

func TestBackfillNeverMergesTwoSessionsAHumanSplit(t *testing.T) {
	d := testDB(t)
	ids := make([]int64, 4)
	for i, off := range []time.Duration{0, time.Hour, 2 * time.Hour, 3 * time.Hour} {
		ids[i] = seedRecordingAt(t, d, "rec-"+itoa(i)+".mkv", base.Add(off), time.Hour)
	}
	// The user splits the run in two by hand.
	first, err := d.CreateSession(Metadata{Title: "Part one"}, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	second, err := d.CreateSession(Metadata{Title: "Part two"}, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := d.SetSessionRecordings(first.ID, ids[:2]); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := d.SetSessionRecordings(second.ID, ids[2:]); err != nil {
		t.Fatalf("set: %v", err)
	}

	res, err := d.BackfillSessions(DefaultSessionRules())
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if res.Created != 0 || res.Assigned != 0 {
		t.Errorf("backfill disturbed a hand-made split: %+v", res)
	}
	sessions, _ := d.ListSessions()
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want the 2 the user made", len(sessions))
	}
	for _, s := range sessions {
		if s.Recordings != 2 {
			t.Errorf("session %q has %d recordings, want 2", s.Title, s.Recordings)
		}
	}
}

func TestBackfillPlacesAnUngroupedRecordingBesideItsNeighbourWhenTheRunIsSplit(t *testing.T) {
	d := testDB(t)
	ids := make([]int64, 4)
	for i, off := range []time.Duration{0, time.Hour, 2 * time.Hour, 3 * time.Hour} {
		ids[i] = seedRecordingAt(t, d, "rec-"+itoa(i)+".mkv", base.Add(off), time.Hour)
	}
	first, _ := d.CreateSession(Metadata{Title: "Part one"}, false)
	second, _ := d.CreateSession(Metadata{Title: "Part two"}, false)
	if err := d.SetSessionRecordings(first.ID, ids[:1]); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := d.SetSessionRecordings(second.ID, ids[3:]); err != nil {
		t.Fatalf("set: %v", err)
	}

	if _, err := d.BackfillSessions(DefaultSessionRules()); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	// The two orphans in the middle join the side they follow, and no third
	// session appears.
	sessions, _ := d.ListSessions()
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(sessions))
	}
	for _, id := range ids[1:3] {
		s, err := d.SessionForRecording(id)
		if err != nil {
			t.Fatalf("orphan %d ungrouped: %v", id, err)
		}
		if s.ID != first.ID {
			t.Errorf("orphan %d joined %d, want %d", id, s.ID, first.ID)
		}
	}
}

func TestBackfillOnAnEmptyDatabase(t *testing.T) {
	d := testDB(t)
	res, err := d.BackfillSessions(DefaultSessionRules())
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if res.Created != 0 || res.Groups != 0 {
		t.Errorf("result = %+v, want zeroes", res)
	}
}

func TestAssignRecordingChainsOntoTheLiveSession(t *testing.T) {
	d := testDB(t)
	a := seedRecordingAt(t, d, "rec-0.mkv", base, time.Hour)
	first, err := d.AssignRecording(a, DefaultSessionRules())
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if first.Recordings != 1 {
		t.Fatalf("session has %d recordings", first.Recordings)
	}

	b := seedRecordingAt(t, d, "rec-1.mkv", base.Add(time.Hour), time.Hour)
	joined, err := d.AssignRecording(b, DefaultSessionRules())
	if err != nil {
		t.Fatalf("assign b: %v", err)
	}
	if joined.ID != first.ID {
		t.Errorf("b started session %d, want to join %d", joined.ID, first.ID)
	}
	if joined.Recordings != 2 {
		t.Errorf("session has %d recordings, want 2", joined.Recordings)
	}

	// A recording a day later opens a new one.
	c := seedRecordingAt(t, d, "rec-2.mkv", base.Add(25*time.Hour), time.Hour)
	fresh, err := d.AssignRecording(c, DefaultSessionRules())
	if err != nil {
		t.Fatalf("assign c: %v", err)
	}
	if fresh.ID == first.ID {
		t.Errorf("a recording a day later should not join the previous session")
	}

	// Re-assigning is a no-op, not a second session.
	again, err := d.AssignRecording(b, DefaultSessionRules())
	if err != nil {
		t.Fatalf("reassign: %v", err)
	}
	if again.ID != first.ID {
		t.Errorf("reassign moved the recording to %d", again.ID)
	}
}

func TestAssignRecordingRejectsAnUnknownRecording(t *testing.T) {
	d := testDB(t)
	if _, err := d.AssignRecording(999, DefaultSessionRules()); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestSessionMembershipEditing(t *testing.T) {
	d := testDB(t)
	ids := make([]int64, 3)
	for i, off := range []time.Duration{0, time.Hour, 2 * time.Hour} {
		ids[i] = seedRecordingAt(t, d, "rec-"+itoa(i)+".mkv", base.Add(off), time.Hour)
	}
	s, err := d.CreateSession(Metadata{Title: "The good stream"}, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := d.SetSessionRecordings(s.ID, []int64{ids[2], ids[0]}); err != nil {
		t.Fatalf("set: %v", err)
	}
	members, err := d.SessionRecordings(s.ID)
	if err != nil {
		t.Fatalf("members: %v", err)
	}
	if len(members) != 2 || members[0].ID != ids[0] || members[1].ID != ids[2] {
		t.Fatalf("members are not in broadcast order: %v", members)
	}

	if err := d.AddRecordingToSession(s.ID, ids[1]); err != nil {
		t.Fatalf("add: %v", err)
	}
	members, _ = d.SessionRecordings(s.ID)
	if len(members) != 3 || members[1].ID != ids[1] {
		t.Errorf("added recording was not slotted into time order: %v", members)
	}

	if err := d.RemoveRecordingFromSession(ids[1]); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := d.SessionForRecording(ids[1]); !errors.Is(err, ErrNotFound) {
		t.Errorf("removed recording still has a session")
	}
	if err := d.RemoveRecordingFromSession(ids[1]); !errors.Is(err, ErrNotFound) {
		t.Errorf("second remove = %v, want ErrNotFound", err)
	}
	got, _ := d.GetSession(s.ID)
	if got.Recordings != 2 {
		t.Errorf("span not recomputed after removal: %d recordings", got.Recordings)
	}
}

func TestMovingARecordingRecomputesBothSessions(t *testing.T) {
	d := testDB(t)
	a := seedRecordingAt(t, d, "rec-0.mkv", base, time.Hour)
	b := seedRecordingAt(t, d, "rec-1.mkv", base.Add(time.Hour), time.Hour)
	one, _ := d.CreateSession(Metadata{Title: "one"}, false)
	two, _ := d.CreateSession(Metadata{Title: "two"}, false)
	if err := d.SetSessionRecordings(one.ID, []int64{a, b}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := d.AddRecordingToSession(two.ID, b); err != nil {
		t.Fatalf("move: %v", err)
	}
	gotOne, _ := d.GetSession(one.ID)
	gotTwo, _ := d.GetSession(two.ID)
	if gotOne.Recordings != 1 {
		t.Errorf("source session = %d recordings, want 1", gotOne.Recordings)
	}
	if gotTwo.Recordings != 1 {
		t.Errorf("target session = %d recordings, want 1", gotTwo.Recordings)
	}
}

func TestARecordingBelongsToAtMostOneSession(t *testing.T) {
	d := testDB(t)
	a := seedRecordingAt(t, d, "rec-0.mkv", base, time.Hour)
	one, _ := d.CreateSession(Metadata{}, false)
	two, _ := d.CreateSession(Metadata{}, false)
	if err := d.AddRecordingToSession(one.ID, a); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := d.AddRecordingToSession(two.ID, a); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM session_recordings WHERE recording_id = ?`, a).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("membership rows = %d, want 1", n)
	}
	s, _ := d.SessionForRecording(a)
	if s.ID != two.ID {
		t.Errorf("recording is in session %d, want %d", s.ID, two.ID)
	}
}

func TestDeletingASessionKeepsItsRecordings(t *testing.T) {
	d := testDB(t)
	a := seedRecordingAt(t, d, "rec-0.mkv", base, time.Hour)
	s, _ := d.CreateSession(Metadata{}, false)
	if err := d.AddRecordingToSession(s.ID, a); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := d.DeleteSession(s.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := d.GetRecording(a); err != nil {
		t.Fatalf("deleting a label deleted the footage: %v", err)
	}
	ungrouped, err := d.UngroupedRecordings()
	if err != nil {
		t.Fatalf("ungrouped: %v", err)
	}
	if len(ungrouped) != 1 || ungrouped[0].ID != a {
		t.Errorf("recording did not become ungrouped: %v", ungrouped)
	}
	if err := d.DeleteSession(s.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete = %v, want ErrNotFound", err)
	}
}

func TestDeletingARecordingLeavesTheSessionConsistent(t *testing.T) {
	d := testDB(t)
	a := seedRecordingAt(t, d, "rec-0.mkv", base, time.Hour)
	b := seedRecordingAt(t, d, "rec-1.mkv", base.Add(time.Hour), time.Hour)
	s, _ := d.CreateSession(Metadata{}, false)
	if err := d.SetSessionRecordings(s.ID, []int64{a, b}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := d.DeleteRecording(b); err != nil {
		t.Fatalf("delete recording: %v", err)
	}
	// The membership row went with it through the cascade; the stored span is
	// stale until a recalc, which is why RecalcSession exists.
	if err := d.RecalcSession(s.ID); err != nil {
		t.Fatalf("recalc: %v", err)
	}
	got, _ := d.GetSession(s.ID)
	if got.Recordings != 1 {
		t.Errorf("recordings = %d, want 1", got.Recordings)
	}
	members, _ := d.SessionRecordings(s.ID)
	if len(members) != 1 || members[0].ID != a {
		t.Errorf("members = %v", members)
	}
}

func TestPruneEmptySessionsSparesManualOnes(t *testing.T) {
	d := testDB(t)
	auto, _ := d.CreateSession(Metadata{}, true)
	manual, _ := d.CreateSession(Metadata{Title: "planned"}, false)
	n, err := d.PruneEmptySessions()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned = %d, want 1", n)
	}
	if _, err := d.GetSession(auto.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty automatic session survived")
	}
	if _, err := d.GetSession(manual.ID); err != nil {
		t.Errorf("empty manual session was pruned: %v", err)
	}
}

func TestSessionMetadataIsEditable(t *testing.T) {
	d := testDB(t)
	s, err := d.CreateSession(Metadata{Title: "  Draft  ", Tags: []string{"live"}}, true)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if s.Title != "Draft" {
		t.Errorf("title = %q, want trimmed", s.Title)
	}
	if !s.Auto {
		t.Errorf("a session created by the grouper should be automatic")
	}

	got, err := d.UpdateSessionMeta(s.ID, Metadata{
		Title:       "The good stream",
		Description: "The one where the guest actually showed up",
		Tags:        []string{"Live", "live", " podcast ", ""},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Title != "The good stream" {
		t.Errorf("title = %q", got.Title)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "Live" || got.Tags[1] != "podcast" {
		t.Errorf("tags = %v, want [Live podcast]", got.Tags)
	}
	// Editing takes ownership, so the grouper stops adjusting it.
	if got.Auto {
		t.Errorf("an edited session should no longer be automatic")
	}
	if _, err := d.UpdateSessionMeta(999, Metadata{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of a missing session = %v, want ErrNotFound", err)
	}
}

func TestSessionDisplayTitleFallsBackToTheDate(t *testing.T) {
	tests := []struct {
		name string
		in   Session
		want string
	}{
		{name: "titled", in: Session{Title: "The good stream"}, want: "The good stream"},
		{name: "whitespace title", in: Session{Title: "   ", StartedAt: base}, want: "Session " + base.Format("2006-01-02 15:04")},
		{name: "untitled with a span", in: Session{StartedAt: base}, want: "Session " + base.Format("2006-01-02 15:04")},
		{name: "untitled with no span", in: Session{}, want: "Untitled session"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.DisplayTitle(); got != tc.want {
				t.Errorf("DisplayTitle() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSessionTagFiltering(t *testing.T) {
	d := testDB(t)
	a, _ := d.CreateSession(Metadata{Title: "a", Tags: []string{"Rock"}}, false)
	if _, err := d.CreateSession(Metadata{Title: "b", Tags: []string{"rockabilly"}}, false); err != nil {
		t.Fatalf("create: %v", err)
	}
	c, _ := d.CreateSession(Metadata{Title: "c", Tags: []string{"rock", "talk"}}, false)

	got, err := d.ListSessionsByTag("ROCK")
	if err != nil {
		t.Fatalf("by tag: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("hits = %d, want 2 (a substring match would find 3)", len(got))
	}
	ids := map[int64]bool{got[0].ID: true, got[1].ID: true}
	if !ids[a.ID] || !ids[c.ID] {
		t.Errorf("wrong sessions matched: %v", ids)
	}
	if empty, _ := d.ListSessionsByTag("  "); len(empty) != 0 {
		t.Errorf("blank tag matched %d sessions", len(empty))
	}
	tags, err := d.SessionTags()
	if err != nil {
		t.Fatalf("tags: %v", err)
	}
	if len(tags) != 3 {
		t.Errorf("tags = %v, want 3 distinct", tags)
	}
}

func TestNormalizeTags(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "nil", in: nil, want: []string{}},
		{name: "trimmed", in: []string{"  live  "}, want: []string{"live"}},
		{name: "empties dropped", in: []string{"", "   ", "live"}, want: []string{"live"}},
		{name: "case insensitive dedupe keeps first casing", in: []string{"Live", "live", "LIVE"}, want: []string{"Live"}},
		{name: "order preserved", in: []string{"z", "a", "m"}, want: []string{"z", "a", "m"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeTags(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got %v, want %v", got, tc.want)
					break
				}
			}
		})
	}
}

func TestRecordingMetadataIsEditable(t *testing.T) {
	d := testDB(t)
	a := seedRecordingAt(t, d, "rec-0.mkv", base, time.Hour)

	// No metadata is the zero value, not an error: most recordings never get
	// a title.
	got, err := d.GetRecordingMeta(a)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "" || len(got.Tags) != 0 {
		t.Errorf("absent metadata = %+v, want zeroes", got)
	}

	set, err := d.SetRecordingMeta(a, Metadata{Title: "Hour one", Description: "the cold open", Tags: []string{"clip", "Clip"}})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if set.Title != "Hour one" || set.Description != "the cold open" {
		t.Errorf("metadata = %+v", set)
	}
	if len(set.Tags) != 1 || set.Tags[0] != "clip" {
		t.Errorf("tags = %v, want [clip]", set.Tags)
	}
	if set.UpdatedAt.IsZero() {
		t.Errorf("updatedAt not stamped")
	}

	// A second write updates in place rather than colliding.
	if _, err := d.SetRecordingMeta(a, Metadata{Title: "Hour one, take two"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = d.GetRecordingMeta(a)
	if got.Title != "Hour one, take two" {
		t.Errorf("title = %q", got.Title)
	}

	if _, err := d.SetRecordingMeta(999, Metadata{Title: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("metadata for a missing recording = %v, want ErrNotFound", err)
	}
}

func TestRecordingMetadataDiesWithItsRecording(t *testing.T) {
	d := testDB(t)
	a := seedRecordingAt(t, d, "rec-0.mkv", base, time.Hour)
	if _, err := d.SetRecordingMeta(a, Metadata{Title: "doomed"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := d.DeleteRecording(a); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM recording_meta WHERE recording_id = ?`, a).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("orphaned metadata row survived")
	}
}

func TestListRecordingMetaBatches(t *testing.T) {
	d := testDB(t)
	a := seedRecordingAt(t, d, "rec-0.mkv", base, time.Hour)
	b := seedRecordingAt(t, d, "rec-1.mkv", base.Add(time.Hour), time.Hour)
	if _, err := d.SetRecordingMeta(a, Metadata{Title: "titled", Tags: []string{"x"}}); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := d.ListRecordingMeta([]int64{a, b})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("entries = %d, want 1 (recordings without metadata are absent)", len(got))
	}
	if got[a].Title != "titled" || len(got[a].Tags) != 1 {
		t.Errorf("entry = %+v", got[a])
	}
	if m, err := d.ListRecordingMeta(nil); err != nil || len(m) != 0 {
		t.Errorf("empty input = %v, %v", m, err)
	}
}

func TestRecalcSessionsRefreshesEveryStoredSpan(t *testing.T) {
	d := testDB(t)
	a := seedRecordingAt(t, d, "rec-0.mkv", base, time.Hour)
	s, _ := d.CreateSession(Metadata{}, true)
	if err := d.AddRecordingToSession(s.ID, a); err != nil {
		t.Fatalf("add: %v", err)
	}
	// The scanner measures the segment properly on a later pass.
	if err := d.UpsertRecording(&Recording{
		Filename:   "rec-0.mkv",
		StartedAt:  base,
		FinishedAt: base.Add(2 * time.Hour),
		Bytes:      4096,
		DurationMS: (2 * time.Hour).Milliseconds(),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := d.RecalcSessions(); err != nil {
		t.Fatalf("recalc: %v", err)
	}
	got, _ := d.GetSession(s.ID)
	if got.DurationMS != (2 * time.Hour).Milliseconds() {
		t.Errorf("duration = %d, want %d", got.DurationMS, (2 * time.Hour).Milliseconds())
	}
	if got.Bytes != 4096 {
		t.Errorf("bytes = %d, want 4096", got.Bytes)
	}
}

func TestSessionIDsForRecordings(t *testing.T) {
	d := testDB(t)
	a := seedRecordingAt(t, d, "rec-0.mkv", base, time.Hour)
	b := seedRecordingAt(t, d, "rec-1.mkv", base.Add(25*time.Hour), time.Hour)
	s, _ := d.CreateSession(Metadata{}, true)
	if err := d.AddRecordingToSession(s.ID, a); err != nil {
		t.Fatalf("add: %v", err)
	}
	got, err := d.SessionIDsForRecordings([]int64{a, b})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got[a] != s.ID {
		t.Errorf("a -> %d, want %d", got[a], s.ID)
	}
	if _, ok := got[b]; ok {
		t.Errorf("ungrouped recording appeared in the map")
	}
	if m, err := d.SessionIDsForRecordings(nil); err != nil || len(m) != 0 {
		t.Errorf("empty input = %v, %v", m, err)
	}
}

func TestUnmarshalTagsSurvivesCorruptJSON(t *testing.T) {
	d := testDB(t)
	s, _ := d.CreateSession(Metadata{Title: "kept"}, false)
	if _, err := d.sql.Exec(`UPDATE sessions SET tags = 'not json' WHERE id = ?`, s.ID); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	// A tags column that will not parse is a display problem; refusing to load
	// the session would hide the recordings too.
	got, err := d.GetSession(s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "kept" {
		t.Errorf("session lost to a bad tags column")
	}
	if len(got.Tags) != 0 {
		t.Errorf("tags = %v, want empty", got.Tags)
	}
}

// TestSchemaSurvivesReopen pins that everything this feature adds is written
// with IF NOT EXISTS, including the FTS5 virtual table and its triggers, which
// are the two kinds of statement that do not tolerate being applied twice.
// Every existing install runs the schema against a database that already has
// most of it.
func TestSchemaSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	rec := seedRecording(t, first, "rec-0.mkv", base, time.Hour.Milliseconds())
	seedTranscript(t, first, rec)
	s, err := first.CreateSession(Metadata{Title: "kept"}, false)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := first.AddRecordingToSession(s.ID, rec); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	hits, err := second.SearchTranscripts(TranscriptQuery{Text: "multitrack"})
	if err != nil {
		t.Fatalf("search after reopen: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("hits after reopen = %d, want 1", len(hits))
	}
	got, err := second.GetSession(s.ID)
	if err != nil {
		t.Fatalf("session after reopen: %v", err)
	}
	if got.Title != "kept" || got.Recordings != 1 {
		t.Errorf("session after reopen = %+v", got)
	}
}

// seedRecordingAt indexes a recording and returns its id.
func seedRecordingAt(t *testing.T, d *DB, name string, started time.Time, dur time.Duration) int64 {
	t.Helper()
	return seedRecording(t, d, name, started, dur.Milliseconds())
}

func itoa(i int) string { return string(rune('0' + i)) }
