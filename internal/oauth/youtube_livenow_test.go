package oauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The live-now broadcast, asserted at the wire like the scheduled path: every trap here is a request that
// succeeds while doing the wrong thing.

const (
	lnKey     = "key-abc"
	lnStreams = `{"items":[
	 {"id":"stream-77","snippet":{"title":"polyemesis"},"cdn":{"ingestionType":"rtmp","ingestionInfo":{
	   "streamName":"key-abc","ingestionAddress":"rtmp://a.example/live2"}}},
	 {"id":"stream-88","snippet":{"title":"other"},"cdn":{"ingestionType":"rtmp","ingestionInfo":{
	   "streamName":"key-other","ingestionAddress":"rtmp://a.example/live2"}}}]}`
)

// lnStub serves list, active read, insert and bind; handlers override by "METHOD path".
func lnStub(t *testing.T, log *[]capture, handlers map[string]http.HandlerFunc, opts ...func(host string) ProviderOption) *YouTube {
	t.Helper()
	srv := httptest.NewServer(recordAll(t, log, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if h, ok := handlers[r.Method+" "+r.URL.Path]; ok {
			h(w, r)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET " + ytStreamsPath:
			io.WriteString(w, lnStreams)
		case "GET " + ytBroadcastsPath:
			io.WriteString(w, `{"items":[]}`)
		case "POST " + ytBroadcastsPath:
			io.WriteString(w, `{"id":"bcast-9","snippet":{"title":"t","liveChatId":"chat-9"},"status":{"lifeCycleStatus":"created"}}`)
		case "POST " + ytBindPath:
			io.WriteString(w, `{"id":"bcast-9"}`)
		default:
			t.Errorf("unexpected %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	po := []ProviderOption{WithBaseURL(srv.URL)}
	for _, o := range opts {
		po = append(po, o(strings.TrimPrefix(srv.URL, "http://")))
	}
	return NewYouTube(po...)
}

func callsOf(log []capture) []string {
	var out []string
	for _, c := range log {
		out = append(out, c.Method+" "+c.Path)
	}
	return out
}

// MUTATION, youtube_livenow.go insertLiveNow: "enableAutoStop": false -> true. Observed: FAIL on the auto-stop assertion.
func TestOpenLiveNowListsThenInsertsThenBindsWithAutoStartOnAndAutoStopOff(t *testing.T) {
	var log []capture
	y := lnStub(t, &log, nil)
	r, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, Title: "Friday show"})
	if err != nil {
		t.Fatalf("OpenLiveNow: %v", err)
	}
	want := []string{"GET /liveStreams", "GET /liveBroadcasts", "GET /liveBroadcasts", "POST /liveBroadcasts", "POST /liveBroadcasts/bind"}
	if got := callsOf(log); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	ins := log[3]
	if q := mustQuery(t, &ins); q.Get("part") != "snippet,status,contentDetails" {
		t.Errorf("insert part = %q, want exactly the parts the body carries", q.Get("part"))
	}
	cd, _ := ins.Body["contentDetails"].(map[string]any)
	if cd["enableAutoStart"] != true {
		t.Errorf("enableAutoStart = %v, want true: the broadcast goes live when video arrives", cd["enableAutoStart"])
	}
	if cd["enableAutoStop"] != false {
		t.Errorf("enableAutoStop = %v, want false: a reconnect gap must never complete the broadcast", cd["enableAutoStop"])
	}
	snip, _ := ins.Body["snippet"].(map[string]any)
	if snip["title"] != "Friday show" || snip["scheduledStartTime"] == "" {
		t.Errorf("snippet = %v", snip)
	}
	if st, _ := ins.Body["status"].(map[string]any); st["privacyStatus"] != "public" {
		t.Errorf("default privacy = %v, want public", st["privacyStatus"])
	}
	bq := mustQuery(t, &log[4])
	if bq.Get("id") != "bcast-9" || bq.Get("streamId") != "stream-77" {
		t.Errorf("bind query = %v, want the new broadcast bound to the stream holding the key", bq)
	}
	if r.BroadcastID != "bcast-9" || r.LiveChatID != "chat-9" {
		t.Errorf("result = %+v, want the broadcast id and the insert response's liveChatId", r)
	}
}

func TestOpenLiveNowPrivacyOverrideAndTitleLimit(t *testing.T) {
	var log []capture
	y := lnStub(t, &log, nil)
	long := strings.Repeat("é", 130)
	if _, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, Privacy: "private", Title: long}); err != nil {
		t.Fatal(err)
	}
	ins := find(log, http.MethodPost, ytBroadcastsPath)
	if st, _ := ins.Body["status"].(map[string]any); st["privacyStatus"] != "private" {
		t.Errorf("privacy = %v, want the destination's override", st["privacyStatus"])
	}
	if got, _ := ins.Body["snippet"].(map[string]any)["title"].(string); len([]rune(got)) != 100 {
		t.Errorf("title is %d characters, want it cut to YouTube's 100", len([]rune(got)))
	}
}

// A key the channel does not list is a fault that names the cause, and nothing is created or rotated.
func TestOpenLiveNowNeverCreatesAStreamOrRotatesAKeyWhenTheKeyIsNotListed(t *testing.T) {
	var log []capture
	y := lnStub(t, &log, nil)
	_, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: "unlisted-secret-key"})
	if !errors.Is(err, ErrStreamNotListed) {
		t.Fatalf("err = %v, want ErrStreamNotListed", err)
	}
	for _, c := range log {
		if c.Method != http.MethodGet {
			t.Errorf("a %s %s was sent; an unlisted key must send nothing but the list", c.Method, c.Path)
		}
	}
	if strings.Contains(err.Error(), "unlisted-secret-key") {
		t.Errorf("the error names the key: %v", err)
	}
}

