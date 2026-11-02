package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/analytics"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/metrics"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// fakeLiveStats stands in for Twitch Helix and the YouTube Data API: the
// endpoints oauth's Stats calls make, answered per bearer token, with every
// hit counted so a test can see what left the process. No live account is
// connected anywhere in this suite.
type fakeLiveStats struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits map[string]int
	// twitch maps a bearer token to the channel's live state: absent from the
	// map is offline, a nil count is a live stream that sent no viewer_count.
	twitch map[string]*int
	// youtube is the one YouTube channel's concurrentViewers, "" for none.
	youtube string
	// titleFor overrides a Twitch channel's stream title, by token.
	titleFor map[string]string
}

func newFakeLiveStats(t *testing.T) *fakeLiveStats {
	t.Helper()
	f := &fakeLiveStats{hits: map[string]int{}, twitch: map[string]*int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		f.hits[r.URL.Path]++
		count, live := f.twitch[tok]
		yt := f.youtube
		title, titled := f.titleFor[tok]
		f.mu.Unlock()
		if !titled {
			title = "Twitch title " + tok
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/users":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{
				"id": "uid-" + tok, "login": "login-" + tok, "display_name": "Chan " + tok}}})
		case "/streams":
			if !live {
				_, _ = w.Write([]byte(`{"data":[]}`))
				return
			}
			s := map[string]any{"user_login": "login-" + tok, "type": "live", "title": title}
			if count != nil {
				s["viewer_count"] = *count
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{s}})
		case "/channels":
			_, _ = w.Write([]byte(`{"items":[{"id":"yt-chan","snippet":{"title":"YT channel"}}]}`))
		case "/liveBroadcasts":
			_, _ = w.Write([]byte(`{"items":[{"id":"bc1","snippet":{"channelId":"yt-chan","title":"YouTube title"}}]}`))
		case "/videos":
			d := map[string]any{}
			if yt != "" {
				d["concurrentViewers"] = yt
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"liveStreamingDetails": d}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLiveStats) hit(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

// analyticsFixture is a server whose providers talk to the fake, with
// accounts and destinations planted and a controllable clock and "running"
// set.
type analyticsFixture struct {
	s       *Server
	h       http.Handler
	store   *db.DB
	fake    *fakeLiveStats
	sign    func(*http.Request)
	now     time.Time
	running map[int64]bool
	charged []int
	allowYT bool
	dest    map[string]int64
}

func newAnalyticsFixture(t *testing.T) *analyticsFixture {
	t.Helper()
	fake := newFakeLiveStats(t)
	s, h, store := testServerWith(t, Options{Providers: oauth.NewSet(oauth.WithBaseURL(fake.srv.URL))})
	f := &analyticsFixture{s: s, h: h, store: store, fake: fake, now: time.Unix(1_800_000_000, 0),
		running: map[int64]bool{}, allowYT: true, dest: map[string]int64{}}
	s.analyticsNow = func() time.Time { return f.now }
	s.analyticsRunning = func() map[int64]bool { return f.running }
	s.analyticsChargeQuota = func(_ string, cost int) bool {
		f.charged = append(f.charged, cost)
		return f.allowYT
	}
	for _, p := range []db.Platform{db.PlatformTwitch, db.PlatformYouTube} {
		if err := store.PutPlatformCreds(s.box, p, "cid", "client-pass"); err != nil {
			t.Fatalf("creds: %v", err)
		}
	}
	f.sign = login(t, h)
	return f
}

// account plants a connected account with its own bearer token and a
// destination linked to it.
func (f *analyticsFixture) account(t *testing.T, p db.Platform, name, token string) int64 {
	t.Helper()
	acct, err := f.store.UpsertPlatformAccount(f.s.box, &db.PlatformAccount{
		Platform: p, AccountName: name, AccountRef: name + "-ref",
		AccessToken: token, RefreshToken: "refresh-" + token, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	d, err := f.store.CreateDestination(&db.Destination{
		Name: name, Kind: db.DestRTMP, Platform: p,
		URL: "rtmp://ingest.example/app", StreamKey: "key-" + token, AccountID: &acct.ID,
	})
	if err != nil {
		t.Fatalf("destination: %v", err)
	}
	f.dest[name] = d.ID
	return acct.ID
}

func (f *analyticsFixture) tick(t *testing.T) {
	t.Helper()
	f.s.audience.Tick(context.Background())
}

func (f *analyticsFixture) get(t *testing.T, path string, out any) *httptest.ResponseRecorder {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, path, nil)
	f.sign(r)
	w := do(t, f.h, r)
	if out != nil {
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("GET %s: decode: %v (%s)", path, err, w.Body.String())
		}
	}
	return w
}

// TestAnalyticsLiveSumsOnlyReportedCountsFromTheRealProviders drives the
// sampler through oauth's own Stats code against fake platform servers: a
// Twitch channel with 12 viewers, a Twitch channel live with no viewer_count
// (a hidden count), and a YouTube broadcast with 30. The hidden channel is
// "not reported" and is left out of the total; it is never a 0.
func TestAnalyticsLiveSumsOnlyReportedCountsFromTheRealProviders(t *testing.T) {
	f := newAnalyticsFixture(t)
	twelve := 12
	f.fake.twitch["tok-a"] = &twelve
	f.fake.twitch["tok-b"] = nil
	f.fake.youtube = "30"
	f.account(t, db.PlatformTwitch, "shown", "tok-a")
	f.account(t, db.PlatformTwitch, "hidden", "tok-b")
	f.account(t, db.PlatformYouTube, "tube", "tok-y")
	// An account no running destination streams to is not this broadcast's
	// audience and must not be asked.
	f.account(t, db.PlatformTwitch, "idle", "tok-idle")
	for _, n := range []string{"shown", "hidden", "tube"} {
		f.running[f.dest[n]] = true
	}

	f.tick(t)
	var live analytics.Live
	f.get(t, "/api/v1/analytics/live", &live)
	if !live.OnAir || live.Stream == nil {
		t.Fatalf("live = %+v, want on air with a stream", live)
	}
	if live.Total == nil || *live.Total != 42 || live.Reported != 2 {
		t.Fatalf("total = %v over %d, want 42 over 2 (the hidden Twitch count left out)", live.Total, live.Reported)
	}
	byName := map[string]analytics.Reading{}
	for _, r := range live.Readings {
		byName[r.Name] = r
	}
	if r := byName["hidden"]; r.Viewers != nil || r.Reason != analytics.ReasonHidden || !r.Live {
		t.Fatalf("hidden channel = %+v, want live, no count, reason hidden", r)
	}
	if _, asked := byName["idle"]; asked {
		t.Fatal("an account with no running destination was sampled")
	}
	if len(f.charged) != 1 || f.charged[0] != youTubeStatsQuotaCost || youTubeStatsQuotaCost != 3 {
		t.Fatalf("YouTube charged %v, want one charge of 3 units (channels + liveBroadcasts + videos)", f.charged)
	}

	// The raw body has no viewers key for the hidden row: the wire says what
	// the platform said, which is nothing.
	w := f.get(t, "/api/v1/analytics/live", nil)
	var raw struct {
		Readings []map[string]any `json:"readings"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	for _, r := range raw.Readings {
		if r["name"] == "hidden" {
			if _, present := r["viewers"]; present {
				t.Fatalf("the hidden row carries viewers=%v on the wire", r["viewers"])
			}
		}
	}

	// The scrape: per platform, with the not-reported platform absent from
	// the viewers family and present as 0 in the reported family.
	body := metrics.Render(metrics.Snapshot{Audience: f.s.audienceMetrics()})
	for _, want := range []string{
		`polyemesis_platform_viewers{platform="twitch"} 12`,
		`polyemesis_platform_viewers{platform="youtube"} 30`,
		`polyemesis_platform_viewers_reported{platform="twitch"} 1`,
		"polyemesis_audience_viewers 42",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the scrape is missing %q", want)
		}
	}
}

// TestAnalyticsChargesYouTubeToThePacerAndRespectsARefusal: a refused charge
// means the YouTube endpoints are not called at all, and the row says quota.
func TestAnalyticsChargesYouTubeToThePacerAndRespectsARefusal(t *testing.T) {
	f := newAnalyticsFixture(t)
	f.fake.youtube = "30"
	f.allowYT = false
	f.account(t, db.PlatformYouTube, "tube", "tok-y")
	f.running[f.dest["tube"]] = true

	f.tick(t)
	for _, p := range []string{"/channels", "/liveBroadcasts", "/videos"} {
		if n := f.fake.hit(p); n != 0 {
			t.Fatalf("the pacer refused and %s was called %d times", p, n)
		}
	}
	var live analytics.Live
	f.get(t, "/api/v1/analytics/live", &live)
	if len(live.Readings) != 1 || live.Readings[0].Reason != analytics.ReasonQuota || live.Readings[0].Viewers != nil {
		t.Fatalf("readings = %+v, want one quota-refused row with no count", live.Readings)
	}
	if live.Total != nil {
		t.Fatalf("total = %d with nothing reported, want absent", *live.Total)
	}
	body := metrics.Render(metrics.Snapshot{Audience: f.s.audienceMetrics()})
	if strings.Contains(body, `polyemesis_platform_viewers{platform="youtube"}`) {
		t.Error("a refused read was published as a viewer count")
	}
	if !strings.Contains(body, `polyemesis_platform_viewers_reported{platform="youtube"} 0`) {
		t.Error("the scrape does not say YouTube was asked and did not report")
	}
	if strings.Contains(body, "\npolyemesis_audience_viewers ") {
		t.Error("the total was published with nothing reported")
	}
}

// TestAnalyticsReportCarriesPeakAverageChatAndCSV runs a stream end to end:
// two ticks, chat between them, the stream closing after the grace, and the
// report as JSON and CSV.
func TestAnalyticsReportCarriesPeakAverageChatAndCSV(t *testing.T) {
	f := newAnalyticsFixture(t)
	n := 10
	f.fake.twitch["tok-a"] = &n
	f.account(t, db.PlatformTwitch, "shown", "tok-a")
	f.running[f.dest["shown"]] = true

	f.tick(t)
	if _, err := f.store.AppendChatMessages([]db.ChatMessage{
		{Platform: db.PlatformTwitch, Account: "shown-ref", MessageID: "1", AuthorID: "v1", AuthorName: "=SUM(A1)", Text: "hi", At: f.now},
		{Platform: db.PlatformTwitch, Account: "shown-ref", MessageID: "2", AuthorID: "v1", AuthorName: "=SUM(A1)", Text: "again", At: f.now},
		{Platform: db.PlatformTwitch, Account: "shown-ref", MessageID: "3", AuthorID: "v2", AuthorName: "quiet", Text: "o/", At: f.now},
	}); err != nil {
		t.Fatal(err)
	}
	n = 30
	f.fake.mu.Lock()
	f.fake.twitch["tok-a"] = &n
	f.fake.mu.Unlock()
	f.now = f.now.Add(time.Minute)
	f.tick(t)

	// Off air past the grace: the stream closes at its last on-air tick.
	f.running = map[int64]bool{}
	f.now = f.now.Add(analytics.CloseGrace)
	f.tick(t)

	var list struct {
		Streams []analytics.Stream `json:"streams"`
	}
	f.get(t, "/api/v1/analytics/streams", &list)
	if len(list.Streams) != 1 {
		t.Fatalf("streams = %+v", list.Streams)
	}
	st := list.Streams[0]
	if st.EndedAt == nil || st.DurationMs != 60_000 || st.Title != "Twitch title tok-a" {
		t.Fatalf("stream = %+v, want ended, 60 s, titled from the platform", st)
	}
	if st.PeakTotal == nil || *st.PeakTotal != 30 || st.AverageTotal == nil || *st.AverageTotal != 20 {
		t.Fatalf("list peak/avg = %v/%v, want 30/20", st.PeakTotal, st.AverageTotal)
	}

	path := "/api/v1/analytics/streams/" + strconv.FormatInt(st.ID, 10)
	var rep analytics.Report
	f.get(t, path, &rep)
	if len(rep.Summary.Rows) != 1 || *rep.Summary.Rows[0].Peak != 30 || *rep.Summary.Rows[0].Average != 20 {
		t.Fatalf("rows = %+v", rep.Summary.Rows)
	}
	if len(rep.Chat) != 1 || rep.Chat[0].Messages != 3 {
		t.Fatalf("chat = %+v, want 3 Twitch messages", rep.Chat)
	}
	if len(rep.TopChatters) != 2 || rep.TopChatters[0].Name != "=SUM(A1)" || rep.TopChatters[0].Messages != 2 {
		t.Fatalf("top chatters = %+v", rep.TopChatters)
	}

	w := f.get(t, path+"?format=csv", nil)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("csv: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if cd := w.Header().Get("Content-Disposition"); cd != `attachment; filename="polyemesis-stream-`+strconv.FormatInt(st.ID, 10)+`.csv"` {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	if body := w.Body.String(); !strings.Contains(body, "twitch,'=SUM(A1),2") || !strings.Contains(body, "total,,,,,30,20.0") {
		t.Fatalf("csv body:\n%s", body)
	}

	if w := f.get(t, path+"?format=xml", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("an unknown format answered %d, want 400", w.Code)
	}
	if w := f.get(t, "/api/v1/analytics/streams/999", nil); w.Code != http.StatusNotFound {
		t.Fatalf("a missing stream answered %d, want 404", w.Code)
	}
}

// TestAnalyticsOffAirAsksNothing: nothing running, nothing asked.
func TestAnalyticsOffAirAsksNothing(t *testing.T) {
	f := newAnalyticsFixture(t)
	f.account(t, db.PlatformTwitch, "shown", "tok-a")
	f.tick(t)
	if f.fake.hit("/users")+f.fake.hit("/streams") != 0 {
		t.Fatal("the sampler called Twitch with nothing on air")
	}
	var live analytics.Live
	f.get(t, "/api/v1/analytics/live", &live)
	if live.OnAir || live.Total != nil || len(live.Readings) != 0 || !live.Enabled {
		t.Fatalf("off-air live = %+v", live)
	}
}

// TestAnalyticsSettingsBoundsAreThePackagesBounds pins the settings
// validation to the analytics package's own constants and defaults.
func TestAnalyticsSettingsBoundsAreThePackagesBounds(t *testing.T) {
	d := db.DefaultSettings().Analytics
	if !d.Enabled || d.IntervalSeconds != int(analytics.DefaultInterval/time.Second) ||
		d.RetentionDays != analytics.DefaultRetentionDays {
		t.Fatalf("defaults = %+v, want the analytics package's", d)
	}
	for _, c := range []struct {
		interval, retention int
		ok                  bool
	}{
		{analytics.MinIntervalSeconds, analytics.MinRetentionDays, true},
		{analytics.MaxIntervalSeconds, analytics.MaxRetentionDays, true},
		{analytics.MinIntervalSeconds - 1, 90, false},
		{analytics.MaxIntervalSeconds + 1, 90, false},
		{60, analytics.MinRetentionDays - 1, false},
		{60, analytics.MaxRetentionDays + 1, false},
	} {
		s := db.DefaultSettings()
		s.Ingest.Mode = db.IngestSRT
		s.Analytics.IntervalSeconds, s.Analytics.RetentionDays = c.interval, c.retention
		err := s.Validate()
		if (err == nil) != c.ok {
			t.Errorf("interval %d retention %d: err=%v, want ok=%v", c.interval, c.retention, err, c.ok)
		}
	}
}
