package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// The HTTP surface for recurring templates and occurrences (#18). It is the calendar's data path,
// and the rollover lane (#19) reads the same template through GET.
//
// Every route here sits behind the same session and CSRF rules as /api/v1/schedules, because it is in
// the same route group. The fixture is the recurring sweep's, with a real session.

// recurringHTTP is a logged-in server whose YouTube provider is the fake editor.
type recurringHTTP struct {
	h        http.Handler
	sign     func(*http.Request)
	store    *db.DB
	fake     *fakeYouTubeEditor
	schedule int64
	dest     *db.Destination
}

func newRecurringHTTP(t *testing.T) *recurringHTTP {
	t.Helper()
	stub := newPlatformStub(t)
	fake := &fakeYouTubeEditor{YouTube: oauth.NewYouTube(oauth.WithBaseURL(stub.URL))}
	set := stub.set().With(db.PlatformYouTube, fake)
	s, h, store, sign := engineServer(t, defaultTools(), Options{Config: config.Config{}, Providers: set})
	seedCreds(t, s, store, db.PlatformYouTube)
	d := seedDestination(t, s, store, db.PlatformYouTube, "yt")
	schedID := seedStartSchedule(t, store, scheduler.KindWeekly, time.Date(2026, 11, 3, 19, 0, 0, 0, time.UTC), d.ID)
	return &recurringHTTP{h: h, sign: sign, store: store, fake: fake, schedule: schedID, dest: d}
}

// templateBody is a valid template for the fixture's schedule.
func templateBody() map[string]any {
	return map[string]any{
		"titlePattern": "Tuesday live {date}",
		"description":  "Every week at seven.",
		"privacy":      "unlisted",
		"lookahead":    2,
	}
}

// Saving a template is a PUT on the schedule it hangs off, and the reply is the stored template with
// its thumbnail reported as a flag rather than as bytes.
func TestPuttingATemplateOntoAScheduleStoresItAndReadsItBack(t *testing.T) {
	f := newRecurringHTTP(t)
	path := "/api/v1/schedules/" + strconv.FormatInt(f.schedule, 10) + "/template"

	var saved map[string]any
	decodeInto(t, send(t, f.h, f.sign, http.MethodPut, path, templateBody(), http.StatusOK), &saved)
	if saved["titlePattern"] != "Tuesday live {date}" || saved["privacy"] != "unlisted" {
		t.Fatalf("reply did not carry the saved template: %v", saved)
	}
	var got map[string]any
	decodeInto(t, send(t, f.h, f.sign, http.MethodGet, path, nil, http.StatusOK), &got)
	if got["lookahead"] != float64(2) {
		t.Fatalf("read-back lookahead = %v, want 2", got["lookahead"])
	}
	if _, has := got["thumbnail"]; has {
		t.Fatalf("the template read carried thumbnail bytes; it must carry only a flag")
	}
}

// A template for a schedule that does not exist has nothing to run, so it is refused as not found.
func TestATemplateForAScheduleThatDoesNotExistIsNotFound(t *testing.T) {
	f := newRecurringHTTP(t)
	send(t, f.h, f.sign, http.MethodPut, "/api/v1/schedules/99999/template", templateBody(), http.StatusNotFound)
}

// A bad template is refused with the scheduler's own sentence, so the operator sees which token or
// bound was wrong. An unknown token must not be stored.
func TestAnUnknownTokenInTheTitleIsRefusedAndNotStored(t *testing.T) {
	f := newRecurringHTTP(t)
	path := "/api/v1/schedules/" + strconv.FormatInt(f.schedule, 10) + "/template"
	body := templateBody()
	body["titlePattern"] = "Show {dte}"
	raw := send(t, f.h, f.sign, http.MethodPut, path, body, http.StatusBadRequest)
	if !strings.Contains(string(raw), "{dte}") {
		t.Fatalf("the refusal %s does not name the token", raw)
	}
	send(t, f.h, f.sign, http.MethodGet, path, nil, http.StatusNotFound)
}

