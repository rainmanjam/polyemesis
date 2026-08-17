package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/secrets"
	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// freeUDPPort asks the kernel for a UDP port nobody is using.
//
// Bind :0, read back what we got, release it. There is a window between the
// release and the manager's own bind, and it is deliberately not defended
// against: the alternative is a probe that tries to PREDICT whether the real
// bind will succeed, and that cannot be done honestly here. gosrt sets
// SO_REUSEADDR on its listener and a plain ListenPacket does not, so a probe and
// the real thing do not even agree on what "in use" means.
//
// If the window is lost, the manager fails to bind and the test that cares says
// so while naming the port. That is the right failure: loud, specific, and about
// the thing that actually happened.
// #211: one implementation, in internal/testenv, instead of four copies of it.
// The reasoning above is unchanged and is repeated there, once.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	return testenv.FreeUDPPort(t)
}

// srtPortOf reports the port the fixture settled on, for failure messages that
// name a real number instead of "the SRT port".
func srtPortOf(t *testing.T, store *db.DB) int {
	t.Helper()
	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	return st.Listeners.SRTPort
}

// renditionServer is testServer plus a real engine, because every rendition
// mutation reconciles.
//
// The FFmpeg path is deliberately bogus: the engine is created but never
// Start()ed, so the only child a reconcile tries to spawn is the ingest, and a
// path that cannot exec makes that a logged failure instead of a real encoder
// binding a real port from a unit test.
func renditionServer(t *testing.T, tools *ffmpeg.Tools) (http.Handler, *db.DB, func(*http.Request)) {
	t.Helper()
	_, h, store, sign := engineServer(t, tools, Options{})
	return h, store, sign
}

// engineServer is renditionServer with the caller's Options merged in, and it
// exists for the fixtures that need BOTH a real engine and a stub standing in
// for a platform API.
//
// The two used to be alternatives: testServerWith could stub the providers and
// left the manager nil, renditionServer built the manager and stubbed nothing.
// requireSource is what made the combination necessary -- a route that reaches
// a platform and then reconciles is refused outright on a server with no
// programme, so a test about what Facebook received can no longer be written
// against a manager-less server.
//
// Log, Config, DB, Secrets, Engine, Events and Version are this fixture's to
// set; anything else the caller passes is kept.
func engineServer(t *testing.T, tools *ffmpeg.Tools, o Options) (*Server, http.Handler, *db.DB, func(*http.Request)) {
	t.Helper()

	dir := t.TempDir()
	store := dbtest.OpenAt(t, filepath.Join(dir, "polyemesis.db"))
	if _, err := store.CreateUser("admin", testPassword); err != nil {
		t.Fatalf("create admin: %v", err)
	}

	box, err := secrets.New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	cfg := config.Config{DataDir: dir}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	// Give the shared SRT listener a port this test owns.
	//
	// The manager binds a REAL UDP socket on Start, even here -- the bogus FFmpeg
	// path stops encoders from spawning, but the listener is gosrt inside our own
	// process and nothing about a fake FFmpeg prevents it. On the 6000 default
	// that makes every test in this fixture depend on 6000 being free, and on a
	// developer machine it very often is not: Docker Desktop publishes on it.
	//
	// The symptom was not a clear "port in use" either. The listener silently
	// failed, tokenEnforced read false, and the test that asserts on it reported
	// something that could not be true. An isolated port removes the shared
	// resource rather than teaching each test to tolerate losing it.
	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.Listeners.SRTPort = freeUDPPort(t)
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}

	bus := events.NewBroker()
	// A manager rather than a bare engine: the API is source-aware now, and
	// Start is what creates the engine for the source the migration made.
	eng := engine.NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), cfg, store, tools, bus)
	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("engine manager Start: %v", err)
	}
	t.Cleanup(eng.Stop)

	o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	o.Config = cfg
	o.DB = store
	o.Secrets = box
	o.Engine = eng
	o.Events = bus
	o.Version = "test"
	s := New(o)
	h := s.Handler()
	lastTestServer = s
	return s, h, store, login(t, h)
}

// lastTestServer is the Server most recently built by renditionServer.
//
// A few tests need the Server itself rather than its handler -- to ask where
// the stems directory is, or what the data directory resolved to -- and those
// answers must come from the SAME instance under test, not from a second one
// built with the same config. Package tests do not run in parallel, so the last
// one built is the one the caller just asked for.
var lastTestServer *Server

