package db

import (
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// cascadeFixture is two destinations on one programme, A and B, and four
// schedules that name them in every shape the prune has to tell apart.
type cascadeFixture struct {
	d                    *DB
	source               int64
	a, b                 int64
	both, onlyA, all, pl int64
}

func newCascadeFixture(t *testing.T) cascadeFixture {
	t.Helper()
	d := testDB(t)
	src := &Source{Name: "studio b", Ingest: DefaultSettings().Ingest, Position: 2}
	if err := d.CreateSource(src); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	mk := func(name string) int64 {
		t.Helper()
		dst := validDest()
		dst.Name = name
		dst.SourceID = &src.ID
		created, err := d.CreateDestination(dst)
		if err != nil {
			t.Fatalf("CreateDestination(%q): %v", name, err)
		}
		return created.ID
	}
	f := cascadeFixture{d: d, source: src.ID, a: mk("A"), b: mk("B")}
	sched := func(name string, action scheduler.Action, ids []int64, all bool) int64 {
		t.Helper()
		sc := validSchedule()
		sc.Name, sc.Action, sc.DestinationIDs, sc.AllDestinations = name, action, ids, all
		created, err := d.CreateSchedule(sc)
		if err != nil {
			t.Fatalf("CreateSchedule(%q): %v", name, err)
		}
		return created.ID
	}
	f.both = sched("start both", scheduler.ActionStart, []int64{f.a, f.b}, false)
	f.onlyA = sched("stop A", scheduler.ActionStop, []int64{f.a}, false)
	f.all = sched("start everything", scheduler.ActionStart, nil, true)
	// A playlist schedule does not act on destinations. Written raw, naming A,
	// so the test pins that the prune's filter leaves it alone rather than
	// relying on Validate never letting one name a destination.
	now := time.Now().Unix()
	res, err := d.sql.Exec(`INSERT INTO schedules
		(name, enabled, action, kind, destination_ids, tz, at_minutes, days,
		 run_at, grace_seconds, last_run_at, created_at, updated_at, all_destinations)
		VALUES ('filler',1,'playlist.start','daily',?,'UTC',60,'[]',0,300,0,?,?,0)`,
		"["+idText(f.a)+"]", now, now)
	if err != nil {
		t.Fatalf("insert playlist schedule: %v", err)
	}
	f.pl, _ = res.LastInsertId()
	return f
}

func idText(n int64) string { return strconv.FormatInt(n, 10) }

func (f cascadeFixture) schedule(t *testing.T, id int64) *scheduler.Schedule {
	t.Helper()
	sc, err := f.d.GetSchedule(id)
	if err != nil {
		t.Fatalf("GetSchedule(%d): %v", id, err)
	}
	return sc
}

// assertPruned is the whole contract, checked the same way for the
// destination delete and for the programme delete that cascades to it.
func (f cascadeFixture) assertPruned(t *testing.T, prune SchedulePrune, wantNarrowed []string) {
	t.Helper()
	if got := f.schedule(t, f.both); !slices.Equal(got.DestinationIDs, []int64{f.b}) || !got.Enabled {
		t.Errorf("the schedule naming A and B: ids=%v enabled=%v, want [%d] and still on -- "+
			"the deleted destination's id was left in it", got.DestinationIDs, got.Enabled, f.b)
	}
	onlyA := f.schedule(t, f.onlyA)
	if onlyA.Enabled {
		t.Error("the stop schedule whose only target was deleted is still enabled; it will error on every occurrence")
	}
	if !slices.Equal(onlyA.DestinationIDs, []int64{f.a}) || onlyA.AllDestinations {
		t.Errorf("the emptied stop schedule reads ids=%v all=%v; it must keep the dead id rather than "+
			"become an empty list", onlyA.DestinationIDs, onlyA.AllDestinations)
	}
	if got := f.schedule(t, f.all); !got.AllDestinations || !got.Enabled {
		t.Errorf("the every-destination schedule changed: %+v", got)
	}
	var plIDs string
	if err := f.d.sql.QueryRow(`SELECT destination_ids FROM schedules WHERE id = ?`, f.pl).Scan(&plIDs); err != nil {
		t.Fatal(err)
	}
	if plIDs != "["+idText(f.a)+"]" {
		t.Errorf("the playlist schedule was rewritten to %s", plIDs)
	}
	if !slices.Equal(prune.Disabled, []string{"stop A"}) {
		t.Errorf("reported disabled %v, want [stop A]", prune.Disabled)
	}
	if !slices.Equal(prune.Narrowed, wantNarrowed) {
		t.Errorf("reported narrowed %v, want %v", prune.Narrowed, wantNarrowed)
	}

	// THE TRAP. MigrateScheduleAllDestinations folds an empty start/stop list
	// into "every destination" on every Open, enabled or not. A prune that left
	// "[]" would come back from the next restart as stop-everything.
	if err := f.d.MigrateScheduleAllDestinations(); err != nil {
		t.Fatalf("MigrateScheduleAllDestinations: %v", err)
	}
	if again := f.schedule(t, f.onlyA); again.AllDestinations {
		t.Fatal("after the boot migration the emptied stop schedule acts on EVERY destination")
	}
}

func TestDeletingADestinationPrunesItFromSchedules(t *testing.T) {
	f := newCascadeFixture(t)
	prune, err := f.d.DeleteDestinationPruned(f.a)
	if err != nil {
		t.Fatalf("DeleteDestinationPruned: %v", err)
	}
	f.assertPruned(t, prune, []string{"start both"})
}

// The programme delete reaches the destinations through ON DELETE CASCADE,
// which no Go code sees. Both A and B go, so "start both" is emptied too.
func TestDeletingAProgrammePrunesItsDestinationsFromSchedules(t *testing.T) {
	f := newCascadeFixture(t)
	// A third destination on another programme keeps "start both" from being
	// emptied, so the narrowed branch is exercised through the cascade too.
	other := validDest()
	other.Name = "elsewhere"
	c, err := f.d.CreateDestination(other)
	if err != nil {
		t.Fatal(err)
	}
	sc := f.schedule(t, f.both)
	sc.DestinationIDs = []int64{f.a, f.b, c.ID}
	if _, err := f.d.UpdateSchedule(sc); err != nil {
		t.Fatal(err)
	}
	prune, err := f.d.DeleteSourcePruned(f.source, 2)
	if err != nil {
		t.Fatalf("DeleteSourcePruned: %v", err)
	}
	if got := f.schedule(t, f.both); !slices.Equal(got.DestinationIDs, []int64{c.ID}) {
		t.Errorf("after the programme delete the schedule names %v, want only [%d]", got.DestinationIDs, c.ID)
	}
	if !slices.Equal(prune.Disabled, []string{"stop A"}) || !slices.Equal(prune.Narrowed, []string{"start both"}) {
		t.Errorf("prune report %+v", prune)
	}
	if again := f.schedule(t, f.onlyA); again.Enabled {
		t.Error("the stop schedule whose only destination went with the programme is still enabled")
	}
}

// A delete that names no schedule reports nothing, and a missing row is still
// ErrNotFound with nothing touched.
func TestADeleteThatTouchesNoScheduleReportsNothing(t *testing.T) {
	d := testDB(t)
	ids := seedDestinations(t, d, "lonely")
	prune, err := d.DeleteDestinationPruned(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(prune.Disabled)+len(prune.Narrowed) != 0 {
		t.Errorf("a delete no schedule named reported %+v", prune)
	}
	if _, err := d.DeleteDestinationPruned(ids[0]); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v, want ErrNotFound", err)
	}
}

// #15: the rollover row is keyed by destination id with no foreign key, and
// ListEnabledRollovers kept returning a deleted destination's row for ever.
func TestDeletingADestinationDeletesItsRolloverRow(t *testing.T) {
	d := testDB(t)
	ids := seedDestinations(t, d, "gone", "kept")
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	for _, id := range ids {
		if err := d.SaveRolloverConfig(RolloverSettings{DestinationID: id, Enabled: true, MarginMinutes: 30}, now); err != nil {
			t.Fatal(err)
		}
		if err := d.RecordRolloverResult(id, "margin", "rolled over", now); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.DeleteDestination(ids[0]); err != nil {
		t.Fatal(err)
	}
	rows, err := d.ListEnabledRollovers()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].DestinationID != ids[1] {
		t.Fatalf("enabled rollovers after the delete: %+v, want only destination %d", rows, ids[1])
	}
	if got, err := d.GetRollover(ids[0]); err != nil || got.Enabled || got.LastResult != "" {
		t.Errorf("the deleted destination still has rollover state: %+v err=%v", got, err)
	}
}

// The programme path drops rollover rows too: the cascade deletes the
// destinations, and nothing else would.
func TestDeletingAProgrammeDeletesItsDestinationsRolloverRows(t *testing.T) {
	f := newCascadeFixture(t)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	if err := f.d.SaveRolloverConfig(RolloverSettings{DestinationID: f.a, Enabled: true, MarginMinutes: 30}, now); err != nil {
		t.Fatal(err)
	}
	if err := f.d.DeleteSource(f.source); err != nil {
		t.Fatal(err)
	}
	rows, err := f.d.ListEnabledRollovers()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("a destination deleted with its programme still has a rollover row: %+v", rows)
	}
}

