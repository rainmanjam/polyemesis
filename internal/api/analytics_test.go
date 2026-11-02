package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/analytics"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
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
	// ytEnded makes the video report an actualEndTime; ytQuota makes
	// videos.list answer quotaExceeded; twitchRetry makes /streams answer 429
	// with this Retry-After (seconds).
	ytEnded, ytQuota bool
	twitchRetry      int
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
		ended, quota, retry := f.ytEnded, f.ytQuota, f.twitchRetry
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
			if retry > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(retry))
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
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
			_, _ = w.Write([]byte(`{"items":[{"id":"bc1","snippet":{"channelId":"tube-ref","title":"YouTube title"}}]}`))
		case "/videos":
			if quota {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"errors":[{"reason":"quotaExceeded"}]}}`))
				return
			}
			d := map[string]any{}
			if yt != "" {
				d["concurrentViewers"] = yt
			}
			if ended {
				d["actualEndTime"] = "2026-11-01T20:00:00Z"
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
	dest    map[string]int64
}

// ytUsed is what the shared YouTube allowance has spent today.
func (f *analyticsFixture) ytUsed() int { return f.s.ytQuota().Status().Used }

func newAnalyticsFixture(t *testing.T) *analyticsFixture {
	t.Helper()
	fake := newFakeLiveStats(t)
	s, h, store := testServerWith(t, Options{Providers: oauth.NewSet(oauth.WithBaseURL(fake.srv.URL))})
	f := &analyticsFixture{s: s, h: h, store: store, fake: fake, now: time.Unix(1_800_043_200, 0), // midday Pacific: no test races the quota reset (R13)
		running: map[int64]bool{}, dest: map[string]int64{}}
	s.analyticsNow = func() time.Time { return f.now }
	s.analyticsRunning = func() map[int64]bool { return f.running }
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
	// The real shared allowance, with no chat attached (#1, #16): the first
	// tick finds the live video (1) and reads it (1); channels.list is never
	// called (#3).
	if used := f.ytUsed(); used != 2 {
		t.Fatalf("YouTube allowance used %d, want 2 (liveBroadcasts.list + videos.list)", used)
	}
	if f.fake.hit("/channels") != 0 {
		t.Fatal("channels.list was called; the account ref already holds the channel id")
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
	f.account(t, db.PlatformYouTube, "tube", "tok-y")
	f.running[f.dest["tube"]] = true
	// The shared allowance is spent down to its send reserve by somebody
	// else -- chat, a title push -- and there is no chat adapter at all.
	q := f.s.ytQuota()
	st := q.Status()
	q.Spend(st.Limit - 200)

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
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("csv Cache-Control = %q; the report names chatters", cc)
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

// TestAudienceStateForMQTTLeavesUnreportedPlatformsNil: the MQTT mapping
// carries a reported count per platform, nil for one that did not report, and
// the player only when it runs.
func TestAudienceStateForMQTTLeavesUnreportedPlatformsNil(t *testing.T) {
	f := newAnalyticsFixture(t)
	twelve := 12
	f.fake.twitch["tok-a"] = &twelve
	f.fake.twitch["tok-b"] = nil
	f.account(t, db.PlatformTwitch, "shown", "tok-a")
	f.account(t, db.PlatformTwitch, "hidden", "tok-b")
	f.account(t, db.PlatformYouTube, "tube", "tok-y")
	for _, n := range []string{"shown", "hidden", "tube"} {
		f.running[f.dest[n]] = true
	}

	off := f.s.AudienceState()
	if off == nil || off.OnAir || off.Total != nil || off.Twitch != nil {
		t.Fatalf("before a tick = %+v, want off air with no counts", off)
	}
	f.tick(t)
	st := f.s.AudienceState()
	if !st.OnAir || st.Total == nil || *st.Total != 12 || st.Reported != 1 {
		t.Fatalf("audience = %+v, want 12 over 1 reported", st)
	}
	if st.Twitch == nil || *st.Twitch != 12 {
		t.Fatalf("twitch = %v, want 12 (the hidden channel adds nothing)", st.Twitch)
	}
	if st.YouTube != nil || st.Kick != nil || st.Player != nil {
		t.Fatalf("youtube %v kick %v player %v, want all nil: YouTube sent no count, Kick and the player were not running",
			st.YouTube, st.Kick, st.Player)
	}
	if !st.At.Equal(f.now) {
		t.Fatalf("at = %v, want the tick %v", st.At, f.now)
	}
}

// TestAnalyticsReadsTheCachedVideoForOneUnitATick (#3): after the lookup,
// every tick is one videos.list against the remembered id.
func TestAnalyticsReadsTheCachedVideoForOneUnitATick(t *testing.T) {
	f := newAnalyticsFixture(t)
	f.fake.youtube = "30"
	f.account(t, db.PlatformYouTube, "tube", "tok-y")
	f.running[f.dest["tube"]] = true
	for i := 0; i < 3; i++ {
		f.tick(t)
		f.now = f.now.Add(time.Minute)
	}
	if lb, v := f.fake.hit("/liveBroadcasts"), f.fake.hit("/videos"); lb != 1 || v != 3 {
		t.Fatalf("liveBroadcasts=%d videos=%d, want one lookup and one read a tick", lb, v)
	}
	if used := f.ytUsed(); used != 4 {
		t.Fatalf("allowance used %d, want 4 (2 + 1 + 1)", used)
	}
	// The broadcast ends: the id is dropped and looked up again.
	f.fake.mu.Lock()
	f.fake.ytEnded = true
	f.fake.mu.Unlock()
	f.tick(t)
	if f.fake.hit("/liveBroadcasts") != 2 {
		t.Fatal("an ended video was not looked up again")
	}
}

// TestAnalyticsQuotaExceededPausesTheSharedAllowance (#5): YouTube's own
// quotaExceeded is "quota", never "hidden", and stops every spender.
func TestAnalyticsQuotaExceededPausesTheSharedAllowance(t *testing.T) {
	f := newAnalyticsFixture(t)
	f.fake.ytQuota = true
	f.account(t, db.PlatformYouTube, "tube", "tok-y")
	f.running[f.dest["tube"]] = true
	f.tick(t)
	var live analytics.Live
	f.get(t, "/api/v1/analytics/live", &live)
	if len(live.Readings) != 1 || live.Readings[0].Reason != analytics.ReasonQuota || !live.Partial {
		t.Fatalf("readings = %+v, want quota (not hidden) and a partial tick", live.Readings)
	}
	if f.s.ytQuota().Afford(1) {
		t.Fatal("quotaExceeded did not pause the shared allowance")
	}
}

// TestAnalyticsShareStretchesTheInterval (#2): analytics is held to its share
// of the allowance; when the configured interval cannot be afforded until the
// reset, the interval is stretched and the live view says so.
func TestAnalyticsShareStretchesTheInterval(t *testing.T) {
	f := newAnalyticsFixture(t)
	f.fake.youtube = "30"
	f.account(t, db.PlatformYouTube, "tube", "tok-y")
	f.running[f.dest["tube"]] = true
	// A 1,000-unit project: a 150-unit share, nearly all of it spent.
	f.s.ytQuotaShared.SetLimits(1000, 0)
	q := f.s.ytQuota()
	for q.ShareLeft(analyticsQuotaTag, analyticsQuotaShare) > 3 {
		if !q.ChargeShare(analyticsQuotaTag, analyticsQuotaShare, 1) {
			t.Fatal("could not spend the share down")
		}
	}
	f.tick(t)
	var live analytics.Live
	f.get(t, "/api/v1/analytics/live", &live)
	if !live.Stretched || live.EffectiveIntervalSeconds <= live.IntervalSeconds {
		t.Fatalf("interval %d effective %d stretched %v, want a stretched wait",
			live.IntervalSeconds, live.EffectiveIntervalSeconds, live.Stretched)
	}
}

// TestAnalyticsTwitchRateLimitBacksOff (#4): a 429 and its Retry-After keep
// the sampler off that account until it passes.
func TestAnalyticsTwitchRateLimitBacksOff(t *testing.T) {
	f := newAnalyticsFixture(t)
	n := 5
	f.fake.twitch["tok-a"] = &n
	f.fake.twitchRetry = 150
	f.account(t, db.PlatformTwitch, "shown", "tok-a")
	f.running[f.dest["shown"]] = true
	f.tick(t)
	first := f.fake.hit("/streams")
	f.fake.mu.Lock()
	f.fake.twitchRetry = 0
	f.fake.mu.Unlock()
	f.now = f.now.Add(time.Minute)
	f.tick(t)
	if f.fake.hit("/streams") != first {
		t.Fatal("asked Twitch again inside its Retry-After")
	}
	f.now = f.now.Add(2 * time.Minute)
	f.tick(t)
	if f.fake.hit("/streams") != first+1 {
		t.Fatal("did not ask Twitch again after the Retry-After")
	}
}

// TestAnalyticsReadsTheVerticalHalfThroughItsHorizontalAccount (#9): a
// running vertical half has no account of its own; it is the paired
// horizontal's broadcast, read once.
func TestAnalyticsReadsTheVerticalHalfThroughItsHorizontalAccount(t *testing.T) {
	f := newAnalyticsFixture(t)
	f.fake.youtube = "30"
	f.account(t, db.PlatformYouTube, "tube", "tok-y")
	vert, err := f.store.CreateDestination(&db.Destination{
		Name: "tube vertical", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
		URL: "rtmp://ingest.example/app", StreamKey: "key-vertical",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateYouTubeVerticalPair(f.dest["tube"], vert.ID, "vertical-stream-id"); err != nil {
		t.Fatal(err)
	}
	f.running[vert.ID] = true
	f.tick(t)
	var live analytics.Live
	f.get(t, "/api/v1/analytics/live", &live)
	if len(live.Readings) != 1 || live.Readings[0].Name != "tube" || live.Total == nil || *live.Total != 30 {
		t.Fatalf("readings = %+v, want the horizontal account read once", live.Readings)
	}
	f.running[f.dest["tube"]] = true
	f.now = f.now.Add(time.Minute)
	f.tick(t)
	f.get(t, "/api/v1/analytics/live", &live)
	if len(live.Readings) != 1 {
		t.Fatalf("both halves running: %d readings, want one per broadcast", len(live.Readings))
	}
}

// TestAnalyticsShowsAPastedKeyDestinationAsUnlinked (owner question 2).
func TestAnalyticsShowsAPastedKeyDestinationAsUnlinked(t *testing.T) {
	f := newAnalyticsFixture(t)
	d, err := f.store.CreateDestination(&db.Destination{
		Name: "Kick by key", Kind: db.DestRTMP, Platform: db.PlatformKick,
		URL: "rtmp://ingest.example/app", StreamKey: "key-pasted",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.running[d.ID] = true
	f.tick(t)
	var live analytics.Live
	f.get(t, "/api/v1/analytics/live", &live)
	if len(live.Readings) != 1 || live.Readings[0].Reason != analytics.ReasonUnlinked ||
		live.Readings[0].Name != "Kick by key" || live.Readings[0].Platform != "kick" {
		t.Fatalf("readings = %+v, want the pasted-key destination shown as not connected", live.Readings)
	}
}

// TestTheVerticalReadAlwaysChargesTheSharedAllowance (#1): with no chat
// attached, the vertical status read still spends from, and is refused by,
// the shared allowance.
func TestTheVerticalReadAlwaysChargesTheSharedAllowance(t *testing.T) {
	s, _, store := testServer(t, config.Config{})
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid", "client-pass"); err != nil {
		t.Fatal(err)
	}
	q := s.ytQuota()
	if !s.verticalChargeOK() || q.Status().Used != 1 {
		t.Fatalf("used = %d, want the read charged with no chat attached", q.Status().Used)
	}
	q.Pause()
	if s.verticalChargeOK() {
		t.Fatal("the vertical read went ahead on a spent allowance")
	}
}

// TestAPartialTickHasNoTotalOnTheScrapeOrMQTT (#6).
func TestAPartialTickHasNoTotalOnTheScrapeOrMQTT(t *testing.T) {
	f := newAnalyticsFixture(t)
	n := 7
	f.fake.twitch["tok-a"] = &n
	f.fake.ytQuota = true
	f.account(t, db.PlatformTwitch, "shown", "tok-a")
	f.account(t, db.PlatformYouTube, "tube", "tok-y")
	f.running[f.dest["shown"]], f.running[f.dest["tube"]] = true, true
	f.tick(t)
	body := metrics.Render(metrics.Snapshot{Audience: f.s.audienceMetrics()})
	if strings.Contains(body, "\npolyemesis_audience_viewers ") {
		t.Fatal("a partial tick's total was published")
	}
	if !strings.Contains(body, `polyemesis_platform_viewers{platform="twitch"} 7`) {
		t.Fatal("the platform that did report is missing")
	}
	if st := f.s.AudienceState(); st.Total != nil || st.Twitch == nil {
		t.Fatalf("MQTT = %+v, want no total and Twitch's 7", st)
	}
}

// TestTitlePushAndGoLiveChargeTheServersAllowance (R13): through the same
// helper main uses, a title push and a go-live transition made by the oauth
// providers are charged to this server's one YouTube allowance.
func TestTitlePushAndGoLiveChargeTheServersAllowance(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/liveBroadcasts" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"items":[{"id":"bc1","snippet":{"title":"Old","channelId":"ch"},"status":{"lifeCycleStatus":"testing"}}]}`))
		default:
			_, _ = w.Write([]byte(`{"id":"bc1","status":{"lifeCycleStatus":"live"},"snippet":{"title":"New"}}`))
		}
	}))
	t.Cleanup(stub.Close)
	s, _, store := testServer(t, config.Config{})
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid", "client-pass"); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(stub.URL)
	InstallYouTubeMeter(s, u.Host)
	t.Cleanup(func() { oauth.SetYouTubeMeter(nil) })

	y := oauth.NewYouTube(oauth.WithBaseURL(stub.URL))
	q := s.ytQuota()
	before := q.Status().Used
	_, _ = y.PushMetadata(context.Background(), "cid", "tok", "ch", oauth.Metadata{Title: "New"})
	pushed := q.Status().Used - before
	if pushed < 50 {
		t.Fatalf("a title push charged %d units, want at least the 50 of its write", pushed)
	}
	before = q.Status().Used
	_, _ = y.TransitionBroadcast(context.Background(), "tok", "bc1", oauth.PhaseLive)
	if went := q.Status().Used - before; went < 50 {
		t.Fatalf("a go-live transition charged %d units, want at least 50", went)
	}
}