// The thumbnail is uploaded as the raw image, and its bytes must really be the type it claims. A file
// with a PNG content type and no PNG signature is refused: the platform would refuse it too, later,
// and by then the event would already exist.
func TestAThumbnailMustBeAPictureOfTheTypeItClaims(t *testing.T) {
	f := newRecurringHTTP(t)
	base := "/api/v1/schedules/" + strconv.FormatInt(f.schedule, 10)
	send(t, f.h, f.sign, http.MethodPut, base+"/template", templateBody(), http.StatusOK)

	bad := httptest.NewRequest(http.MethodPut, base+"/template/thumbnail", bytes.NewReader([]byte("not a picture")))
	bad.Header.Set("Content-Type", "image/png")
	bad.RemoteAddr = "203.0.113.5:44444"
	f.sign(bad)
	if w := do(t, f.h, bad); w.Code != http.StatusBadRequest {
		t.Fatalf("a non-PNG body with a PNG type got %d, want 400", w.Code)
	}

	good := httptest.NewRequest(http.MethodPut, base+"/template/thumbnail",
		bytes.NewReader([]byte("\x89PNG\r\n\x1a\n-body")))
	good.Header.Set("Content-Type", "image/png")
	good.RemoteAddr = "203.0.113.5:44444"
	f.sign(good)
	if w := do(t, f.h, good); w.Code != http.StatusNoContent {
		t.Fatalf("a real PNG got %d, want 204", w.Code)
	}
}

// Deleting the template is a DELETE on the schedule's template. The schedule keeps running; only the
// template and, at the next sweep, its upcoming events go.
func TestDeletingTheTemplateLeavesTheScheduleRunning(t *testing.T) {
	f := newRecurringHTTP(t)
	base := "/api/v1/schedules/" + strconv.FormatInt(f.schedule, 10)
	send(t, f.h, f.sign, http.MethodPut, base+"/template", templateBody(), http.StatusOK)
	send(t, f.h, f.sign, http.MethodDelete, base+"/template", nil, http.StatusNoContent)
	send(t, f.h, f.sign, http.MethodGet, base+"/template", nil, http.StatusNotFound)
	send(t, f.h, f.sign, http.MethodGet, base, nil, http.StatusOK)
}

// The calendar reads a window of occurrences. A window wider than 90 days is refused, so one request
// cannot list an unbounded history.
func TestTheCalendarListsAWindowAndRefusesAnUnboundedOne(t *testing.T) {
	f := newRecurringHTTP(t)
	at := time.Date(2026, 11, 3, 19, 0, 0, 0, time.UTC)
	id, err := f.store.ClaimOccurrence(scheduler.Occurrence{ScheduleID: f.schedule, DestinationID: f.dest.ID,
		At: at, State: scheduler.OccurrencePending})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.store.ResolveOccurrence(id, "yt-occ-1", "Tuesday live 2026-11-03"); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	var rows []map[string]any
	q := "/api/v1/recurring/occurrences?from=2026-11-01T00:00:00Z&to=2026-11-30T00:00:00Z"
	decodeInto(t, send(t, f.h, f.sign, http.MethodGet, q, nil, http.StatusOK), &rows)
	if len(rows) != 1 || rows[0]["title"] != "Tuesday live 2026-11-03" {
		t.Fatalf("calendar window returned %v, want the one occurrence", rows)
	}

	wide := "/api/v1/recurring/occurrences?from=2026-01-01T00:00:00Z&to=2026-12-31T00:00:00Z"
	send(t, f.h, f.sign, http.MethodGet, wide, nil, http.StatusBadRequest)
}

// Skipping an occurrence through the calendar cancels its event and reports the new state.
func TestSkippingAnOccurrenceThroughTheCalendarCancelsItsEvent(t *testing.T) {
	f := newRecurringHTTP(t)
	// In the future, so the API acts on it: a past occurrence is refused.
	at := time.Now().Add(72 * time.Hour).Truncate(time.Hour)
	id, err := f.store.ClaimOccurrence(scheduler.Occurrence{ScheduleID: f.schedule, DestinationID: f.dest.ID,
		At: at, State: scheduler.OccurrencePending})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.store.ResolveOccurrence(id, "yt-occ-1", "t"); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	var out map[string]any
	path := "/api/v1/recurring/occurrences/" + strconv.FormatInt(id, 10) + "/skip"
	decodeInto(t, send(t, f.h, f.sign, http.MethodPost, path, nil, http.StatusOK), &out)
	if out["state"] != "skipped" {
		t.Fatalf("state after skip = %v, want skipped", out["state"])
	}
	if got := f.fake.cancelled(); len(got) != 1 || got[0] != "yt-occ-1" {
		t.Fatalf("skip cancelled %v, want yt-occ-1", got)
	}
}

