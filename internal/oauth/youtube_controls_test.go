package oauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
	y, _ := ytStub(t, &log, ytControlsBroadcast)
	on, off := true, false
	res, err := y.PushCompliance(context.Background(), "cid", "tok", ComplianceTarget{}, db.Compliance{
		YouTube: &db.YouTubeBroadcastControls{
			LatencyPreference: db.YTLatencyLow, EnableDvr: &off, RecordFromStart: &on,
			EnableAutoStart: &on, ClosedCaptionsType: db.YTCaptionsEmbedded,
		},
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

func TestYouTubeBroadcastControlsFailureIsAWarningNotAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, strings.Replace(ytControlsBroadcast, `"ready"`, `"live"`, 1))
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":403,"errors":[{"reason":"enableDvrModificationNotAllowed"}]}}`)
		}
	}))
	t.Cleanup(srv.Close)
	y := NewYouTube(WithBaseURL(srv.URL))
	off := false
	res, err := y.PushCompliance(context.Background(), "cid", "tok", ComplianceTarget{}, db.Compliance{
		YouTube: &db.YouTubeBroadcastControls{EnableDvr: &off},
	})
	if err != nil {
		t.Fatalf("a frozen broadcast failed the whole push: %v", err)
	}
	if len(res.Skipped) != 1 || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], `"live"`) {
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