// serverUnderTest returns the Server behind a handler from renditionServer.
func serverUnderTest(t *testing.T, _ http.Handler) *Server {
	t.Helper()
	if lastTestServer == nil {
		t.Fatal("no test server has been built")
	}
	return lastTestServer
}

// fakeTools is a detected FFmpeg that reports a plausible encoder list without
// one being installed on the machine running the tests.
func fakeTools(encoders ...string) *ffmpeg.Tools {
	return &ffmpeg.Tools{
		FFmpeg: filepath.Join("/nonexistent", "ffmpeg"), FFprobe: filepath.Join("/nonexistent", "ffprobe"),
		Version: "6.1", Major: 6, Minor: 1,
		VideoEncoders: encoders,
	}
}

func defaultTools() *ffmpeg.Tools {
	return fakeTools(string(db.EncoderX264), string(db.EncoderX265), "mpeg4")
}

// send signs and performs a request, failing the test unless the status matches.
func send(t *testing.T, h http.Handler, sign func(*http.Request), method, path string, body any, want int) []byte {
	t.Helper()
	r := jsonRequest(t, method, path, body)
	sign(r)
	w := do(t, h, r)
	if w.Code != want {
		t.Fatalf("%s %s: status %d, want %d, body %s", method, path, w.Code, want, w.Body.String())
	}
	return w.Body.Bytes()
}

// mustJSONError asserts a request produced the expected status AND a JSON body
// carrying an error message -- that is, that a real handler answered.
//
// A status code alone proves nothing about a route. api.go registers the
// embedded SPA as the root mux's NotFound handler, so an unrouted /api/v1/...
// path is answered by web.Handler either way:
//
//   - with internal/web/dist/index.html present, as it is after `make ui`: 200
//     and the SPA bundle. So a 200-only assertion passes with its route deleted
//     on a developer's machine -- measured, on the test then called
//     TestDeliveriesRouteExists, since rewritten and renamed to
//     TestDeliveriesRouteReturnsTheAttemptsItMade. The measurement was against
//     the old status-only version; the name is given so the record can be
//     traced, not re-run.
//   - with it absent, as in CI, whose Go job never builds the UI: 404 and
//     "UI not built.". So a 404-only assertion passes with its route deleted in
//     CI -- measured, on TestScheduleRoutesRejectAnUnknownID.
//
// Neither configuration can produce JSON with an "error" field, which is what
// makes this the assertion to reach for whenever a test's only claim about a
// route is the status it answered with.
//
// One nuance worth recording, because it decides how much a status is worth:
// removing ONE method from a path that still has others registered gives chi's
// own 405, which a status assertion does catch. The fallback is only reached
// when the whole pattern goes -- a handler renamed, a Group deleted, a route
// block moved and half-reconnected. That is the refactor this is written for.
func mustJSONError(t *testing.T, h http.Handler, sign func(*http.Request), method, path string, body any, want int) string {
	t.Helper()
	raw := send(t, h, sign, method, path, body, want)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s %s: %d, but the body is not JSON (%v) -- the SPA fallback "+
			"answered instead of a handler: %.80s", method, path, want, err, raw)
	}
	msg, _ := out["error"].(string)
	if msg == "" {
		t.Fatalf("%s %s: %d with no error field; the operator is told a number "+
			"and nothing else: %s", method, path, want, raw)
	}
	return msg
}

