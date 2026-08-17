package oauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// YouTube's broadcast state machine.
//
// Fixtures here are shaped like the documented wire, not like the structs that
// decode them: a transition refusal is the real Google error envelope
// (error.errors[].reason), a state read is items[] of broadcast and stream
// resources. youtube_stats_test.go records why -- a fake that agrees with the
// code proves only that the code agrees with itself.

// ytTransitionRefusal builds the error envelope Google actually sends. message
// is padded by the caller in one case on purpose; see the truncation test.
func ytTransitionRefusal(reason, message string) string {
	return `{"error":{"code":403,"message":"` + message + `","errors":[{"message":"` + message +
		`","domain":"youtube.liveBroadcast","reason":"` + reason + `"}]}}`
}

// youtubeLifecycleStub answers the three paths this capability touches and
// fails the test on anything else, so a call that drifts to another endpoint is
// a failure rather than a 404 the code quietly tolerates.
func youtubeLifecycleStub(t *testing.T, h http.HandlerFunc) *YouTube {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case ytTransitionPath, ytBroadcastsPath, ytStreamsPath:
			h(w, r)
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return NewYouTube(WithBaseURL(srv.URL))
}

// The parameter form is the thing most likely to be got wrong by someone
// reading only the method signature: a POST that changes an object usually
// carries a body, and this one carries none. A request with the status in the
// payload is accepted by YouTube and does nothing, so nothing downstream would
// notice -- which is exactly why it is asserted on the wire here rather than
// assumed.
func TestYouTubeSendsTheTransitionAsQueryParametersWithNoBody(t *testing.T) {
	var got *http.Request
	var body []byte
	y := youtubeLifecycleStub(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"id":"bcast-1","status":{"lifeCycleStatus":"live"}}`))
	})

	res, err := y.TransitionBroadcast(context.Background(), "tok", "bcast-1", PhaseLive)
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	if got.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.Method)
	}
	if got.URL.Path != "/liveBroadcasts/transition" {
		t.Errorf("path = %s, want /liveBroadcasts/transition", got.URL.Path)
	}
	q := got.URL.Query()
	if q.Get("broadcastStatus") != "live" {
		t.Errorf("broadcastStatus query parameter = %q, want \"live\"; the status is a QUERY "+
			"PARAMETER on this call, not a body field", q.Get("broadcastStatus"))
	}
	if q.Get("id") != "bcast-1" {
		t.Errorf("id query parameter = %q, want \"bcast-1\"", q.Get("id"))
	}
	if q.Get("part") == "" {
		t.Error("part is a required query parameter on liveBroadcasts.transition and was not sent")
	}
	if len(body) != 0 {
		t.Errorf("transition sent a %d-byte body; this call has none, and a body carrying the "+
			"status is accepted and ignored: %s", len(body), body)
	}
	if res.Status != "live" {
		t.Errorf("Status = %q, want the lifeCycleStatus YouTube answered with", res.Status)
	}
	if res.Redundant {
		t.Error("Redundant = true on a transition YouTube accepted")
	}
}

// Each documented refusal, classified. The point of the table is that all of
// these arrive as HTTP 403 with the same envelope: if the classification were
// dropped, every row here would still be "the transition failed" and a caller
// would have one response for five situations.
func TestYouTubeClassifiesEachDocumentedTransitionRefusal(t *testing.T) {
	longMessage := "The requested transition is not allowed when the stream that is bound to the " +
		"broadcast is inactive. " + strings.Repeat("padding so this body is longer than the 300 "+
		"characters statusError keeps for display. ", 12)

	tests := []struct {
		name string
		// body is the refusal envelope; status the HTTP code.
		body        string
		status      int
		wantErr     bool
		wantRefusal TransitionRefusal
		wantFault   bool
		// wantRedundant asserts the success path.
		wantRedundant bool
		// wantUnclassified asserts the error came through untouched.
		wantUnclassified bool
	}{
		{
			// EXPECTED, NOT A FAULT. No bytes are arriving yet, which is the
			// state every broadcast is in before its encoder connects.
			name:        "stream inactive is a refusal but not a fault",
			body:        ytTransitionRefusal("errorStreamInactive", "The requested transition is not allowed when the stream that is bound to the broadcast is inactive"),
			status:      http.StatusForbidden,
			wantErr:     true,
			wantRefusal: RefusalStreamInactive,
			wantFault:   false,
		},
		{
			// Classification must survive a body longer than the 300 characters
			// statusError keeps for display. metadata.go records this exact bug
			// on the Facebook side: every code-specific branch silently stopped
			// firing once the body grew.
			name:        "stream inactive is still classified when the body is truncated for display",
			body:        ytTransitionRefusal("errorStreamInactive", longMessage),
			status:      http.StatusForbidden,
			wantErr:     true,
			wantRefusal: RefusalStreamInactive,
			wantFault:   false,
		},
		{
			name:        "invalid transition is a fault, and means re-read the state",
			body:        ytTransitionRefusal("invalidTransition", "The live broadcast can't transition from its current status to the requested status"),
			status:      http.StatusForbidden,
			wantErr:     true,
			wantRefusal: RefusalInvalidTransition,
			wantFault:   true,
		},
		{
			// The ceiling refuses HERE rather than at create, and YouTube
			// publishes no number for it.
			name:        "the concurrent broadcast ceiling is its own classification",
			body:        ytTransitionRefusal("concurrentBroadcastsExceedLimit", "The channel already has the maximum number of concurrent live broadcasts"),
			status:      http.StatusForbidden,
			wantErr:     true,
			wantRefusal: RefusalConcurrencyLimit,
			wantFault:   true,
		},
		{
			name:        "the shared ingestion ceiling classifies with the other ceiling",
			body:        ytTransitionRefusal("sharedIngestionBroadcastsExceedLimit", "too many broadcasts share this ingestion point"),
			status:      http.StatusForbidden,
			wantErr:     true,
			wantRefusal: RefusalConcurrencyLimit,
			wantFault:   true,
		},
		{
			name:        "asking too often is rate limiting, not a state problem",
			body:        ytTransitionRefusal("userRequestsExceedRateLimit", "The request cannot be completed"),
			status:      http.StatusForbidden,
			wantErr:     true,
			wantRefusal: RefusalRateLimited,
			wantFault:   true,
		},
		{
			name:        "a backend error is transient and says nothing about the broadcast",
			body:        `{"error":{"code":500,"errors":[{"reason":"errorExecutingTransition","message":"An error occurred while changing the broadcast's status"}]}}`,
			status:      http.StatusInternalServerError,
			wantErr:     true,
			wantRefusal: RefusalTransient,
			wantFault:   true,
		},
		{
			// ALREADY THERE IS SUCCESS. This is the retry path: a transition
			// whose response was lost, sent again, must not read as a failure.
			name:          "redundantTransition is success, because the broadcast is already where it was asked to be",
			body:          ytTransitionRefusal("redundantTransition", "The broadcast is already in the requested status"),
			status:        http.StatusForbidden,
			wantRedundant: true,
		},
		{
			// No invented classification. A reason this build has never heard of
			// comes back as the error it already was.
			name:             "an undocumented reason is not classified into something it is not",
			body:             ytTransitionRefusal("someReasonInventedNextYear", "who knows"),
			status:           http.StatusForbidden,
			wantErr:          true,
			wantUnclassified: true,
		},
		{
			name:             "a body that is not the error envelope classifies as nothing",
			body:             `<html>502 Bad Gateway</html>`,
			status:           http.StatusBadGateway,
			wantErr:          true,
			wantUnclassified: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			y := youtubeLifecycleStub(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			res, err := y.TransitionBroadcast(context.Background(), "tok", "bcast-1", PhaseTesting)

			if tc.wantRedundant {
				if err != nil {
					t.Fatalf("redundantTransition returned an error: %v — it means the broadcast "+
						"is already in the requested status, which is what was wanted", err)
				}
				if !res.Redundant {
					t.Error("Redundant = false on a redundantTransition refusal")
				}
				if res.Requested != PhaseTesting {
					t.Errorf("Requested = %q, want %q", res.Requested, PhaseTesting)
				}
				return
			}
			if !tc.wantErr {
				t.Fatal("test case asserts nothing")
			}
			if err == nil {
				t.Fatal("a refused transition returned no error")
			}

			var refusal *TransitionRefused
			ok := errors.As(err, &refusal)
			if tc.wantUnclassified {
				if ok {
					t.Fatalf("an undocumented failure was classified as %q; unrecognised errors "+
						"must pass through unchanged rather than be guessed at", refusal.Refusal)
				}
				return
			}
			if !ok {
				t.Fatalf("error was not a *TransitionRefused, so a caller cannot tell this "+
					"refusal from any other 403: %v", err)
			}
			if refusal.Refusal != tc.wantRefusal {
				t.Errorf("Refusal = %q, want %q", refusal.Refusal, tc.wantRefusal)
			}
			if refusal.Fault() != tc.wantFault {
				t.Errorf("Fault() = %v, want %v — errorStreamInactive means no bytes have "+
					"arrived yet, which is normal and must not be counted against anything",
					refusal.Fault(), tc.wantFault)
			}
			// The platform's own words survive, so a bug report can name the
			// exact reason even where the classification merges two of them.
			if refusal.Reason == "" {
				t.Error("Reason is empty; YouTube's own reason string must survive classification")
			}
		})
	}
}

// A transition is the one write in this package that can END a broadcast, so
// the two ways of aiming it at the wrong thing are refused before any request
// leaves the process.
func TestYouTubeRefusesAnUnnamedBroadcastAndAnUndocumentedPhaseWithoutCalling(t *testing.T) {
	y := youtubeLifecycleStub(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a request was sent for a call that should have been refused locally: %s", r.URL)
	})
	ctx := context.Background()

	if _, err := y.TransitionBroadcast(ctx, "tok", "   ", PhaseLive); err == nil {
		t.Error("an empty broadcast id was accepted; a transition to no object can read as success")
	}
	if _, err := y.TransitionBroadcast(ctx, "tok", "bcast-1", BroadcastPhase("ready")); err == nil {
		t.Error("\"ready\" was accepted as a transition target; only testing, live and complete " +
			"are documented")
	}
	if _, err := y.BroadcastState(ctx, "tok", ""); err == nil {
		t.Error("an empty broadcast id was accepted on a state read")
	}
}

// The state read is what makes idempotency possible without a local record: ask
// the platform what the broadcast IS, rather than remembering what was sent to
// it.
func TestYouTubeBroadcastStateJoinsTheBoundStreamAndNeverGuessesAPrecondition(t *testing.T) {
	const broadcast = `{"items":[{"id":"bcast-1","snippet":{"title":"Friday night set"},` +
		`"status":{"lifeCycleStatus":"ready"},"contentDetails":{"boundStreamId":"str-9",` +
		`"monitorStream":{"enableMonitorStream":true}}}]}`

	tests := []struct {
		name         string
		broadcast    string
		stream       string
		streamStatus int
		wantErr      bool
		wantStatus   string
		wantActive   *bool
		wantMonitor  *bool
		wantReady    bool
	}{
		{
			name:        "both preconditions met",
			broadcast:   broadcast,
			stream:      `{"items":[{"status":{"streamStatus":"active"}}]}`,
			wantStatus:  "ready",
			wantActive:  ptrBool(true),
			wantMonitor: ptrBool(true),
			wantReady:   true,
		},
		{
			// "ready" is the trap in the streamStatus enum: it sounds like the
			// answer and only "active" satisfies the transition.
			name:        "a stream that is ready is not a stream that is active",
			broadcast:   broadcast,
			stream:      `{"items":[{"status":{"streamStatus":"ready"}}]}`,
			wantStatus:  "ready",
			wantActive:  ptrBool(false),
			wantMonitor: ptrBool(true),
			wantReady:   false,
		},
		{
			// ABSENT IS NOT FALSE. A failed stream read must not report a stream
			// that has been sending for ten minutes as inactive.
			name:         "a failed stream read leaves liveness unknown rather than inactive",
			broadcast:    broadcast,
			stream:       `{"error":{"code":500}}`,
			streamStatus: http.StatusInternalServerError,
			wantStatus:   "ready",
			wantActive:   nil,
			wantMonitor:  ptrBool(true),
			wantReady:    false,
		},
		{
			// Same rule on the other precondition: a broadcast whose
			// contentDetails came back without the key has an UNKNOWN monitor
			// stream, not a disabled one.
			name: "an absent monitor stream flag is unknown, not disabled",
			broadcast: `{"items":[{"id":"bcast-1","status":{"lifeCycleStatus":"created"},` +
				`"contentDetails":{"boundStreamId":"str-9","monitorStream":{}}}]}`,
			stream:      `{"items":[{"status":{"streamStatus":"active"}}]}`,
			wantStatus:  "created",
			wantActive:  ptrBool(true),
			wantMonitor: nil,
			wantReady:   false,
		},
		{
			name: "a broadcast bound to nothing asks no stream at all",
			broadcast: `{"items":[{"id":"bcast-1","status":{"lifeCycleStatus":"created"},` +
				`"contentDetails":{"monitorStream":{"enableMonitorStream":true}}}]}`,
			wantStatus:  "created",
			wantActive:  nil,
			wantMonitor: ptrBool(true),
			wantReady:   false,
		},
		{
			// Empty items, never items[0] blind: the list reference documents no
			// 404 for an id that matches nothing.
			name:      "an id that matches nothing is an error, not a panic",
			broadcast: `{"items":[]}`,
			wantErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			y := youtubeLifecycleStub(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case ytBroadcastsPath:
					if got := r.URL.Query().Get("id"); got != "bcast-1" {
						t.Errorf("state read sent id=%q, want the id it was asked about", got)
					}
					_, _ = w.Write([]byte(tc.broadcast))
				case ytStreamsPath:
					if tc.stream == "" {
						t.Error("liveStreams was called for a broadcast bound to no stream")
						return
					}
					if got := r.URL.Query().Get("id"); got != "str-9" {
						t.Errorf("stream read sent id=%q, want the bound stream id", got)
					}
					if tc.streamStatus != 0 {
						w.WriteHeader(tc.streamStatus)
					}
					_, _ = w.Write([]byte(tc.stream))
				}
			})

			state, err := y.BroadcastState(context.Background(), "tok", "bcast-1")
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error for a broadcast YouTube did not return")
				}
				return
			}
			if err != nil {
				t.Fatalf("BroadcastState: %v", err)
			}
			if state.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q (YouTube's own word, unmapped)", state.Status, tc.wantStatus)
			}
			assertBoolPtr(t, "StreamActive", state.StreamActive, tc.wantActive)
			assertBoolPtr(t, "MonitorStream", state.MonitorStream, tc.wantMonitor)

			ready, why := state.ReadyForTesting()
			if ready != tc.wantReady {
				t.Errorf("ReadyForTesting() = %v (%q), want %v", ready, why, tc.wantReady)
			}
			if !ready && why == "" {
				t.Error("ReadyForTesting() said no without saying which precondition was missing")
			}
		})
	}
}

// assertBoolPtr keeps nil ("we were not told") distinct from false ("no") in the
// failure message, because a test that printed both as "false" would be the same
// bug the pointers exist to prevent.
func assertBoolPtr(t *testing.T, name string, got, want *bool) {
	t.Helper()
	show := func(p *bool) string {
		if p == nil {
			return "unknown"
		}
		if *p {
			return "true"
		}
		return "false"
	}
	if (got == nil) != (want == nil) || (got != nil && *got != *want) {
		t.Errorf("%s = %s, want %s", name, show(got), show(want))
	}
}

// The optional-capability shape, from both ends. YouTube is the only platform
// whose broadcast lifecycle can be commanded, and the value of the interface is
// that ABSENT is a supported answer rather than a stub method that returns an
// error.
func TestOnlyYouTubeOffersBroadcastLifecycleAndTheSetTwinResolvesToTheStub(t *testing.T) {
	var claimed, denied int
	for platform := range Providers() {
		_, ok := LifecycleFor(platform)
		if platform == db.PlatformYouTube {
			claimed++
			if !ok {
				t.Error("LifecycleFor(youtube) did not resolve, but YouTube implements the transition calls")
			}
			continue
		}
		denied++
		if ok {
			t.Errorf("LifecycleFor(%s) resolved; that platform has no documented transition call, "+
				"and a caller branching on this bool would offer a control that cannot work", platform)
		}
	}
	// Neither half may quietly cover nothing.
	if claimed == 0 || denied == 0 {
		t.Fatalf("this test needs both cases to mean anything: %d claim the capability, %d deny it",
			claimed, denied)
	}

	// The Set twin, resolved and DRIVEN. A twin that exists but reads the
	// production providers is the failure endpoints.go warns about, and for a
	// transition it would mean a test starting or ending a broadcast on a real
	// channel.
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"bcast-1","status":{"lifeCycleStatus":"testing"}}`))
	}))
	t.Cleanup(srv.Close)

	set := NewSet(WithBaseURL(srv.URL))
	bl, ok := set.LifecycleFor(db.PlatformYouTube)
	if !ok {
		t.Fatal("Set.LifecycleFor(youtube) did not resolve; the twin is not wired")
	}
	if _, err := bl.TransitionBroadcast(context.Background(), "tok", "bcast-1", PhaseTesting); err != nil {
		t.Fatalf("transition through the Set: %v", err)
	}
	if !reached {
		t.Error("the transition did not reach the stub, so the twin resolved a production provider")
	}
	if _, ok := set.LifecycleFor(db.PlatformTwitch); ok {
		t.Error("Set.LifecycleFor(twitch) resolved; Twitch has no broadcast to transition")
	}
}
