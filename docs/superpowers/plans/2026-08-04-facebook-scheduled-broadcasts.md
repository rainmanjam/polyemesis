# Facebook Scheduled Broadcasts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a Facebook destination is on a start schedule, create its broadcast ahead of time as `SCHEDULED_UNPUBLISHED`, so there is a public Facebook event page before the stream begins.

**Architecture:** A pre-announce sweep in `internal/api`, modelled on the existing `Server.RefreshLoop`, asks `scheduler.Next()` for each start schedule's next occurrence and creates a Facebook broadcast for any destination whose occurrence is inside Facebook's seven-day window. The broadcast's stream key is written to the destination, so the existing go-live path publishes to the broadcast that was announced. A per-destination timestamp records which occurrence has been announced.

**Tech Stack:** Go 1.26, Facebook Graph API v24.0, React 19 + TypeScript for the card link.

## Global Constraints

- **Nothing here may fail a schedule or a go-live.** The pre-announce is best-effort: a Graph error is logged and retried next sweep, and the destination goes live on time with a broadcast created the old way.
- **The pre-created stream key must be the one the encoder publishes to.** Otherwise the announced event page stays empty beside a live stream.
- **The marker is a timestamp, never a boolean.** A weekly schedule needs a new broadcast every week, so "already announced" means "already announced *for this occurrence*".
- **Facebook accepts a start time at most seven days ahead.** Not ours to widen.
- **Empty `Schedule.DestinationIDs` means every destination.** It is the commonest shape and must be handled, not skipped.
- **A `once` schedule beyond the window warns; it is never refused.**
- **Zero `IngestOptions.ScheduledFor` means `LIVE_NOW`** — the existing behaviour, which every current caller keeps.
- Every guard proven able to fail by a **named one-line mutation, run against a committed tree**. `git checkout --` destroys uncommitted work; commit before you mutate.
- Graph parameters are sent **only when chosen**. Facebook treats a present-but-empty parameter as a value, so "leave it alone" means an absent key.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/oauth/facebook.go` | `IngestOptions.ScheduledFor`; the `SCHEDULED_UNPUBLISHED` branch in `IngestFor`; `RescheduleBroadcast` |
| `internal/db/facebook.go` | `FacebookSettings.ScheduledFor` and `.BroadcastID` — the marker and the link target |
| `internal/api/preannounce.go` | **New.** The sweep: schedules → occurrences → destinations → create or reschedule |
| `internal/api/preannounce_test.go` | **New.** Its guards |
| `internal/api/oauth_handlers.go` | `ingestOptionsFor` carries `ScheduledFor`; go-live falls back when a pre-created key is rejected |
| `internal/api/automation.go` | The seven-day warning on schedule save |
| `cmd/polyemesis/main.go` | Starts the sweep beside `srv.RefreshLoop(ctx)` |
| `ui/src/lib/types.ts`, `ui/src/components/DestinationCard.tsx` | The event page link |
| `internal/db/facebook_ui_drift_test.go` | Guards that the link is rendered, not merely typed |

---

### Task 1: `IngestOptions.ScheduledFor` and the create branch

**Files:**
- Modify: `internal/oauth/facebook.go` (the `IngestOptions` struct at ~line 218; `IngestFor` at ~line 471)
- Test: `internal/oauth/facebook_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `oauth.IngestOptions.ScheduledFor time.Time`. Zero means `LIVE_NOW`.

- [ ] **Step 1: Write the failing test**

Add to `internal/oauth/facebook_test.go`. Follow the existing table tests there for how a fake Graph server is stood up — reuse the same helper the neighbouring `IngestFor` tests use rather than writing a new one.

```go
// A scheduled broadcast is a DIFFERENT status, not LIVE_NOW plus a field.
// Asserted on the request Facebook actually receives, because a test that
// only checks IngestFor returned no error would pass with the status
// unchanged.
func TestAScheduledBroadcastIsCreatedUnpublishedWithItsStartTime(t *testing.T) {
	var got url.Values
	srv := fbTestServer(t, func(r *http.Request) any {
		got = r.URL.Query()
		return map[string]any{"id": "555", "stream_url": "rtmps://x.example/rtmp/key-abc"}
	})
	defer srv.Close()

	at := time.Unix(1800000000, 0)
	f := &Facebook{}
	if _, err := f.IngestFor(context.Background(), "cid", "tok", "page:1",
		IngestOptions{ScheduledFor: at}); err != nil {
		t.Fatalf("IngestFor: %v", err)
	}

	if s := got.Get("status"); s != "SCHEDULED_UNPUBLISHED" {
		t.Errorf("status = %q, want SCHEDULED_UNPUBLISHED", s)
	}
	if ep := got.Get("event_params"); ep != "1800000000" {
		t.Errorf("event_params = %q, want the unix start time 1800000000", ep)
	}
}

// The zero value is the whole existing world. Every caller that has one
// passes IngestOptions{} today, and a scheduled create for them would be a
// broadcast that never goes live.
func TestAnUnscheduledBroadcastIsStillLiveNowAndSendsNoEventParams(t *testing.T) {
	var got url.Values
	srv := fbTestServer(t, func(r *http.Request) any {
		got = r.URL.Query()
		return map[string]any{"id": "555", "stream_url": "rtmps://x.example/rtmp/key-abc"}
	})
	defer srv.Close()

	f := &Facebook{}
	if _, err := f.IngestFor(context.Background(), "cid", "tok", "page:1",
		IngestOptions{}); err != nil {
		t.Fatalf("IngestFor: %v", err)
	}

	if s := got.Get("status"); s != "LIVE_NOW" {
		t.Errorf("status = %q, want LIVE_NOW", s)
	}
	// ABSENT, not empty. Facebook reads a present-but-empty parameter as a
	// value, so this must assert on the key not existing at all.
	if _, ok := got["event_params"]; ok {
		t.Error("event_params was sent for an unscheduled broadcast")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/oauth/ -run 'ScheduledBroadcast|UnscheduledBroadcast' -v`