func TestOpenLiveNowPinnedStreamMustBelongToTheKey(t *testing.T) {
	var log []capture
	y := lnStub(t, &log, nil)
	_, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, StreamID: "stream-88"})
	if !errors.Is(err, ErrStreamNotListed) || !strings.Contains(err.Error(), "stream-88") {
		t.Fatalf("err = %v, want a refusal naming the pinned stream", err)
	}
	if find(log, http.MethodPost, ytBroadcastsPath) != nil {
		t.Error("a broadcast was made on a stream that would receive nothing")
	}
	_, err = y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, StreamID: "gone"})
	if !errors.Is(err, ErrStreamNotListed) {
		t.Errorf("a pinned stream the channel lacks: err = %v", err)
	}
	if _, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, StreamID: "stream-77"}); err != nil {
		t.Errorf("a pin matching the key must work: %v", err)
	}
}

// A destination already on air through Studio's Stream now is left alone.
func TestOpenLiveNowLeavesAStreamThatAlreadyHasALiveBroadcast(t *testing.T) {
	var log []capture
	y := lnStub(t, &log, map[string]http.HandlerFunc{
		"GET " + ytBroadcastsPath: func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"items":[{"id":"studio-1","contentDetails":{"boundStreamId":"stream-77"}}]}`)
		},
	})
	_, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey})
	var act *ErrActiveBroadcastOnStream
	if !errors.As(err, &act) || act.BroadcastID != "studio-1" {
		t.Fatalf("err = %v, want ErrActiveBroadcastOnStream naming studio-1", err)
	}
	for _, c := range log {
		if c.Method == http.MethodPost {
			t.Errorf("a %s %s was sent beside a live broadcast", c.Method, c.Path)
		}
	}
	q := mustQuery(t, find(log, http.MethodGet, ytBroadcastsPath))
	if q.Get("broadcastStatus") != "active" || q.Get("mine") != "" {
		t.Errorf("the active read filters = %v: broadcastStatus and mine are mutually exclusive", q)
	}
}

func TestOpenLiveNowDeletesABroadcastItCouldNotBind(t *testing.T) {
	var log []capture
	y := lnStub(t, &log, map[string]http.HandlerFunc{
		"POST " + ytBindPath: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"error":{"errors":[{"reason":"liveBroadcastBindingNotAllowed"}]}}`)
		},
		"DELETE " + ytBroadcastsPath: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) },
	})
	if _, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey}); err == nil {
		t.Fatal("a refused bind must be an error")
	}
	del := find(log, http.MethodDelete, ytBroadcastsPath)
	if del == nil || mustQuery(t, del).Get("id") != "bcast-9" {
		t.Errorf("the unbound broadcast was not deleted: %v", callsOf(log))
	}
}

func TestOpenLiveNowSendsTheCaptionSettingsAHTTPPostDestinationNeeds(t *testing.T) {
	var log []capture
	y := lnStub(t, &log, nil)
	if _, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey, CaptionPOST: true, CaptionDelay: 30e9}); err != nil {
		t.Fatal(err)
	}
	cd := find(log, http.MethodPost, ytBroadcastsPath).Body["contentDetails"].(map[string]any)
	ms, _ := cd["monitorStream"].(map[string]any)
	if cd["closedCaptionsType"] != ytClosedCaptionsHTTPPost || ms["enableMonitorStream"] != true || ms["broadcastStreamDelayMs"] != float64(30000) {
		t.Errorf("contentDetails = %v", cd)
	}
}

// QUOTA: one open is five calls, and each is priced at the production host's published unit cost. The stub's own
// host is priced by method (a write at 50), so the calls the open made are re-priced as the real host would be.
func TestOpenLiveNowIsFiveUnitsOnTheProductionHost(t *testing.T) {
	var log []capture
	y := lnStub(t, &log, nil)
	if _, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey}); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, c := range log {
		r, _ := http.NewRequest(c.Method, "https://www.googleapis.com/youtube/v3"+c.Path+"?"+c.Query, nil)
		cost, ok := youTubeCost(r)
		if !ok {
			t.Fatalf("%s %s is not metered", c.Method, c.Path)
		}
		total += cost
	}
	if total != 5 || len(log) != 5 {
		t.Errorf("an open is %d calls costing %d units, want 5 and 5", len(log), total)
	}
}

func TestYouTubeQuotaSpentRecognisesASpentAllowanceAndNothingElse(t *testing.T) {
	var log []capture
	reason := "quotaExceeded"
	y := lnStub(t, &log, map[string]http.HandlerFunc{
		"GET " + ytStreamsPath: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"error":{"code":403,"errors":[{"reason":"`+reason+`"}]}}`)
		},
	})
	_, err := y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey})
	if !YouTubeQuotaSpent(err) {
		t.Errorf("a quotaExceeded 403 was not recognised: %v", err)
	}
	reason = "rateLimitExceeded"
	_, err = y.OpenLiveNow(context.Background(), "tok", LiveNowRequest{StreamKey: lnKey})
	if YouTubeQuotaSpent(err) {
		t.Errorf("a short rate-limit window was read as a spent allowance: %v", err)
	}
	if YouTubeQuotaSpent(errors.New("boom")) || YouTubeQuotaSpent(nil) {
		t.Error("a plain error is not a spent allowance")
	}
}
