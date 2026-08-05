package api

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

// The schedule routes were entirely untested. What makes them worth covering is
// not the CRUD -- it is that a schedule which fails to validate must fail at the
// API, in front of the operator, rather than at fire time. A schedule is
// something you configure once and then trust to run while you are not
// watching, so a bad row that is accepted now is a broadcast that does not start
// at 19:00 with nobody there to notice.

func createSchedule(t *testing.T, h http.Handler, sign func(*http.Request), body map[string]any) map[string]any {
	t.Helper()
	var out map[string]any
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/schedules", body, http.StatusCreated), &out)
	return out
}

func dailySchedule() map[string]any {
	return map[string]any{
		"name": "evening show", "enabled": true,
		"action": "start", "kind": "daily",
		"tz": "UTC", "atMinutes": 19 * 60,
	}
}

func TestScheduleCRUDRoundTrip(t *testing.T) {
	h, _, sign := sourceServer(t)

	created := createSchedule(t, h, sign, dailySchedule())
	id := strconv.FormatInt(int64(created["id"].(float64)), 10)
	if created["name"] != "evening show" {
		t.Errorf("name = %v", created["name"])
	}

	var list []map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/schedules", nil, http.StatusOK), &list)
	if len(list) != 1 {
		t.Fatalf("list has %d schedules, want 1", len(list))
	}

	var got map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/schedules/"+id, nil, http.StatusOK), &got)
	if got["name"] != "evening show" {
		t.Errorf("fetched name = %v", got["name"])
	}

	body := dailySchedule()
	body["name"] = "late show"
	body["atMinutes"] = 22 * 60
	send(t, h, sign, http.MethodPut, "/api/v1/schedules/"+id, body, http.StatusOK)

	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/schedules/"+id, nil, http.StatusOK), &got)
	if got["name"] != "late show" {
		t.Errorf("the update did not persist: name = %v", got["name"])
	}

	send(t, h, sign, http.MethodDelete, "/api/v1/schedules/"+id, nil, http.StatusOK)
	send(t, h, sign, http.MethodGet, "/api/v1/schedules/"+id, nil, http.StatusNotFound)
}

func TestScheduleValidationRefusesWhatCannotFire(t *testing.T) {
	// Every one of these would be accepted by a handler that only decoded JSON,
	// and every one of them is a broadcast that silently does not happen.
	h, _, sign := sourceServer(t)

	bad := func(name string, mutate func(map[string]any)) {
		t.Run(name, func(t *testing.T) {
			body := dailySchedule()
			mutate(body)
			send(t, h, sign, http.MethodPost, "/api/v1/schedules", body, http.StatusBadRequest)
		})
	}

	bad("no name", func(b map[string]any) { b["name"] = "" })
	bad("unknown action", func(b map[string]any) { b["action"] = "explode" })
	bad("unknown kind", func(b map[string]any) { b["kind"] = "fortnightly" })
	bad("unknown timezone", func(b map[string]any) { b["tz"] = "Mars/Olympus_Mons" })
	bad("time of day past midnight", func(b map[string]any) { b["atMinutes"] = 24 * 60 })
	bad("negative time of day", func(b map[string]any) { b["atMinutes"] = -1 })
	// Weekly with no weekdays can never fire -- there is no day for it to
	// match, so it would sit in the list looking configured forever.
	bad("weekly with no days", func(b map[string]any) {
		b["kind"] = "weekly"
		b["days"] = []int{}
	})
	// A one-off with no date is the same failure in a different shape.
	bad("once with no date", func(b map[string]any) {
		b["kind"] = "once"
		delete(b, "runAt")
	})
}

func TestAWeeklyScheduleNeedsAtLeastOneDay(t *testing.T) {
	// The positive half, so the check above cannot pass by refusing everything.
	h, _, sign := sourceServer(t)
	body := dailySchedule()
	body["kind"] = "weekly"
	body["days"] = []int{int(time.Saturday), int(time.Sunday)}
	created := createSchedule(t, h, sign, body)
	if created["kind"] != "weekly" {
		t.Errorf("kind = %v, want weekly", created["kind"])
	}
}

func TestAOneOffScheduleAcceptsADate(t *testing.T) {
	h, _, sign := sourceServer(t)
	body := dailySchedule()
	body["kind"] = "once"
	body["runAt"] = time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	created := createSchedule(t, h, sign, body)
	if created["kind"] != "once" {
		t.Errorf("kind = %v, want once", created["kind"])
	}
}

// The 404s are asserted with their BODIES, not by their status alone. CI's Go
// job never builds the UI, so the SPA fallback answers an unrouted /api/v1/...
// path with 404 "UI not built." -- and a test that only counts to 404 cannot
// tell that apart from a handler refusing an id.
//
// Measured, on a committed tree: with all three r.Get/r.Put/r.Delete
// registrations for "/schedules/{id}" commented out and
// internal/web/dist/index.html moved aside, the status assertion still passed
// and only the body assertion failed --
//
//	404, but the body is not JSON ... UI not built. Run `make ui`
//
// With index.html present the same mutation answers 200 instead. This is the
// measurement mustJSONError's note generalises from.
//
// Mutation: comment out all three "/schedules/{id}" registrations. Removing
// only one gives chi's 405, which the status alone would have caught.
func TestScheduleRoutesRejectAnUnknownID(t *testing.T) {
	h, _, sign := sourceServer(t)
	mustJSONError(t, h, sign, http.MethodGet, "/api/v1/schedules/99999", nil, http.StatusNotFound)
	mustJSONError(t, h, sign, http.MethodDelete, "/api/v1/schedules/99999", nil, http.StatusNotFound)
	mustJSONError(t, h, sign, http.MethodPut, "/api/v1/schedules/99999", dailySchedule(), http.StatusNotFound)
	mustJSONError(t, h, sign, http.MethodGet, "/api/v1/schedules/abc", nil, http.StatusBadRequest)
}

func TestScheduleRunsIsReadableBeforeAnythingHasRun(t *testing.T) {
	// A fresh install has no runs. The route must still answer with an empty
	// list rather than an error -- the automation page renders this on first
	// load, and a 500 there reads as "automation is broken".
	h, _, sign := sourceServer(t)
	var runs []map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/schedules/runs", nil, http.StatusOK), &runs)
	if len(runs) != 0 {
		t.Errorf("a fresh install reported %d schedule runs", len(runs))
	}
}