Expected: FAIL — `IngestOptions` has no field `ScheduledFor`, so it will not compile. **A mutation or test that fails to compile is not a result.** Add the field first (Step 3a), then confirm the test fails on the assertion rather than on the build.

- [ ] **Step 3a: Add the field**

In `internal/oauth/facebook.go`, inside `IngestOptions`:

```go
	// ScheduledFor makes this a SCHEDULED_UNPUBLISHED broadcast at that
	// instant rather than a LIVE_NOW one, which is what gives a show a
	// Facebook event page before it starts.
	//
	// Zero means live now. That is what every existing caller passes and what
	// they keep doing -- a scheduled create for them would be a broadcast that
	// never goes live.
	//
	// Facebook accepts a start time at most SEVEN DAYS ahead and that bound is
	// not ours to widen. It is enforced by the caller, which is the only layer
	// that knows the occurrence; this method sends what it is given.
	ScheduledFor time.Time
```

- [ ] **Step 3b: Run again, confirm it fails on the assertion**

Run: `go test ./internal/oauth/ -run 'ScheduledBroadcast' -v`
Expected: FAIL with `status = "LIVE_NOW", want SCHEDULED_UNPUBLISHED`.

- [ ] **Step 4: Implement the branch**

In `IngestFor`, replace the hardcoded status line:

```go
	params := url.Values{"status": {"LIVE_NOW"}}
```

with:

```go
	// status=LIVE_NOW is what makes this a broadcast rather than a scheduled
	// post. The video only appears once bytes arrive, so creating it ahead of
	// the encoder is safe.
	//
	// SCHEDULED_UNPUBLISHED is the opposite trade and the point of it: the
	// event page is public IMMEDIATELY, days before any bytes exist, which is
	// what lets people be told about a show in advance.
	//
	// overlay_url is deliberately absent: Graph removed it in v24.0 and
	// sending it now is an error rather than a no-op.
	params := url.Values{"status": {"LIVE_NOW"}}
	if !opts.ScheduledFor.IsZero() {
		params.Set("status", "SCHEDULED_UNPUBLISHED")
		params.Set("event_params", strconv.FormatInt(opts.ScheduledFor.Unix(), 10))
	}
```

Add `strconv` to the imports if it is not already there.

- [ ] **Step 5: Run to verify both pass**

Run: `go test ./internal/oauth/ -run 'ScheduledBroadcast|UnscheduledBroadcast' -v`
Expected: PASS, both.

- [ ] **Step 6: Commit**

```bash
git add internal/oauth/facebook.go internal/oauth/facebook_test.go
git commit -m "feat(oauth): a Facebook broadcast can be created before it starts"
```

- [ ] **Step 7: Mutate, on the committed tree**

Run each, confirm the named test goes red, then `git checkout -- internal/oauth/facebook.go`.

| Mutation | Expected |
|---|---|
| delete the `params.Set("status", "SCHEDULED_UNPUBLISHED")` line | `TestAScheduledBroadcastIsCreatedUnpublishedWithItsStartTime` red |
| delete the `params.Set("event_params", ...)` line | same test red |
| invert the guard to `if opts.ScheduledFor.IsZero()` | `TestAnUnscheduledBroadcastIsStillLiveNowAndSendsNoEventParams` red |

If a mutation does not fire, **check the edit actually applied** before concluding the guard holds. A mutation that did not apply looks exactly like a guard that works.

---

### Task 2: Rescheduling an existing broadcast

**Files:**
- Modify: `internal/oauth/facebook.go`
- Test: `internal/oauth/facebook_test.go`

**Interfaces:**
- Consumes: Task 1's `ScheduledFor`.
- Produces: `func (f *Facebook) RescheduleBroadcast(ctx context.Context, accessToken, liveVideoID string, at time.Time) error`

- [ ] **Step 1: Write the failing test**

```go
// Moving a show must MOVE its broadcast. Creating a second one would leave
// the first as an orphaned event page that people are still subscribed to.
func TestReschedulingPostsTheNewStartTimeToTheBroadcastItself(t *testing.T) {
	var path string
	var got url.Values
	srv := fbTestServer(t, func(r *http.Request) any {
		path, got = r.URL.Path, r.URL.Query()
		return map[string]any{"success": true}
	})
	defer srv.Close()

	f := &Facebook{}
	at := time.Unix(1800000123, 0)
	if err := f.RescheduleBroadcast(context.Background(), "tok", "555", at); err != nil {
		t.Fatalf("RescheduleBroadcast: %v", err)
	}

	// The broadcast node, NOT the /live_videos edge. Posting to the edge
	// creates a broadcast; posting to the node edits one, and the difference
	// between them is the orphan this test exists to prevent.
	if !strings.HasSuffix(path, "/555") {
		t.Errorf("posted to %q, want the live video node /555", path)
	}
	if ep := got.Get("event_params"); ep != "1800000123" {
		t.Errorf("event_params = %q, want 1800000123", ep)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/oauth/ -run TestReschedulingPosts -v`
