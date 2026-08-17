package oauth

// Ending a broadcast, reading its ingest health, and the wire format of a
// scheduled start time.
//
// A file of their own rather than more of facebook_test.go, for the reason
// youtube_broadcast_test.go is not youtube_test.go: these three share a stub
// shape and a set of citations that the create/metadata tests do not, and a
// 1,600-line file that grows a fourth unrelated section is one nobody reads
// the top of. The helpers are still facebook_test.go's -- fbServer is the only
// provider a test may use, because a bare &Facebook{} talks to the real
// graph.facebook.com.

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// fbEndStub answers the two calls an end makes: the POST that ends it, and the
// status read that confirms it. status is what the read-back reports.
func fbEndStub(t *testing.T, id, status string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/me/accounts":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"data": []map[string]any{
				{"id": "555", "name": "Ada's Bakery", "category": "Bakery", "access_token": "page-token"},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/"+id:
			writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
		case r.Method == http.MethodGet && r.URL.Path == "/"+id:
			writeJSONBody(t, w, http.StatusOK, map[string]any{"id": id, "status": status})
		default:
			http.Error(w, `{"error":{"message":"Unsupported request","code":100}}`, http.StatusNotFound)
		}
	}
}

// The end is one POST to the live video NODE carrying end_live_video=true, and
// it is reported as ended only once Facebook says VOD -- "This ends your
// broadcast and saves it as a video on demand (VOD)".
//
// MUTATION M1, internal/oauth/facebook.go in EndBroadcast:
// `{"end_live_video": {"true"}}` -> `{"end_live_video": {"1"}}`.
// Observed: FAIL -- end_live_video = "1", want true. Graph's boolean spelling
// is the whole request here, so a test that did not read it back would pass
// against a call Facebook ignores.
func TestEndingABroadcastPostsEndLiveVideoAndConfirmsTheVOD(t *testing.T) {
	fb, log := fbServer(t, fbEndStub(t, "9", "VOD"))

	res, err := fb.EndBroadcast(context.Background(), "user-token", "user:1000", "9")
	if err != nil {
		t.Fatalf("EndBroadcast: %v", err)
	}
	post := fbCall(*log, http.MethodPost, "/9")
	if post == nil {
		t.Fatalf("no POST to the live video node; calls were %+v", *log)
	}
	q, err := url.ParseQuery(post.Query)
	if err != nil {
		t.Fatalf("parse query %q: %v", post.Query, err)
	}
	if got := q.Get("end_live_video"); got != "true" {
		t.Errorf("end_live_video = %q, want true", got)
	}
	// Nothing else travels with it: this call is not a place to also edit the
	// broadcast, and a parameter Graph did not expect would fail the end.
	if len(q) != 1 {
		t.Errorf("the end POST carried %v, want end_live_video alone", q)
	}
	if !res.Ended || res.Status != "VOD" {
		t.Errorf("EndBroadcast = %+v, want Ended with status VOD", res)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings on a confirmed end: %v", res.Warnings)
	}
	// The edge creates a broadcast and the node ends one. Ending through
	// /me/live_videos would create a second broadcast and leave the first on
	// air, which is the worst outcome this call has available to it.
	if c := fbCall(*log, http.MethodPost, "/me/live_videos"); c != nil {
		t.Errorf("the end went to the create edge: %+v", c)
	}
}

// A Page's live video is addressable only with that Page's token. Ending it
// with the user token is a permission error on the one call an operator is
// watching for the end of.
func TestAPageBroadcastIsEndedWithThePageToken(t *testing.T) {
	fb, log := fbServer(t, fbEndStub(t, "9", "VOD"))

	if _, err := fb.EndBroadcast(context.Background(), "user-token", "page:555", "9"); err != nil {
		t.Fatalf("EndBroadcast: %v", err)
	}
	post := fbCall(*log, http.MethodPost, "/9")
	if post == nil {
		t.Fatalf("no POST to the live video node; calls were %+v", *log)
	}
	if post.Auth != "Bearer page-token" {
		t.Errorf("the end used %q, want the Page token", post.Auth)
	}
}

