package api

import (
	"encoding/json"
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

// A CREATE THAT NAMES NO PROGRAMME IS REFUSED, and the server no longer picks
// one on the operator's behalf.
//
// db.CreateDestination fills an omitted source_id with DefaultSourceID(), the
// first source by id. On a one-source install that is the only possible answer.
// On an install with several it is a choice the operator never made and was
// never shown: the destination is created, attached to a programme nobody
// picked, and nothing on screen says which. It then carries the wrong feed, or
// sits idle while its intended programme streams without it.
//
// DELIBERATELY UNCONDITIONAL. An earlier draft refused only when more than one
// source existed, which costs no test churn and protects the only case where
// the choice is ambiguous. It was rejected: a rule that changes with hidden
// state is a mode, and an operator who learns "sourceId is optional" on their
// first install meets a different API on their second. One rule.
//
// This does NOT go through send() or createDestination(), both of which fill
// the field in for the tests that are not about it. Going through them would
// mean this test could not observe the thing it exists to observe.
func TestACreateThatNamesNoSourceIsRefused(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())

	// A body per endpoint: the decoder rejects unknown fields, so one shared
	// map would be refused for the wrong reason and this test would pass while
	// asserting nothing about sources.
	for _, tc := range []struct {
		path, what string
		body       map[string]any
	}{
		{"/api/v1/destinations", "destination", map[string]any{
			"name": "unnamed programme", "kind": "rtmp",
			"url": "rtmp://example.invalid/live", "streamKey": "k"}},
		{"/api/v1/renditions", "rendition", map[string]any{
			"name": "unnamed programme", "height": 720, "videoBitrate": 3000}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			r := jsonRequest(t, http.MethodPost, tc.path, tc.body)
			sign(r)
			w := do(t, h, r)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: the server chose a programme the "+
					"operator did not (body %s)", w.Code, w.Body.String())
			}
			if got := noSourceCode(t, w.Body); got != codeSourceRequired {
				t.Errorf("code = %q, want %q. The UI branches on this to open the "+
					"source picker rather than show a red toast", got, codeSourceRequired)
			}
			// The refusal has to say what to pick, not just that something is
			// missing. "sourceId is required" tells an operator what they failed
			// to type; naming the programmes tells them what to type instead.
			if b := w.Body.String(); !strings.Contains(b, "Available:") {
				t.Errorf("the refusal lists no programmes to choose from, so it names "+
					"no way forward: %s", b)
			}
		})
	}

	// THE POSITIVE CONTROL. Every assertion above is "this is refused", and a
	// handler that refused every create would satisfy them all while being far
	// worse than the bug.
	t.Run("positive control: naming one is accepted", func(t *testing.T) {
		got := createDestination(t, h, sign, destinationBody("named", false, nil))
		if got == nil || got.SourceID == nil {
			t.Fatal("a create that named a source was not accepted, so the refusals " +
				"above prove nothing about the guard")
		}
	})
}

