package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// A DESTINATION IS ANSWERED FOR BY ITS OWN PROGRAMME'S ENGINE, NOT BY THE FIRST.
//
// s.eng() is s.mgr.Default(), which is Engines()[0] -- the right engine only on
// an install with one source. applyDestinationEnabled already carries this
// finding for the enable path ("EVERY ENGINE, NOT THE DEFAULT ONE ... The bug is
// invisible on a single-source install, which is every development box"). The
// same shape was still live in three more places, and they differ in how loudly
// they fail:
//
//   - handleRestartDestination asked the default engine to restart an id it had
//     never heard of, turned the refusal into a 500, and left the destination
//     the operator was trying to restart running exactly as it was.
//   - handleListDestinations, handleGetDestination and handleUpdateDestination
//     compiled every destination's audio routing against the DEFAULT source's
//     track layout. routing.Source carries Tracks, and two sources are two
//     ingests that routinely differ -- a six-track OBS feed and a stereo camera
//     -- so the "Tracks 1, 2, 4 -> stereo" summary on the dashboard described
//     the wrong programme, or failed as a routingError for tracks that are
//     present in the source that destination actually carries.
//
// The second kind is the dangerous one: it renders as a plausible answer.
func TestTheEngineForADestinationIsItsOwnProgrammeNotTheFirst(t *testing.T) {
	s, _, _, _ := managerServer(t, defaultTools())

	first := s.eng()
	if first == nil {
		t.Fatal("the fixture started no engine, so there is no default to be wrongly " +
			"preferred and every assertion below would pass on nil")
	}

	// A second programme. Without it this test cannot distinguish "asked the
	// owning engine" from "asked the only engine", which is exactly the
	// single-source blindness that hid the bug.
	second := &db.Source{Name: "second programme", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := s.store.CreateSource(second); err != nil {
		t.Fatalf("create second source: %v", err)
	}
	if err := s.mgr.Sync(); err != nil {
		t.Fatalf("sync after creating a second source: %v", err)
	}
	if got := len(s.mgr.Engines()); got < 2 {
		t.Fatalf("the manager runs %d engine(s) after a second source was created; "+
			"this test needs two or it is asserting nothing", got)
	}
	if second.ID == first.SourceID() {
		t.Fatalf("both sources report id %d, so the lookup cannot be shown to "+
			"discriminate", second.ID)
	}

	// THE ASSERTION. Resolved by the destination's source id, this must be the
	// second programme's engine -- and must NOT be the default, which is what
	// the code did before.
	got := s.engineForSource(&second.ID)
	if got == nil {
		t.Fatalf("no engine resolved for source %d, which is running", second.ID)
	}
	if got.SourceID() != second.ID {
		t.Errorf("engineForSource(%d) answered the engine for source %d -- the "+
			"destination would be restarted on, and compiled against, the wrong "+
			"programme", second.ID, got.SourceID())
	}
	if got == first {
		t.Errorf("engineForSource(%d) answered the DEFAULT engine (source %d). That is "+
			"the bug: s.mgr.Default() is Engines()[0] and is only ever right by "+
			"coincidence on an install with one source", second.ID, first.SourceID())
	}

	// THE POSITIVE CONTROL. The first source must still resolve to the first
	// engine -- a lookup that returned nil, or the wrong engine both ways,
	// would satisfy the inequality above while being more broken.
	if back := s.engineForSource(&[]int64{first.SourceID()}[0]); back != first {
		t.Errorf("the DEFAULT source no longer resolves to the default engine; " +
			"the lookup is not discriminating, it is just wrong in a new direction")
	}
}

// A source id naming no running engine must answer nil rather than falling back
// to the default, because the fallback IS the bug: it restarts, and compiles
// against, a programme the caller did not ask about. The handlers turn this nil
// into a 409 rather than dereferencing it.
func TestAnUnknownSourceResolvesToNoEngineRatherThanTheDefault(t *testing.T) {
	s, _, _, _ := managerServer(t, defaultTools())
	if s.eng() == nil {
		t.Fatal("no engine in the fixture, so a fallback to the default could not be " +
			"observed even if it happened")
	}
	missing := int64(0x7fffffff)
	if got := s.engineForSource(&missing); got != nil {
		t.Errorf("source %d is not running, yet an engine was returned (source %d). "+
			"A fallback here silently addresses the wrong programme", missing, got.SourceID())
	}
	if got := s.engineForSource(nil); got != nil {
		t.Errorf("a nil source id resolved to an engine (source %d); a destination with "+
			"no programme belongs to none", got.SourceID())
	}
}

// RESTARTING A DESTINATION WHOSE PROGRAMME IS NOT RUNNING SAYS SO, rather than
// dereferencing nothing or restarting somebody else's.
//
// engineForSource answers nil for a source with no engine -- a programme
// deleted under this request, or one the manager has not reconciled yet. The
// handler turns that into a 409.
//
// WHAT IT REPLACED, MEASURED RATHER THAN ASSUMED. The first draft of this
// comment said the old code answered 500. It does not. Restoring the fallback
// and running this test gives:
//
//	status = 200, body {"status":"restarting"}
//
// The default engine ACCEPTS a restart for a destination it does not own and
// reports success. So the failure was never an error an operator could see: it
// was a green answer to a request that restarted nothing, or restarted
// somebody else's destination if the ids happened to line up. That is why this
// asserts the status rather than merely asserting "not 500".
//
// The fixture creates the source WITHOUT a manager sync, which is exactly the
// "not reconciled yet" case rather than a contrived one: the manager learns
// about sources on Sync, so a row written between two syncs genuinely has no
// engine.
func TestRestartingADestinationWhoseProgrammeIsNotRunningIsRefused(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	if s.eng() == nil {
		t.Fatal("no engine in the fixture, so requireSource would answer before the " +
			"branch under test is reached")
	}

	// A programme the manager has not been told about.
	unsynced := &db.Source{Name: "not yet running", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := s.store.CreateSource(unsynced); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if got := s.mgr.Engine(unsynced.ID); got != nil {
		t.Fatalf("source %d has an engine without a sync, so this test cannot reach "+
			"the branch it is named for", unsynced.ID)
	}

	// A destination on it, written through the store because the API would
	// reconcile and give the source an engine.
	dst, err := s.store.CreateDestination(&db.Destination{
		Name: "orphan", Kind: db.DestRTMP, Platform: db.PlatformCustom,
		URL: "rtmp://example.invalid/live", StreamKey: "k", SourceID: &unsynced.ID,
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}

	r := jsonRequest(t, http.MethodPost, "/api/v1/destinations/"+strconv.FormatInt(dst.ID, 10)+"/restart", nil)
	sign(r)
	w := do(t, h, r)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409. A restart aimed at a programme that is not "+
			"running must say so. Measured with the fallback restored, this answers "+
			"200 {\"status\":\"restarting\"} -- a green response to a restart that "+
			"reached the wrong engine (body %s)", w.Code, w.Body.String())
	}
	if b := strings.ToLower(w.Body.String()); !strings.Contains(b, "source") {
		t.Errorf("the refusal does not mention the source, so it names no cause: %s", b)
	}

	// THE POSITIVE CONTROL. A destination on a programme that IS running must
	// still restart -- a handler that 409'd every restart would satisfy the
	// assertions above while being useless.
	live := createDestination(t, h, sign, destinationBody("running", false, nil))
	r2 := jsonRequest(t, http.MethodPost, "/api/v1/destinations/"+strconv.FormatInt(live.ID, 10)+"/restart", nil)
	sign(r2)
	if w2 := do(t, h, r2); w2.Code == http.StatusConflict {
		t.Errorf("a destination on a RUNNING programme was also refused with 409, so "+
			"the check is not discriminating (body %s)", w2.Body.String())
	}
}