// A refused end is an ERROR, not a warning attached to a result that reads as
// success. The asymmetry with the privacy push is the consequence: a failed
// privacy push leaves the create-time value applied, while a failed end leaves
// a broadcast on air that the operator believes is over.
func TestARefusedEndIsAnErrorRatherThanAResultThatReadsAsEnded(t *testing.T) {
	fb, log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/9" {
			http.Error(w, `{"error":{"message":"(#200) Permissions error",`+
				`"type":"OAuthException","code":200}}`, http.StatusForbidden)
			return
		}
		http.Error(w, `{"error":{"message":"Unsupported request","code":100}}`, http.StatusNotFound)
	})

	res, err := fb.EndBroadcast(context.Background(), "user-token", "user:1000", "9")
	if err == nil {
		t.Fatalf("a refused end returned %+v and no error", res)
	}
	if res != nil {
		t.Errorf("a refused end returned a result as well as an error: %+v", res)
	}
	if !strings.Contains(err.Error(), "publish_video") {
		t.Errorf("the error names no permission to grant: %v", err)
	}
	// The confirmation read must not run after a refused POST: a broadcast that
	// is still LIVE would be read back and reported as "accepted, not yet
	// confirmed", which is the refusal wearing a softer word.
	if c := fbCall(*log, http.MethodGet, "/9"); c != nil {
		t.Errorf("the status was read back after the end was refused: %+v", c)
	}
}

// Facebook documents that it saves the VOD, not how fast the node settles. A
// status that is not VOD is therefore neither an error nor a confirmation, and
// the value that was seen is carried out rather than smoothed over.
//
// MUTATION M2, in EndBroadcast's confirmation switch:
// `case strings.EqualFold(strings.TrimSpace(confirm.Status), fbStatusVOD):`
// -> `case true:`, i.e. treat the POST's 200 as the confirmation.
// Observed: FAIL on all three subtests -- LIVE, PROCESSING and a missing status
// were each reported as an ended broadcast.
func TestAnEndFacebookHasNotConfirmedIsNotReportedAsEnded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
	}{
		{"still live", "LIVE"},
		{"still processing", "PROCESSING"},
		{"no status at all", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fb, _ := fbServer(t, fbEndStub(t, "9", tc.status))
			res, err := fb.EndBroadcast(context.Background(), "user-token", "user:1000", "9")
			if err != nil {
				t.Fatalf("EndBroadcast: %v", err)
			}
			if res.Ended {
				t.Errorf("status %q was reported as an ended broadcast", tc.status)
			}
			if len(res.Warnings) == 0 {
				t.Error("an unconfirmed end carried no warning saying so")
			}
		})
	}
}

// An unreadable confirmation is not a failed end -- the POST already succeeded
// -- and it is not a confirmed one either.
func TestAnUnreadableEndConfirmationIsWarnedAboutRatherThanTrusted(t *testing.T) {
	fb, _ := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/9" {
			writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
			return
		}
		http.Error(w, `{"error":{"message":"Please reduce the amount of data","code":1}}`,
			http.StatusInternalServerError)
	})

	res, err := fb.EndBroadcast(context.Background(), "user-token", "user:1000", "9")
	if err != nil {
		t.Fatalf("EndBroadcast: %v", err)
	}
	if res.Ended || res.Status != "" {
		t.Errorf("EndBroadcast = %+v, want no status and no confirmation", res)
	}
	if len(res.Warnings) == 0 {
		t.Error("an unconfirmable end carried no warning saying so")
	}
}

// An empty id would make this a POST to "/", which Graph answers in a way that
// reads as success -- and here that success would be a still-live broadcast
// reported as ended.
func TestEndingWithNoBroadcastIdIsRefusedBeforeAnyCall(t *testing.T) {
	fb, log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a request was made for an empty live video id: %s %s", r.Method, r.URL.Path)
		writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
	})
	if _, err := fb.EndBroadcast(context.Background(), "user-token", "user:1000", "  "); err == nil {
		t.Fatal("an empty live video id was accepted")
	}
	if len(*log) != 0 {
		t.Errorf("calls were made anyway: %+v", *log)
	}
}