// THE "WOULD CARRY NO AUDIO" REFUSAL IS COMPILED AGAINST THE DESTINATION'S OWN
// PROGRAMME (#527).
//
// refuseIfSilent is the guard whose own error text calls streaming silence "the
// one failure this product exists to prevent". It took a routing.Profile and
// nothing else, so it compiled that profile against s.engOrNil().Source() --
// the DEFAULT engine's measured layout -- while its three sibling destination
// routes all resolve sourceForDestination(row.SourceID). The caller had the
// owner in hand: handleCreateDestination proves row.SourceID is present with
// requireNamedSource on the line above, and then threw it away.
//
// Both directions are silent. This test drives the FALSE ACCEPT, because that
// is the one that reaches air: a profile that is fine against Main compiles to
// audio, the guard passes, the row is stored, and the destination goes live
// carrying nothing.
//
// THE DISCRIMINATOR IS TRACK ROLES, and it is not contrived -- it is the
// mechanism #527 names. Annotations are stored per SOURCE (that is the whole of
// #497's headline), routing.Compile resolves ExcludeRoles against the
// annotations on the layout it is handed, and two programmes therefore give
// different answers for the same profile. Ask the wrong programme and the
// exclusion finds nothing to exclude.
//
// MEASURED WITH THE FALLBACK RESTORED: with `src := s.engOrNil().Source()` the
// create below answers 201 and the destination is stored. That is the bug --
// not an error anybody sees, a green response to a destination that will
// publish silence.
func TestASilentDestinationIsRefusedAgainstItsOwnProgrammesTracks(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	first := s.eng()
	if first == nil {
		t.Fatal("no default engine in the fixture, so there is no wrong programme to be " +
			"asked and this test would pass having observed nothing")
	}
	second := secondProgramme(t, s)

	// Studio B's track 0 is the music bus. Main's track 0 is annotated by
	// nobody, which is what makes the two programmes answer differently.
	send(t, h, sign, http.MethodPut,
		"/api/v1/source/annotations?source="+itoa(second.ID),
		map[string]any{"annotations": []map[string]any{
			{"track": 0, "label": "music bus", "role": "music"},
		}}, http.StatusOK)

	// A destination on Studio B that selects track 0 and excludes the music
	// role. Against Studio B's layout that leaves nothing: ErrNoAudio, and the
	// operator has to be told before it is stored. Against MAIN's layout --
	// where track 0 carries no role at all -- the exclusion drops nothing and
	// the profile compiles to audio.
	body := map[string]any{
		"name": "studio b silence", "kind": "rtmp", "platform": "custom",
		"url": "rtmp://example.invalid/live", "streamKey": "k",
		"sourceId": second.ID,
		"profile": map[string]any{
			"mode": "simple", "normalize": "auto", "sampleRate": 48000,
			"excludeRoles": []string{"music"},
			"tracks": []map[string]any{
				{"track": 0, "enabled": true, "gain": 1.0},
			},
		},
	}

	r := jsonRequest(t, http.MethodPost, "/api/v1/destinations", body)
	sign(r)
	w := do(t, h, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. This destination carries nothing but a track "+
			"Studio B's own role policy excludes, and it was accepted because the guard "+
			"compiled it against programme %d's tracks instead. Measured with the "+
			"fallback restored this is 201 and the row is stored: body %s",
			w.Code, first.SourceID(), w.Body.String())
	}
	if b := w.Body.String(); !strings.Contains(b, "no audio") {
		t.Errorf("the refusal does not name the cause, so it is a 400 for some other "+
			"reason and this test is not observing the guard: %s", b)
	}

	// AND THE POSITIVE CONTROL, which is the false-refuse direction and is the
	// reason this cannot be fixed by refusing everything. The SAME profile on
	// the programme whose tracks carry no music role must be accepted: an
	// operator refused here has nothing they can change, and the message would
	// name a stream their destination does not read.
	onMain := map[string]any{}
	for k, v := range body {
		onMain[k] = v
	}
	onMain["name"] = "main is fine"
	onMain["sourceId"] = first.SourceID()

	r2 := jsonRequest(t, http.MethodPost, "/api/v1/destinations", onMain)
	sign(r2)
	if w2 := do(t, h, r2); w2.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201: the identical profile on a programme whose "+
			"tracks carry no music role selects real audio, and refusing it is the "+
			"other half of the same defect -- a destination the operator cannot "+
			"create and cannot fix (body %s)", w2.Code, w2.Body.String())
	}
}

// THE STATUS PAYLOAD SAYS WHICH PROGRAMME EACH DESTINATION CARRIES.
//
// The dashboard draws every destination on the install in one grid, from this
// payload. Nothing in it named a programme, so on a multi-source install two
// destinations called "Twitch" were the same card twice with no way to tell
// them apart -- and a UI badge added to fix that would have rendered nothing at
// all, silently, because the field it reads was never sent.
//
// That is the failure this pins: not a wrong value, an absent one. It is
// asserted on the wire rather than on the struct, because a json tag is the
// half that actually reaches the browser.
func TestTheStatusPayloadNamesEachDestinationsProgramme(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	if s.eng() == nil {
		t.Fatal("no engine in the fixture, so there is no status to read")
	}

	made := createDestination(t, h, sign, destinationBody("twitch", false, nil))
	if made.SourceID == nil {
		t.Fatal("the created destination carries no source, so this test cannot tell " +
			"an absent field from an absent value")
	}

	r := jsonRequest(t, http.MethodGet, "/api/v1/status", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}

	var got struct {
		Destinations []struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			SourceID *int64 `json:"sourceId"`
		} `json:"destinations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if len(got.Destinations) == 0 {
		t.Fatal("the status lists no destinations, so the field below is not being " +
			"checked on anything")
	}
	for _, d := range got.Destinations {
		if d.SourceID == nil {
			t.Errorf("destination %d (%q) reports no sourceId. A card cannot say which "+
				"programme it carries, and a badge reading this field renders nothing "+
				"while looking like it works", d.ID, d.Name)
			continue
		}
		if *d.SourceID != *made.SourceID {
			t.Errorf("destination %d reports source %d, want %d", d.ID, *d.SourceID, *made.SourceID)
		}
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
