package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// The process endpoints were the one egress path this project did not apply its
// redaction policy to. `GET /processes` and `GET /processes/{name}/logs` render
// a destination's argv, and a destination's argv is its publish URL -- with
// Facebook backup ingest on, two of them.
//
// The rendering is asserted rather than the route, because a route test needs an
// engine to own a process, and having no cheap way to look at what these two
// endpoints emit is a large part of why nobody looked. processSummary and
// processDetail exist so that is no longer true.
//
// Proven able to fail against the committed tree by deleting the line
// `a = alerts.Redact(a)` from CommandString in internal/supervisor/supervisor.go
// -- the tree built, and both endpoint payloads below carried both keys.
func TestProcessEndpointsSerialiseNoStreamKey(t *testing.T) {
	const (
		streamKey = "FB-101234567890123-0-AbMainKeyMaterial"
		backupKey = "FB-101234567890123-0-XyBackupKeyMaterial"
		host      = "live-api-s.facebook.com"
	)
	p := supervisor.New(slog.New(slog.NewTextHandler(io.Discard, nil)), supervisor.Spec{
		Name: "dest:3",
		Kind: "destination",
		Bin:  "ffmpeg",
		Args: []string{
			"-i", "srt://127.0.0.1:6001",
			"-f", "flv", "rtmps://" + host + ":443/rtmp/" + streamKey,
			"-f", "flv", "rtmps://" + host + ":443/rtmp/" + backupKey,
		},
	})

	routes := map[string]any{
		"GET /processes":             processSummary(p),
		"GET /processes/{name}/logs": processDetail("dest:3", p),
	}
	for route, payload := range routes {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("%s: payload does not encode: %v", route, err)
		}
		body := string(b)

		for _, secret := range []string{streamKey, backupKey} {
			if strings.Contains(body, secret) {
				t.Errorf("%s returns %q in its response body.\n"+
					"Every other egress in this project masks these bytes -- alerts, the "+
					"lifecycle hooks and the MQTT broker logs all do. This one reaches "+
					"devtools, screenshots and support-ticket pastes.\nbody: %s",
					route, secret, body)
			}
		}
		if !strings.Contains(body, alerts.Mask) {
			t.Errorf("%s: neither key is present and nothing is masked either, so the "+
				"command is being rendered some third way this test no longer "+
				"describes.\nbody: %s", route, body)
		}
		// A response that masked the whole command would pass everything above
		// and tell an operator nothing about which destination is failing.
		for _, want := range []string{"ffmpeg", "rtmps://", host, "/rtmp/"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s has lost %q, so the response no longer identifies the "+
					"endpoint it describes.\nbody: %s", route, want, body)
			}
		}
	}
}

// GET /system describes the MACHINE -- the FFmpeg build, its encoders, its
// filters -- and the machine is there whether or not a programme is.
//
// It used to read that off s.eng().Tools(), so an install whose engines will
// not build could not answer the one page an operator opens to find out what
// their install has. The detection is per install and every engine was handed
// the same pointer, so nothing about the answer was ever the engine's to give.
func TestSystemReportsTheDetectedFFmpegWithNoEngineRunning(t *testing.T) {
	s, h, _, sign := managerServerWithoutEngines(t, fakeTools("libx264", "hevc_videotoolbox"))
	if s.eng() != nil {
		t.Fatal("the fixture left an engine running, so this proves nothing about its absence")
	}

	var body struct {
		FFmpeg *struct {
			Version       string   `json:"version"`
			VideoEncoders []string `json:"videoEncoders"`
		} `json:"ffmpeg"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/system", nil, http.StatusOK), &body)
	if body.FFmpeg == nil {
		t.Fatal("GET /system reported no FFmpeg at all on a box that detected one: the " +
			"encoder list, the filter support and the version card are all blank on an " +
			"install with no source")
	}
	if body.FFmpeg.Version == "" || len(body.FFmpeg.VideoEncoders) == 0 {
		t.Errorf("the detected build came back empty: %+v", body.FFmpeg)
	}
}

// The listener ports on GET /system come from the STORE, not from an engine's
// snapshot, and this is the difference: an engine's copy is whatever its last
// reconcile installed, so the endpoint used to describe the PREVIOUS ports
// until something reconciled -- while the settings page it sits next to showed
// the new ones. Two screens, one install, two answers.
func TestSystemReportsAPortChangeWithoutWaitingForAReconcile(t *testing.T) {
	h, store, sign := sourceServer(t)

	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	// Written straight to the store, which is precisely the state under test:
	// nothing has told the engine, and nothing has to.
	st.Listeners.SRTPort = 9987
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}

	var body struct {
		IngestURL string `json:"ingestUrl"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/system", nil, http.StatusOK), &body)
	if !strings.Contains(body.IngestURL, "9987") {
		t.Errorf("GET /system still advertises the old port after a settings save: %q. "+
			"An operator pastes this URL into OBS, so a stale one sends their encoder "+
			"at a port nothing is listening on", body.IngestURL)
	}
}