func decodeInto(t *testing.T, raw []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

func createRendition(t *testing.T, h http.Handler, sign func(*http.Request), body any) *db.Rendition {
	t.Helper()
	var resp struct {
		Rendition *db.Rendition `json:"rendition"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/renditions", body, http.StatusCreated), &resp)
	return resp.Rendition
}

func createDestination(t *testing.T, h http.Handler, sign func(*http.Request), body map[string]any) *db.Destination {
	t.Helper()
	var resp struct {
		Destination *db.Destination `json:"destination"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/destinations", body, http.StatusCreated), &resp)
	return resp.Destination
}

func destinationBody(name string, enabled bool, renditionID *int64) map[string]any {
	body := map[string]any{
		"name": name, "kind": "rtmp", "platform": "custom",
		"url": "rtmp://example.com/live", "streamKey": "abc", "enabled": enabled,
	}
	if renditionID != nil {
		body["renditionId"] = *renditionID
	}
	return body
}

// A route that exists but is unauthenticated answers 401; one that was never
// registered falls through to the SPA handler. So this pins registration.
func TestEveryRenditionRouteIsRegisteredAndRequiresAuth(t *testing.T) {
	h, _, _ := renditionServer(t, defaultTools())

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "list", method: http.MethodGet, path: "/api/v1/renditions"},
		{name: "create", method: http.MethodPost, path: "/api/v1/renditions"},
		{name: "presets", method: http.MethodGet, path: "/api/v1/renditions/presets"},
		{name: "read", method: http.MethodGet, path: "/api/v1/renditions/1"},
		{name: "update", method: http.MethodPut, path: "/api/v1/renditions/1"},
		{name: "delete", method: http.MethodDelete, path: "/api/v1/renditions/1"},
		{name: "restart", method: http.MethodPost, path: "/api/v1/renditions/1/restart"},
		{name: "encoders", method: http.MethodGet, path: "/api/v1/encoders"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := do(t, h, jsonRequest(t, tt.method, tt.path, nil))
			if w.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d (route not registered?)", w.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestCreateRenditionNeedsOnlyNameHeightAndBitrate(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())

	got := createRendition(t, h, sign, map[string]any{
		"name": "1080p", "height": 1080, "videoBitrate": 6000,
	})

	// The store fills these in; the API must not require the client to know them.
	if got.Encoder != db.EncoderX264 {
		t.Errorf("encoder = %q, want %q", got.Encoder, db.EncoderX264)
	}
	if got.Preset != "veryfast" {
		t.Errorf("preset = %q, want %q", got.Preset, "veryfast")
	}
	if got.GOPSeconds != 2 {
		t.Errorf("gopSeconds = %v, want 2", got.GOPSeconds)
	}
	// Width unset means "keep the source's aspect", not "zero pixels wide".
	if got.Width != 0 {
		t.Errorf("width = %d, want 0", got.Width)
	}
}

func TestCreateRenditionIsRejectedWhen(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())

	tests := []struct {
		name string
		body map[string]any
		want string
	}{
		{
			name: "the name is blank",
			body: map[string]any{"name": "  ", "height": 720, "videoBitrate": 3000},
			want: "name is required",
		},
		{
			name: "a dimension is odd",
			body: map[string]any{"name": "odd", "height": 721, "videoBitrate": 3000},
			want: "even number of pixels",
		},
		{
			name: "the bitrate looks like Mbps",
			body: map[string]any{"name": "typo", "height": 1080, "videoBitrate": 6},
			want: "bitrate",
		},
		{
			name: "the encoder is not one we know",
			body: map[string]any{"name": "x", "height": 720, "videoBitrate": 3000, "encoder": "libaom-av1"},
			want: "encoder",
		},
		{
			name: "the preset carries shell punctuation",
			body: map[string]any{"name": "x", "height": 720, "videoBitrate": 3000, "preset": "veryfast; rm -rf /"},
			want: "preset",
		},
		{
			name: "an unknown field is sent",
			body: map[string]any{"name": "x", "height": 720, "videoBitrate": 3000, "audioBitrate": 160},
			want: "unknown field",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := send(t, h, sign, http.MethodPost, "/api/v1/renditions", tt.body, http.StatusBadRequest)
			if !strings.Contains(string(raw), tt.want) {
				t.Errorf("error %s does not mention %q", raw, tt.want)
			}
		})
	}
}

// A rendition has no audio field and must never grow one: audio is passed
// through with -c:a copy so per-destination routing keeps working on top of a
// shared video encode.
func TestARenditionCannotCarryAudioSettings(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())

	for _, field := range []string{"audioBitrate", "audioCodec", "profile"} {
		t.Run(field, func(t *testing.T) {
			body := map[string]any{"name": "x", "height": 720, "videoBitrate": 3000, field: "anything"}
			send(t, h, sign, http.MethodPost, "/api/v1/renditions", body, http.StatusBadRequest)
		})
	}
}

