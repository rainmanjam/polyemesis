package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/playout"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

const beaconPath = "/api/v1/playout/beacon"

const validBeaconBody = `{"v":1,"sid":"0123456789abcdef0123456789abcdef","seq":1,"kind":"heartbeat",` +
	`"startupMs":900,"rebufferCount":1,"rebufferMs":250,"latencyMs":8000,"errors":["network"]}`

func beaconRequest(body string, mutate ...func(*http.Request)) *http.Request {
	r := httptest.NewRequest(http.MethodPost, beaconPath, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "203.0.113.9:5555"
	for _, m := range mutate {
		m(r)
	}
	return r
}

func withToken(r *http.Request) { r.Header.Set(playoutTokenHeader, testToken) }

func noPlayoutCookie(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == playoutTokenCookie {
			t.Fatalf("the beacon endpoint minted the playback cookie: %v", c)
		}
	}
}

// The gate comes first and answers exactly as /playout/public does, so the
// beacon endpoint is not a second, chattier oracle for the stream's existence.
func TestPlayoutBeaconHonoursTheGate(t *testing.T) {
	protected := playoutPublish{Protection: PlayoutProtectToken, Token: testToken}
	cases := []struct {
		name   string
		set    db.PlayoutSettings
		mutate func(*http.Request)
		want   int
	}{
		{"disabled", db.PlayoutSettings{}, withToken, http.StatusNotFound},
		{"enabled but private", enabledPlayout(false), withToken, http.StatusNotFound},
		{"public, no token", enabledPlayout(true), func(*http.Request) {}, http.StatusUnauthorized},
		{"public, wrong token", enabledPlayout(true),
			func(r *http.Request) { r.Header.Set(playoutTokenHeader, "nope") }, http.StatusUnauthorized},
		// Past the gate: no engine in this fixture, so the body is checked and
		// then the missing manager is reported.
		{"public, token in header", enabledPlayout(true), withToken, http.StatusServiceUnavailable},
		{"public, token in query", enabledPlayout(true),
			func(r *http.Request) { r.URL.RawQuery = "t=" + testToken }, http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, h := playoutTestServer(t, c.set, protected)
			w := do(t, h, beaconRequest(validBeaconBody, c.mutate))
			if w.Code != c.want {
				t.Fatalf("status = %d, want %d (%s)", w.Code, c.want, w.Body.String())
			}
			if w.Code == http.StatusUnauthorized && w.Header().Get("WWW-Authenticate") != "" {
				t.Fatal("a beacon refusal raised a browser credential prompt")
			}
			noPlayoutCookie(t, w)
		})
	}
}

// A read-scoped API token is a stranger here, exactly as on /playout/public.
func TestPlayoutBeaconTreatsAReadTokenAsAnonymous(t *testing.T) {
	_, h := playoutTestServer(t, enabledPlayout(true),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
	sign := login(t, h)
	read := createScopedToken(t, h, sign, "beacon-read", db.ScopeRead)

	anon := do(t, h, beaconRequest(validBeaconBody))
	asRead := do(t, h, beaconRequest(validBeaconBody, bearer(read)))
	if anon.Code != http.StatusUnauthorized || asRead.Code != anon.Code ||
		asRead.Body.String() != anon.Body.String() {
		t.Fatalf("read token %d %q vs anonymous %d %q", asRead.Code, asRead.Body.String(),
			anon.Code, anon.Body.String())
	}
}

func TestPlayoutBeaconRefusesBodiesOutsideTheSchema(t *testing.T) {
	_, h := playoutTestServer(t, enabledPlayout(true),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
	cases := map[string]string{
		"unknown field": strings.Replace(validBeaconBody, `"v":1,`, `"v":1,"ip":"203.0.113.9",`, 1),
		"user agent":    strings.Replace(validBeaconBody, `"v":1,`, `"v":1,"ua":"Mozilla/5.0",`, 1),
		"trailing data": validBeaconBody + `{"v":1}`,
		"not json":      "v=1&sid=x",
		"array":         "[" + validBeaconBody + "]",
		"oversized": strings.Replace(validBeaconBody, `"kind"`,
			`"pad":"`+strings.Repeat("x", playout.MaxBeaconBytes)+`","kind"`, 1),
		"wrong version":   strings.Replace(validBeaconBody, `"v":1`, `"v":2`, 1),
		"bad sid":         strings.Replace(validBeaconBody, "0123456789abcdef0123456789abcdef", "short", 1),
		"free-text error": strings.Replace(validBeaconBody, `["network"]`, `["GET seg.ts?t=x failed"]`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			w := do(t, h, beaconRequest(body, withToken))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body.String())
			}
		})
	}
}

