package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// A SCHEDULE THAT NAMES DESTINATIONS MUST NOT BE STORED NAMING NONE.
//
// Empty destinationIds is not "no destinations". scheduleTargets reads it as
// EVERY destination on this install, because "start the show" usually names
// nothing and that is the commonest shape the feature has. So the two lists sit
// at opposite ends of the blast radius while looking like neighbours, and a
// request that loses its ids on the way in is silently converted from one into
// the other -- for a `stop` schedule, into "stop every broadcast on this box",
// fired unattended, at whatever hour it was set for, with
// destinations_bulk.go recording each stopped YouTube broadcast as permanently
// completed.
//
// [null] was the way in. encoding/json writes 0 for a null element of a
// []int64 and returns no error, Normalized drops every id <= 0, and Validate
// only ever saw the list afterwards -- it checks MaxTargets and the playlist
// conflict, neither of which can tell an emptied list from an empty one.
//
// These go through the ROUTES rather than calling the guards, because the
// defect was never in a function: it was in what the two of them, composed,
// let through.

func scheduleWithTargets(ids ...any) map[string]any {
	b := dailySchedule()
	b["action"] = "stop"
	b["destinationIds"] = ids
	return b
}

// storedTargets reads destinationIds back off a schedule view.
func storedTargets(t *testing.T, view map[string]any) []int64 {
	t.Helper()
	raw, ok := view["destinationIds"]
	if !ok || raw == nil {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("destinationIds is %T, not a list: %v", raw, raw)
	}
	out := make([]int64, 0, len(list))
	for _, v := range list {
		n, ok := v.(float64)
		if !ok {
			t.Fatalf("destination id %v is %T, not a number", v, v)
		}
		out = append(out, int64(n))
	}
	return out
}

func TestCreatingAScheduleCannotTurnNamedDestinationsIntoAllOfThem(t *testing.T) {
	h, _, sign := sourceServer(t)

	refused := func(name string, ids ...any) {
		t.Run(name, func(t *testing.T) {
			msg := mustJSONError(t, h, sign, http.MethodPost, "/api/v1/schedules",
				scheduleWithTargets(ids...), http.StatusBadRequest)
			// The operator has to be told WHICH way it would have gone wrong.
			// "invalid destinationIds" would leave them re-sending the same
			// body with the same null in it.
			if !strings.Contains(msg, "EVERY destination") {
				t.Fatalf("the refusal does not say that an empty list means every "+
					"destination, which is the whole reason this is refused: %q", msg)
			}
		})
	}

	// A JSON null. This is the one that decoded to [0] with a nil error.
	refused("a single null", nil)
	// A null beside a real id: the list is not emptied, but an id the operator
	// believes they named is gone, and nothing after the decode could know.
	refused("a null beside a real id", float64(7), nil)
	// Well-formed numbers that Normalized drops as junk. These reach the
	// boundary check rather than the decode, which is what that check is for.
	refused("a zero", float64(0))
	refused("a negative id", float64(-3))
	refused("only junk ids", float64(0), float64(-1), float64(0))
	// Not a number at all. []int64 would have zero-filled this too. The second
	// case is the one that isolates the decode from the boundary check: with a
	// real id beside it the list does not end up empty, so nothing further in
	// would ever notice the entry that vanished.
	refused("a string", "7")
	refused("a string beside a real id", "7", float64(5))
}

func TestAScheduleThatNamesRealDestinationsKeepsThem(t *testing.T) {
	// The positive control. Every assertion above is satisfied by a handler
	// that refuses every schedule carrying destinationIds at all, which would
	// remove the feature rather than mistake-proof it.
	h, _, sign := sourceServer(t)

	created := createSchedule(t, h, sign, scheduleWithTargets(float64(7), float64(3)))
	got := storedTargets(t, created)
	if len(got) != 2 || got[0] != 3 || got[1] != 7 {
		t.Fatalf("destinationIds = %v, want the two ids that were named, sorted", got)
	}
}