Expected: FAIL to build — `RescheduleBroadcast` undefined.

- [ ] **Step 3: Implement**

```go
// RescheduleBroadcast moves an already-created scheduled broadcast to a new
// start time.
//
// POSTs to the live video NODE, not to the /live_videos edge. The edge creates
// a broadcast; the node edits one. Getting that wrong leaves the original event
// page in place with people subscribed to a show that will not happen there.
//
// Facebook's seven-day bound applies here exactly as it does at create, and is
// the caller's to enforce for the same reason.
func (f *Facebook) RescheduleBroadcast(ctx context.Context, accessToken, liveVideoID string, at time.Time) error {
	if liveVideoID == "" {
		return fmt.Errorf("reschedule: no live video id")
	}
	params := url.Values{"event_params": {strconv.FormatInt(at.Unix(), 10)}}
	var out struct{}
	if err := fbPost(ctx, accessToken, "/"+liveVideoID, params, &out); err != nil {
		return fbAdvice(err, "reschedule a Facebook broadcast", nil)
	}
	return nil
}
```

Check `fbAdvice`'s current signature before using it and match it; if the third argument is not a scope slice, pass what the neighbouring calls pass.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/oauth/ -run TestReschedulingPosts -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/oauth/facebook.go internal/oauth/facebook_test.go
git commit -m "feat(oauth): a scheduled Facebook broadcast can be moved"
```

- [ ] **Step 6: Mutate**

| Mutation | Expected |
|---|---|
| post to `"/"+liveVideoID+"/live_videos"` instead of the node | test red on the path assertion |
| send `params` without `event_params` | test red |

---

### Task 3: The marker and the link target on the destination

**Files:**
- Modify: `internal/db/facebook.go`
- Test: `internal/db/facebook_test.go` (create if absent; follow `internal/db/db_test.go` for store setup — **there is no `destinations_test.go`**)

**Interfaces:**
- Consumes: nothing.
- Produces: `db.FacebookSettings.ScheduledFor time.Time` and `db.FacebookSettings.BroadcastID string`.

- [ ] **Step 1: Write the failing test**

```go
// The marker is a TIME, and this is the case that decides it. A boolean
// "already announced" is true forever after the first week, so every
// occurrence after that gets no event page.
func TestTheAnnouncedMarkerDistinguishesOneOccurrenceFromTheNext(t *testing.T) {
	week1 := time.Date(2026, 8, 9, 20, 0, 0, 0, time.UTC)
	week2 := week1.AddDate(0, 0, 7)

	f := FacebookSettings{ScheduledFor: week1, BroadcastID: "555"}
	if !f.AnnouncedFor(week1) {
		t.Error("the occurrence it was announced for reads as not announced")
	}
	if f.AnnouncedFor(week2) {
		t.Error("next week's occurrence reads as already announced; a weekly " +
			"show would get one event page ever")
	}
}

