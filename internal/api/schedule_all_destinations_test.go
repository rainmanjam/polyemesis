package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// app-council A54 at the API: "every destination" is a field a client sends,
// allDestinations, and a stop schedule that sends neither it nor a list is
// refused rather than read as "stop the whole broadcast".
//
// Old clients keep working. A body that predates the field omits it, so
// "absent" is told apart from "false": on an existing schedule it keeps what
// the row already says (the migrated value), and on a new start it keeps the
// old meaning of an empty list. Only a NEW stop -- or an edit that turns a
// named stop into an empty one -- has nothing safe to fall back on, and that
// is the case refused, with a message naming the field that fixes it.

func allDestinationsOf(t *testing.T, view map[string]any) bool {
	t.Helper()
	v, ok := view["allDestinations"].(bool)
	if !ok {
		t.Fatalf("allDestinations is %T (%v), not a bool", view["allDestinations"], view["allDestinations"])
	}
	return v
}

func stopSchedule() map[string]any {
	b := dailySchedule()
	b["action"] = "stop"
	return b
}

// The mutation: make the absent-flag fallback return true for every action and
// the first case stores a stop that takes everything down.
func TestANewStopScheduleNamingNothingIsRefusedAndNamesAllDestinations(t *testing.T) {
	h, _, sign := sourceServer(t)

	for name, body := range map[string]map[string]any{
		"no list and no flag": stopSchedule(),
		"an empty list":       scheduleWithTargets(),
		"an explicit false": func() map[string]any {
			b := scheduleWithTargets()
			b["allDestinations"] = false
			return b
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			msg := mustJSONError(t, h, sign, http.MethodPost, "/api/v1/schedules", body, http.StatusBadRequest)
			if !strings.Contains(msg, "allDestinations") {
				t.Fatalf("the refusal does not name allDestinations, the field that would fix it: %q", msg)
			}
		})
	}
	var list []map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/schedules", nil, http.StatusOK), &list)
	if len(list) != 0 {
		t.Fatalf("a refused stop was stored anyway: %v", list)
	}
}

func TestAStopScheduleThatChoosesAllDestinationsIsStoredAsAll(t *testing.T) {
	h, _, sign := sourceServer(t)
	body := stopSchedule()
	body["allDestinations"] = true
	created := createSchedule(t, h, sign, body)
	if !allDestinationsOf(t, created) {
		t.Fatalf("allDestinations = false on a stop that chose every destination: %v", created)
	}
	if got := storedTargets(t, created); len(got) != 0 {
		t.Fatalf("destinationIds = %v, want none beside allDestinations", got)
	}
}

func TestAllDestinationsBesideANamedListIsRefused(t *testing.T) {
	h, _, sign := sourceServer(t)
	body := scheduleWithTargets(float64(4))
	body["allDestinations"] = true
	msg := mustJSONError(t, h, sign, http.MethodPost, "/api/v1/schedules", body, http.StatusBadRequest)
	if !strings.Contains(msg, "allDestinations") {
		t.Fatalf("the refusal does not say which two fields disagree: %q", msg)
	}
}

// An old client's "start the show": no list, no flag. It meant every
// destination before this change and still does.
func TestAnOldClientsEmptyStartStillMeansEveryDestination(t *testing.T) {
	h, _, sign := sourceServer(t)
	created := createSchedule(t, h, sign, dailySchedule())
	if !allDestinationsOf(t, created) {
		t.Fatalf("an old client's empty start lost its meaning: %v", created)
	}
}

// An old client editing a migrated schedule -- the enable switch on the
// schedule list sends the whole row back, without the field it has never
// heard of. The row must keep stopping everything, not become invalid or,
// worse, quietly stop nothing.
func TestAnOldClientsEditKeepsAStopsAllDestinations(t *testing.T) {
	h, _, sign := sourceServer(t)
	body := stopSchedule()
	body["allDestinations"] = true
	created := createSchedule(t, h, sign, body)
	id := strconv.FormatInt(int64(created["id"].(float64)), 10)

	edit := stopSchedule() // no allDestinations, no destinationIds
	edit["enabled"] = false
	var got map[string]any
	decodeInto(t, send(t, h, sign, http.MethodPut, "/api/v1/schedules/"+id, edit, http.StatusOK), &got)
	if !allDestinationsOf(t, got) {
		t.Fatalf("an old client's edit dropped allDestinations from a stop: %v", got)
	}
	if got["enabled"] != false {
		t.Fatalf("the edit itself did not apply: %v", got)
	}

	// And a client that DOES know the field can turn it off by naming a list.
	named := scheduleWithTargets(float64(6))
	decodeInto(t, send(t, h, sign, http.MethodPut, "/api/v1/schedules/"+id, named, http.StatusOK), &got)
	if allDestinationsOf(t, got) {
		t.Fatalf("naming a destination left allDestinations on: %v", got)
	}
}

// The edit that is refused: a stop that named destinations, sent back by an old
// client with the list cleared. To that client an empty list meant "every
// destination"; to this server it is a stop aimed at nothing in particular.
// Neither reading is safe to guess, so it is refused and nothing changes.
func TestAnEditThatEmptiesANamedStopIsRefused(t *testing.T) {
	h, _, sign := sourceServer(t)
	created := createSchedule(t, h, sign, scheduleWithTargets(float64(3)))
	id := strconv.FormatInt(int64(created["id"].(float64)), 10)

	msg := mustJSONError(t, h, sign, http.MethodPut, "/api/v1/schedules/"+id, stopSchedule(), http.StatusBadRequest)
	if !strings.Contains(msg, "allDestinations") {
		t.Fatalf("the refusal does not name allDestinations: %q", msg)
	}
	var got map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/schedules/"+id, nil, http.StatusOK), &got)
	if ids := storedTargets(t, got); len(ids) != 1 || ids[0] != 3 || allDestinationsOf(t, got) {
		t.Fatalf("a refused edit changed the row: ids=%v all=%v", ids, got["allDestinations"])
	}
}

// The pre-announce sweep and the scheduleWarnings check read the same flag the
// runner does. If they still read an empty list as every destination, a stop
// that the runner refuses to act on would have the sweep creating Facebook
// event pages for it -- the two halves disagreeing about what one row means.
//
// The mutation: restore `len(sc.DestinationIDs) == 0` in scheduleTargets and
// the first case reports a target.
func TestScheduleTargetsReadsAllDestinationsNotAnEmptyList(t *testing.T) {
	if scheduleTargets(scheduler.Schedule{Action: scheduler.ActionStop}, 7) {
		t.Error("a schedule naming nothing and not every destination targets destination 7")
	}
	if !scheduleTargets(scheduler.Schedule{Action: scheduler.ActionStart, AllDestinations: true}, 7) {
		t.Error("an every-destination schedule does not target destination 7")
	}
	if !scheduleTargets(scheduler.Schedule{Action: scheduler.ActionStart, DestinationIDs: []int64{3, 7}}, 7) {
		t.Error("a schedule naming destination 7 does not target it")
	}
}
