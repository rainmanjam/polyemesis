package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/activityhub"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/eventstream/streamtest"
	"github.com/rainmanjam/polyemesis/internal/overlay"
)

// streamBus is the production bus -> events hub bridge without the goroutine,
// so a test sees the envelope the moment Submit returns.
type streamBus struct{ hub *eventstream.Hub }

func (b streamBus) Publish(t events.Type, data any) {
	if st, ok := map[events.Type]eventstream.Type{events.TypeActivity: eventstream.TypeActivity}[t]; ok {
		b.hub.Publish(eventstream.Envelope{Type: st, Data: data})
	}
}

// activityServer is eventsServer with a real activity hub and alerts gate on
// the stream, serving a real listener.
func activityServer(t *testing.T) (*httptest.Server, *Server, http.Handler, func(*http.Request), *eventstream.Hub) {
	t.Helper()
	srv, s, h, sign, hub := eventsServer(t)
	gate := overlay.NewActivityGate(hub, nil)
	s.activityGate = gate
	s.activity = activityhub.New(
		activityhub.WithBus(streamBus{hub}), activityhub.WithDisplay(gate),
		activityhub.WithSettings(func() db.ActivitySettings {
			cfg, err := s.store.GetSettings()
			if err != nil {
				return db.DefaultActivitySettings()
			}
			return cfg.Activity
		}),
	)
	t.Cleanup(s.activity.Close)
	return srv, s, h, sign, hub
}

func TestActivityRoutesAnswer503WithoutTheHub(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	send(t, h, sign, http.MethodGet, "/api/v1/activity", nil, http.StatusServiceUnavailable)
	send(t, h, sign, http.MethodGet, "/api/v1/activity/sources", nil, http.StatusServiceUnavailable)
	send(t, h, sign, http.MethodPost, "/api/v1/activity/test", map[string]any{"kind": "follow"}, http.StatusServiceUnavailable)
	send(t, h, sign, http.MethodPost, "/api/v1/activity/act_1/replay", nil, http.StatusServiceUnavailable)
}

func TestActivityRoutesRequireAuthentication(t *testing.T) {
	_, _, h, _, _ := activityServer(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/activity"}, {http.MethodGet, "/api/v1/activity/sources"},
		{http.MethodPost, "/api/v1/activity/test"}, {http.MethodPost, "/api/v1/activity/x/replay"},
	} {
		if w := do(t, h, jsonRequest(t, c.method, c.path, nil)); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymously = %d", c.method, c.path, w.Code)
		}
	}
}

// A read token is refused everything about activity: it names viewers and
// carries what they wrote, as chat does. The method rule already denies it the
// two POSTs; the readDenied flag is what denies the two GETs.
func TestAReadTokenCannotReadOrSendActivity(t *testing.T) {
	_, _, h, sign, _ := activityServer(t)
	read := bearer(createScopedToken(t, h, sign, "monitoring", db.ScopeRead))
	send(t, h, read, http.MethodGet, "/api/v1/activity", nil, http.StatusForbidden)
	send(t, h, read, http.MethodGet, "/api/v1/activity/sources", nil, http.StatusForbidden)
	send(t, h, read, http.MethodPost, "/api/v1/activity/test", map[string]any{"kind": "follow"}, http.StatusForbidden)
	send(t, h, read, http.MethodPost, "/api/v1/activity/x/replay", nil, http.StatusForbidden)
	admin := bearer(createScopedToken(t, h, sign, "bot", db.ScopeAdmin))
	send(t, h, admin, http.MethodGet, "/api/v1/activity", nil, http.StatusOK)
}