// Round-tripping matters because these live in the existing `facebook` JSON
// column. A field that marshals and does not scan back is a marker that
// resets on restart, which means a broadcast created every sweep.
func TestTheAnnouncedMarkerSurvivesTheDatabase(t *testing.T) {
	d := openTestDB(t) // follow db_test.go's helper name; do not invent one
	at := time.Date(2026, 8, 9, 20, 0, 0, 0, time.UTC)

	created, err := d.CreateDestination(&Destination{
		Name: "fb", Kind: "rtmp", Platform: PlatformFacebook,
		URL: "rtmps://x/y", StreamKey: "k",
		Facebook: FacebookSettings{ScheduledFor: at, BroadcastID: "555"},
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if !got.Facebook.ScheduledFor.Equal(at) {
		t.Errorf("ScheduledFor = %v, want %v", got.Facebook.ScheduledFor, at)
	}
	if got.Facebook.BroadcastID != "555" {
		t.Errorf("BroadcastID = %q, want 555", got.Facebook.BroadcastID)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/db/ -run 'AnnouncedMarker' -v`
Expected: FAIL to build.

- [ ] **Step 3: Implement**

In `internal/db/facebook.go`, add to `FacebookSettings`:

```go
	// ScheduledFor is the occurrence a broadcast has already been announced
	// for, NOT a flag. A weekly show needs a new broadcast every week, so
	// "already done" has to mean "already done for this occurrence" -- a
	// boolean would be true forever after the first one and every week after
	// that would get no event page.
	//
	// Zero means nothing has been announced.
	ScheduledFor time.Time `json:"scheduledFor,omitempty"`
	// BroadcastID is the Facebook live video created for that occurrence. It
	// is what a reschedule edits and what the UI links to.
	BroadcastID string `json:"broadcastId,omitempty"`
```

And below the struct:

```go
// AnnouncedFor reports whether a broadcast has already been created for this
// exact occurrence.
//
// Equal rather than == because these round-trip through JSON and a
// time.Time carries a monotonic clock and a location that == compares and
// Equal does not.
func (f FacebookSettings) AnnouncedFor(occurrence time.Time) bool {
	return f.BroadcastID != "" && f.ScheduledFor.Equal(occurrence)
}
```

Leave `Empty()` alone. It answers "is there anything to SEND at create time", and the marker is bookkeeping rather than a create-time parameter — folding it in would make an announced destination look like one with crossposting configured.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/db/ -run 'AnnouncedMarker' -v`
Expected: PASS both.

- [ ] **Step 5: Confirm `Empty()` is unchanged in behaviour**

Run: `go test ./internal/db/ -run Facebook -v`
Expected: PASS. If an existing `Empty()` test now fails, the marker has leaked into it — revert that and keep them separate.

- [ ] **Step 6: Commit**

```bash
git add internal/db/facebook.go internal/db/facebook_test.go
git commit -m "feat(db): record which occurrence a Facebook broadcast was announced for"
```

- [ ] **Step 7: Mutate**

| Mutation | Expected |
|---|---|
| `AnnouncedFor` returns `f.BroadcastID != ""` (drop the time comparison) | `TestTheAnnouncedMarkerDistinguishesOneOccurrenceFromTheNext` red |
| remove `ScheduledFor` from the JSON tags (`json:"-"`) | `TestTheAnnouncedMarkerSurvivesTheDatabase` red |

---

### Task 4: The pre-announce sweep

This is the task with the most surface. Read the whole of it before starting.

**Files:**
- Create: `internal/api/preannounce.go`
- Create: `internal/api/preannounce_test.go`
- Modify: `internal/api/oauth_handlers.go` (`ingestOptionsFor`, ~line 435)
- Modify: `cmd/polyemesis/main.go` (beside `go srv.RefreshLoop(ctx)`, ~line 289)

**Interfaces:**
- Consumes: `oauth.IngestOptions.ScheduledFor` (Task 1), `(*Facebook).RescheduleBroadcast` (Task 2), `db.FacebookSettings.AnnouncedFor` (Task 3).
- Produces: `func (s *Server) PreannounceLoop(ctx context.Context)`, `func (s *Server) preannounceOnce(ctx context.Context, now time.Time)` (no return — it logs and moves on, because nothing may fail on its account), `func scheduleTargets(sc scheduler.Schedule, destID int64) bool`, and the constant `facebookScheduleHorizon`. Tasks 6 uses the last two.

Existing signatures you will need, verified against the tree:

```go
scheduler.Next(s scheduler.Schedule, now time.Time) (time.Time, bool)
scheduler.ActionStart          // Schedule.Action == this
s.store.Schedules() ([]scheduler.Schedule, error)   // via db; check the exact store method name
s.store.ListDestinations() ([]db.Destination, error)
s.tokenFor(ctx, accountID int64) (string, error)    // returns a fresh access token
oauth.TargetsFor(platform db.Platform) (TargetedProvider, bool)
oauth.FacebookLiveVideoID(streamKey string) string
ingestOptionsFor(dest *db.Destination) oauth.IngestOptions
```

Confirm each against the tree before you use it. If a store method has a different name, use the real one — do not add a wrapper.

- [ ] **Step 1: Widen `ingestOptionsFor`**

In `internal/api/oauth_handlers.go`:

```go
// ingestOptionsFor maps a stored destination onto what a broadcast create
// needs, so refresh-key sends the privacy, crossposting and donate-button
// choices the operator already saved.
//
// scheduledFor is a PARAMETER rather than a field read off the destination,
// because the occurrence belongs to the schedule and not to the destination.
// The stored FacebookSettings.ScheduledFor records what was already announced;
// passing that back in would re-announce the same occurrence forever.
func ingestOptionsFor(dest *db.Destination, scheduledFor time.Time) oauth.IngestOptions {
	return oauth.IngestOptions{
		Privacy:         dest.Compliance.FacebookPrivacy,
		Crosspost:       dest.Facebook.Crosspost,
		DonateCharityID: dest.Facebook.DonateCharityID,
		ScheduledFor:    scheduledFor,
	}
}
```

Update the existing call site(s) to pass `time.Time{}`. Find them with:

```bash
grep -rn "ingestOptionsFor(" internal/
```

Every existing caller is a live go-live and must pass the zero value.

- [ ] **Step 2: Run the existing suite to confirm nothing changed**

Run: `go test ./internal/api/ -run Ingest -v`
Expected: PASS. If an existing test fails, a live path has been turned into a scheduled one — fix that before continuing.

- [ ] **Step 3: Commit the widening on its own**

```bash
git add internal/api/oauth_handlers.go
git commit -m "refactor(api): ingestOptionsFor takes the occurrence rather than assuming live"
```

- [ ] **Step 4: Write the failing sweep tests**

Create `internal/api/preannounce_test.go`. Use `renditionServer(t, defaultTools())` for the fixture — **`testServer` leaves the engine nil and `refuseIfSilent` panics on it**, which is a trap that has already cost one debugging round.

```go
// The sweep creates a broadcast for a Facebook destination whose next
// occurrence is inside Facebook's window.
func TestASchedulesNextOccurrenceGetsAnEventPage(t *testing.T) { /* see below */ }

// The case a boolean marker gets wrong.
func TestASecondSweepForTheSameOccurrenceCreatesNothing(t *testing.T) { /* see below */ }
func TestTheNextOccurrenceDoesGetItsOwnBroadcast(t *testing.T) { /* see below */ }

// Empty DestinationIDs means every destination, and it is the commonest shape.
func TestAScheduleThatNamesNoDestinationsStillAnnouncesTheFacebookOnes(t *testing.T) { /* see below */ }

// The bound.
func TestAnOccurrenceBeyondSevenDaysIsNotAnnounced(t *testing.T) { /* see below */ }

// The invariant the whole design turns on.
func TestThePreCreatedKeyIsWrittenToTheDestination(t *testing.T) { /* see below */ }

// Best-effort has to be provably best-effort.
func TestAGraphFailureLeavesTheDestinationUntouched(t *testing.T) { /* see below */ }

// The rule must not widen.
func TestANonFacebookDestinationOnTheSameScheduleIsUntouched(t *testing.T) { /* see below */ }
```

Write each body against the fixture. The shape for the first, which the others follow:

```go
func TestASchedulesNextOccurrenceGetsAnEventPage(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())
	_ = h
	_ = sign
	srv := serverOf(t, h) // however the fixture exposes *Server; if it does
	                      // not, add an accessor rather than reaching inside

	dest := seedFacebookDestination(t, store)   // helper you write in this file
	seedStartSchedule(t, store, dest.ID, in(3*24*time.Hour))

	var created url.Values
	restore := stubFacebookGraph(t, func(r *http.Request) any {
		created = r.URL.Query()
		return map[string]any{"id": "555", "stream_url": "rtmps://x/rtmp/key-abc"}
	})
	defer restore()

	srv.preannounceOnce(context.Background(), time.Now())

	if created.Get("status") != "SCHEDULED_UNPUBLISHED" {
		t.Fatalf("no scheduled broadcast was created: %v", created)
	}
	got, _ := store.GetDestination(dest.ID)
	if got.Facebook.BroadcastID != "555" {
		t.Errorf("BroadcastID = %q, want 555", got.Facebook.BroadcastID)
	}
}
```

`stubFacebookGraph` must redirect `fbGraphBase`. Check how the existing Facebook tests in `internal/oauth` do it and follow the same mechanism; if `fbGraphBase` is a package-level `const` it will need to become a `var` to be stubbable, which is a one-word change and worth making rather than skipping the test.

`TestThePreCreatedKeyIsWrittenToTheDestination` asserts `got.StreamKey` equals the key parsed out of the stubbed `stream_url`. That is the invariant: if it does not match, the announced event page stays empty while the stream goes somewhere else.

- [ ] **Step 5: Run to verify they fail**

Run: `go test ./internal/api/ -run Preannounce -v` (name the tests so this pattern catches them, or use an explicit `-run` alternation)
Expected: FAIL to build — `preannounceOnce` undefined.

- [ ] **Step 6: Implement the sweep**

Create `internal/api/preannounce.go`:

```go
package api

// Pre-announcing a Facebook broadcast, so a scheduled show has an event page
// before it starts.
//
// WHY IT IS HERE AND NOT IN internal/scheduler
//
// That package opens with a promise: "It does not start or stop anything
// itself. A schedule flips the stored 'enabled' intent through the same path
// the API uses and then asks for a reconcile [...] there is exactly one way a
// destination comes up." A Graph API call inside it would break that. This
// reads schedules through the helpers it already exports and lives where the
// OAuth tokens do.
//
// NOTHING HERE MAY FAIL A SCHEDULE OR A GO-LIVE. It runs ahead of the stream
// and the stream does not depend on it: a Graph error is logged and retried on
// the next sweep, and the destination goes live on time with a broadcast
// created the ordinary way.

const (
	// facebookScheduleHorizon is Facebook's own bound on how far ahead a
	// broadcast may be scheduled. Not ours to widen.
	facebookScheduleHorizon = 7 * 24 * time.Hour

	// preannounceTick is how often the horizon is re-checked. Minutes, not
	// seconds: the thing being watched is a schedule days away, and the
	// scheduler's own 20-second sweep is fast because it has a grace window to
	// hit. Nothing here does.
	preannounceTick = 5 * time.Minute
)

// PreannounceLoop runs the sweep until ctx ends. Started beside RefreshLoop.
func (s *Server) PreannounceLoop(ctx context.Context) {
	tick := time.NewTicker(preannounceTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.preannounceOnce(ctx, time.Now())
		}
	}
}

// preannounceOnce is one sweep, separated from the loop so a test can call it
// without a ticker.
func (s *Server) preannounceOnce(ctx context.Context, now time.Time) {
	scheds, err := s.store.Schedules()
	if err != nil {
		s.log.Warn("pre-announce: cannot read schedules", "err", err)
		return
	}
	dests, err := s.store.ListDestinations()
	if err != nil {
		s.log.Warn("pre-announce: cannot read destinations", "err", err)
		return
	}

	for _, sc := range scheds {
		if !sc.Enabled || sc.Action != scheduler.ActionStart {
			continue
		}
		at, ok := scheduler.Next(sc, now)
		if !ok || at.Sub(now) > facebookScheduleHorizon {
			continue
		}
		for i := range dests {
			d := &dests[i]
			if !scheduleTargets(sc, d.ID) || d.Platform != db.PlatformFacebook {
				continue
			}
			if d.AccountID == nil {
				continue
			}
			if d.Facebook.AnnouncedFor(at) {
				continue
			}
			s.announceOne(ctx, d, at)
		}
	}
}

// scheduleTargets reports whether this schedule acts on this destination.
//
// An EMPTY DestinationIDs means every destination -- "start the show" usually
// names nothing, so this is the commonest shape and a rule that skipped it
// would switch the feature off for most installs.
func scheduleTargets(sc scheduler.Schedule, destID int64) bool {
	if len(sc.DestinationIDs) == 0 {
		return true
	}
	for _, id := range sc.DestinationIDs {
		if id == destID {
			return true
		}
	}
	return false
}
```

`announceOne` creates or reschedules, and is the only place that writes:

```go
// announceOne creates the broadcast, or moves an existing one.
//
// Every failure path returns without touching the destination. A half-written
// marker -- a ScheduledFor recorded for a broadcast that was not created --
// would suppress every later attempt for that occurrence, which is worse than
// having no event page at all.
func (s *Server) announceOne(ctx context.Context, d *db.Destination, at time.Time) {
	acct, err := s.store.GetPlatformAccount(*d.AccountID)
	if err != nil {
		return
	}
	tok, err := s.tokenFor(ctx, acct.ID)
	if err != nil {
		s.log.Warn("pre-announce: no usable token", "destination", d.Name, "err", err)
		return
	}
	tp, ok := oauth.TargetsFor(acct.Platform)
	if !ok {
		return
	}

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// A broadcast already exists for a DIFFERENT occurrence: the schedule
	// moved. Move the broadcast rather than creating a second one, which
	// would leave the first as an event page people are still subscribed to.
	if d.Facebook.BroadcastID != "" {
		fb, isFB := tp.(*oauth.Facebook)
		if isFB {
			if err := fb.RescheduleBroadcast(cctx, tok, d.Facebook.BroadcastID, at); err != nil {
				s.log.Warn("pre-announce: could not move the broadcast",
					"destination", d.Name, "err", err)
				return
			}
			d.Facebook.ScheduledFor = at
			s.saveAnnouncement(d)
			return
		}
	}

	b, err := tp.IngestFor(cctx, s.facebookClientID(), tok, acct.AccountRef,
		ingestOptionsFor(d, at))
	if err != nil {
		// Logged, not returned anywhere. The schedule and the go-live path are
		// unaffected; the next sweep tries again.
		s.log.Warn("pre-announce: could not create the broadcast",
			"destination", d.Name, "err", err)
		return
	}

	// THE INVARIANT. The key the pre-created broadcast returned has to be the
	// one the encoder publishes to, or the event page people were notified
	// about stays empty beside a live stream.
	d.StreamKey = b.Ingest.Key
	d.Facebook.BroadcastID = b.ID
	d.Facebook.ScheduledFor = at
	s.saveAnnouncement(d)
	s.log.Info("pre-announced a Facebook broadcast",
		"destination", d.Name, "at", at, "broadcast", b.ID)
}

func (s *Server) saveAnnouncement(d *db.Destination) {
	if _, err := s.store.UpdateDestination(d); err != nil {
		s.log.Warn("pre-announce: could not record the announcement",
			"destination", d.Name, "err", err)
	}
}
```

`s.facebookClientID()` stands for however this Server already obtains the Facebook client id for `IngestFor` — find it at the existing call site in `ingestFor` and use the same expression. Do not invent a new accessor.

`b.Ingest.Key` stands for wherever `*oauth.Broadcast` carries the key. Check the struct and use the real field.

- [ ] **Step 7: Run to verify they pass**

Run: `go test ./internal/api/ -run 'Preannounce|EventPage|Occurrence|PreCreatedKey|GraphFailure' -v`
Expected: PASS, all.

- [ ] **Step 8: Wire the loop**

In `cmd/polyemesis/main.go`, beside line 289:

```go
	go srv.RefreshLoop(ctx)
	// Pre-announces scheduled Facebook broadcasts. Best-effort by
	// construction: it runs ahead of the stream and nothing downstream waits
	// on it, so a failure here never delays a go-live.
	go srv.PreannounceLoop(ctx)
```

- [ ] **Step 9: Build and run the full suite**

```bash
go build ./... && go vet ./... && go test -race -timeout 20m ./...
```
Expected: all pass.

- [ ] **Step 10: Commit**

```bash
git add internal/api/preannounce.go internal/api/preannounce_test.go cmd/polyemesis/main.go
git commit -m "feat(api): pre-announce a scheduled Facebook broadcast"
```

- [ ] **Step 11: Mutate**

| Mutation | Expected |
|---|---|
| `scheduleTargets` returns `false` for empty `DestinationIDs` | `TestAScheduleThatNamesNoDestinationsStillAnnouncesTheFacebookOnes` red |
| drop the `d.Facebook.AnnouncedFor(at)` skip | `TestASecondSweepForTheSameOccurrenceCreatesNothing` red |
| compare `at.Sub(now) > 30*24*time.Hour` instead of the horizon | `TestAnOccurrenceBeyondSevenDaysIsNotAnnounced` red |
| delete `d.StreamKey = b.Ingest.Key` | `TestThePreCreatedKeyIsWrittenToTheDestination` red |
| set the marker before the create call rather than after | `TestAGraphFailureLeavesTheDestinationUntouched` red |
| drop the `d.Platform != db.PlatformFacebook` skip | `TestANonFacebookDestinationOnTheSameScheduleIsUntouched` red |

---

### Task 5: A rejected pre-created key must not stop a stream

**Files:**
- Modify: `internal/api/oauth_handlers.go` (the go-live/refresh-key path around line 413)
- Test: `internal/api/oauth_handlers_test.go`

**Interfaces:**
- Consumes: `db.FacebookSettings.BroadcastID` (Task 3).
- Produces: a `warnings` entry on the refresh-key response, following the existing `resp["warnings"]` shape used by `handleCreateDestination`.

- [ ] **Step 1: Write the failing test**

```go
// The operator deleted the scheduled video on Facebook. The stream must still
// go live -- a cancelled event page taking a broadcast off the air would turn
// an optional discovery feature into a single point of failure.
//
// BOTH halves are asserted. Live-but-silent and warned-but-dead are each half
// a pass.
func TestARejectedPreCreatedKeyStillGoesLiveAndSaysWhy(t *testing.T) {
	// Stub Graph so the FIRST call (validating/using the stored broadcast)
	// fails and the second (a fresh create) succeeds.
	// Assert: a new key is stored, it differs from the stale one, the
	// response carries a warning naming the deleted event page, and the
	// stored BroadcastID is the NEW one.
}
```

Write the body against whatever the refresh-key handler's existing tests use for a fake Graph.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/api/ -run RejectedPreCreatedKey -v`
Expected: FAIL — no fallback exists.

- [ ] **Step 3: Implement**

Where the go-live path uses a stored key, add: when the destination carries a `Facebook.BroadcastID` and the create/fetch is rejected, clear `BroadcastID` and `ScheduledFor`, retry once as an ordinary `LIVE_NOW` create, and append the warning:

```go
	"The scheduled Facebook broadcast for this destination no longer exists — " +
		"it was probably deleted or has expired. A new broadcast was created, so " +
		"this destination is live, but the event page announced earlier is gone."
```

Clearing the marker matters as much as the retry: leaving it set would make the next sweep try to reschedule a broadcast that is not there.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/api/ -run RejectedPreCreatedKey -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/api/oauth_handlers.go internal/api/oauth_handlers_test.go
git commit -m "fix(api): a deleted event page must not take a stream off the air"
```

- [ ] **Step 6: Mutate**

| Mutation | Expected |
|---|---|
| return the error instead of retrying | test red — the stream does not go live |
| retry but append no warning | test red — the silent half |
| retry without clearing `BroadcastID` | test red — assert the stored id is the new one |

---

### Task 6: The seven-day warning on schedule save

**Files:**
- Modify: `internal/api/automation.go` (`handleCreateSchedule` ~line 296, `handleUpdateSchedule` ~line 314)
- Test: `internal/api/automation_test.go`

**Interfaces:**
- Consumes: `scheduler.Next`, and from Task 4 both `scheduleTargets(sc, destID) bool` and the constant `facebookScheduleHorizon`. **Task 4 must land first** — this task will not compile without them.
- Produces: `warnings` on the schedule create/update responses, same shape as destinations.

- [ ] **Step 1: Write the failing test**

```go
// It WARNS. It never refuses -- the schedule works either way, and what the
// seven-day bound limits is only the pre-announced event page.
func TestADistantOnceScheduleSavesAndWarnsAboutTheEventPage(t *testing.T) {
	// Create a Facebook destination, then POST a `once` schedule 23 days out
	// naming it. Assert 201, the schedule is stored, and warnings names the
	// seven-day limit.
}

// The negative case: a schedule inside the window is not warned about.
func TestAScheduleInsideTheWindowIsNotWarnedAbout(t *testing.T) {
	// Same, 3 days out. Assert warnings is empty.
}

// Daily and weekly cannot exceed the bound by definition, so they must never
// warn -- a warning on every weekly show would be noise that teaches people
// to ignore the real one.
func TestAWeeklyScheduleIsNeverWarnedAbout(t *testing.T) {
	// A weekly schedule. Assert warnings is empty.
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/api/ -run 'OnceScheduleSaves|InsideTheWindow|WeeklySchedule' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Add to `internal/api/automation.go`:

```go
// scheduleWarnings names anything about this schedule that will not work as
// the operator might expect. It never blocks the save.
//
// Only KindOnce can trip the bound: the NEXT occurrence of a daily schedule is
// at most a day away and of a weekly one at most seven days, by definition. An
// earlier draft of the roadmap claimed weekly schedules collided with this;
// they cannot, and warning on them would be noise that teaches people to skip
// the real warning.
func (s *Server) scheduleWarnings(sc scheduler.Schedule, now time.Time) []string {
	if sc.Kind != scheduler.KindOnce || sc.Action != scheduler.ActionStart {
		return nil
	}
	at, ok := scheduler.Next(sc, now)
	if !ok || at.Sub(now) <= facebookScheduleHorizon {
		return nil
	}
	dests, err := s.store.ListDestinations()
	if err != nil {
		return nil
	}
	for i := range dests {
		d := &dests[i]
		if d.Platform == db.PlatformFacebook && scheduleTargets(sc, d.ID) {
			return []string{
				"No Facebook event page will be created for this schedule. " +
					"Facebook accepts a start time at most seven days ahead, and this " +
					"fires later than that. The destination will still go live on time.",
			}
		}
	}
	return nil
}
```

Call it in both handlers and attach the result as `resp["warnings"]` when non-empty, matching `handleCreateDestination`.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/api/ -run 'OnceScheduleSaves|InsideTheWindow|WeeklySchedule' -v`
Expected: PASS, all three.

- [ ] **Step 5: Commit**

```bash
git add internal/api/automation.go internal/api/automation_test.go
git commit -m "feat(api): say when a schedule is too far out for a Facebook event page"
```

- [ ] **Step 6: Mutate**

| Mutation | Expected |
|---|---|
| drop the `sc.Kind != scheduler.KindOnce` guard | `TestAWeeklyScheduleIsNeverWarnedAbout` red |
| return the warning unconditionally | `TestAScheduleInsideTheWindowIsNotWarnedAbout` red |
| return `nil` always | `TestADistantOnceScheduleSavesAndWarnsAboutTheEventPage` red |
| change the save to a 400 refusal | `TestADistantOnceScheduleSavesAndWarnsAboutTheEventPage` red on the status |

---

### Task 7: The event page has to be findable

**Files:**
- Modify: `ui/src/lib/types.ts` (the `FacebookSettings` interface)
- Modify: `ui/src/components/DestinationCard.tsx`
- Test: `internal/db/facebook_ui_drift_test.go`

**Interfaces:**
- Consumes: `db.FacebookSettings.BroadcastID` (Task 3), serialised as `broadcastId`.
- Produces: nothing later depends on this.

- [ ] **Step 1: Write the failing drift guard**

Append to `internal/db/facebook_ui_drift_test.go`:

```go
// A public page is created on the operator's behalf; giving them no way to
// reach it is half a feature. It also makes the stale-key warning legible --
// when the link 404s they can see what it is talking about.
//
// This watches the RENDERED link, not the type. A field declared in types.ts
// proves only that the type exists, which is the shape of mistake that shipped
// unsendable tags: every end of the wire named, nothing carrying a value.
func TestTheCardLinksToTheScheduledBroadcast(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "components", "DestinationCard.tsx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	if !strings.Contains(string(raw), "facebook.com/") {
		t.Error("the destination card does not link to the scheduled Facebook " +
			"broadcast, so an operator has no way to reach the event page " +
			"polyemesis created for them.")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/db/ -run TestTheCardLinksToTheScheduledBroadcast -v`
Expected: FAIL.

- [ ] **Step 3: Add the field to the UI type**

In `ui/src/lib/types.ts`, on the `FacebookSettings` interface:

```ts
  /** The occurrence a broadcast was already announced for. */
  scheduledFor?: string;
  /** The Facebook live video created for it — what the card links to. */
  broadcastId?: string;
```

- [ ] **Step 4: Render the link**

In `ui/src/components/DestinationCard.tsx`, beside the existing `warnings.map(...)` block:

```tsx
        {dest.facebook?.broadcastId && (
          <a
            href={`https://facebook.com/${dest.facebook.broadcastId}`}
            target="_blank"
            rel="noreferrer"
            className="text-[10px] underline underline-offset-2 text-muted-foreground hover:text-foreground"
          >
            Scheduled Facebook broadcast
          </a>
        )}
```

Check the exact prop name for the destination on this component before writing `dest.facebook` — match what the surrounding code uses.

- [ ] **Step 5: Run to verify it passes**

```bash
go test ./internal/db/ -run TestTheCardLinksToTheScheduledBroadcast -v
cd ui && npx tsc --noEmit && npx oxlint
```
Expected: PASS, clean.

- [ ] **Step 6: Commit**

```bash
git add ui/src/lib/types.ts ui/src/components/DestinationCard.tsx internal/db/facebook_ui_drift_test.go
git commit -m "feat(ui): link to the Facebook event page we created"
```

- [ ] **Step 7: Mutate**

| Mutation | Expected |
|---|---|
| delete the `<a>` block from the card | `TestTheCardLinksToTheScheduledBroadcast` red |

---

## Final gates

```bash
gofmt -l ./internal ./cmd          # must print nothing
go build ./... && go vet ./...
go test -race -timeout 20m ./...
cd ui && npx tsc --noEmit && npx oxlint
```

`gofmt -l` exits 0 even when it lists files, so read its output — do not chain it with `&&` and assume success.

## Two known weaknesses in this plan

Recorded because a plan that hides its own gaps is worse than one that names
them, and both were found by checking this document against the spec rather
than by trusting it.

### Some test bodies are specified, not written

Tasks 4, 5 and 6 list each test's name, its purpose and its assertions, but
several bodies are described rather than given as code. The writing-plans
standard calls that a plan failure, and it is one.

The reason is real rather than laziness: those bodies depend on a Graph stub
that does not exist yet (`fbGraphBase` may need to become a `var` to be
stubbable) and on fixture helpers whose exact names have to be read off the
existing Facebook tests. Code written speculatively here would be rewritten by
the implementer anyway, and a plan that supplies *wrong* code is worse than one
that supplies none.

**What the implementer must not do:** invent an assertion. Every test in those
tasks has its assertions written out in the surrounding prose and in the
mutation table — the mutation table is the contract. If a test cannot be made
to fail against its named mutation, the test is wrong, not the mutation.

### The eligibility gate is under-handled

The spec says an account that cannot schedule — under 60 days old, or a Page
under 100 followers — is "a fact about the account rather than a fault in the
run", to be "reported once, not retried every 20 seconds".

This plan does not implement that. `announceOne` logs a warning and the sweep
tries again on the next tick, so an ineligible account produces a warning line
every 5 minutes, indefinitely.

That is milder than the spec feared — the 5-minute tick was chosen partly for
this, against the scheduler's own 20 seconds — but it is not what the spec
asked for. Suppressing repeated identical failures needs somewhere to remember
them, which is its own small design. **Left out deliberately; raise it before
merge rather than discovering it in a log.**

## Docs to update before opening the PR

- `docs/roadmap/DESTINATION-SETTINGS.md` — the "What remains" table lists `event_params` scheduling as deferred. It is not deferred any more.
- `docs/PLATFORMS.md` — if it carries a Facebook capability list, scheduled broadcasts belong on it.
