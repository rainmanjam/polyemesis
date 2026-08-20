package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// Expert arguments are reachable only through the expert routes.
//
// decodeJSON uses DisallowUnknownFields, so the moment these fields landed on
// db.Destination the ordinary destination endpoints started ACCEPTING them.
// Only the expert routes enforce the confirm step, the guard acknowledgement
// and the dry run; a create or an update carrying extraOutputArgs would be a
// way past all three, and the audio routing graph is exactly what -map and
// -filter_complex there could displace.
func TestDestinationEndpointsCannotSetExpertArgs(t *testing.T) {
	h, store, auth := renditionServer(t, defaultTools())

	post := func(path string, body any) *json.Decoder {
		if m, ok := body.(map[string]any); ok && path == "/api/v1/destinations" {
			withOnlySource(t, h, auth, m)
		}
		t.Helper()
		r := jsonRequest(t, http.MethodPost, path, body)
		auth(r)
		w := do(t, h, r)
		if w.Code != http.StatusCreated && w.Code != http.StatusOK {
			t.Fatalf("%s: status %d, body %s", path, w.Code, w.Body.String())
		}
		return json.NewDecoder(w.Body)
	}

	var created struct {
		Destination db.Destination `json:"destination"`
	}
	if err := post("/api/v1/destinations", map[string]any{
		"name": "twitch", "kind": "rtmp", "url": "rtmp://ingest.example/app",
		"streamKey": "key", "audioBitrate": 160,
		// The bypass attempt: a -map here would replace the routing graph.
		"extraOutputArgs":   "-map 0:a:0",
		"expertAckReencode": true,
	}).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Destination.ExpertArgsSet() || created.Destination.ExpertAckReencode {
		t.Fatalf("POST /destinations set expert args: %+v", created.Destination)
	}

	id := created.Destination.ID
	// Save something through the endpoint that is allowed to, so the update
	// below is proved to PRESERVE rather than merely to blank.
	r := jsonRequest(t, http.MethodPut, "/api/v1/destinations/"+strconv.FormatInt(id, 10)+"/expert",
		map[string]any{"inputArgs": "-analyzeduration 10M", "confirm": true})
	auth(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("put expert: status %d, body %s", w.Code, w.Body.String())
	}

	r = jsonRequest(t, http.MethodPut, "/api/v1/destinations/"+strconv.FormatInt(id, 10),
		map[string]any{
			"name": "twitch renamed", "kind": "rtmp", "url": "rtmp://ingest.example/app",
			"streamKey": "key", "audioBitrate": 160,
			"extraInputArgs": "-re", "extraOutputArgs": "-filter_complex [0:a]anull[x]",
		})
	auth(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("update destination: status %d, body %s", w.Code, w.Body.String())
	}

	row, err := store.GetDestination(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Name != "twitch renamed" {
		t.Errorf("the ordinary fields did not update: name = %q", row.Name)
	}
	if row.ExtraInputArgs != "-analyzeduration 10M" {
		t.Errorf("PUT /destinations overwrote the saved expert args: %q", row.ExtraInputArgs)
	}
	if row.ExtraOutputArgs != "" {
		t.Errorf("PUT /destinations set expert output args: %q", row.ExtraOutputArgs)
	}
}

// Every supervised process except the ingest is named with a colon — "dest:1",
// "rendition:2", "playout:source" — and a browser percent-encodes that in a
// path. chi routes on RawPath whenever it differs from Path, so the handler
// receives the still-encoded segment and matched nothing at all.
func TestProcessLogsFindsAProcessWhoseNameIsPercentEncoded(t *testing.T) {
	h, store, auth := renditionServer(t, defaultTools())

	created, err := store.CreateDestination(&db.Destination{
		Name: "twitch", Kind: db.DestRTMP, URL: "rtmp://ingest.example/app",
		StreamKey: "key", AudioBitrate: 160, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	name := "dest:" + strconv.FormatInt(created.ID, 10)

	// The engine never spawned it (the FFmpeg path is deliberately bogus), so a
	// 404 is the honest answer here — but it has to be OUR 404 rather than
	// chi's, which is what tells us routing and the name comparison both ran.
	for _, path := range []string{
		"/api/v1/processes/" + url.PathEscape(name) + "/logs",
		"/api/v1/processes/" + name + "/logs",
	} {
		r := jsonRequest(t, http.MethodGet, path, nil)
		auth(r)
		w := do(t, h, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d, want 404", path, w.Code)
		}
		if !strings.Contains(w.Body.String(), "no such process") {
			t.Errorf("%s: body %q, want the handler's own 404 rather than the router's",
				path, w.Body.String())
		}
	}
}
