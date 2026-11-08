package oauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The error and edge branches of the live-now open, the stream and event listings and the auto-start write, each
// asserted by the error it returns, what it sent, and that no key appears in a message.

func failing(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

func TestOpenBroadcastIsTheDefaultsOpenAndReportsItsFailures(t *testing.T) {
	var log []capture
	y := lnStub(t, &log, nil)
	id, err := y.OpenBroadcast(context.Background(), "tok", lnKey)
	if err != nil || id != "bcast-9" {
		t.Errorf("OpenBroadcast = %q, %v", id, err)
	}
	if !y.LivestreamAccess() {
		t.Error("YouTube has no Livestream gate to close")
	}
	if _, err := y.OpenBroadcast(context.Background(), "tok", "not-a-listed-key"); !errors.Is(err, ErrStreamNotListed) {
		t.Errorf("an unlisted key: %v", err)
	}
	if _, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: "  "}); err == nil ||
		!strings.Contains(err.Error(), "no stream key") {
		t.Errorf("an empty key: %v", err)
	}
	if got := (&ErrActiveBroadcastOnStream{BroadcastID: "b-1"}).Error(); !strings.Contains(got, "b-1") {
		t.Errorf("message = %q", got)
	}
}

// Every read the open makes can fail, and each failure is the open's failure with nothing created.
func TestAnOpenStopsAtEachReadThatFails(t *testing.T) {
	cases := map[string]struct {
		handlers map[string]http.HandlerFunc
		req      LiveNowRequest
	}{
		"the stream list": {map[string]http.HandlerFunc{"GET " + ytStreamsPath: failing(500, `{}`)}, LiveNowRequest{StreamKey: lnKey}},
		"the active read": {map[string]http.HandlerFunc{"GET " + ytBroadcastsPath: failing(500, `{}`)}, LiveNowRequest{StreamKey: lnKey}},
		"the upcoming read": {map[string]http.HandlerFunc{"GET " + ytBroadcastsPath: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("broadcastStatus") == "upcoming" {
				w.WriteHeader(500)
				return
			}
			io.WriteString(w, `{"items":[]}`)
		}}, LiveNowRequest{StreamKey: lnKey, AttemptAt: time.Now()}},
	}
	for name, tc := range cases {
		var log []capture
		y := lnStub(t, &log, tc.handlers)
		if _, err := y.OpenLiveNow(context.Background(), "tok", tc.req); err == nil {
			t.Errorf("%s: a failed read was not an error", name)
		}
		noPost(t, log, ytBroadcastsPath)
		noPost(t, log, ytBindPath)
	}
}

func TestAnInsertYouTubeRefusesOrAnswersWithoutAnIdIsAnError(t *testing.T) {
	var log []capture
	y := lnStub(t, &log, map[string]http.HandlerFunc{"POST " + ytBroadcastsPath: failing(403,
		`{"error":{"errors":[{"reason":"liveStreamingNotEnabled"}]}}`)})
	_, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey})
	if err == nil || !strings.Contains(err.Error(), "not enabled for live streaming") {
		t.Errorf("a refused insert: %v, want the advice naming live streaming", err)
	}
	noPost(t, log, ytBindPath)

	log = nil
	y = lnStub(t, &log, map[string]http.HandlerFunc{"POST " + ytBroadcastsPath: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) }})
	if _, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey}); err == nil ||
		!strings.Contains(err.Error(), "no id") {
		t.Errorf("an insert with no id: %v", err)
	}

	// A spent allowance during the insert is recognised as one.
	log = nil
	y = lnStub(t, &log, map[string]http.HandlerFunc{"POST " + ytBroadcastsPath: failing(403, `{"error":{"errors":[{"reason":"quotaExceeded"}]}}`)})
	_, err = y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey})
	if !YouTubeQuotaSpent(err) {
		t.Errorf("a quotaExceeded insert was not recognised as a spent allowance: %v", err)
	}
}

