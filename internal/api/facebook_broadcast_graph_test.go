package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// The two Facebook broadcast routes driven all the way through the REAL
// provider to a Graph stub, rather than stopping at the refusals.
//
// facebook_broadcast_test.go beside this one covers every way these routes say
// NO: not a Facebook destination, no session, no account, no broadcast. Those
// are the paths that return before a single Graph call is made -- so between
// them they proved the guards and nothing about what the handlers do when the
// guards pass. That is the half an operator actually uses.
//
// WHAT THE UNCOVERED HALF CONTAINS IS NOT BOILERPLATE. handleEndFacebookBroadcast
// has to render a result whose most important field is FALSE on a success:
// Facebook accepting the end without yet reporting VOD is an ordinary outcome
// and must not read as a failure, and reporting `ended: true` there would tell
// an operator their broadcast is over while it is still on air. Nothing
// exercised that branch.
//
// Driven through oauth.NewSet(oauth.WithBaseURL(...)) rather than a fake
// provider, following oauth_stub_test.go's reasoning: a fake would prove the
// handler calls something, and this proves the request that actually leaves the
// process and the response the handler builds from what comes back.

// graphStub answers the handful of Graph calls these two routes make, and
// records them. Separate from platformStub deliberately: that stub answers a
// live-video node read with a fixed LIVE_NOW status shared by many tests, and
// what is under test here is precisely how the handler behaves as that status
// varies.
type graphStub struct {
	URL string

	mu   sync.Mutex
	seen []string

	// nodeStatus is what a read of the live-video node reports back after an
	// end. Facebook's own success case is "VOD"; anything else is the
	// accepted-but-unconfirmed shape.
	nodeStatus string
	// ingestStreams is the ingest_streams payload the health read answers with.
	ingestStreams any
	// failWrite, when set, makes the end POST fail the way Graph does.
	failWrite string
}

func newGraphStub(t *testing.T) *graphStub {
	t.Helper()
	g := &graphStub{nodeStatus: "VOD"}
	srv := httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(srv.Close)
	g.URL = srv.URL
	return g
}

func (g *graphStub) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.seen = append(g.seen, r.Method+" "+r.URL.Path+"?"+r.URL.Query().Encode())
	status, streams, failWrite := g.nodeStatus, g.ingestStreams, g.failWrite
	g.mu.Unlock()

	switch {
	case r.URL.Path == "/me":
		writeStubJSON(w, map[string]any{"id": "1000", "name": "Ada Lovelace"})
	case r.URL.Path == "/me/accounts":
		// No Pages, so resolveTarget falls through to the profile and the calls
		// below address the "me" node with the user token.
		writeStubJSON(w, map[string]any{"data": []any{}})
	case r.Method == http.MethodPost:
		if failWrite != "" {
			writeStubError(w, failWrite)
			return
		}
		writeStubJSON(w, map[string]any{"success": true})
	default:
		// A read of the live-video node. Which fields were asked for decides
		// which read this is -- the end's confirmation, or the health poll.
		out := map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/")}
		if strings.Contains(r.URL.Query().Get("fields"), "ingest_streams") {
			if streams != nil {
				out["ingest_streams"] = streams
			}
		} else {
			out["status"] = status
		}
		writeStubJSON(w, out)
	}
}

// sawWrite reports whether the end POST reached Graph, and with the one
// parameter EndBroadcast is documented to send alone.
func (g *graphStub) sawWrite(liveVideoID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.seen {
		if strings.HasPrefix(c, "POST /"+liveVideoID+"?") &&
			strings.Contains(c, url.Values{"end_live_video": {"true"}}.Encode()) {
			return true
		}
	}
	return false
}

// fbGraphServer builds a server whose providers point at the stub, plus a
// Facebook destination carrying liveVideoID.
func fbGraphServer(t *testing.T, g *graphStub, liveVideoID string) (http.Handler, func(*http.Request), string) {
	t.Helper()
	s, h, store := testServerWith(t, Options{Providers: oauth.NewSet(oauth.WithBaseURL(g.URL))})
	sign := login(t, h)
	id := fbBroadcastDest(t, s, store, liveVideoID)
	return h, sign, strconv.FormatInt(id, 10)
}

