package api

import (
	"net/http"
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