func TestAnUnbindableBroadcastWhoseDeleteIsRefusedTellsTheOperatorToDeleteIt(t *testing.T) {
	var log []capture
	y := lnStub(t, &log, map[string]http.HandlerFunc{
		"POST " + ytBindPath:         failing(403, `{"error":{"errors":[{"reason":"liveBroadcastBindingNotAllowed"}]}}`),
		"DELETE " + ytBroadcastsPath: failing(500, `{}`),
	})
	_, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey})
	if err == nil || !strings.Contains(err.Error(), "would not delete it either") || !strings.Contains(err.Error(), "bcast-9") {
		t.Errorf("err = %v, want the broadcast named for deletion in Studio", err)
	}
}

func TestAPickedEventsReadAndBindFailuresAreTheOpensFailures(t *testing.T) {
	var log []capture
	y := evStub(t, &log, evJSON("ev-1", "ready", "", false), map[string]http.HandlerFunc{
		"GET " + ytBroadcastsPath: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("id") != "" {
				w.WriteHeader(500)
				return
			}
			io.WriteString(w, `{"items":[]}`)
		},
	})
	if _, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, EventID: "ev-1"}); err == nil {
		t.Error("an event that could not be read was not an error")
	}
	noPost(t, log, ytBroadcastsPath)

	log = nil
	y = evStub(t, &log, evJSON("ev-1", "ready", "", false), map[string]http.HandlerFunc{
		"POST " + ytBindPath: failing(403, `{"error":{"errors":[{"reason":"liveBroadcastBindingNotAllowed"}]}}`),
	})
	_, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, EventID: "ev-1"})
	if err == nil || !strings.Contains(err.Error(), "picked event ev-1") {
		t.Errorf("err = %v, want a refused bind of the picked event named", err)
	}
	if find(log, http.MethodDelete, ytBroadcastsPath) != nil {
		t.Error("a picked event was deleted after a refused bind")
	}

	// A revoked event is finished too, and a live broadcast elsewhere on the stream blocks the pick.
	log = nil
	y = evStub(t, &log, evJSON("ev-2", "revoked", "stream-77", true), nil)
	if r, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, EventID: "ev-2"}); err != nil || r.Existing {
		t.Errorf("a revoked event: %+v %v, want a new broadcast", r, err)
	}
	log = nil
	y = evStub(t, &log, evJSON("ev-3", "ready", "stream-77", true), map[string]http.HandlerFunc{
		"GET " + ytBroadcastsPath: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("id") != "" {
				io.WriteString(w, evJSON("ev-3", "ready", "stream-77", true))
				return
			}
			io.WriteString(w, `{"items":[{"id":"someone-else","contentDetails":{"boundStreamId":"stream-77"}}]}`)
		},
	})
	_, err = y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, EventID: "ev-3"})
	var act *ErrActiveBroadcastOnStream
	if !errors.As(err, &act) || act.BroadcastID != "someone-else" {
		t.Errorf("err = %v, want the live broadcast on the stream named", err)
	}
}

func TestUpcomingCandidatesThatAreNotOursAreSkipped(t *testing.T) {
	start := time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339)
	mk := func(extra string) string {
		return `{"items":[{"id":"c-1","snippet":{"title":"Friday","scheduledStartTime":"` + start + `"},` + extra + `}]}`
	}
	for name, body := range map[string]string{
		"bound to another stream": mk(`"status":{"lifeCycleStatus":"created"},"contentDetails":{"boundStreamId":"stream-88","enableAutoStart":true,"enableAutoStop":false}`),
		"already live":            mk(`"status":{"lifeCycleStatus":"live"},"contentDetails":{"enableAutoStart":true,"enableAutoStop":false}`),
		"auto-start off":          mk(`"status":{"lifeCycleStatus":"created"},"contentDetails":{"enableAutoStart":false,"enableAutoStop":false}`),
		"no start time":           `{"items":[{"id":"c-1","snippet":{"title":"Friday"},"status":{"lifeCycleStatus":"created"},"contentDetails":{"enableAutoStart":true}}]}`,
	} {
		var log []capture
		y := lnStub(t, &log, map[string]http.HandlerFunc{"GET " + ytBroadcastsPath: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("broadcastStatus") == "upcoming" {
				io.WriteString(w, body)
				return
			}
			io.WriteString(w, `{"items":[]}`)
		}})
		r, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, Title: "Friday", AttemptAt: time.Now()})
		if err != nil || r.BroadcastID != "bcast-9" {
			t.Errorf("%s: %+v %v, want a fresh broadcast", name, r, err)
		}
	}
}