func TestAScheduleThatNamesNoDestinationsStillMeansAllOfThem(t *testing.T) {
	// The second control, and the more important one. An empty list is the
	// commonest shape this feature has -- "start the show" names nothing -- so
	// a guard that refused it would switch scheduling off for most installs
	// while passing every test above.
	h, _, sign := sourceServer(t)

	body := dailySchedule()
	body["action"] = "stop"
	created := createSchedule(t, h, sign, body)
	if got := storedTargets(t, created); len(got) != 0 {
		t.Fatalf("destinationIds = %v, want none: an omitted list means every destination", got)
	}

	// Explicitly empty is the same request, sent by a client that spells it out.
	created = createSchedule(t, h, sign, scheduleWithTargets())
	if got := storedTargets(t, created); len(got) != 0 {
		t.Fatalf("an explicitly empty destinationIds was not accepted as-is: %v", got)
	}
}

func TestAScheduleThatRepeatsADestinationIsNotRefused(t *testing.T) {
	// Deduplication is Normalized doing its job, and it does not change what
	// the schedule acts on. Only the transition from some destinations to none
	// changes the meaning, so only that is refused -- a check written as
	// "nothing may be dropped" would refuse this.
	h, _, sign := sourceServer(t)

	created := createSchedule(t, h, sign, scheduleWithTargets(float64(7), float64(7)))
	if got := storedTargets(t, created); len(got) != 1 || got[0] != 7 {
		t.Fatalf("destinationIds = %v, want [7]", got)
	}
}

func TestUpdatingAScheduleCannotTurnNamedDestinationsIntoAllOfThem(t *testing.T) {
	// The edit path replaces the destination list wholesale, so it is exactly
	// as capable of this as the create path -- and it is the likelier one: the
	// schedule already exists, already looks right in the list, and the operator
	// is changing a time rather than reviewing what it acts on.
	h, _, sign := sourceServer(t)

	created := createSchedule(t, h, sign, scheduleWithTargets(float64(7), float64(3)))
	id := strconv.FormatInt(int64(created["id"].(float64)), 10)

	for _, ids := range [][]any{{nil}, {float64(0)}, {float64(-3), float64(0)}} {
		msg := mustJSONError(t, h, sign, http.MethodPut, "/api/v1/schedules/"+id,
			scheduleWithTargets(ids...), http.StatusBadRequest)
		if !strings.Contains(msg, "EVERY destination") {
			t.Fatalf("the refusal does not say what an empty list would have meant: %q", msg)
		}
	}

	// Refused means REFUSED, not "stored and warned about". A stop schedule
	// left pointing at everything is the whole hazard.
	var got map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/schedules/"+id, nil, http.StatusOK), &got)
	if ids := storedTargets(t, got); len(ids) != 2 || ids[0] != 3 || ids[1] != 7 {
		t.Fatalf("the stored destinationIds are %v; a refused edit changed the row", ids)
	}

	// The control: a real edit of the same field still works.
	body := scheduleWithTargets(float64(9))
	send(t, h, sign, http.MethodPut, "/api/v1/schedules/"+id, body, http.StatusOK)
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/schedules/"+id, nil, http.StatusOK), &got)
	if ids := storedTargets(t, got); len(ids) != 1 || ids[0] != 9 {
		t.Fatalf("destinationIds = %v after a legitimate edit, want [9]", ids)
	}

	// And so does clearing the list on purpose. An operator who genuinely means
	// "every destination" must still be able to say so on an existing schedule.
	send(t, h, sign, http.MethodPut, "/api/v1/schedules/"+id, dailySchedule(), http.StatusOK)
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/schedules/"+id, nil, http.StatusOK), &got)
	if ids := storedTargets(t, got); len(ids) != 0 {
		t.Fatalf("destinationIds = %v, want none: an omitted list is a legitimate edit", ids)
	}
}