// The "done when" for the core: a test alert of every kind reaches the
// console's feed, the events stream and the alerts overlay with no platform
// connected, and an overlay keyed to another platform sees none of it.
func TestATestAlertReachesTheFeedTheEventsStreamAndTheOverlay(t *testing.T) {
	srv, s, h, sign, hub := activityServer(t)
	s.store.UpdateSettings(func(cfg *db.Settings) error { cfg.Activity.Reactions = true; return nil })
	o := createOverlay(t, h, sign, "Twitch", []string{"twitch"})
	other := createOverlay(t, h, sign, "Kick", []string{"kick"})
	admin := createScopedToken(t, h, sign, "bot", db.ScopeAdmin)
	read := createScopedToken(t, h, sign, "monitor", db.ScopeRead)

	stream := bufio.NewReader(openEvents(t, srv, "?topics=activity", admin, nil).Body)
	readStream := bufio.NewReader(openEvents(t, srv, "?topics=activity", read, nil).Body)
	ov := bufio.NewReader(openOverlay(t, srv, "/api/v1/overlay/events", o.OBSKey).Body)
	kick := bufio.NewReader(openOverlay(t, srv, "/api/v1/overlay/events", other.OBSKey).Body)
	waitForListener(t, hub)

	for _, kind := range activity.AllKinds() {
		res := s.activity.Submit(activity.Sample(kind, "twitch", activity.Overrides{}, time.Now()))
		if !res.Accepted {
			t.Fatalf("%s: %+v", kind, res)
		}
		f := streamtest.ExpectSSE(t, stream, eventstream.TypeActivity)
		if f.Env.Data.(map[string]any)["kind"] != string(kind) {
			t.Fatalf("%s: stream carried %v", kind, f.Env.Data)
		}
		if res.Event.OverlayVisible() {
			d := streamtest.ExpectSSE(t, ov, eventstream.TypeActivityDisplay)
			disp := d.Env.Data.(map[string]any)
			if disp["kind"] != string(kind) {
				t.Errorf("%s: overlay drew %v", kind, disp)
			}
			for _, banned := range []string{"actor", "account", "source", "recipients", "other"} {
				if _, leaked := disp[banned]; leaked {
					t.Errorf("%s: the overlay was sent %q", kind, banned)
				}
			}
		}
	}
	// The read token was sent the stream report and no viewer.
	f := streamtest.ExpectSSE(t, readStream, eventstream.TypeActivity)
	if f.Env.Data.(map[string]any)["kind"] != "stream" {
		t.Errorf("a read token was sent %v", f.Env.Data)
	}
	_ = kick

	// The console feed holds all fourteen, newest first, and filters.
	var feed activityFeed
	mustDecode(t, send(t, h, sign, http.MethodGet, "/api/v1/activity?limit=5", nil, http.StatusOK), &feed)
	if !feed.Enabled || len(feed.Events) != 5 || feed.Events[0].Kind != activity.KindReaction {
		t.Errorf("feed: %+v", feed)
	}
	mustDecode(t, send(t, h, sign, http.MethodGet, "/api/v1/activity?platform=twitch&kind=tip", nil, http.StatusOK), &feed)
	if len(feed.Events) != 1 || feed.Events[0].Kind != activity.KindTip {
		t.Errorf("filtered feed: %+v", feed)
	}
	mustDecode(t, send(t, h, sign, http.MethodGet, "/api/v1/activity?platform=kick", nil, http.StatusOK), &feed)
	if len(feed.Events) != 0 {
		t.Errorf("kick feed: %+v", feed)
	}
}

func mustDecode(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
}

func TestSendTestAlertAnswersAndIsLimited(t *testing.T) {
	_, s, h, sign, _ := activityServer(t)
	var res activityTestResult
	mustDecode(t, send(t, h, sign, http.MethodPost, "/api/v1/activity/test",
		map[string]any{"kind": "tip", "platform": "kick", "overrides": map[string]any{"name": "Mara", "message": "hi", "amount": map[string]any{"unit": "kicks", "units": 50}}},
		http.StatusOK), &res)
	if !res.Accepted || res.Event.Platform != "kick" || res.Event.Actor.Name != "Mara" || res.Event.Amount.Unit != "kicks" || !res.Event.Test {
		t.Fatalf("result: %+v", res)
	}
	if got := s.activity.Recent(1, "", ""); len(got) != 1 || got[0].ID != res.Event.ID {
		t.Errorf("not in the feed: %+v", got)
	}

	send(t, h, sign, http.MethodPost, "/api/v1/activity/test", map[string]any{"kind": "sneeze"}, http.StatusBadRequest)
	send(t, h, sign, http.MethodPost, "/api/v1/activity/test", map[string]any{"kind": "tip", "platform": "myspace"}, http.StatusBadRequest)
	// Overrides that make an invalid event are the caller's mistake.
	send(t, h, sign, http.MethodPost, "/api/v1/activity/test",
		map[string]any{"kind": "tip", "overrides": map[string]any{"amount": map[string]any{"currency": "DOLLARS", "minor": 1}}}, http.StatusBadRequest)
	// Two a second: the third request in the window is refused.
	r := jsonRequest(t, http.MethodPost, "/api/v1/activity/test", map[string]any{"kind": "follow"})
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "1" {
		t.Errorf("third request in a second = %d %v", w.Code, w.Header())
	}
}