// The limiter sits after the gate: exhausting it for a hidden stream still
// answers 404, so a 429 can never reveal that something is there.
func TestPlayoutBeaconRateLimitIsBehindTheGate(t *testing.T) {
	s, h := playoutTestServer(t, enabledPlayout(true),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
	now := time.Unix(1_700_000_000, 0)
	s.beacons.clockFor = func() time.Time { return now }

	var limited int
	for i := 0; i < int(playout.BeaconBurstPerKey)+5; i++ {
		w := do(t, h, beaconRequest(validBeaconBody, withToken))
		if w.Code == http.StatusTooManyRequests {
			limited++
			if w.Header().Get("Retry-After") == "" {
				t.Fatal("a 429 without Retry-After")
			}
		}
	}
	if limited != 5 {
		t.Fatalf("%d requests were limited, want the 5 past the burst", limited)
	}
	// Another address is unaffected.
	other := do(t, h, beaconRequest(validBeaconBody, withToken,
		func(r *http.Request) { r.RemoteAddr = "198.51.100.4:1000" }))
	if other.Code == http.StatusTooManyRequests {
		t.Fatal("one address's flood limited another")
	}
	if got := s.beaconGate().limited.Load(); got != 5 {
		t.Fatalf("limited counter = %d", got)
	}

	// Same exhausted address, stream no longer published: the gate answers.
	hidden, hh := playoutTestServer(t, enabledPlayout(false),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
	hidden.beacons.clockFor = func() time.Time { return now }
	for i := 0; i < int(playout.BeaconBurstPerKey)+5; i++ {
		if w := do(t, hh, beaconRequest(validBeaconBody, withToken)); w.Code != http.StatusNotFound {
			t.Fatalf("hidden stream answered %d to request %d, want 404", w.Code, i)
		}
	}
}

// With a real playout manager: accepted, aggregated, and on the scrape.
func TestPlayoutBeaconIsAggregatedAndScraped(t *testing.T) {
	_, h, sign := playoutOriginServer(t, enabledPlayout(true),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})

	w := do(t, h, beaconRequest(validBeaconBody, withToken))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (%s)", w.Code, w.Body.String())
	}
	noPlayoutCookie(t, w)
	// The fixture's rungs never start, so no name is on the ladder.
	bad := strings.Replace(validBeaconBody, `"seq":1,`, `"seq":2,"variant":"source",`, 1)
	if w := do(t, h, beaconRequest(bad, withToken)); w.Code != http.StatusBadRequest {
		t.Fatalf("a variant that is not running: %d", w.Code)
	}

	r := httptest.NewRequest(http.MethodGet, "/api/v1/playout", nil)
	sign(r)
	var view struct {
		Status struct {
			Analytics struct {
				QoE playout.QoESummary `json:"qoe"`
			} `json:"analytics"`
		} `json:"status"`
	}
	if err := json.Unmarshal(do(t, h, r).Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	q := view.Status.Analytics.QoE
	if q.Beacons != 1 || q.Startups != 1 || q.StartupMs != 900 || q.Rebuffers != 1 ||
		q.Errors[playout.BeaconErrNetwork] != 1 || q.Active != 1 {
		t.Fatalf("admin analytics qoe = %+v", q)
	}

	r = httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	sign(r)
	scrape := do(t, h, r).Body.String()
	for _, want := range []string{
		"polyemesis_playout_qoe_beacons_total 1\n",
		"polyemesis_playout_qoe_startup_seconds_total 0.9\n",
		`polyemesis_playout_qoe_errors_total{code="network"} 1` + "\n",
		`polyemesis_playout_qoe_beacons_rejected_total{reason="invalid"} 1` + "\n",
		"polyemesis_playout_qoe_latency_seconds 8\n",
	} {
		if !strings.Contains(scrape, want) {
			t.Errorf("scrape is missing %q", want)
		}
	}
	if strings.Contains(scrape, "0123456789abcdef") {
		t.Fatal("the session id reached the scrape")
	}
}

func TestPlayoutPublicViewCarriesTheOfflineCard(t *testing.T) {
	s, h := playoutTestServer(t, enabledPlayout(true), playoutPublish{
		Protection: PlayoutProtectToken, Token: testToken,
		OfflineMessage: "Back Sunday", VODURL: "https://example.com/vod/1",
	})
	at := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	if _, err := s.store.CreateSchedule(&scheduler.Schedule{
		Name: "secret show name", Enabled: true, Action: scheduler.ActionStart,
		Kind: scheduler.KindOnce, RunAt: at, AllDestinations: true,
	}); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/api/v1/playout/public", nil)
	withToken(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var v playoutPublicView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.State != playout.LiveStateOffline {
		t.Fatalf("state = %q with no manager, want offline", v.State)
	}
	if v.NextStartAt == nil || !v.NextStartAt.Equal(at) {
		t.Fatalf("nextStartAt = %v, want %v", v.NextStartAt, at)
	}
	if v.Offline.Message != "Back Sunday" || v.Offline.VODURL != "https://example.com/vod/1" || v.Offline.Clip != "" {
		t.Fatalf("offline = %+v", v.Offline)
	}
	if strings.Contains(w.Body.String(), "secret show name") {
		t.Fatal("the schedule's name reached the public view")
	}
}

func TestEarliestStartPicksTheSoonestStart(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	once := func(d time.Duration, action scheduler.Action, enabled bool) scheduler.Schedule {
		return scheduler.Schedule{Enabled: enabled, Action: action, Kind: scheduler.KindOnce, RunAt: now.Add(d)}
	}
	list := []scheduler.Schedule{
		once(time.Hour, scheduler.ActionStop, true),            // a stop is not a start
		once(2*time.Hour, scheduler.ActionStart, false),        // disabled
		once(-time.Hour, scheduler.ActionStart, true),          // past
		once(5*time.Hour, scheduler.ActionPlaylistStart, true), // counts
		once(3*time.Hour, "", true),                            // empty action is a start
		{Enabled: true, Action: scheduler.ActionStart, Kind: scheduler.KindDaily, AtMinutes: 13 * 60},
	}
	got := earliestStart(list, now)
	if got == nil || !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("earliest = %v, want the daily 13:00", got)
	}
	if earliestStart(list[:3], now) != nil {
		t.Fatal("found a start among a stop, a disabled start and a past one")
	}
	if got := earliestStart(list[:5], now); got == nil || !got.Equal(now.Add(3*time.Hour)) {
		t.Fatalf("earliest without the daily = %v", got)
	}
}

func TestPlayoutPublishValidatesTheOfflineCard(t *testing.T) {
	_, h := playoutTestServer(t, enabledPlayout(true),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
	sign := login(t, h)
	put := func(body map[string]any) *httptest.ResponseRecorder {
		r := jsonRequest(t, http.MethodPut, "/api/v1/playout/publish", body)
		sign(r)
		return do(t, h, r)
	}
	for name, body := range map[string]map[string]any{
		"javascript url": {"vodUrl": "javascript:alert(1)"},
		"relative url":   {"vodUrl": "/vod/1"},
		"data url":       {"vodUrl": "data:text/html,hi"},
		"long message":   {"offlineMessage": strings.Repeat("é", maxOfflineMessage+1)},
		"no such clip":   {"offlineClipJob": 4242},
		"negative clip":  {"offlineClipJob": -1},
	} {
		if w := put(body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, w.Code)
		}
	}
	w := put(map[string]any{"vodUrl": "https://example.com/v", "offlineMessage": " Back soon ", "offlineClipJob": 0})
	if w.Code != http.StatusOK {
		t.Fatalf("valid offline card refused: %d %s", w.Code, w.Body.String())
	}
	var saved playoutPublish
	_ = json.Unmarshal(w.Body.Bytes(), &saved)
	if saved.VODURL != "https://example.com/v" || saved.OfflineMessage != "Back soon" {
		t.Fatalf("saved %+v", saved)
	}
}

func TestPlayoutOfflineClipIsGatedAndAbsentWithoutAClip(t *testing.T) {
	_, h := playoutTestServer(t, enabledPlayout(false),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/playout/offline-clip?t="+testToken, nil)
	if w := do(t, h, r); w.Code != http.StatusNotFound || w.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("private stream: %d", w.Code)
	}
	_, h = playoutTestServer(t, enabledPlayout(true),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
	r = httptest.NewRequest(http.MethodGet, "/api/v1/playout/offline-clip", nil)
	if w := do(t, h, r); w.Code != http.StatusNotFound || w.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("no token: %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/playout/offline-clip?t="+testToken, nil)
	if w := do(t, h, r); w.Code != http.StatusNotFound {
		t.Fatalf("no clip configured: %d", w.Code)
	}
}

func TestValidVODURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"":                         true,
		"https://youtu.be/abc":     true,
		"http://example.com/v?x=1": true,
		"javascript:alert(1)":      false,
		"https://":                 false,
		"ftp://example.com/v":      false,
		"//example.com/v":          false,
		"https://example.com/" + strings.Repeat("a", maxVODURL): false,
	} {
		if got := validVODURL(raw); got != want {
			t.Errorf("validVODURL(%.40q) = %v, want %v", raw, got, want)
		}
	}
}