// endResult is the envelope handleEndFacebookBroadcast writes.
type endResult struct {
	Ended    bool     `json:"ended"`
	Status   string   `json:"status"`
	Warnings []string `json:"warnings"`
}

// A CONFIRMED END REPORTS ended:true AND SAYS NOTHING ELSE.
//
// This is the only shape in which the UI may show an operator that their
// broadcast is over, and it requires Facebook to have said so: the POST was
// accepted AND the read-back reports VOD. Warnings must be empty, because a
// warning on a clean end trains an operator to ignore warnings on a dirty one.
func TestEndingAFacebookBroadcastReportsEndedOnlyWhenFacebookConfirmsTheVOD(t *testing.T) {
	g := newGraphStub(t)
	g.nodeStatus = "VOD"
	h, sign, id := fbGraphServer(t, g, "lv-99")

	r := jsonRequest(t, http.MethodPost, "/api/v1/destinations/"+id+"/facebook/end-broadcast", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}

	var got endResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Ended {
		t.Error("Facebook confirmed the broadcast is a VOD and polyemesis still reported it as not ended")
	}
	if got.Status != "VOD" {
		t.Errorf("status = %q, want the status Facebook actually reported", got.Status)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("a clean end carried warnings %q; a warning here teaches an operator "+
			"to ignore the ones that matter", got.Warnings)
	}
	// The request that left the process, not merely the answer built from it.
	if !g.sawWrite("lv-99") {
		t.Error("no POST with end_live_video=true reached Graph, so nothing was ended")
	}
}

// AN ACCEPTED-BUT-UNCONFIRMED END IS A SUCCESS THAT SAYS ended:false, AND THIS
// IS THE BRANCH WORTH THE MOST.
//
// Facebook accepting the end and still reporting LIVE on the read-back is
// ordinary -- the transition to VOD is not instant. The handler must answer 200
// with ended:false and a warning naming what it actually saw. The two wrong
// answers are both worse than the right one: reporting an error would tell an
// operator the end failed when it did not, and reporting ended:true would tell
// them a live broadcast is over.
func TestAnAcceptedButUnconfirmedEndIsNotReportedAsEnded(t *testing.T) {
	g := newGraphStub(t)
	g.nodeStatus = "LIVE"
	h, sign, id := fbGraphServer(t, g, "lv-100")

	r := jsonRequest(t, http.MethodPost, "/api/v1/destinations/"+id+"/facebook/end-broadcast", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: Facebook accepted the end, so this is not a failure "+
			"(body %s)", w.Code, w.Body.String())
	}

	var got endResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Ended {
		t.Fatal("reported ended:true while Facebook still reports the broadcast as LIVE — " +
			"this tells an operator their broadcast is over while it is on air")
	}
	if len(got.Warnings) == 0 {
		t.Fatal("said nothing about an end Facebook has not confirmed, so the UI shows a " +
			"silent no-op")
	}
	if !strings.Contains(strings.Join(got.Warnings, " "), "LIVE") {
		t.Errorf("warnings %q do not name the status Facebook actually reported, so an "+
			"operator cannot tell how far the end got", got.Warnings)
	}
}

