package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/eventstream/streamtest"
	"github.com/rainmanjam/polyemesis/internal/obsbridge"
	"github.com/rainmanjam/polyemesis/internal/obsbridge/obstest"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// NEITHER OBS NOR STREAMER.BOT IS RUN BY ANY TEST HERE. obs-websocket is
// obstest.Server, a mock written from the protocol document; the OBS script's
// side is the POST it makes, sent by hand. What these prove is that polyemesis
// keeps its half of each contract.

// syncBuffer is a log sink a test can read while the server writes to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type obsRig struct {
	srv   *httptest.Server
	s     *Server
	h     http.Handler
	sign  func(*http.Request)
	hub   *eventstream.Hub
	b     *obsbridge.Bridge
	logs  *syncBuffer
	audit *[]alerts.Event
	amu   *sync.Mutex
}

// obsServer is a server with a real engine, the events hub, and a running OBS
// bridge wired exactly as cmd/polyemesis wires it: OBSLoader over the store,
// the hub as publisher, the server as Actions. The server's and the bridge's
// logs go to one private buffer, and audit events to a slice.
func obsServer(t *testing.T) *obsRig {
	t.Helper()
	withBulkPacing(t, time.Millisecond)
	srv, s, h, sign, hub := eventsServer(t)
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s.log = log
	var (
		audit []alerts.Event
		amu   sync.Mutex
	)
	s.auditSink = func(ev alerts.Event) {
		amu.Lock()
		defer amu.Unlock()
		audit = append(audit, ev)
	}
	b := obsbridge.New(obsbridge.Options{Log: log, Pub: hub, Load: OBSLoader(s.store, s.box)})
	s.obs = b
	b.SetActions(s.OBSActions())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; b.Close() })
	return &obsRig{srv: srv, s: s, h: h, sign: sign, hub: hub, b: b, logs: logs, audit: &audit, amu: &amu}
}

func (o *obsRig) auditText() string {
	o.amu.Lock()
	defer o.amu.Unlock()
	raw, _ := json.Marshal(*o.audit)
	return string(raw)
}