// TestYouTubeReadsWaitForTheClientToBeKnown (R3): with no YouTube
// credentials there is no client to charge, so the sampler does not read
// YouTube on an unbound budget.
func TestYouTubeReadsWaitForTheClientToBeKnown(t *testing.T) {
	fake := newFakeLiveStats(t)
	s, _, _ := testServerWith(t, Options{Providers: oauth.NewSet(oauth.WithBaseURL(fake.srv.URL))})
	if (analyticsSource{s}).Allow(analytics.Target{Platform: "youtube", AccountID: 1, Account: "tube-ref"}) {
		t.Fatal("a YouTube read was allowed with no client to charge it to")
	}
	if !(analyticsSource{s}).Allow(analytics.Target{Platform: "twitch", AccountID: 2}) {
		t.Fatal("Twitch was held back by YouTube's missing credentials")
	}
}

// TestSavingYouTubeCredentialsBindsTheAllowance (R3, R12): the save path
// binds the configured client, and a different client starts a new tally.
func TestSavingYouTubeCredentialsBindsTheAllowance(t *testing.T) {
	s, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	put := func(id string) {
		r := jsonRequest(t, http.MethodPut, "/api/v1/platforms/credentials/youtube",
			map[string]string{"clientId": id, "clientSecret": "client-pass"})
		sign(r)
		if w := do(t, h, r); w.Code != http.StatusOK {
			t.Fatalf("save creds: %d %s", w.Code, w.Body.String())
		}
	}
	put("project-one")
	q := s.ytQuotaShared
	if q.ClientID() != "project-one" {
		t.Fatalf("bound to %q after the save", q.ClientID())
	}
	q.Spend(300)
	put("project-two")
	if q.ClientID() != "project-two" || q.Status().Used != 0 {
		t.Fatalf("after a new client: %q used %d, want a fresh tally", q.ClientID(), q.Status().Used)
	}
}