// Every open player page beacons every fifteen seconds and a stranger can send
// as many refusals as they like, so neither reaches the request log -- the
// scrape counts them instead. A 5xx still does.
func TestPlayoutBeaconsStayOutOfTheRequestLog(t *testing.T) {
	s, h := playoutTestServer(t, enabledPlayout(true),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
	var buf bytes.Buffer
	s.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	do(t, h, beaconRequest(validBeaconBody))                                     // 401
	do(t, h, beaconRequest(`{"v":9}`, withToken))                                // 400
	do(t, h, httptest.NewRequest(http.MethodGet, "/api/v1/playout/public", nil)) // control: logged
	out := buf.String()
	if strings.Contains(out, "/playout/beacon") {
		t.Fatalf("a beacon refusal was logged:\n%s", out)
	}
	if !strings.Contains(out, "/playout/public") {
		t.Fatalf("the control request was not logged, so this proves nothing:\n%s", out)
	}
}

// The offline clip end to end: an operator picks a finished MP4 export, the
// public view names it, and the gated route streams it with Range support. A
// buffer-style .ts export and a path outside the exports directory are refused.
func TestPlayoutOfflineClipServesAChosenExport(t *testing.T) {
	s, h := playoutTestServer(t, enabledPlayout(true),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
	sign := login(t, h)
	dir := s.clipExportDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mp4 := filepath.Join(dir, "teaser.mp4")
	body := []byte("0123456789abcdefghij") // stands in for an MP4; nothing here decodes it
	if err := os.WriteFile(mp4, body, 0o644); err != nil {
		t.Fatal(err)
	}
	ts := filepath.Join(dir, "buffer.ts")
	if err := os.WriteFile(ts, body, 0o644); err != nil {
		t.Fatal(err)
	}
	good := seedDoneClipExport(t, s.store, mp4)
	bad := seedDoneClipExport(t, s.store, ts)
	outside := seedDoneClipExport(t, s.store, filepath.Join(t.TempDir(), "elsewhere.mp4"))

	put := func(id int64) int {
		r := jsonRequest(t, http.MethodPut, "/api/v1/playout/publish", map[string]any{"offlineClipJob": id})
		sign(r)
		return do(t, h, r).Code
	}
	if c := put(bad.ID); c != http.StatusBadRequest {
		t.Fatalf("a .ts export was accepted as the offline clip: %d", c)
	}
	if c := put(outside.ID); c != http.StatusBadRequest {
		t.Fatalf("an export outside the exports directory was accepted: %d", c)
	}
	if c := put(good.ID); c != http.StatusOK {
		t.Fatalf("the MP4 export was refused: %d", c)
	}

	r := httptest.NewRequest(http.MethodGet, "/api/v1/playout/public", nil)
	withToken(r)
	var v playoutPublicView
	if err := json.Unmarshal(do(t, h, r).Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Offline.Clip != playoutOfflineClipPath {
		t.Fatalf("public view offline.clip = %q", v.Offline.Clip)
	}

	r = httptest.NewRequest(http.MethodGet, playoutOfflineClipPath+"?t="+testToken, nil)
	w := do(t, h, r)
	if w.Code != http.StatusOK || w.Body.String() != string(body) {
		t.Fatalf("clip: %d %q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "video/mp4" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.HasPrefix(cc, "private") {
		t.Fatalf("Cache-Control = %q: a credential-gated body must not be shared-cacheable", cc)
	}
	r = httptest.NewRequest(http.MethodGet, playoutOfflineClipPath+"?t="+testToken, nil)
	r.Header.Set("Range", "bytes=10-14")
	w = do(t, h, r)
	if w.Code != http.StatusPartialContent || w.Body.String() != "abcde" {
		t.Fatalf("range: %d %q", w.Code, w.Body.String())
	}
	noPlayoutCookie(t, w)

	// The export deleted from disk after it was chosen: 404, and the public
	// view stops naming a clip that cannot play.
	if err := os.Remove(mp4); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest(http.MethodGet, playoutOfflineClipPath+"?t="+testToken, nil)
	if w := do(t, h, r); w.Code != http.StatusNotFound {
		t.Fatalf("deleted clip: %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/playout/public", nil)
	withToken(r)
	v = playoutPublicView{}
	if err := json.Unmarshal(do(t, h, r).Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Offline.Clip != "" {
		t.Fatalf("the public view still names a deleted clip: %q", v.Offline.Clip)
	}
}

// /watch/<token> is a wildcard route, so the request log records the raw path --
// which for the shared player link IS the playback token. A token in a log is a
// credential handed to whoever reads the log.
func TestTheWatchTokenNeverReachesTheRequestLog(t *testing.T) {
	s, h := playoutTestServer(t, enabledPlayout(true),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
	var buf bytes.Buffer
	s.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	for _, p := range []string{"/watch/" + testToken, "/watch/" + testToken + "/", "/watch/" + testToken + "?t=1793541600"} {
		do(t, h, httptest.NewRequest(http.MethodGet, p, nil))
	}
	out := buf.String()
	if !strings.Contains(out, "/watch/") {
		t.Fatalf("no /watch request was logged, so this proves nothing:\n%s", out)
	}
	if strings.Contains(out, testToken) {
		t.Fatalf("the playback token reached the request log:\n%s", out)
	}
}

// #10. "Reset counters" on the Playout page means every playout QoE counter,
// the refusals included -- a scrape after a reset must not show refusals from
// before it next to beacon counts from after.
func TestResetPlayoutAnalyticsAlsoClearsBeaconRefusals(t *testing.T) {
	s, h, sign := playoutOriginServer(t, enabledPlayout(true),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
	if w := do(t, h, beaconRequest(`{"v":9}`, withToken)); w.Code != http.StatusBadRequest {
		t.Fatalf("setup: %d", w.Code)
	}
	if s.beaconGate().invalid.Load() != 1 {
		t.Fatal("setup: the refusal was not counted")
	}
	s.beaconGate().limited.Store(3)
	r := jsonRequest(t, http.MethodPost, "/api/v1/playout/analytics/reset", nil)
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("reset: %d", w.Code)
	}
	if g := s.beaconGate(); g.invalid.Load() != 0 || g.limited.Load() != 0 {
		t.Fatalf("refusals survived the reset: invalid=%d limited=%d", g.invalid.Load(), g.limited.Load())
	}
}
