package oauth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// THE BROADCAST SIDE OF HTTP POST CAPTIONS (#8).
//
// The broadcast must be closedCaptionsHttpPost before it leaves created/ready
// (docs-youtube B13), with the monitor stream on and a 30 s or 60 s delay (C5,
// C7). polyemesis sets all three when it creates a caption destination's
// scheduled broadcast, and every later liveBroadcasts.update carries them back,
// because the update is destructive by part (B16).

// TestYouTubeScheduledCaptionBroadcastIsCreatedForHTTPPostCaptions.
//
// MUTATION: the CaptionPOST branch in createScheduledBroadcast removed. Observed:
// FAIL, the insert carried no contentDetails.
func TestYouTubeScheduledCaptionBroadcastIsCreatedForHTTPPostCaptions(t *testing.T) {
	var log []capture
	y := ytCaptionStub(t, &log)
	_, err := y.IngestFor(context.Background(), "cid", "tok", "UCchannel", IngestOptions{
		CaptionPOST: true, CaptionDelay: 60 * time.Second, IngestLabel: "Captioned",
		ScheduledFor: time.Date(2030, 1, 1, 18, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("IngestFor: %v", err)
	}
	ins := find(log, http.MethodPost, ytBroadcastsPath)
	q := mustQuery(t, ins)
	if !strings.Contains(q.Get("part"), "contentDetails") {
		t.Fatalf("part = %q: the caption settings are on contentDetails, which must be named", q.Get("part"))
	}
	cd, _ := ins.Body["contentDetails"].(map[string]any)
	if cd == nil {
		t.Fatalf("the insert sent no contentDetails: %+v", ins.Body)
	}
	if cd["closedCaptionsType"] != "closedCaptionsHttpPost" {
		t.Errorf("closedCaptionsType = %v, want closedCaptionsHttpPost", cd["closedCaptionsType"])
	}
	if _, sent := cd["enableClosedCaptions"]; sent {
		t.Errorf("enableClosedCaptions is deprecated and may not be sent with closedCaptionsType (B11)")
	}
	mon, _ := cd["monitorStream"].(map[string]any)
	if mon == nil || mon["enableMonitorStream"] != true || mon["broadcastStreamDelayMs"] != float64(60000) {
		t.Errorf("monitorStream = %+v, want enabled with the 60000 ms delay", cd["monitorStream"])
	}
}

// TestYouTubeScheduledCaptionBroadcastRefusesADelayYouTubeDoesNotTake: the
// create is not sent with a delay the POST path cannot use.
func TestYouTubeScheduledCaptionBroadcastRefusesADelayYouTubeDoesNotTake(t *testing.T) {
	var log []capture
	y := ytCaptionStub(t, &log)
	_, err := y.IngestFor(context.Background(), "cid", "tok", "UCchannel", IngestOptions{
		CaptionPOST: true, CaptionDelay: 45 * time.Second,
		ScheduledFor: time.Date(2030, 1, 1, 18, 0, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatalf("a caption broadcast was created with a 45 s delay")
	}
	if find(log, http.MethodPost, ytBroadcastsPath) != nil {
		t.Fatalf("the insert was sent before the delay was checked")
	}
}

// TestYouTubeRescheduleKeepsTheBroadcastsCaptionTypeAndLatency is the latent
// bug: an update that names contentDetails without closedCaptionsType resets it
// to the default, which switches a captioned broadcast's captions off.
//
// MUTATION: the closedCaptionsType carry in writeBroadcastParts removed.
// Observed: FAIL, the PUT had no closedCaptionsType.
func TestYouTubeRescheduleKeepsTheBroadcastsCaptionTypeAndLatency(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, map[string]http.HandlerFunc{
		"GET " + ytBroadcastsPath: func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, strings.Replace(ytStubExistingBroadcast, `"enableDvr":true,`,
				`"enableDvr":true,"closedCaptionsType":"closedCaptionsHttpPost","latencyPreference":"low",`, 1))
		},
		"PUT " + ytBroadcastsPath: func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, `{"id":"bcast-1"}`)
		},
	})
	if err := y.RescheduleBroadcast(context.Background(), "tok", "bcast-1",
		time.Date(2030, 2, 3, 9, 30, 0, 0, time.UTC)); err != nil {
		t.Fatalf("RescheduleBroadcast: %v", err)
	}
	put := find(log, http.MethodPut, ytBroadcastsPath)
	cd, _ := put.Body["contentDetails"].(map[string]any)
	if cd["closedCaptionsType"] != "closedCaptionsHttpPost" {
		t.Errorf("closedCaptionsType = %v, want the broadcast's own value carried through", cd["closedCaptionsType"])
	}
	if cd["latencyPreference"] != "low" {
		t.Errorf("latencyPreference = %v, want the broadcast's own value carried through", cd["latencyPreference"])
	}
}