func TestUpdateRenditionKeepsFieldsTheClientDidNotSend(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	created := createRendition(t, h, sign, map[string]any{
		"name": "1080p60", "width": 1920, "height": 1080, "fps": 60, "videoBitrate": 6000,
		"note": "for the platforms that cap below 4K",
	})

	path := "/api/v1/renditions/" + strconv.FormatInt(created.ID, 10)
	var resp struct {
		Rendition *db.Rendition `json:"rendition"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPut, path,
		map[string]any{"videoBitrate": 4500}, http.StatusOK), &resp)

	if resp.Rendition.VideoBitrate != 4500 {
		t.Errorf("videoBitrate = %d, want 4500", resp.Rendition.VideoBitrate)
	}
	if resp.Rendition.Name != created.Name || resp.Rendition.Height != 1080 || resp.Rendition.FPS != 60 {
		t.Errorf("partial update clobbered the row: %+v", resp.Rendition)
	}
	if resp.Rendition.Note != created.Note {
		t.Errorf("note = %q, want %q", resp.Rendition.Note, created.Note)
	}
}

// Mutation: comment out any of the three r.Get/r.Put/r.Delete registrations for
// "/renditions/{id}" in api.go. Observed FAIL in both UI configurations. The
// status-only version passed with all three deleted whenever index.html was
// absent, which is how CI runs.
func TestMissingRenditionIsNotFound(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())

	tests := []struct {
		name   string
		method string
		body   any
	}{
		{name: "read", method: http.MethodGet},
		{name: "update", method: http.MethodPut, body: map[string]any{"videoBitrate": 3000}},
		{name: "delete", method: http.MethodDelete},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustJSONError(t, h, sign, tt.method, "/api/v1/renditions/404", tt.body, http.StatusNotFound)
		})
	}
}

func TestRenditionListReportsHowManyDestinationsUseEach(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	rend := createRendition(t, h, sign, map[string]any{"name": "720p", "height": 720, "videoBitrate": 3000})

	createDestination(t, h, sign, destinationBody("twitch", true, &rend.ID))
	createDestination(t, h, sign, destinationBody("kick", false, &rend.ID))
	createDestination(t, h, sign, destinationBody("youtube", true, nil))

	var list []struct {
		Rendition           *db.Rendition `json:"rendition"`
		Destinations        int           `json:"destinations"`
		EnabledDestinations int           `json:"enabledDestinations"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/renditions", nil, http.StatusOK), &list)

	if len(list) != 1 {
		t.Fatalf("got %d renditions, want 1", len(list))
	}
	if list[0].Destinations != 2 {
		t.Errorf("destinations = %d, want 2", list[0].Destinations)
	}
	// Only the enabled one costs an encode; the passthrough destination is not
	// counted against any rendition at all.
	if list[0].EnabledDestinations != 1 {
		t.Errorf("enabledDestinations = %d, want 1", list[0].EnabledDestinations)
	}
}

func TestDeletingARenditionInUseWarnsHowManyDestinationsFellBackToPassthrough(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())
	rend := createRendition(t, h, sign, map[string]any{"name": "1080p60", "height": 1080, "videoBitrate": 6000})

	live := createDestination(t, h, sign, destinationBody("twitch", true, &rend.ID))
	idle := createDestination(t, h, sign, destinationBody("kick", false, &rend.ID))

	var resp struct {
		Status              string `json:"status"`
		Destinations        int    `json:"destinations"`
		EnabledDestinations int    `json:"enabledDestinations"`
		Warning             string `json:"warning"`
	}
	decodeInto(t, send(t, h, sign, http.MethodDelete,
		"/api/v1/renditions/"+strconv.FormatInt(rend.ID, 10), nil, http.StatusOK), &resp)

	if resp.Destinations != 2 || resp.EnabledDestinations != 1 {
		t.Errorf("counts = %d/%d, want 2/1", resp.Destinations, resp.EnabledDestinations)
	}
	// Silently downgrading a live output to the source video could get the
	// stream rejected, so the response has to say it happened.
	if resp.Warning == "" {
		t.Error("delete returned no warning while destinations were using the rendition")
	}
	if !strings.Contains(resp.Warning, "passthrough") {
		t.Errorf("warning %q does not say the destinations fell back to passthrough", resp.Warning)
	}

	// The destinations survive, keep their enabled flag, and are passthrough.
	for _, want := range []*db.Destination{live, idle} {
		got, err := store.GetDestination(want.ID)
		if err != nil {
			t.Fatalf("destination %d disappeared with the rendition: %v", want.ID, err)
		}
		if got.RenditionID != nil {
			t.Errorf("destination %d renditionId = %v, want nil", got.ID, *got.RenditionID)
		}
		if got.Enabled != want.Enabled {
			t.Errorf("destination %d enabled = %v, want %v", got.ID, got.Enabled, want.Enabled)
		}
	}
}

