package api

import (
	"encoding/json"
	"io"
	"log/slog"
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