// TestYouTubeRescheduleSendsNoCaptionTypeItWasNotTold: an empty value read back
// is not sent, so a broadcast that reports none is not given an invalid one.
func TestYouTubeRescheduleSendsNoCaptionTypeItWasNotTold(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, map[string]http.HandlerFunc{
		"GET " + ytBroadcastsPath: func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, ytStubExistingBroadcast)
		},
		"PUT " + ytBroadcastsPath: func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, `{"id":"bcast-1"}`)
		},
	})
	if err := y.RescheduleBroadcast(context.Background(), "tok", "bcast-1",
		time.Date(2030, 2, 3, 9, 30, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	cd, _ := find(log, http.MethodPut, ytBroadcastsPath).Body["contentDetails"].(map[string]any)
	for _, k := range []string{"closedCaptionsType", "latencyPreference"} {
		if _, sent := cd[k]; sent {
			t.Errorf("%s was sent although the broadcast reported none", k)
		}
	}
}

// TestYouTubeBroadcastStateReportsTheCaptionSettings: the go-live check reads
// them from the same call the lifecycle already makes.
func TestYouTubeBroadcastStateReportsTheCaptionSettings(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, map[string]http.HandlerFunc{
		"GET " + ytBroadcastsPath: func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, `{"items":[{"id":"bcast-1","status":{"lifeCycleStatus":"ready"},
				"contentDetails":{"closedCaptionsType":"closedCaptionsHttpPost",
				"monitorStream":{"enableMonitorStream":true,"broadcastStreamDelayMs":30000}}}]}`)
		},
	})
	st, err := y.BroadcastState(context.Background(), "tok", "bcast-1")
	if err != nil {
		t.Fatal(err)
	}
	if st.ClosedCaptionsType != "closedCaptionsHttpPost" {
		t.Errorf("ClosedCaptionsType = %q", st.ClosedCaptionsType)
	}
	if st.StreamDelayMs == nil || *st.StreamDelayMs != 30000 {
		t.Errorf("StreamDelayMs = %v, want 30000", st.StreamDelayMs)
	}
	if st.MonitorStream == nil || !*st.MonitorStream {
		t.Errorf("MonitorStream = %v, want true", st.MonitorStream)
	}
}

// TestAnUltraLowLatencyBroadcastIsRefusedForPOSTCaptions (w3h tail, #33): YouTube takes no captions on an
// ultra-low-latency broadcast (B9). polyemesis never sets the latency, but the coordinator also reads broadcasts it
// did not create, so an adopted ultraLow broadcast with closedCaptionsHttpPost used to pass the go-live check and
// air without captions. The latency is read from the same call, and only ultraLow is refused: normal, low and an
// unreported latency (the default) all take captions.
//
// MUTATION, internal/oauth/lifecycle.go, in CaptionPOSTProblem: delete the ultraLow refusal. Observed: FAIL --
// "an ultraLow broadcast passed the caption check". And separately in youtube_lifecycle.go: drop the
// LatencyPreference copy. Observed: FAIL, the same way.
func TestAnUltraLowLatencyBroadcastIsRefusedForPOSTCaptions(t *testing.T) {
	for _, tc := range []struct {
		latency string
		refused bool
	}{
		{`"latencyPreference":"ultraLow",`, true},
		{`"latencyPreference":"low",`, false},
		{`"latencyPreference":"normal",`, false},
		{``, false},
	} {
		var log []capture
		y := ytScheduleStub(t, &log, map[string]http.HandlerFunc{
			"GET " + ytBroadcastsPath: func(w http.ResponseWriter, _ *http.Request) {
				io.WriteString(w, `{"items":[{"id":"bcast-1","status":{"lifeCycleStatus":"ready"},
					"contentDetails":{"closedCaptionsType":"closedCaptionsHttpPost",`+tc.latency+`
					"monitorStream":{"enableMonitorStream":true,"broadcastStreamDelayMs":30000}}}]}`)
			},
		})
		st, err := y.BroadcastState(context.Background(), "tok", "bcast-1")
		if err != nil {
			t.Fatal(err)
		}
		why := st.CaptionPOSTProblem()
		switch {
		case tc.refused && why == "":
			t.Errorf("an ultraLow broadcast passed the caption check (%s)", tc.latency)
		case !tc.refused && why != "":
			t.Errorf("latency %q was refused: %s", tc.latency, why)
		}
	}
}