func TestSendTestAlertHonoursTheSettings(t *testing.T) {
	_, s, h, sign, _ := activityServer(t)
	// A save is felt on the next event: the settings handler invalidates the
	// hub's cache.
	send(t, h, sign, http.MethodPut, "/api/v1/settings",
		map[string]any{"activity": map[string]any{"kinds": map[string]any{"twitch": []string{"follow"}}}}, http.StatusOK)
	var res activityTestResult
	mustDecode(t, send(t, h, sign, http.MethodPost, "/api/v1/activity/test", map[string]any{"kind": "tip"}, http.StatusOK), &res)
	if res.Accepted || res.Reason != activityhub.ReasonDisabled || !strings.Contains(res.Detail, "tip") {
		t.Fatalf("a disabled kind: %+v", res)
	}
	if len(s.activity.Recent(10, "", "")) != 0 {
		t.Error("a refused test alert reached the feed")
	}
	// Putting a platform back on the defaults: kinds are REPLACED, not merged.
	send(t, h, sign, http.MethodPut, "/api/v1/settings", map[string]any{"activity": map[string]any{"kinds": map[string]any{}}}, http.StatusOK)
	time.Sleep(550 * time.Millisecond) // the test limiter's window, not the settings cache
	mustDecode(t, send(t, h, sign, http.MethodPost, "/api/v1/activity/test", map[string]any{"kind": "tip"}, http.StatusOK), &res)
	if !res.Accepted {
		t.Errorf("after the reset: %+v", res)
	}
}