func TestDeletingAnUnusedRenditionWarnsAboutNothing(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	rend := createRendition(t, h, sign, map[string]any{"name": "720p", "height": 720, "videoBitrate": 3000})

	raw := send(t, h, sign, http.MethodDelete,
		"/api/v1/renditions/"+strconv.FormatInt(rend.ID, 10), nil, http.StatusOK)
	if strings.Contains(string(raw), "warning") {
		t.Errorf("unused rendition delete carried a warning: %s", raw)
	}
}

func TestPresetsAreOfferedWithTheDisclaimerVerbatim(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())

	var resp struct {
		Presets    []db.RenditionPreset `json:"presets"`
		Disclaimer string               `json:"disclaimer"`
		Bounds     map[string]float64   `json:"bounds"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/renditions/presets", nil, http.StatusOK), &resp)

	if resp.Disclaimer != db.PresetDisclaimer {
		t.Errorf("disclaimer = %q, want %q", resp.Disclaimer, db.PresetDisclaimer)
	}
	if len(resp.Presets) == 0 {
		t.Fatal("no presets offered")
	}
	if !resp.Presets[0].Passthrough || resp.Presets[0].Rendition != nil {
		t.Errorf("first preset = %+v, want the zero-cost passthrough", resp.Presets[0])
	}
	for _, p := range resp.Presets[1:] {
		if p.Rendition == nil {
			t.Fatalf("preset %q carries no template", p.Key)
		}
		// The ceilings move and differ by partner status; the note is the only
		// place they are stated and it must arrive with its caveat attached.
		if !strings.HasSuffix(p.Rendition.Note, db.PresetDisclaimer) {
			t.Errorf("preset %q note %q does not end with the disclaimer", p.Key, p.Rendition.Note)
		}
	}
	if resp.Bounds["maxFps"] != db.MaxRenditionFPS {
		t.Errorf("bounds.maxFps = %v, want %v", resp.Bounds["maxFps"], db.MaxRenditionFPS)
	}
}

// A preset is a starting point, so posting one back unedited has to be a valid
// rendition.
func TestEveryPresetTemplateCreatesCleanly(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())

	for _, p := range db.RenditionPresets() {
		if p.Passthrough {
			continue
		}
		t.Run(p.Key, func(t *testing.T) {
			got := createRendition(t, h, sign, p.Rendition)
			if got.VideoBitrate != p.Rendition.VideoBitrate || got.FPS != p.Rendition.FPS {
				t.Errorf("stored %+v, want the template's values", got)
			}
		})
	}
}

func TestEncoderListMarksWhatThisFFmpegActuallyHas(t *testing.T) {
	type encoder struct {
		Name      db.VideoEncoder `json:"name"`
		Codec     string          `json:"codec"`
		Hardware  bool            `json:"hardware"`
		Available bool            `json:"available"`
	}
	type response struct {
		Encoders []encoder `json:"encoders"`
		Default  string    `json:"default"`
		Probed   bool      `json:"probed"`
	}
	get := func(t *testing.T, tools *ffmpeg.Tools) response {
		t.Helper()
		h, _, sign := renditionServer(t, tools)
		var resp response
		decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/encoders", nil, http.StatusOK), &resp)
		return resp
	}

	t.Run("a software-only build offers no hardware encoder", func(t *testing.T) {
		resp := get(t, fakeTools(string(db.EncoderX264), string(db.EncoderX265)))
		if !resp.Probed {
			t.Error("probed = false on a build that listed encoders")
		}
		if resp.Default != string(db.EncoderX264) {
			t.Errorf("default = %q, want libx264", resp.Default)
		}
		for _, e := range resp.Encoders {
			want := e.Name == db.EncoderX264 || e.Name == db.EncoderX265
			if e.Available != want {
				t.Errorf("%s available = %v, want %v", e.Name, e.Available, want)
			}
		}
	})

	t.Run("a hardware encoder is offered when the binary registers it", func(t *testing.T) {
		resp := get(t, fakeTools(string(db.EncoderX264), string(db.EncoderNVENCH264)))
		for _, e := range resp.Encoders {
			if e.Name == db.EncoderNVENCH264 {
				if !e.Available || !e.Hardware {
					t.Errorf("h264_nvenc = %+v, want available hardware", e)
				}
			}
			if e.Name == db.EncoderQSVH264 && e.Available {
				t.Error("h264_qsv offered on a build that does not register it")
			}
		}
	})

	t.Run("every known encoder is listed so a saved one still renders", func(t *testing.T) {
		resp := get(t, fakeTools(string(db.EncoderX264)))
		if len(resp.Encoders) != len(db.KnownEncoders) {
			t.Fatalf("got %d encoders, want all %d", len(resp.Encoders), len(db.KnownEncoders))
		}
		for i, e := range resp.Encoders {
			if e.Name != db.KnownEncoders[i] {
				t.Errorf("encoder %d = %q, want %q (UI order, software first)", i, e.Name, db.KnownEncoders[i])
			}
			if e.Codec != e.Name.Codec() {
				t.Errorf("%s codec = %q, want %q", e.Name, e.Codec, e.Name.Codec())
			}
		}
	})

	// An unprobed build is the "assume the best" case: claiming nothing works
	// would leave the user unable to create any rendition at all.
	t.Run("an unprobed build is not reported as having no encoders", func(t *testing.T) {
		resp := get(t, fakeTools())
		if resp.Probed {
			t.Error("probed = true with an empty encoder list")
		}
		for _, e := range resp.Encoders {
			if !e.Available {
				t.Errorf("%s reported unavailable on an unprobed build", e.Name)
			}
		}
	})
}

// ------------------------------------------------- destinations x renditions

func TestDestinationSelectsARendition(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	rend := createRendition(t, h, sign, map[string]any{"name": "1080p60", "height": 1080, "videoBitrate": 6000})

	created := createDestination(t, h, sign, destinationBody("twitch", true, &rend.ID))
	if created.RenditionID == nil || *created.RenditionID != rend.ID {
		t.Fatalf("renditionId = %v, want %d", created.RenditionID, rend.ID)
	}

	path := "/api/v1/destinations/" + strconv.FormatInt(created.ID, 10)
	// A fresh struct per response: renditionId is omitempty, so decoding a
	// passthrough reply over a reused one would leave the old id standing.
	update := func(body map[string]any) *db.Destination {
		t.Helper()
		var resp struct {
			Destination *db.Destination `json:"destination"`
		}
		decodeInto(t, send(t, h, sign, http.MethodPut, path, body, http.StatusOK), &resp)
		return resp.Destination
	}

	// Omitting the field leaves the selection alone, the same as every other
	// field in a partial update.
	if got := update(map[string]any{"name": "twitch main"}); got.RenditionID == nil || *got.RenditionID != rend.ID {
		t.Errorf("renditionId = %v after an unrelated edit, want %d", got.RenditionID, rend.ID)
	}

	// Explicit null is how the UI says "passthrough".
	if got := update(map[string]any{"renditionId": nil}); got.RenditionID != nil {
		t.Errorf("renditionId = %v, want nil for passthrough", *got.RenditionID)
	}
}

// A destination created before renditions existed has no rendition_id and must
// keep working with zero user action.
func TestADestinationWithNoRenditionIsPassthrough(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())

	created := createDestination(t, h, sign, destinationBody("youtube", true, nil))
	if created.RenditionID != nil {
		t.Errorf("renditionId = %v, want nil", *created.RenditionID)
	}
}

func TestADestinationCannotSelectARenditionThatDoesNotExist(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	missing := int64(4242)
	existing := createDestination(t, h, sign, destinationBody("ok", true, nil))

	tests := []struct {
		name   string
		method string
		path   string
		body   map[string]any
	}{
		{
			name: "on create", method: http.MethodPost, path: "/api/v1/destinations",
			body: destinationBody("bad", true, &missing),
		},
		{
			name: "on update", method: http.MethodPut,
			path: "/api/v1/destinations/" + strconv.FormatInt(existing.ID, 10),
			body: map[string]any{"renditionId": missing},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := send(t, h, sign, tt.method, tt.path, tt.body, http.StatusBadRequest)
			// A field-level message, not "FOREIGN KEY constraint failed".
			if !strings.Contains(string(raw), "does not exist") {
				t.Errorf("error %s does not name the missing rendition", raw)
			}
		})
	}
}