// The measurements come back under FACEBOOK'S names rather than under names
// invented here: the node reference that would pin the spellings is a real 404,
// so stream_health is carried through as it arrived.
func TestStreamHealthCarriesFacebooksOwnMeasurementNames(t *testing.T) {
	fb, log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONBody(t, w, http.StatusOK, map[string]any{
			"id": "9",
			"ingest_streams": map[string]any{"data": []map[string]any{{
				"id": "stream-1",
				"stream_health": map[string]any{
					"video_bitrate":   4500.5,
					"audio_bitrate":   128.0,
					"video_framerate": 30.0,
				},
			}}},
		})
	})

	got, err := fb.StreamHealth(context.Background(), "user-token", "user:1000", "9")
	if err != nil {
		t.Fatalf("StreamHealth: %v", err)
	}
	if len(got) != 1 || got[0].ID != "stream-1" {
		t.Fatalf("StreamHealth = %+v, want the one ingest stream", got)
	}
	if got[0].Health["video_bitrate"] != 4500.5 || got[0].Health["video_framerate"] != 30 {
		t.Errorf("health = %v, want the bitrates and frame rate Facebook sent", got[0].Health)
	}
	// ABSENT IS NOT ZERO. A measurement Facebook did not send has no key at
	// all, so the second return distinguishes it from a genuine zero -- and a
	// zero bitrate is a real reading that means the encoder is sending nothing.
	if v, ok := got[0].Health["dropped_frames"]; ok {
		t.Errorf("a measurement Facebook never sent reads back as %v", v)
	}
	// One read, and it names the field: a Graph read without fields returns a
	// default set that carries no ingest_streams at all.
	get := fbCall(*log, http.MethodGet, "/9")
	if get == nil {
		t.Fatalf("no health read; calls were %+v", *log)
	}
	q, err := url.ParseQuery(get.Query)
	if err != nil {
		t.Fatalf("parse query %q: %v", get.Query, err)
	}
	if q.Get("fields") != "ingest_streams" {
		t.Errorf("fields = %q, want ingest_streams", q.Get("fields"))
	}
}

// THE CASE THIS METHOD IS MOST OFTEN CALLED IN. A scheduled broadcast has no
// ingest yet, an ended one has none any more, and a live one whose encoder went
// quiet reports nothing until Facebook's own four-second timeout fires. All
// three are "nothing to describe", and an error would make a health pane shout
// during the ordinary pause between Go Live and the first byte.
func TestStreamHealthOnAStreamThatIsNotLiveIsEmptyRatherThanAnError(t *testing.T) {
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"no ingest_streams field at all", map[string]any{"id": "9"}},
		{"an empty list", map[string]any{"id": "9", "ingest_streams": map[string]any{"data": []map[string]any{}}}},
		{"an explicit null", map[string]any{"id": "9", "ingest_streams": nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fb, _ := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSONBody(t, w, http.StatusOK, tc.body)
			})
			got, err := fb.StreamHealth(context.Background(), "user-token", "user:1000", "9")
			if err != nil {
				t.Fatalf("StreamHealth: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("StreamHealth = %+v, want no ingest streams", got)
			}
		})
	}
}

// Which spelling Graph uses for a list-valued FIELD is settled by nothing
// reachable -- the node reference 404s -- so both are decoded. Guessing one
// would render a perfectly healthy stream as an empty pane.
//
// MUTATION M3, in fbIngestStreams.UnmarshalJSON: drop the bare-array branch and
// return nil after the envelope attempt.
// Observed: FAIL on the bare-array subtest only -- StreamHealth = [], while the
// envelope subtest stayed green. Both halves are here because either spelling
// alone passes for the wrong reason.
func TestStreamHealthReadsBothSpellingsOfTheIngestStreamsList(t *testing.T) {
	entry := map[string]any{"id": "stream-1", "stream_health": map[string]any{"video_bitrate": 4500.0}}
	for _, tc := range []struct {
		name  string
		field any
	}{
		{"the data envelope", map[string]any{"data": []map[string]any{entry}}},
		{"a bare array", []map[string]any{entry}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fb, _ := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSONBody(t, w, http.StatusOK, map[string]any{"id": "9", "ingest_streams": tc.field})
			})
			got, err := fb.StreamHealth(context.Background(), "user-token", "user:1000", "9")
			if err != nil {
				t.Fatalf("StreamHealth: %v", err)
			}
			if len(got) != 1 || got[0].Health["video_bitrate"] != 4500 {
				t.Fatalf("StreamHealth = %+v, want the one stream's bitrate", got)
			}
		})
	}
}

// A field polyemesis cannot read looks exactly like a field Facebook did not
// send, and only one of those two is a bug here. So it is named, not dropped.
func TestAStreamHealthFieldThatIsNotANumberIsNamedRatherThanDropped(t *testing.T) {
	fb, _ := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONBody(t, w, http.StatusOK, map[string]any{
			"id": "9",
			"ingest_streams": map[string]any{"data": []map[string]any{{
				"id": "stream-1",
				"stream_health": map[string]any{
					"video_bitrate": 4500.0,
					"status":        "OK",
				},
			}}},
		})
	})

	got, err := fb.StreamHealth(context.Background(), "user-token", "user:1000", "9")
	if err != nil {
		t.Fatalf("StreamHealth: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("StreamHealth = %+v, want one stream", got)
	}
	if !slices.Contains(got[0].Unparsed, "status") {
		t.Errorf("unparsed = %v, want the non-numeric field named", got[0].Unparsed)
	}
	// And the numbers beside it still arrive: one unreadable field does not
	// cost the whole read.
	if got[0].Health["video_bitrate"] != 4500 {
		t.Errorf("health = %v, want the bitrate that was readable", got[0].Health)
	}
}