// The boot prune heals installs whose rows were orphaned before the delete
// took them along, and keeps a live destination's row.
func TestTheBootMigrationDropsOrphanedRolloverRows(t *testing.T) {
	d := testDB(t)
	ids := seedDestinations(t, d, "live")
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	for _, id := range []int64{ids[0], ids[0] + 1000} {
		if _, err := d.sql.Exec(`INSERT INTO destination_rollover (destination_id, enabled, updated_at) VALUES (?, 1, ?)`,
			id, now.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.MigrateWave3RolloverState(); err != nil {
		t.Fatal(err)
	}
	rows, err := d.ListEnabledRollovers()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].DestinationID != ids[0] {
		t.Fatalf("after the boot prune: %+v, want only the live destination's row", rows)
	}
}

// #13: the template goes with its schedule; the occurrence rows, which the
// sweep needs to cancel the platform events, do not.
func TestDeletingAScheduleDeletesItsBroadcastTemplate(t *testing.T) {
	d := testDB(t)
	sc, err := d.CreateSchedule(validSchedule())
	if err != nil {
		t.Fatal(err)
	}
	keep, err := d.CreateSchedule(validSchedule())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{sc.ID, keep.ID} {
		if _, err := d.SaveBroadcastTemplate(scheduler.BroadcastTemplate{ScheduleID: id, TitlePattern: "Show {date}", Lookahead: 1}); err != nil {
			t.Fatalf("SaveBroadcastTemplate: %v", err)
		}
		if err := d.SetBroadcastTemplateThumbnail(id, []byte("png bytes"), "image/png"); err != nil {
			t.Fatalf("SetBroadcastTemplateThumbnail: %v", err)
		}
	}
	at := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	occ, err := d.ClaimOccurrence(scheduler.Occurrence{ScheduleID: sc.ID, DestinationID: 1, At: at})
	if err != nil {
		t.Fatalf("ClaimOccurrence: %v", err)
	}
	if err := d.ResolveOccurrence(occ, "event-1", "Show"); err != nil {
		t.Fatalf("ResolveOccurrence: %v", err)
	}

	if err := d.DeleteSchedule(sc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GetBroadcastTemplate(sc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the deleted schedule's template is still stored (err=%v)", err)
	}
	if _, _, err := d.BroadcastThumbnail(sc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the deleted schedule's thumbnail is still stored (err=%v)", err)
	}
	if _, err := d.GetBroadcastTemplate(keep.ID); err != nil {
		t.Errorf("another schedule's template went too: %v", err)
	}
	open, err := d.ListOpenOccurrences()
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].PlatformEventID != "event-1" {
		t.Fatalf("the occurrence row must survive for the sweep to cancel its event: %+v", open)
	}

	// A delete that fails deletes nothing, including another schedule's template.
	if err := d.DeleteSchedule(sc.ID + 1000); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown schedule: %v, want ErrNotFound", err)
	}
	if _, err := d.GetBroadcastTemplate(keep.ID); err != nil {
		t.Errorf("a failed delete took a template: %v", err)
	}
}