// refuseEmptiedTargets is reachable through both routes, and the tests above go
// through them. This one pins the predicate itself, because the two halves it
// compares are easy to read the wrong way round: it must fire when a list
// arrived non-empty and normalised to nothing, and stay silent for every other
// combination, including the two that look like it.
func TestRefuseEmptiedTargetsFiresOnlyWhenAListWasEmptied(t *testing.T) {
	cases := []struct {
		name  string
		named int
		left  []int64
		want  bool
	}{
		{"named some, none left", 3, nil, true},
		{"named one, none left", 1, []int64{}, true},
		{"named none, none left", 0, nil, false},
		{"named some, some left", 2, []int64{4, 9}, false},
		{"named some, deduplicated to one", 2, []int64{4}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			sc := scheduler.Schedule{DestinationIDs: tc.left}
			if got := refuseEmptiedTargets(w, tc.named, sc); got != tc.want {
				t.Fatalf("refuseEmptiedTargets(%d, %v) = %v, want %v",
					tc.named, tc.left, got, tc.want)
			}
			if tc.want && w.Code != http.StatusBadRequest {
				t.Fatalf("refused with status %d, want 400", w.Code)
			}
			if !tc.want && w.Code != http.StatusOK {
				t.Fatalf("wrote a %d response for a schedule it did not refuse", w.Code)
			}
		})
	}
}

// THE 400 MUST NOT DESCRIBE A CONSEQUENCE THAT COULD NOT HAVE HAPPENED.
//
// "An empty destinationIds means EVERY destination on this install" is true of
// the destination actions and false of the playlist ones: Schedule.Validate
// refuses a playlist schedule that names destinations at all, so an empty list
// there means the playlist and never everything. A message that says otherwise
// teaches the reader to distrust the next one, which is the whole value the
// other message is carrying.
//
// The refusal itself stays. Returning early instead would store
// {"action":"playlist.start","destinationIds":[0]} as a valid playlist schedule
// while the identical body with [5] is a 400 from Validate -- the same
// malformed request succeeding or failing on whether its junk id happened to
// survive Normalized.
//
// MUTATION: delete the TargetsPlaylist branch in refuseEmptiedTargets and the
// playlist case fails on the "EVERY destination" assertion.
func TestTheRefusalDescribesWhatAnEmptyListWouldHaveMeantForThisAction(t *testing.T) {
	cases := []struct {
		name     string
		action   scheduler.Action
		wantSaid string
		wantGone string
	}{
		{"stop names every destination", scheduler.ActionStop, "EVERY destination", "playlist"},
		{"playlist start names the playlist", scheduler.ActionPlaylistStart, "playlist", "EVERY destination"},
		{"playlist stop names the playlist", scheduler.ActionPlaylistStop, "playlist", "EVERY destination"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			sc := scheduler.Schedule{Action: tc.action}
			if !refuseEmptiedTargets(w, 3, sc) {
				t.Fatalf("a %q schedule that named 3 destinations and kept none was accepted", tc.action)
			}
			body := w.Body.String()
			if !strings.Contains(body, tc.wantSaid) {
				t.Fatalf("the refusal for %q never says %q: %s", tc.action, tc.wantSaid, body)
			}
			if strings.Contains(body, tc.wantGone) {
				t.Fatalf("the refusal for %q claims %q, which could not have happened: %s",
					tc.action, tc.wantGone, body)
			}
		})
	}
}

// The route-level twin: a playlist body with [0] gets the playlist sentence and
// is not stored. The unit test above pins the predicate; this proves the branch
// is on the path a real request travels.
func TestAPlaylistScheduleWithAnEmptiedListIsRefusedInItsOwnTerms(t *testing.T) {
	h, _, sign := sourceServer(t)

	body := dailySchedule()
	body["action"] = string(scheduler.ActionPlaylistStart)
	body["destinationIds"] = []any{float64(0)}

	msg := mustJSONError(t, h, sign, http.MethodPost, "/api/v1/schedules", body, http.StatusBadRequest)
	if strings.Contains(msg, "EVERY destination") {
		t.Fatalf("a playlist schedule was refused with the destination-action "+
			"consequence, which scheduler.Validate makes impossible: %q", msg)
	}
	if !strings.Contains(msg, "playlist") {
		t.Fatalf("the refusal never mentions the playlist this schedule acts on: %q", msg)
	}

	var list []map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/schedules", nil, http.StatusOK), &list)
	if len(list) != 0 {
		t.Fatalf("the refused playlist schedule was stored anyway: %v", list)
	}
}