func TestStreamHealthRefusesAnEmptyLiveVideoIDBeforeAnyCall(t *testing.T) {
	fb, log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a request was made for an empty live video id: %s %s", r.Method, r.URL.Path)
		writeJSONBody(t, w, http.StatusOK, map[string]any{"id": ""})
	})
	if _, err := fb.StreamHealth(context.Background(), "user-token", "user:1000", ""); err == nil {
		t.Fatal("an empty live video id was accepted")
	}
	if len(*log) != 0 {
		t.Errorf("calls were made anyway: %+v", *log)
	}
}

// Both numbers are Facebook's, quoted beside the constants: "Stream health data
// refreshes every 2 seconds, so limit queries to no more than once every 2
// seconds. A stream timeout will be detected and reported after 4 seconds of no
// data being received."
//
// The guard is against TUNING THEM BY FEEL. A poll loop that felt sluggish is
// not a reason to move a documented floor; re-reading the Broadcasting guide
// is, and this test is what sends someone to go and do that.
func TestTheStreamHealthPacingIsFacebooksPublishedNumbers(t *testing.T) {
	if FacebookStreamHealthInterval != 2*time.Second {
		t.Errorf("poll floor = %v, want the documented 2s", FacebookStreamHealthInterval)
	}
	if FacebookStreamTimeout != 4*time.Second {
		t.Errorf("stream timeout = %v, want the documented 4s", FacebookStreamTimeout)
	}
	// A poll floor at or above the platform's own timeout could not observe the
	// timeout it exists to let a caller detect.
	if FacebookStreamHealthInterval >= FacebookStreamTimeout {
		t.Errorf("poll floor %v is not shorter than the %v timeout it must observe",
			FacebookStreamHealthInterval, FacebookStreamTimeout)
	}
}

// event_params is documented two ways and only one can be the wire format: the
// scheduling guide's own literal sample sends a bare unix scalar
// (event_params=1541539800), while the v26.0 edge reference types it as a
// {start_time, cover} object scoped to Live Online Events. The scalar is what
// goes out, and this pins it -- moving to the object form is a decision to take
// deliberately against a live account, not one to make by editing a struct.
//
// MUTATION M4, in IngestFor: send the structured form instead,
// `{"start_time":<unix>}`.
// Observed: FAIL on the create subtest, on both the value assertion and the
// object assertion, and a green reschedule subtest -- which is exactly the
// divergence between create and move this test exists to catch.
func TestSchedulingSendsTheScalarEventParamsNotTheStructuredObject(t *testing.T) {
	at := time.Unix(1541539800, 0)
	for _, tc := range []struct {
		name string
		send func(fb *Facebook) error
		call func(log []fbReq) *fbReq
	}{
		{
			name: "at create",
			send: func(fb *Facebook) error {
				_, err := fb.IngestFor(context.Background(), "cid", "user-token", "",
					IngestOptions{ScheduledFor: at})
				return err
			},
			call: func(log []fbReq) *fbReq { return fbCall(log, http.MethodPost, "/me/live_videos") },
		},
		{
			// The move must agree with the create. Two spellings of one wire
			// format would both work until the day one of them stopped.
			name: "at reschedule",
			send: func(fb *Facebook) error {
				return fb.RescheduleBroadcast(context.Background(), "user-token", "777", at)
			},
			call: func(log []fbReq) *fbReq { return fbCall(log, http.MethodPost, "/777") },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fb, log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/777" {
					writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
					return
				}
				graphStub(t, fbLiveResponse("777"))(w, r)
			})
			if err := tc.send(fb); err != nil {
				t.Fatalf("send: %v", err)
			}
			post := tc.call(*log)
			if post == nil {
				t.Fatalf("no call carrying event_params; calls were %+v", *log)
			}
			q, err := url.ParseQuery(post.Query)
			if err != nil {
				t.Fatalf("parse query %q: %v", post.Query, err)
			}
			ep := q.Get("event_params")
			if ep != "1541539800" {
				t.Errorf("event_params = %q, want the bare unix scalar 1541539800", ep)
			}
			// Asserted separately rather than left to the equality above: the
			// structured form is a JSON object, and this is the line that says
			// which of the two documented spellings was rejected.
			if strings.HasPrefix(strings.TrimSpace(ep), "{") {
				t.Errorf("event_params was sent as the structured object %q", ep)
			}
		})
	}
}
