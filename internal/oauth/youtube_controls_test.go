package oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

const ytControlsBroadcast = `{"items":[{"id":"bcast",
	"snippet":{"title":"t","description":"d","scheduledStartTime":"2026-07-01T10:00:00Z"},
	"status":{"lifeCycleStatus":"ready"},
	"contentDetails":{"enableDvr":true,"enableAutoStart":false,"enableAutoStop":false,
		"enableEmbed":true,"recordFromStart":true,"latencyPreference":"normal",
		"closedCaptionsType":"closedCaptionsDisabled",
		"monitorStream":{"enableMonitorStream":false,"broadcastStreamDelayMs":0}}}]}`

// ytStatefulStub serves the controls broadcast and applies a PUT's
// contentDetails to it, except the keys in ignore, which models YouTube
// accepting a call and silently not honouring a field.
func ytStatefulStub(t *testing.T, log *[]capture, body string, ignore ...string) *YouTube {
	t.Helper()
	var cur map[string]any
	if err := json.Unmarshal([]byte(body), &cur); err != nil {
		t.Fatal(err)
	}
	item := cur["items"].([]any)[0].(map[string]any)
	srv := httptest.NewServer(recordAll(t, log, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(cur)
		case r.Method == http.MethodPut:
			put := (*log)[len(*log)-1].Body
			if cd, ok := put["contentDetails"].(map[string]any); ok {
				have := item["contentDetails"].(map[string]any)
				for k, v := range cd {
					if !slices.Contains(ignore, k) {
						have[k] = v
					}
				}
			}
			_, _ = io.WriteString(w, `{"id":"bcast"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return NewYouTube(WithBaseURL(srv.URL))
}

func updateBody(t *testing.T, log []capture) map[string]any {
	t.Helper()
	for _, c := range log {
		if c.Method == "PUT" && c.Path == "/liveBroadcasts" && strings.Contains(c.Query, "contentDetails") {
			return c.Body["contentDetails"].(map[string]any)
		}
	}
	t.Fatal("no contentDetails update was sent")
	return nil
}

// The destination's controls go out in ONE liveBroadcasts.update, carrying the
// two properties the reference requires on every contentDetails update
// (enableEmbed, recordFromStart) and never the deprecated enableClosedCaptions.
func TestYouTubeBroadcastControlsAreWrittenFromTheDestination(t *testing.T) {
	var log []capture
	y := ytStatefulStub(t, &log, ytControlsBroadcast)
	on, off := true, false
	res, err := y.PushBroadcastSettings(context.Background(), "cid", "tok", BroadcastSettings{
		Defaults: BroadcastSettingsFromControls(&db.YouTubeBroadcastControls{
			LatencyPreference: db.YTLatencyLow, EnableDvr: &off, RecordFromStart: &on,
			EnableAutoStart: &on, ClosedCaptionsType: db.YTCaptionsEmbedded,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	cd := updateBody(t, log)
	if cd["latencyPreference"] != "low" || cd["enableDvr"] != false || cd["enableAutoStart"] != true ||
		cd["closedCaptionsType"] != "closedCaptionsEmbedded" || cd["recordFromStart"] != true {
		t.Errorf("controls = %v", cd)
	}
	if cd["enableEmbed"] != true {
		t.Errorf("enableEmbed was not carried: %v", cd)
	}
	for _, banned := range []string{"enableClosedCaptions", "enableLowLatency", "madeForKids"} {
		if _, ok := cd[banned]; ok {
			t.Errorf("sent the deprecated or read-only %s", banned)
		}
	}
	if len(res.Applied) != 1 || res.Applied[0] != FieldContentDetails || len(res.Warnings) != 0 {
		t.Errorf("result = %+v", res)
	}
}

// An update that leaves recordFromStart or enableEmbed out resets them, and a
// broadcast that did not report one must not be sent an invented false.
func TestYouTubeBroadcastUpdateNeverInventsEmbedOrRecording(t *testing.T) {
	var log []capture
	y, _ := ytStub(t, &log, ytEditableWithDetails) // reports neither
	dvr := false
	if _, err := y.PushBroadcastSettings(context.Background(), "cid", "tok", BroadcastSettings{EnableDvr: &dvr}); err != nil {
		t.Fatal(err)
	}
	cd := updateBody(t, log)
	if _, ok := cd["recordFromStart"]; ok {
		t.Errorf("invented recordFromStart: %v", cd)
	}
	if _, ok := cd["enableEmbed"]; ok {
		t.Errorf("invented enableEmbed: %v", cd)
	}
}

// A stored control that differs from a frozen broadcast is skipped with ONE
// warning and no call, rather than a refused update on every push.
func TestYouTubeStoredControlsAreSkippedOnAFrozenBroadcast(t *testing.T) {
	var log []capture
	y := ytStatefulStub(t, &log, strings.Replace(ytControlsBroadcast, `"ready"`, `"live"`, 1))
	off := false
	res, err := y.PushBroadcastSettings(context.Background(), "cid", "tok", BroadcastSettings{
		Defaults: BroadcastSettingsFromControls(&db.YouTubeBroadcastControls{EnableDvr: &off}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 1 || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], `"live"`) {
		t.Errorf("result = %+v", res)
	}
	if find(log, "PUT", "/liveBroadcasts") != nil {
		t.Error("sent an update to a frozen broadcast")
	}
	// Matching stored values say nothing.
	on := true
	res, err = y.PushBroadcastSettings(context.Background(), "cid", "tok", BroadcastSettings{
		Defaults: BroadcastSettingsFromControls(&db.YouTubeBroadcastControls{EnableDvr: &on}),
	})
	if err != nil || len(res.Warnings) != 0 || len(res.Skipped) != 0 {
		t.Errorf("matching stored controls warned: %+v %v", res, err)
	}
}

// The composer's value beats the destination's stored one in the same write.
func TestYouTubeComposerControlsBeatStoredOnes(t *testing.T) {
	var log []capture
	y := ytStatefulStub(t, &log, ytControlsBroadcast)
	on, off := true, false
	res, err := y.PushBroadcastSettings(context.Background(), "cid", "tok", BroadcastSettings{
		EnableDvr: &on,
		Defaults: BroadcastSettingsFromControls(&db.YouTubeBroadcastControls{
			EnableDvr: &off, EnableAutoStart: &on,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	cd := updateBody(t, log)
	if cd["enableDvr"] != true || cd["enableAutoStart"] != true {
		t.Errorf("controls = %v", cd)
	}
	puts := 0
	for _, c := range log {
		if c.Method == "PUT" {
			puts++
		}
	}
	if puts != 1 || len(res.Warnings) != 0 {
		t.Errorf("puts = %d, result = %+v", puts, res)
	}
}

// latencyPreference and closedCaptionsType are not on the update page's
// settable list: a 2xx that changed nothing must not read as applied.
func TestYouTubeUnhonouredLatencyAndCaptionsAreReportedNotApplied(t *testing.T) {
	var log []capture
	y := ytStatefulStub(t, &log, ytControlsBroadcast, "latencyPreference", "closedCaptionsType")
	low, emb := db.YTLatencyLow, db.YTCaptionsEmbedded
	res, err := y.PushBroadcastSettings(context.Background(), "cid", "tok",
		BroadcastSettings{LatencyPreference: &low, ClosedCaptionsType: &emb})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 || len(res.Skipped) != 1 || res.Skipped[0] != FieldContentDetails ||
		len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "did not apply") {
		t.Errorf("result = %+v", res)
	}
}

func TestYouTubeRecordingOffRefusalSaysWhy(t *testing.T) {
	off := false
	err := broadcastWriteAdvice(errString("403 forbidden"), "ready", BroadcastSettings{RecordFromStart: &off})
	if err == nil || !strings.Contains(err.Error(), "allowed to disable recordings") {
		t.Errorf("advice = %v", err)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestBroadcastSettingsCountTheNewTogglesAsContentDetails(t *testing.T) {
	v, b := "low", false
	for name, s := range map[string]BroadcastSettings{
		"latency": {LatencyPreference: &v}, "captions": {ClosedCaptionsType: &v}, "record": {RecordFromStart: &b},
	} {
		if s.Empty() || !s.TouchesContentDetails() {
			t.Errorf("%s does not register", name)
		}
	}
}