// ----------------------------------------------------------------- the listings and the auto-start write

func TestTheListingsReportTheirFailuresAndSkipWhatTheyShouldNot(t *testing.T) {
	var log []capture
	y := lnStub(t, &log, map[string]http.HandlerFunc{"GET " + ytStreamsPath: failing(500, `{}`)})
	if _, err := y.ListStreams(context.Background(), "tok", nil); err == nil {
		t.Error("ListStreams swallowed a failed list")
	}
	if _, err := y.ListEvents(context.Background(), "tok", nil); err == nil {
		t.Error("ListEvents swallowed a failed stream list")
	}

	log = nil
	y = lnStub(t, &log, map[string]http.HandlerFunc{
		"GET " + ytStreamsPath:    func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, evStreams) },
		"GET " + ytBroadcastsPath: failing(500, `{}`),
	})
	if _, err := y.ListEvents(context.Background(), "tok", nil); err == nil {
		t.Error("ListEvents swallowed a failed broadcast list")
	}

	// A stream that is not RTMP is not listed; an event bound to no stream has no stream state; a duplicate id is once.
	log = nil
	y = lnStub(t, &log, map[string]http.HandlerFunc{
		"GET " + ytStreamsPath: func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"items":[{"id":"dash","snippet":{"title":"d"},"cdn":{"ingestionType":"dash","ingestionInfo":{"streamName":"k"}}},`+
				`{"id":"rtmp","snippet":{"title":"r"},"cdn":{"ingestionType":"RTMP","ingestionInfo":{"streamName":"k2"}}}]}`)
		},
		"GET " + ytBroadcastsPath: func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, evJSON("same", "ready", "", true)) // both the upcoming and the active read answer it
		},
	})
	ss, err := y.ListStreams(context.Background(), "tok", []string{"k2", ""})
	if err != nil || len(ss) != 1 || ss[0].ID != "rtmp" || len(ss[0].MatchedKeys) != 1 || ss[0].MatchedKeys[0] != 0 {
		t.Errorf("streams = %+v %v, want only the RTMP stream, matched to the first key", ss, err)
	}
	evs, err := y.ListEvents(context.Background(), "tok", nil)
	if err != nil || len(evs) != 1 || evs[0].StreamActive != nil {
		t.Errorf("events = %+v %v, want one event with no stream state", evs, err)
	}
}

func TestSetEventAutoStartsRefusals(t *testing.T) {
	var log []capture
	y := evStub(t, &log, evJSON("ev-1", "ready", "stream-77", false), map[string]http.HandlerFunc{
		"GET /channels": func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"items":[{"id":"UCother","snippet":{"title":"Other"}}]}`)
		},
	})
	if err := y.SetEventAutoStart(context.Background(), "tok", "  "); err == nil || !strings.Contains(err.Error(), "no YouTube event id") {
		t.Errorf("a blank id: %v", err)
	}
	// The event belongs to UCchannel (evJSON) but this token is UCother: never written.
	if err := y.SetEventAutoStart(context.Background(), "tok", "ev-1"); err == nil || !strings.Contains(err.Error(), "another channel") {
		t.Errorf("another channel's event: %v", err)
	}
	if find(log, http.MethodPut, ytBroadcastsPath) != nil {
		t.Error("another channel's event was written")
	}

	log = nil
	y = evStub(t, &log, evJSON("ev-1", "ready", "stream-77", false), map[string]http.HandlerFunc{"GET /channels": failing(500, `{}`)})
	if err := y.SetEventAutoStart(context.Background(), "tok", "ev-1"); err == nil {
		t.Error("an account lookup that failed was not an error")
	}

	log = nil
	y = evStub(t, &log, `{"items":[]}`, nil)
	if err := y.SetEventAutoStart(context.Background(), "tok", "gone"); !errors.Is(err, ErrBroadcastNotListed) {
		t.Errorf("an event that is not listed: %v", err)
	}
}