// createDest creates a stopped destination on the fixture's programme.
func (o *obsRig) createDest(t *testing.T, name string) int64 {
	t.Helper()
	body := send(t, o.h, o.sign, http.MethodPost, "/api/v1/destinations", map[string]any{
		"name": name, "kind": "rtmp", "url": "rtmp://example.invalid/live", "streamKey": "k-" + name,
	}, http.StatusCreated)
	var created struct {
		Destination struct {
			ID int64 `json:"id"`
		} `json:"destination"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.Destination.ID == 0 {
		t.Fatalf("create %s: %v (%s)", name, err, body)
	}
	if err := o.s.store.SetDestinationEnabled(created.Destination.ID, false); err != nil {
		t.Fatal(err)
	}
	return created.Destination.ID
}

func (o *obsRig) enabled(t *testing.T, id int64) bool {
	t.Helper()
	row, err := o.s.store.GetDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	return row.Enabled
}

func (o *obsRig) putConfig(t *testing.T, cfg map[string]any, want int) []byte {
	t.Helper()
	return send(t, o.h, o.sign, http.MethodPut, "/api/v1/obs", cfg, want)
}

func withBearer(r *http.Request, tok string) *http.Request {
	r.Header.Set("Authorization", "Bearer "+tok)
	return r
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// THE WHOLE CHAIN, OVER THE WEBSOCKET: a mock obs-websocket reports a stream
// start and a stop, and the selected destination -- and only it -- is started
// and stopped through the same code as its own buttons.
func TestOBSStreamStartAndStopOverObsWebsocketDriveOnlyTheSelectedDestinations(t *testing.T) {
	o := obsServer(t)
	obs := obstest.New(t, "mock-ws-pass-9")
	sel := o.createDest(t, "youtube")
	other := o.createDest(t, "twitch")
	src := onlySourceID(t, o.h, o.sign)

	send(t, o.h, o.sign, http.MethodPut, "/api/v1/obs/password", map[string]string{"password": "mock-ws-pass-9"}, http.StatusOK)
	o.putConfig(t, map[string]any{
		"enabled": true, "connect": true, "url": obs.URL(), "sourceId": src,
		"destinations": []int64{sel}, "startOnStream": true,
		"stopOnStream": true, "stopAcknowledged": true,
	}, http.StatusOK)
	obs.WaitConnected(10 * time.Second)
	waitUntil(t, "the connected state", func() bool { return o.b.Status().Connection.State == obsbridge.ConnConnected })

	obs.StreamState(obsbridge.OutputStarted)
	waitUntil(t, "the selected destination to start", func() bool { return o.enabled(t, sel) })
	if o.enabled(t, other) {
		t.Fatal("a destination the bridge was not given was started")
	}

	obs.StreamState("OBS_WEBSOCKET_OUTPUT_RECONNECTING")
	time.Sleep(200 * time.Millisecond)
	if !o.enabled(t, sel) {
		t.Fatal("OBS reconnecting stopped the destination")
	}

	obs.StreamState(obsbridge.OutputStopped)
	waitUntil(t, "the selected destination to stop", func() bool { return !o.enabled(t, sel) })
	waitUntil(t, "two action records", func() bool { return len(o.b.Status().LastActions) == 2 })
	if a := o.b.Status().LastActions[0]; a.Action != "stop" || len(a.Results) != 1 || a.Results[0].ID != sel {
		t.Fatalf("stop record = %+v", a)
	}
}

// The script's way in, which is also the emission test for obs.stream over
// the HTTP surface a consumer reads it from.
func TestTheOBSScriptsPostDrivesTheBridgeAndReachesTheEventStream(t *testing.T) {
	o := obsServer(t)
	sel := o.createDest(t, "kick")
	src := onlySourceID(t, o.h, o.sign)
	obsTok := createScopedToken(t, o.h, o.sign, "obs script", db.ScopeOBS)
	admin := createScopedToken(t, o.h, o.sign, "dash", db.ScopeAdmin)

	post := func(body any) *httptest.ResponseRecorder {
		t.Helper()
		return do(t, o.h, withBearer(jsonRequest(t, http.MethodPost, "/api/v1/obs/events", body), obsTok))
	}
	// Off: refused with a sentence that says where to turn it on.
	if w := post(map[string]string{"event": "stream.started"}); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), "switched off") {
		t.Fatalf("disabled bridge: %d %s", w.Code, w.Body.String())
	}
	o.putConfig(t, map[string]any{
		"enabled": true, "sourceId": src, "destinations": []int64{sel}, "startOnStream": true,
	}, http.StatusOK)
	for _, bad := range []any{
		map[string]string{"event": "stream.exploded"},
		map[string]string{"event": "scene.changed"},
		map[string]string{"event": "scene.changed", "scene": "a\x00b"},
		map[string]any{"event": "stream.started", "extra": true},
	} {
		if w := post(bad); w.Code != http.StatusBadRequest {
			t.Errorf("%v: %d %s", bad, w.Code, w.Body.String())
		}
	}

	res := openEvents(t, o.srv, "?topics=obs", admin, nil)
	r := bufio.NewReader(res.Body)
	waitForListener(t, o.hub)
	if w := post(map[string]string{"event": "stream.started"}); w.Code != http.StatusAccepted {
		t.Fatalf("stream.started: %d %s", w.Code, w.Body.String())
	}
	f := streamtest.ExpectSSE(t, r, eventstream.TypeOBSStream)
	if raw, _ := json.Marshal(f.Env.Data); !strings.Contains(string(raw), `"via":"script"`) || !strings.Contains(string(raw), `"state":"started"`) {
		t.Fatalf("obs.stream frame = %s", raw)
	}
	waitUntil(t, "the destination to start", func() bool { return o.enabled(t, sel) })

	if w := post(map[string]string{"event": "scene.changed", "scene": "Gameplay"}); w.Code != http.StatusAccepted {
		t.Fatalf("scene.changed: %d", w.Code)
	}
	streamtest.ExpectSSE(t, r, eventstream.TypeOBSScene)
}

// THE OBS SCOPE REACHES ONE ROUTE. Walked over the real router, every method
// and pattern chi knows, including the groups outside /api/v1's main one
// (/ws, /metrics, downloads, /mcp, HLS, playout): an obs token must get
// nothing there that a caller with no credential at all does not get.
func TestAnOBSTokenReachesOnlyTheOBSEventsRoute(t *testing.T) {
	o := obsServer(t)
	obsTok := createScopedToken(t, o.h, o.sign, "obs script", db.ScopeOBS)
	read := createScopedToken(t, o.h, o.sign, "dash", db.ScopeRead)
	mux, ok := o.s.Handler().(*chi.Mux)
	if !ok {
		t.Fatal("Server.Handler is not a *chi.Mux; this walk cannot fall back to a list")
	}
	param := regexp.MustCompile(`\{[^}]+\}`)
	type route struct{ method, pattern string }
	var routes []route
	_ = chi.Walk(mux, func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, route{method, pattern})
		return nil
	})
	if len(routes) < 200 {
		t.Fatalf("the walk found %d routes; it is not looking at the real router", len(routes))
	}
	reachedEvents := false
	for _, rt := range routes {
		path := param.ReplaceAllString(rt.pattern, "1")
		path = strings.ReplaceAll(path, "*", "x")
		req := func(tok string) int {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			body := map[string]any{}
			if rt.pattern == "/api/v1/obs/events" {
				// A valid fact, so the answer is the bridge's and not the
				// body validator's.
				body["event"] = "stream.started"
			}
			r := jsonRequest(t, rt.method, path, body).WithContext(ctx)
			if tok != "" {
				withBearer(r, tok)
			}
			return do(t, o.h, r).Code
		}
		got, anon := req(obsTok), req("")
		if rt.method == http.MethodPost && rt.pattern == "/api/v1/obs/events" {
			// The positive control: reached, and refused only because the
			// bridge is off -- not by the scope.
			if got != http.StatusConflict {
				t.Errorf("POST /api/v1/obs/events with an obs token = %d; want 409 (reached, bridge off)", got)
			}
			reachedEvents = true
			continue
		}
		if got < 400 && got != anon {
			t.Errorf("%s %s: an obs token got %d where no credential gets %d", rt.method, rt.pattern, got, anon)
		}
	}
	if !reachedEvents {
		t.Fatal("POST /api/v1/obs/events is not in the router")
	}
	// The differential that proves the obs scope is not read in disguise: the
	// same GET a read token is allowed is refused to it.
	for _, c := range []struct {
		tok  string
		want int
	}{{read, http.StatusOK}, {obsTok, http.StatusForbidden}} {
		if w := do(t, o.h, withBearer(jsonRequest(t, http.MethodGet, "/api/v1/status", nil), c.tok)); w.Code != c.want {
			t.Errorf("GET /status = %d, want %d", w.Code, c.want)
		}
	}
	// And /mcp, which is outside requireScope. The walk above saw it switched
	// off (a fresh install), where every caller gets the unknown-endpoint 404.
	// Switched on with every level -- the state in which an obs token reaching
	// a tool would matter -- it is still refused: /mcp takes only an MCP-scope
	// token (mcp_access.go), and obs is not one.
	if _, err := o.s.store.PutMCPAccess(true, db.MCPLevels()); err != nil {
		t.Fatal(err)
	}
	r := jsonRequest(t, http.MethodPost, MCPPath, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	r.Header.Set("Accept", "application/json, text/event-stream")
	if w := do(t, o.h, withBearer(r, obsTok)); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "\"tools\"") {
		t.Errorf("POST /mcp (MCP on, every level) with an obs token = %d %s; want 403", w.Code, w.Body.String())
	}
}

// THE PASSWORD NEVER LEAVES. Stored, used against a mock that refuses it (so
// it has been through the client, the handshake, a refusal and a retry), and
// then looked for everywhere this process writes: every principal's GET and
// PUT, the event stream, the log, the audit trail and the metrics.
func TestTheOBSWebsocketPasswordAppearsInNoResponseLogEventAuditOrMetric(t *testing.T) {
	o := obsServer(t)
	obs := obstest.New(t, "the-one-obs-has")
	// Built at run time: no literal in this file looks like a credential.
	secret := "obsws-" + strings.Repeat("Q7x", 5)
	admin := createScopedToken(t, o.h, o.sign, "deploy", db.ScopeAdmin)
	read := createScopedToken(t, o.h, o.sign, "dash", db.ScopeRead)
	src := onlySourceID(t, o.h, o.sign)

	res := openEvents(t, o.srv, "", admin, nil)
	frames := make(chan string, 64)
	go func() {
		r := bufio.NewReader(res.Body)
		for {
			f, ok := streamtest.ReadFrame(r)
			if !ok {
				close(frames)
				return
			}
			raw, _ := json.Marshal(f.Env)
			frames <- string(raw)
		}
	}()
	waitForListener(t, o.hub)

	var bodies []string
	w := do(t, o.h, withBearer(jsonRequest(t, http.MethodPut, "/api/v1/obs/password", map[string]string{"password": secret}), admin))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT password: %d %s", w.Code, w.Body.String())
	}
	bodies = append(bodies, w.Body.String())
	bodies = append(bodies, string(o.putConfig(t, map[string]any{
		"enabled": true, "connect": true, "url": obs.URL(), "sourceId": src,
	}, http.StatusOK)))
	waitUntil(t, "the mock to refuse the password", func() bool {
		_, refusals := obs.Counts()
		return refusals > 0 && o.b.Status().Connection.State == obsbridge.ConnRefused
	})
	got, _ := o.s.store.GetOBSPassword(o.s.box)
	if got != secret {
		t.Fatal("positive control: the password was not stored, so its absence below proves nothing")
	}

	for _, sign := range []func(*http.Request){
		o.sign,
		func(r *http.Request) { withBearer(r, admin) },
		func(r *http.Request) { withBearer(r, read) },
	} {
		r := jsonRequest(t, http.MethodGet, "/api/v1/obs", nil)
		sign(r)
		w := do(t, o.h, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /obs: %d", w.Code)
		}
		bodies = append(bodies, w.Body.String())
		if !strings.Contains(w.Body.String(), `"hasPassword":true`) {
			t.Error("GET /obs does not say a password is set")
		}
	}
	r := withBearer(jsonRequest(t, http.MethodGet, "/api/v1/metrics", nil), admin)
	bodies = append(bodies, do(t, o.h, r).Body.String())

	for i, b := range bodies {
		if strings.Contains(b, secret) {
			t.Errorf("response %d carries the obs-websocket password: %s", i, b)
		}
	}
	// Drain what the stream sent.
	sawConnection := false
	timeout := time.After(500 * time.Millisecond)
	for done := false; !done; {
		select {
		case f, ok := <-frames:
			if !ok {
				done = true
				break
			}
			if strings.Contains(f, secret) {
				t.Errorf("an event carries the password: %s", f)
			}
			if strings.Contains(f, `"obs.connection"`) {
				sawConnection = true
			}
		case <-timeout:
			done = true
		}
	}
	if !sawConnection {
		t.Error("positive control: no obs.connection event was read, so the stream check read nothing")
	}
	if strings.Contains(o.logs.String(), secret) {
		t.Errorf("the log carries the password")
	}
	if !strings.Contains(o.logs.String(), "obs-websocket session ended") {
		t.Error("positive control: the bridge logged nothing, so the log check proves nothing")
	}
	a := o.auditText()
	if strings.Contains(a, secret) {
		t.Errorf("the audit trail carries the password: %s", a)
	}
	if !strings.Contains(a, "obs") {
		t.Errorf("positive control: no audit event named the obs section: %s", a)
	}
}

func TestTheOBSConfigurationIsValidatedAndTheAddressIsMaskedForReadTokens(t *testing.T) {
	o := obsServer(t)
	sel := o.createDest(t, "youtube")
	src := onlySourceID(t, o.h, o.sign)
	read := createScopedToken(t, o.h, o.sign, "dash", db.ScopeRead)

	// Stop without the acknowledgement: refused with stop-all's warning.
	body := o.putConfig(t, map[string]any{
		"enabled": true, "sourceId": src, "destinations": []int64{sel}, "stopOnStream": true,
	}, http.StatusBadRequest)
	if !strings.Contains(string(body), "YouTube broadcast") {
		t.Errorf("refusal = %s", body)
	}
	// A destination that does not exist, and one on another programme.
	other := &db.Source{Name: "Studio B", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := o.s.store.CreateSource(other); err != nil {
		t.Fatal(err)
	}
	body = o.putConfig(t, map[string]any{"enabled": true, "sourceId": other.ID, "destinations": []int64{sel, 9999}}, http.StatusBadRequest)
	for _, want := range []string{"another programme", "does not exist"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("refusal %s lacks %q", body, want)
		}
	}
	// No programme named: the default is resolved and stored as an id, so the
	// destination on it is accepted rather than refused as "another programme".
	body = o.putConfig(t, map[string]any{"enabled": true, "destinations": []int64{sel}}, http.StatusOK)
	if !strings.Contains(string(body), fmt.Sprintf(`"sourceId":%d`, src)) {
		t.Errorf("an omitted sourceId was not resolved to the default programme: %s", body)
	}
	// hasPassword cannot be set by a PUT.
	body = o.putConfig(t, map[string]any{
		"enabled": true, "connect": true, "url": "ws://192.168.1.20:4455", "sourceId": src,
		"destinations": []int64{sel}, "hasPassword": true,
	}, http.StatusOK)
	if !strings.Contains(string(body), `"hasPassword":false`) {
		t.Errorf("a PUT claimed a password: %s", body)
	}
	w := do(t, o.h, withBearer(jsonRequest(t, http.MethodGet, "/api/v1/obs", nil), read))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "192.168.1.20") {
		t.Errorf("a read token was shown the obs-websocket address: %d %s", w.Code, w.Body.String())
	}
	r := jsonRequest(t, http.MethodGet, "/api/v1/obs", nil)
	o.sign(r)
	if w := do(t, o.h, r); !strings.Contains(w.Body.String(), "192.168.1.20") {
		t.Errorf("the operator was not shown the address: %s", w.Body.String())
	}
	// A read token cannot write it.
	if w := do(t, o.h, withBearer(jsonRequest(t, http.MethodPut, "/api/v1/obs", map[string]any{}), read)); w.Code != http.StatusForbidden {
		t.Errorf("PUT /obs with a read token = %d", w.Code)
	}
}

// ------------------------------------------------------------- scene rules

func simpleProfile(tracks ...int) routing.Profile {
	p := routing.Profile{Mode: routing.ModeSimple, Normalize: routing.NormAuto, SampleRate: 48000}
	for i := 0; i < 4; i++ {
		on := false
		for _, t := range tracks {
			on = on || t == i
		}
		p.Tracks = append(p.Tracks, routing.TrackSel{Track: i, Enabled: on, Gain: 1})
	}
	return p
}

func TestASceneRuleIsExpressedAsMutesOnTheTracksAlreadyInTheMix(t *testing.T) {
	cur := simpleProfile(0, 1, 2)
	preset := simpleProfile(2, 3)
	next, changed, notes, silent := sceneLevels(cur, preset)
	if !changed || silent {
		t.Fatalf("changed=%v silent=%v", changed, silent)
	}
	for i, want := range []bool{true, true, false, false} {
		if next.Tracks[i].Muted != want {
			t.Errorf("track %d muted=%v, want %v", i+1, next.Tracks[i].Muted, want)
		}
		if next.Tracks[i].Enabled != cur.Tracks[i].Enabled || next.Tracks[i].Gain != cur.Tracks[i].Gain {
			t.Errorf("track %d: a scene rule changed more than its mute", i+1)
		}
	}
	if cur.Tracks[0].Muted {
		t.Fatal("sceneLevels edited the stored profile in place")
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "track 4") {
		t.Errorf("notes = %q; the preset's track 4 is not in the mix and must be said so", notes)
	}
	src := routing.DefaultSource()
	if !levelsOnly(cur, next, src) {
		t.Error("mutes on tracks already in the mix were judged to need a restart")
	}
	// Selecting another track IS a restart, and levelsOnly says so.
	if levelsOnly(cur, simpleProfile(0, 1, 2, 3), src) {
		t.Error("adding a track to the mix was judged live")
	}
	// A preset that keeps none of the mix would silence it: refused.
	if _, _, _, silent := sceneLevels(simpleProfile(0), simpleProfile(3)); !silent {
		t.Error("a rule that mutes every track in the mix was not refused")
	}
}

func TestASceneSwitchAppliesItsRuleThroughTheTrackLevelPath(t *testing.T) {
	o := obsServer(t)
	sel := o.createDest(t, "twitch")
	src := onlySourceID(t, o.h, o.sign)
	row, err := o.s.store.GetDestination(sel)
	if err != nil {
		t.Fatal(err)
	}
	row.Profile = simpleProfile(0, 1, 2)
	if _, err := o.s.store.UpdateDestination(row); err != nil {
		t.Fatal(err)
	}
	o.putConfig(t, map[string]any{
		"enabled": true, "sourceId": src, "destinations": []int64{sel},
		"scenes": []map[string]any{{"scene": "Just Chatting", "preset": routing.PresetMicOnly,
			"opts": map[string]int{"micTrack": 2}}},
	}, http.StatusOK)
	if err := o.b.Handle(obsbridge.Event{Kind: obsbridge.KindSceneChanged, Scene: "Just Chatting", Via: obsbridge.ViaScript}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the scene's action record", func() bool { return len(o.b.Status().LastActions) == 1 })
	rec := o.b.Status().LastActions[0]
	if rec.Action != "scene" || rec.Preset != routing.PresetMicOnly || len(rec.Results) != 1 || rec.Results[0].Outcome != outcomeApplied {
		t.Fatalf("record = %+v", rec)
	}
	row, _ = o.s.store.GetDestination(sel)
	for i, want := range []bool{true, true, false} {
		if row.Profile.Tracks[i].Muted != want || !row.Profile.Tracks[i].Enabled {
			t.Errorf("track %d = %+v; want enabled and muted=%v", i+1, row.Profile.Tracks[i], want)
		}
	}
}

func TestASceneSwitchIsNotAppliedWhereLiveLevelsAreNotAvailable(t *testing.T) {
	o := obsServer(t)
	sel := o.createDest(t, "twitch")
	row, _ := o.s.store.GetDestination(sel)
	row.Profile = simpleProfile(0, 1, 2)
	if _, err := o.s.store.UpdateDestination(row); err != nil {
		t.Fatal(err)
	}
	liveLevelsSupported = func() bool { return false }
	t.Cleanup(func() { liveLevelsSupported = func() bool { return true } })
	out := o.s.applySceneLevels(sel, obsbridge.SceneRule{Scene: "x", Preset: routing.PresetMicOnly})
	if out.Outcome != string(bulkSkipped) || !strings.Contains(out.Message, "Windows") {
		t.Fatalf("on a platform without live levels: %+v", out)
	}
	row, _ = o.s.store.GetDestination(sel)
	for _, tr := range row.Profile.Tracks {
		if tr.Muted {
			t.Fatal("a skipped scene rule still wrote a mute")
		}
	}
}