// ---------------------------------------------------------- review round 2

// TestAnalyticsCountsChatOnAPlatformWithNoViewerCount (R2): a running
// destination's connected Facebook account has no viewer count to read, but
// its chat is the stream's.
func TestAnalyticsCountsChatOnAPlatformWithNoViewerCount(t *testing.T) {
	f := newAnalyticsFixture(t)
	n := 3
	f.fake.twitch["tok-a"] = &n
	f.account(t, db.PlatformTwitch, "shown", "tok-a")
	f.account(t, db.PlatformFacebook, "page", "tok-f")
	f.running[f.dest["shown"]], f.running[f.dest["page"]] = true, true
	f.tick(t)
	if _, err := f.store.AppendChatMessages([]db.ChatMessage{
		{Platform: db.PlatformFacebook, Account: "page-ref", MessageID: "fb1", AuthorID: "fan", AuthorName: "fan", Text: "hi", At: f.now},
	}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	f.tick(t)
	var live analytics.Live
	f.get(t, "/api/v1/analytics/live", &live)
	if len(live.Readings) != 1 || live.Readings[0].Platform != "twitch" {
		t.Fatalf("readings = %+v, want Twitch only: Facebook has no viewer count", live.Readings)
	}
	var rep analytics.Report
	f.get(t, "/api/v1/analytics/streams/"+strconv.FormatInt(live.Stream.ID, 10), &rep)
	if len(rep.Chat) != 1 || rep.Chat[0].Platform != "facebook" || rep.Chat[0].Messages != 1 {
		t.Fatalf("chat = %+v, want Facebook's message counted", rep.Chat)
	}
}

// TestAnEndedStreamsReportShowsItsStoredTotals (R10): the list and the
// report agree, even when the samples changed after the close.
func TestAnEndedStreamsReportShowsItsStoredTotals(t *testing.T) {
	f := newAnalyticsFixture(t)
	n := 10
	f.fake.twitch["tok-a"] = &n
	f.account(t, db.PlatformTwitch, "shown", "tok-a")
	f.running[f.dest["shown"]] = true
	f.tick(t)
	f.running = map[int64]bool{}
	f.now = f.now.Add(analytics.CloseGrace)
	f.tick(t)
	rows, _ := f.store.ListAnalyticsStreams(1)
	id := rows[0].Stream.ID
	big := 999
	// A sample landing after the close (or the samples thinned by retention):
	// the stored totals are the stream's.
	_ = f.store.RecordAnalyticsTick(id, analytics.Tick{At: f.now, Readings: []analytics.Reading{{Platform: "twitch", Account: "x", Viewers: &big}}})
	var rep analytics.Report
	f.get(t, "/api/v1/analytics/streams/"+strconv.FormatInt(id, 10), &rep)
	if rep.Stream.PeakTotal == nil || *rep.Stream.PeakTotal != 10 || rep.Summary.PeakTotal == nil || *rep.Summary.PeakTotal != 10 {
		t.Fatalf("report peak = %v / %v, want the stored 10", rep.Stream.PeakTotal, rep.Summary.PeakTotal)
	}
}

// ---------------------------------------------------------- final pass

// TestAPastedKeyDestinationsAttachedChatIsCounted (F1): a running Rumble
// destination with a pasted key has no account row and no viewer count, but
// its attached chat adapter's chat is the stream's.
func TestAPastedKeyDestinationsAttachedChatIsCounted(t *testing.T) {
	f := newAnalyticsFixture(t)
	hub := chat.New()
	t.Cleanup(hub.Close)
	f.s.chat = hub
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := hub.Attach(ctx, &moderatableAdapter{platform: db.PlatformRumble, account: "rumble"}); err != nil {
		t.Fatal(err)
	}
	d, err := f.store.CreateDestination(&db.Destination{
		Name: "Rumble by key", Kind: db.DestRTMP, Platform: db.PlatformRumble,
		URL: "rtmp://ingest.example/app", StreamKey: "key-rumble",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.running[d.ID] = true
	f.tick(t)
	if _, err := f.store.AppendChatMessages([]db.ChatMessage{
		{Platform: db.PlatformRumble, Account: "rumble", MessageID: "r1", AuthorID: "fan", AuthorName: "fan", Text: "hi", At: f.now},
	}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	f.tick(t)
	rows, _ := f.store.ListAnalyticsStreams(1)
	var rep analytics.Report
	f.get(t, "/api/v1/analytics/streams/"+strconv.FormatInt(rows[0].Stream.ID, 10), &rep)
	if len(rep.Chat) != 1 || rep.Chat[0].Platform != "rumble" || rep.Chat[0].Messages != 1 {
		t.Fatalf("chat = %+v, want the Rumble adapter's message counted", rep.Chat)
	}
}

// TestDeletingYouTubeCredentialsUnbindsTheAllowance (F3): reads are refused
// until a client is stored again, and a different client starts afresh.
func TestDeletingYouTubeCredentialsUnbindsTheAllowance(t *testing.T) {
	s, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	call := func(method string, body any) {
		r := jsonRequest(t, method, "/api/v1/platforms/credentials/youtube", body)
		sign(r)
		if w := do(t, h, r); w.Code != http.StatusOK {
			t.Fatalf("%s creds: %d %s", method, w.Code, w.Body.String())
		}
	}
	call(http.MethodPut, map[string]string{"clientId": "project-one", "clientSecret": "client-pass"})
	q := s.ytQuotaShared
	q.Spend(40)
	call(http.MethodDelete, nil)
	if q.ClientID() != "" {
		t.Fatalf("still bound to %q after the credentials were deleted", q.ClientID())
	}
	if (analyticsSource{s}).Allow(analytics.Target{Platform: "youtube", AccountID: 1, Account: "ref"}) {
		t.Fatal("a YouTube read was allowed with the credentials deleted")
	}
	call(http.MethodPut, map[string]string{"clientId": "project-two", "clientSecret": "client-pass"})
	if q.ClientID() != "project-two" || q.Status().Used != 0 {
		t.Fatalf("after a new client: %q used %d, want a fresh tally", q.ClientID(), q.Status().Used)
	}
}