// Editing one occurrence's title writes that event, and records the new title. The description and the
// rest of the template still apply, so the edit touches the title and nothing else the operator set.
func TestEditingOneOccurrenceTitleWritesThatEventOnly(t *testing.T) {
	f := newRecurringHTTP(t)
	base := "/api/v1/schedules/" + strconv.FormatInt(f.schedule, 10)
	send(t, f.h, f.sign, http.MethodPut, base+"/template", templateBody(), http.StatusOK)
	// From the real clock: the handler refuses a show whose start has passed.
	at := time.Now().Add(72 * time.Hour).Truncate(time.Hour)
	id, err := f.store.ClaimOccurrence(scheduler.Occurrence{ScheduleID: f.schedule, DestinationID: f.dest.ID,
		At: at, State: scheduler.OccurrencePending})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.store.ResolveOccurrence(id, "yt-occ-1", "old"); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	path := "/api/v1/recurring/occurrences/" + strconv.FormatInt(id, 10) + "/title"
	send(t, f.h, f.sign, http.MethodPost, path, map[string]any{"title": "Special guest, Tuesday"}, http.StatusOK)

	if len(f.fake.edits) != 1 || f.fake.edits[0].Title != "Special guest, Tuesday" {
		t.Fatalf("edits = %+v, want one title write", f.fake.edits)
	}
	if f.fake.editedIDs[0] != "yt-occ-1" {
		t.Fatalf("edited %q, want only yt-occ-1", f.fake.editedIDs[0])
	}
	if f.fake.edits[0].Description != "Every week at seven." {
		t.Fatalf("the single-occurrence edit dropped the template description: %q", f.fake.edits[0].Description)
	}
	got, err := f.store.GetOccurrence(id)
	if err != nil {
		t.Fatalf("GetOccurrence: %v", err)
	}
	if got.Title != "Special guest, Tuesday" {
		t.Fatalf("recorded title = %q", got.Title)
	}
}

// A title longer than the platform allows is refused before any call, the same as a template title.
func TestAnOccurrenceTitleOverTheLimitIsRefusedBeforeAnyPlatformCall(t *testing.T) {
	f := newRecurringHTTP(t)
	// In the future, so the API acts on it: a past occurrence is refused.
	at := time.Now().Add(72 * time.Hour).Truncate(time.Hour)
	id, err := f.store.ClaimOccurrence(scheduler.Occurrence{ScheduleID: f.schedule, DestinationID: f.dest.ID,
		At: at, State: scheduler.OccurrencePending})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.store.ResolveOccurrence(id, "yt-occ-1", "t"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	path := "/api/v1/recurring/occurrences/" + strconv.FormatInt(id, 10) + "/title"
	long := strings.Repeat("x", scheduler.MaxTitleLen+1)
	send(t, f.h, f.sign, http.MethodPost, path, map[string]any{"title": long}, http.StatusBadRequest)
	if len(f.fake.edits) != 0 {
		t.Fatalf("an over-long title reached the platform")
	}
}

// A template is what a START creates events for. A stop schedule carrying one would promise a public
// event for a show that is ending, so the save is refused, and nothing is stored.
func TestAStopScheduleCannotCarryATemplate(t *testing.T) {
	f := newRecurringHTTP(t)
	stop, err := f.store.CreateSchedule(&scheduler.Schedule{
		Name: "end the show", Enabled: true, Action: scheduler.ActionStop, Kind: scheduler.KindWeekly,
		Days: []time.Weekday{time.Tuesday}, AtMinutes: 22 * 60, TZ: "UTC", AllDestinations: true,
	})
	if err != nil {
		t.Fatalf("create stop schedule: %v", err)
	}
	path := "/api/v1/schedules/" + strconv.FormatInt(stop.ID, 10) + "/template"
	send(t, f.h, f.sign, http.MethodPut, path, templateBody(), http.StatusBadRequest)
	send(t, f.h, f.sign, http.MethodGet, path, nil, http.StatusNotFound)
}