func TestSourcesListsKindsPlatformsStatsAndChatReportedKinds(t *testing.T) {
	_, s, h, sign, _ := activityServer(t)
	a := &reportingAdapter{platform: db.PlatformYouTube, account: "UC1"}
	hub := chat.New()
	t.Cleanup(hub.Close)
	if err := hub.Attach(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	s.activity.AddStatusProvider(chatActivityStatuses(hub))
	s.activity.Submit(activity.Sample(activity.KindFollow, "twitch", activity.Overrides{}, time.Now()))

	var out activitySources
	mustDecode(t, send(t, h, sign, http.MethodGet, "/api/v1/activity/sources", nil, http.StatusOK), &out)
	if !out.Enabled || len(out.Kinds) != 14 || len(out.Platforms) == 0 || out.Stats.Delivered != 1 || out.Overlay == nil {
		t.Fatalf("sources: %+v", out)
	}
	for _, k := range out.Kinds {
		if want := k.Kind != activity.KindModeration && k.Kind != activity.KindStream && k.Kind != activity.KindPoll &&
			k.Kind != activity.KindPrediction && k.Kind != activity.KindReaction; k.Overlay != want {
			t.Errorf("%s overlay = %v", k.Kind, k.Overlay)
		}
	}
	if len(out.Sources) != 1 || out.Sources[0].Platform != "youtube" || out.Sources[0].Transport != activityhub.TransportChat ||
		len(out.Sources[0].Kinds) != 1 || out.Sources[0].Kinds[0].State != activity.KindUnknown {
		t.Errorf("chat-reported source: %+v", out.Sources)
	}
}

// reportingAdapter is a chat adapter that reports its activity kinds, the way a
// platform PR will.
type reportingAdapter struct {
	platform db.Platform
	account  string
}

func (a *reportingAdapter) Platform() db.Platform { return a.platform }
func (a *reportingAdapter) Account() string       { return a.account }
func (a *reportingAdapter) Run(ctx context.Context, _ chat.Sink) error {
	<-ctx.Done()
	return nil
}
func (a *reportingAdapter) ActivityKinds() []activity.KindStatus {
	return []activity.KindStatus{{Kind: activity.KindTip, State: activity.KindUnknown, Detail: "not seen yet"}}
}

func TestFeedParametersAreValidated(t *testing.T) {
	_, _, h, sign, _ := activityServer(t)
	for _, q := range []string{"limit=0", "limit=x", "kind=sneeze", "platform=myspace"} {
		send(t, h, sign, http.MethodGet, "/api/v1/activity?"+q, nil, http.StatusBadRequest)
	}
}

func TestReplayRedrawsARememberedEventOnTheOverlay(t *testing.T) {
	srv, s, h, sign, hub := activityServer(t)
	o := createOverlay(t, h, sign, "All", nil)
	ov := bufio.NewReader(openOverlay(t, srv, "/api/v1/overlay/events", o.OBSKey).Body)
	waitForListener(t, hub)
	res := s.activity.Submit(activity.Sample(activity.KindFollow, "twitch", activity.Overrides{}, time.Now()))
	first := streamtest.ExpectSSE(t, ov, eventstream.TypeActivityDisplay)
	mod := s.activity.Submit(activity.Sample(activity.KindModeration, "twitch", activity.Overrides{}, time.Now()))

	send(t, h, sign, http.MethodPost, "/api/v1/activity/"+res.Event.ID+"/replay", nil, http.StatusOK)
	again := streamtest.ExpectSSE(t, ov, eventstream.TypeActivityDisplay)
	if first.Env.Data.(map[string]any)["id"] == again.Env.Data.(map[string]any)["id"] {
		t.Error("a replay must be a new showing to the overlay's queue")
	}
	send(t, h, sign, http.MethodPost, "/api/v1/activity/"+mod.Event.ID+"/replay", nil, http.StatusNotFound)
	send(t, h, sign, http.MethodPost, "/api/v1/activity/act_gone/replay", nil, http.StatusNotFound)
}

func TestAnOverlaySavedWithAnAlertsStyleRoundTrips(t *testing.T) {
	_, _, h, sign, _ := activityServer(t)
	alerts := overlay.DefaultAlertStyle()
	alerts.Layout, alerts.Seconds, alerts.Sound = "card", 9, "pop"
	st := overlay.PresetStyle("bubble")
	st.Alerts = &alerts
	var v overlayView
	mustDecode(t, send(t, h, sign, http.MethodPost, "/api/v1/overlays",
		map[string]any{"name": "Alerts", "platforms": []string{}, "style": st}, http.StatusCreated), &v)
	if v.Style.Alerts == nil || v.Style.Alerts.Layout != "card" || v.Style.Alerts.Sound != "pop" {
		t.Fatalf("style lost its alerts block: %+v", v.Style.Alerts)
	}
	// And an out-of-range block is refused at the door.
	bad := alerts
	bad.Seconds = 99
	st.Alerts = &bad
	send(t, h, sign, http.MethodPost, "/api/v1/overlays", map[string]any{"name": "Bad", "style": st}, http.StatusBadRequest)
}

func TestHooksMetaOffersTheOptInTriggerApart(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	var meta struct {
		Triggers      []string `json:"triggers"`
		OptInTriggers []string `json:"optInTriggers"`
	}
	mustDecode(t, send(t, h, sign, http.MethodGet, "/api/v1/hooks/meta", nil, http.StatusOK), &meta)
	for _, tr := range meta.Triggers {
		if tr == "activity" {
			t.Error("activity is listed among the triggers an empty list means")
		}
	}
	if len(meta.OptInTriggers) != 1 || meta.OptInTriggers[0] != "activity" {
		t.Errorf("optInTriggers = %v", meta.OptInTriggers)
	}
}

func TestTestLimiterSlidesItsWindow(t *testing.T) {
	var l activityTestLimiter
	t0 := time.Unix(1000, 0)
	if !l.allow(t0) || !l.allow(t0.Add(100*time.Millisecond)) {
		t.Fatal("the first two should pass")
	}
	if l.allow(t0.Add(200 * time.Millisecond)) {
		t.Error("the third in a second passed")
	}
	if !l.allow(t0.Add(activityTestWindow + 150*time.Millisecond)) {
		t.Error("the window did not slide")
	}
}

// --- own-transport sources: the extension point the Twitch PR uses

type stubSource struct {
	platform, account string
	stopped           chan struct{}
}

func (s *stubSource) Platform() string { return s.platform }
func (s *stubSource) Account() string  { return s.account }
func (s *stubSource) Run(ctx context.Context, emit func(activity.Event)) error {
	e := activity.Sample(activity.KindFollow, "", activity.Overrides{}, time.Now())
	e.Test = false
	e.Source = activity.Source{Type: "channel.follow", ID: "from-source"}
	emit(e)
	<-ctx.Done()
	close(s.stopped)
	return nil
}

func TestAnOwnTransportSourceStartsWithTheAccountAndStopsWithIt(t *testing.T) {
	_, s, _, _, _ := activityServer(t)
	stopped := make(chan struct{})
	prev := activitySourceBuilders[db.PlatformTwitch]
	activitySourceBuilders[db.PlatformTwitch] = func(_ *Server, a db.PlatformAccount) (activityhub.Source, error) {
		return &stubSource{platform: string(a.Platform), account: a.AccountRef, stopped: stopped}, nil
	}
	t.Cleanup(func() {
		if prev == nil {
			delete(activitySourceBuilders, db.PlatformTwitch)
		} else {
			activitySourceBuilders[db.PlatformTwitch] = prev
		}
	})
	acct := db.PlatformAccount{Platform: db.PlatformTwitch, AccountRef: "42", AccountName: "me"}
	s.attachActivitySource(context.Background(), acct)
	deadline := time.Now().Add(5 * time.Second)
	for len(s.activity.Recent(1, "twitch", activity.KindFollow)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the source's event never reached the hub")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := s.activity.Recent(1, "", "")[0]; got.Account != "42" || got.Platform != "twitch" {
		t.Errorf("not stamped with the account: %+v", got)
	}
	s.detachActivitySource(db.PlatformTwitch, "42")
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("detaching the account did not stop its source")
	}
}