// A GRAPH REFUSAL OF THE WRITE IS A REAL FAILURE AND KEEPS ITS ERROR STATUS.
//
// The read next door deliberately answers 200 supported:false when it cannot
// ask. This is the write, and the opposite rule applies: an end the operator
// pressed a button for and did not get must not be reported as a shrug.
func TestAFacebookEndRefusedByGraphIsReportedAsAFailure(t *testing.T) {
	g := newGraphStub(t)
	g.failWrite = "(#200) Permissions error"
	h, sign, id := fbGraphServer(t, g, "lv-101")

	r := jsonRequest(t, http.MethodPost, "/api/v1/destinations/"+id+"/facebook/end-broadcast", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code < 400 {
		t.Fatalf("status = %d, want a failure status: Graph refused the end and the "+
			"broadcast is still live (body %s)", w.Code, w.Body.String())
	}
}

// STREAM HEALTH RENDERS THE NUMBERS FACEBOOK SENT, AND ONLY THOSE.
//
// The measurement path, which nothing reached before: a numeric field arrives
// as a number, and a field whose value is not a number must not be invented as
// one. A zero Facebook genuinely measured is the most useful number on a
// stalled ingest and must survive.
func TestFacebookStreamHealthReturnsTheMeasurementsFacebookSent(t *testing.T) {
	g := newGraphStub(t)
	g.ingestStreams = map[string]any{"data": []map[string]any{{
		"id": "ingest-1",
		"stream_health": map[string]any{
			"video_bitrate": 5400.5,
			// A genuine zero: measured, not missing.
			"audio_bitrate": 0,
			// Not a number. It must be reported as unparsed rather than coerced.
			"status": "OK",
		},
	}}}
	h, sign, id := fbGraphServer(t, g, "lv-102")

	r := jsonRequest(t, http.MethodGet, "/api/v1/destinations/"+id+"/facebook/stream-health", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}

	var got struct {
		Supported bool `json:"supported"`
		Streams   []struct {
			ID     string             `json:"id"`
			Health map[string]float64 `json:"health"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Supported {
		t.Fatal("reported unsupported for a broadcast Facebook answered with real measurements")
	}
	if len(got.Streams) != 1 {
		t.Fatalf("got %d streams, want the one Facebook reported", len(got.Streams))
	}
	st := got.Streams[0]
	if st.ID != "ingest-1" {
		t.Errorf("stream id = %q, want the one Facebook named", st.ID)
	}
	if st.Health["video_bitrate"] != 5400.5 {
		t.Errorf("video_bitrate = %v, want 5400.5", st.Health["video_bitrate"])
	}
	// The genuine zero survives. A measured 0 is the whole point of the pane on
	// a stalled ingest.
	if v, ok := st.Health["audio_bitrate"]; !ok || v != 0 {
		t.Errorf("audio_bitrate = %v (present %v), want a measured 0 to survive", v, ok)
	}
	// And the non-numeric field was NOT invented as a number.
	if _, ok := st.Health["status"]; ok {
		t.Error("a non-numeric field was coerced into the health numbers, so the pane " +
			"will render a measurement Facebook never made")
	}
}

// AN INGEST NOTHING IS ARRIVING AT IS A 200 WITH AN EMPTY LIST.
//
// This is the state an operator opens the pane to diagnose. Reporting it as an
// error would hide the answer behind a failure, and the handler's comment says
// so; nothing proved it.
func TestFacebookStreamHealthAnswersAnIdleIngestWithAnEmptyListNotAnError(t *testing.T) {
	g := newGraphStub(t)
	g.ingestStreams = map[string]any{"data": []any{}}
	h, sign, id := fbGraphServer(t, g, "lv-103")

	r := jsonRequest(t, http.MethodGet, "/api/v1/destinations/"+id+"/facebook/stream-health", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}

	var got struct {
		Supported bool              `json:"supported"`
		Streams   []json.RawMessage `json:"streams"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Supported {
		t.Error("an idle ingest was reported as unsupported; Facebook answered the question, " +
			"and the answer was 'nothing is arriving'")
	}
	// Non-nil, because the handler builds the slice with make: a JSON null here
	// would make the UI guard for it, and every other list route in this package
	// promises an array.
	if got.Streams == nil {
		t.Error("streams was null rather than [], so the pane has to guard for a case " +
			"no other list route in this package produces")
	}
}

// NOT TESTED HERE, AND THE REASON IS WORTH RECORDING: the `provider is not
// configured` branch in both handlers.
//
// The obvious test — build the server with oauth.NewSet() and expect
// supported:false — does not work, because NewSet() with no options registers
// every real provider aimed at its real host. Written that way it does not
// exercise the branch at all; it sends a live request to graph.facebook.com and
// fails on a token rejection from Facebook itself. That was observed, not
// assumed.
//
// So the branch is unreachable through any seam this package offers, and the
// honest options are to leave it uncovered or to contrive a seam for it. Left
// uncovered: it is a defensive `ok` check on a map lookup that is populated at
// construction, and a test that reaches the public internet to prove a defensive
// branch costs more than the branch is worth.