func TestTheBootMigrationDropsOrphanedTemplates(t *testing.T) {
	d := testDB(t)
	sc, err := d.CreateSchedule(validSchedule())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{sc.ID, sc.ID + 1000} {
		if _, err := d.SaveBroadcastTemplate(scheduler.BroadcastTemplate{ScheduleID: id, TitlePattern: "Show", Lookahead: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.MigrateWave3RecurringTemplates(); err != nil {
		t.Fatal(err)
	}
	tpls, err := d.ListBroadcastTemplates()
	if err != nil {
		t.Fatal(err)
	}
	if len(tpls) != 1 || tpls[0].ScheduleID != sc.ID {
		t.Fatalf("after the boot prune: %+v, want only the live schedule's template", tpls)
	}
}

// #12: removing an app's credentials drops the sign-ins begun against it, and
// only that platform's.
func TestDeletingCredentialsDropsTheirPendingAuthorizations(t *testing.T) {
	d := testDB(t)
	if err := d.PutOAuthState("twitch-state", PlatformTwitch, "twitch-verifier"); err != nil {
		t.Fatal(err)
	}
	if err := d.PutOAuthState("youtube-state", PlatformYouTube, "youtube-verifier"); err != nil {
		t.Fatal(err)
	}
	if err := d.DeletePlatformCreds(PlatformTwitch); err != nil {
		t.Fatalf("DeletePlatformCreds with no creds row must stay idempotent: %v", err)
	}
	if _, _, err := d.TakeOAuthState("twitch-state"); err == nil {
		t.Error("a Twitch sign-in begun before its credentials were removed can still be redeemed")
	}
	if p, _, err := d.TakeOAuthState("youtube-state"); err != nil || p != PlatformYouTube {
		t.Errorf("another platform's pending sign-in went too: platform=%q err=%v", p, err)
	}
}

// The confirmed count is compared inside the delete's transaction, so a
// destination added after the handler read it refuses the delete and nothing
// goes.
func TestAProgrammeDeleteRefusesWhenItsDestinationsChanged(t *testing.T) {
	f := newCascadeFixture(t)
	if _, err := f.d.DeleteSourcePruned(f.source, 1); !errors.Is(err, ErrDestinationsChanged) {
		t.Fatalf("a delete confirmed for 1 destination on a programme with 2: %v, want ErrDestinationsChanged", err)
	}
	if _, err := f.d.GetSource(f.source); err != nil {
		t.Fatalf("the refused delete removed the programme: %v", err)
	}
	if got := f.schedule(t, f.both); len(got.DestinationIDs) != 2 {
		t.Errorf("the refused delete pruned schedules: %v", got.DestinationIDs)
	}
}