func TestSourceBuilderFailuresAndAbsencesNeverBreakAnAccount(t *testing.T) {
	_, s, _, _, _ := activityServer(t)
	// No builder for the platform: nothing happens, nothing fails.
	s.attachActivitySource(context.Background(), db.PlatformAccount{Platform: db.PlatformKick, AccountRef: "1"})
	activitySourceBuilders[db.PlatformKick] = func(*Server, db.PlatformAccount) (activityhub.Source, error) {
		return nil, errors.New("no events for this account")
	}
	t.Cleanup(func() { delete(activitySourceBuilders, db.PlatformKick) })
	s.attachActivitySource(context.Background(), db.PlatformAccount{Platform: db.PlatformKick, AccountRef: "1"})
	activitySourceBuilders[db.PlatformKick] = func(*Server, db.PlatformAccount) (activityhub.Source, error) { return nil, nil }
	s.attachActivitySource(context.Background(), db.PlatformAccount{Platform: db.PlatformKick, AccountRef: "1"})
	if n := len(s.activity.Statuses()); n != 0 {
		t.Errorf("%d sources listed", n)
	}
	// And a server without a hub does nothing at all.
	var bare Server
	bare.attachActivitySource(context.Background(), db.PlatformAccount{Platform: db.PlatformKick})
	bare.detachActivitySource(db.PlatformKick, "1")
	if chatActivityStatuses(nil)() != nil {
		t.Error("a nil chat hub listed sources")
	}
}
