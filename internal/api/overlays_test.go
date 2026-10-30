package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/eventstream/streamtest"
	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/overlay"
)

// createOverlay makes an overlay through the session-only API and returns it.
func createOverlay(t *testing.T, h http.Handler, sign func(*http.Request), name string, platforms []string) overlayView {
	t.Helper()
	r := jsonRequest(t, http.MethodPost, "/api/v1/overlays", map[string]any{
		"name": name, "platforms": platforms, "style": overlay.PresetStyle("bubble"),
	})
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create overlay: %d %s", w.Code, w.Body.String())
	}
	var v overlayView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v.OBSKey, db.OverlayKeyPrefix) {
		t.Fatalf("created overlay has key %q", v.OBSKey)
	}
	return v
}

func openOverlay(t *testing.T, srv *httptest.Server, path, key string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set(OverlayKeyHeader, key)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

// TestOverlayManagementIsSessionOnly: an API token, even an admin one, cannot
// list, mint, rotate or revoke overlay keys -- the same rule as API tokens.
func TestOverlayManagementIsSessionOnly(t *testing.T) {
	_, h, sign, _ := overlayServer(t)
	o := createOverlay(t, h, sign, "All", nil)
	admin := createScopedToken(t, h, sign, "ci", db.ScopeAdmin)
	id := strconv.FormatInt(o.ID, 10)
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/overlays"},
		{http.MethodPost, "/api/v1/overlays"},
		{http.MethodPut, "/api/v1/overlays/" + id},
		{http.MethodDelete, "/api/v1/overlays/" + id},
		{http.MethodPost, "/api/v1/overlays/" + id + "/key"},
		{http.MethodDelete, "/api/v1/overlays/" + id + "/key"},
	} {
		r := jsonRequest(t, rt.method, rt.path, map[string]any{"name": "x"})
		r.Header.Set("Authorization", "Bearer "+admin)
		if w := do(t, h, r); w.Code < 400 {
			t.Errorf("%s %s with an admin API token: %d, want refused", rt.method, rt.path, w.Code)
		}
		r = jsonRequest(t, rt.method, rt.path, map[string]any{"name": "x"})
		r.Header.Set(OverlayKeyHeader, o.OBSKey)
		r.Header.Set("Authorization", "Bearer "+o.OBSKey)
		if w := do(t, h, r); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with the overlay's own key: %d, want 401", rt.method, rt.path, w.Code)
		}
	}
}

func TestOverlayStyleAndPlatformsAreValidatedOnWrite(t *testing.T) {
	_, h, sign, _ := overlayServer(t)
	for name, body := range map[string]map[string]any{
		"bad platform": {"name": "x", "platforms": []string{"myspace"}},
		"css in color": {"name": "x", "style": map[string]any{"preset": "classic", "font": "sans", "fontSize": 20,
			"textColor": "red;}", "backgroundColor": "#000000", "maxLines": 5}},
		"no name": {"name": " "},
	} {
		r := jsonRequest(t, http.MethodPost, "/api/v1/overlays", body)
		sign(r)
		if w := do(t, h, r); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, w.Code, w.Body.String())
		}
	}
}

// TestTheOverlayConfigReturnsStyleAndNothingAboutTheKey.
func TestTheOverlayConfigReturnsStyleAndNothingAboutTheKey(t *testing.T) {
	srv, h, sign, _ := overlayServer(t)
	o := createOverlay(t, h, sign, "Twitch", []string{"twitch"})

	res := openOverlay(t, srv, "/api/v1/overlay/config", o.OBSKey)
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("config: %d %s", res.StatusCode, body)
	}
	if strings.Contains(string(body), o.OBSKey) || strings.Contains(string(body), "obsKey") ||
		strings.Contains(string(body), `"id"`) {
		t.Errorf("config leaks key or id: %s", body)
	}
	var cfg overlayConfig
	if err := json.Unmarshal(body, &cfg); err != nil || cfg.Style.Preset != "bubble" || cfg.Platforms[0] != "twitch" {
		t.Errorf("config = %+v (%v)", cfg, err)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q, want no-store", cc)
	}
	for _, k := range []string{"", "pov_nope", "pmk_x", o.OBSKey + "x"} {
		if res := openOverlay(t, srv, "/api/v1/overlay/config", k); res.StatusCode != http.StatusUnauthorized {
			t.Errorf("key %q: %d, want 401", k, res.StatusCode)
		}
	}
	// The key is read from the header only: in a query string it is nothing.
	if res := openOverlay(t, srv, "/api/v1/overlay/config?key="+o.OBSKey, ""); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("key in query: %d, want 401", res.StatusCode)
	}
}

// collectUntil reads frames until one carries the sentinel text, and returns
// every frame before it.
func collectUntil(t *testing.T, br *bufio.Reader, sentinel string) []streamtest.Frame {
	t.Helper()
	var out []streamtest.Frame
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			f, ok := streamtest.ReadFrame(br)
			if !ok {
				return
			}
			if d, ok := f.Env.Data.(map[string]any); ok && d["text"] == sentinel {
				return
			}
			out = append(out, f)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sentinel never arrived")
	}
	return out
}

// TestTheTwitchOverlayStreamCarriesOnlyTwitchDisplayLines: the wire, not the
// hub. Every catalogued family is published -- raw chat with author ids,
// moderation, destination status, alerts, YouTube display lines -- and the
// Twitch overlay's socket carries Twitch display lines and nothing else,
// whatever topics the URL asks for.
func TestTheTwitchOverlayStreamCarriesOnlyTwitchDisplayLines(t *testing.T) {
	srv, h, sign, hub := overlayServer(t)
	o := createOverlay(t, h, sign, "Twitch", []string{"twitch"})
	res := openOverlay(t, srv, "/api/v1/overlay/events?topics=chat,destination,alert", o.OBSKey)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stream: %d", res.StatusCode)
	}
	waitForListener(t, hub)

	hub.Publish(eventstream.Envelope{Type: eventstream.TypeChatMessage, Data: map[string]any{
		"platform": "twitch", "text": "raw", "author": map[string]any{"id": "AUTHOR-ID-1"}}})
	hub.Publish(eventstream.Envelope{Type: eventstream.TypeChatModeration, Data: chat.Moderation{Action: "ban", UserID: "AUTHOR-ID-1"}})
	hub.Publish(eventstream.Envelope{Type: eventstream.TypeChatRetract, Data: chat.Retraction{Platform: "twitch"}})
	hub.Publish(eventstream.Envelope{Type: eventstream.TypeDestinationDown, Destination: &hooks.DestinationRef{Name: "yt"}})
	hub.Publish(eventstream.Envelope{Type: eventstream.TypeAlert, Data: alerts.Event{Type: alerts.TypeDestinationDown}})
	hub.Publish(eventstream.Envelope{Type: eventstream.TypeChatDisplay, Data: eventstream.DisplayLine{ID: "y", Platform: "youtube", Text: "rival"}})
	hub.Publish(eventstream.Envelope{Type: eventstream.TypeChatUndisplay, Data: eventstream.DisplayRemoval{Platform: "youtube", IDs: []string{"y"}}})
	hub.Publish(eventstream.Envelope{Type: eventstream.TypeChatDisplay, Data: eventstream.DisplayLine{ID: "t", Platform: "twitch", Text: "ours"}})
	hub.Publish(eventstream.Envelope{Type: eventstream.TypeChatDisplay, Data: eventstream.DisplayLine{ID: "s", Platform: "twitch", Text: "SENTINEL"}})

	frames := collectUntil(t, bufio.NewReader(res.Body), "SENTINEL")
	if len(frames) != 1 {
		t.Fatalf("got %d frames before the sentinel, want exactly the one Twitch line: %+v", len(frames), frames)
	}
	if frames[0].Event != string(eventstream.TypeChatDisplay) || frames[0].Env.Data.(map[string]any)["text"] != "ours" {
		t.Errorf("frame = %+v", frames[0])
	}
}

// TestRevokingOrRotatingAnOverlayKeyEndsItsStreamWithinSeconds, and afterwards
// the old key is a 401 on both overlay routes.
func TestRevokingOrRotatingAnOverlayKeyEndsItsStreamWithinSeconds(t *testing.T) {
	for _, tc := range []struct{ name, method, suffix string }{
		{"revoke", http.MethodDelete, "/key"},
		{"rotate", http.MethodPost, "/key"},
		{"delete", http.MethodDelete, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, h, sign, hub := overlayServer(t)
			o := createOverlay(t, h, sign, "All", nil)
			res := openOverlay(t, srv, "/api/v1/overlay/events", o.OBSKey)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("stream: %d", res.StatusCode)
			}
			waitForListener(t, hub)
			ended := make(chan struct{})
			go func() { _, _ = io.Copy(io.Discard, res.Body); close(ended) }()

			start := time.Now()
			r := jsonRequest(t, tc.method, "/api/v1/overlays/"+strconv.FormatInt(o.ID, 10)+tc.suffix, nil)
			sign(r)
			if w := do(t, h, r); w.Code >= 300 {
				t.Fatalf("%s: %d %s", tc.name, w.Code, w.Body.String())
			}
			select {
			case <-ended:
			case <-time.After(2 * time.Second):
				t.Fatalf("the stream was still open 2s after %s", tc.name)
			}
			t.Logf("%s ended the stream in %s", tc.name, time.Since(start).Round(time.Millisecond))
			for _, p := range []string{"/api/v1/overlay/events", "/api/v1/overlay/config"} {
				if res := openOverlay(t, srv, p, o.OBSKey); res.StatusCode != http.StatusUnauthorized {
					t.Errorf("after %s, the old key on %s: %d, want 401", tc.name, p, res.StatusCode)
				}
			}
		})
	}
}

// TestAKeyChangedBehindTheHandlersBackStillEndsTheStream: the periodic recheck
// is the net under the kicker -- a row changed by any other path (a restore, a
// future handler that forgets to kick) still ends the stream.
func TestAKeyChangedBehindTheHandlersBackStillEndsTheStream(t *testing.T) {
	srv, h, sign, hub := overlayServer(t)
	o := createOverlay(t, h, sign, "All", nil)
	res := openOverlay(t, srv, "/api/v1/overlay/events", o.OBSKey)
	waitForListener(t, hub)
	ended := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, res.Body); close(ended) }()
	if _, err := serverUnderTest(t, h).store.RevokeChatOverlayKey(o.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ended:
	case <-time.After(overlayRecheck + 2*time.Second):
		t.Fatal("a key revoked in the store, without a kick, kept its stream open")
	}
}

// ------------------------------------------------------------------ isolation

// TestAnOverlayKeyConfersNothingAnywhereElse is the isolation proof. Over EVERY
// method+pattern this build registers (the ledger's own recorder, not a hand
// list), the overlay key -- presented as a bearer token, in the overlay header,
// as ?key=, ?token= and as a session cookie -- gets exactly the status an
// anonymous request gets. The only exceptions are the overlay's own two
// routes with the key in its own header.
func TestAnOverlayKeyConfersNothingAnywhereElse(t *testing.T) {
	_, h, sign, _ := overlayServer(t)
	o := createOverlay(t, h, sign, "All", nil)
	s := serverUnderTest(t, h)

	presentations := map[string]func(*http.Request){
		"bearer": func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+o.OBSKey) },
		"header": func(r *http.Request) { r.Header.Set(OverlayKeyHeader, o.OBSKey) },
		"cookie": func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "polyemesis_session", Value: o.OBSKey}) },
		"query":  func(r *http.Request) { q := r.URL.Query(); q.Set("key", o.OBSKey); r.URL.RawQuery = q.Encode() },
		"token=": func(r *http.Request) { q := r.URL.Query(); q.Set("token", o.OBSKey); r.URL.RawQuery = q.Encode() },
		"csrf+ck": func(r *http.Request) {
			r.Header.Set("X-CSRF-Token", o.OBSKey)
			r.Header.Set("Authorization", "Bearer "+o.OBSKey)
		},
	}
	n := 0
	ip := 0
	request := func(method, path string, mut func(*http.Request)) int {
		ip++
		r := httptest.NewRequest(method, path, bytes.NewReader([]byte("{}")))
		r.Header.Set("Content-Type", "application/json")
		// A fresh address each time, so the login throttle cannot turn one
		// presentation's 400 into the next one's 429.
		r.RemoteAddr = "198.51.100." + strconv.Itoa(ip%250+1) + ":5" + strconv.Itoa(ip%1000)
		if mut != nil {
			mut(r)
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r.WithContext(ctx))
		return w.Code
	}
	for pair := range recordRegistrations(t, s).expandedPairs() {
		method, pattern, _ := strings.Cut(pair, " ")
		path := concretePath(pattern)
		anon := request(method, path, nil)
		for name, mut := range presentations {
			if strings.HasPrefix(pattern, "/api/v1/overlay/") && method == http.MethodGet && name == "header" {
				continue // the overlay's own routes; proven to work above
			}
			if got := request(method, path, mut); got != anon {
				t.Errorf("%s %s with the overlay key as %s: %d, anonymous gets %d", method, pattern, name, got, anon)
			}
			n++
		}
	}
	if n < 1000 {
		t.Fatalf("only %d requests were made; the population did not enumerate", n)
	}
	t.Logf("%d keyed requests matched the anonymous answer", n)
}

// TestAnOverlayKeyGetsNoTopicOnTheEventsAPIAndNoWebSocket: the two other
// streaming doors, asked for by name and per topic.
func TestAnOverlayKeyGetsNoTopicOnTheEventsAPIAndNoWebSocket(t *testing.T) {
	srv, h, sign, _ := overlayServer(t)
	o := createOverlay(t, h, sign, "All", nil)
	queries := []string{""}
	for _, tp := range eventstream.Topics() {
		queries = append(queries, "?topics="+string(tp))
	}
	for _, q := range queries {
		for _, hdr := range []http.Header{
			{"Authorization": {"Bearer " + o.OBSKey}},
			{OverlayKeyHeader: {o.OBSKey}},
		} {
			if res := openEvents(t, srv, q, "", hdr); res.StatusCode != http.StatusUnauthorized {
				t.Errorf("/api/v1/events%s with the overlay key: %d, want 401", q, res.StatusCode)
			}
		}
	}
	for _, hdr := range []http.Header{
		{"Authorization": {"Bearer " + o.OBSKey}},
		{OverlayKeyHeader: {o.OBSKey}},
	} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/ws?token="+o.OBSKey, nil)
		for k, v := range hdr {
			req.Header[k] = v
		}
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("/ws with the overlay key: %d, want 401", res.StatusCode)
		}
	}
}

// ------------------------------------------------------------- logs, headers

// lockedBuffer is a log sink safe across the server's goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// TestTheOverlayKeyNeverReachesTheAccessLog: at Debug, across a 200, 401, 404
// and 405 -- including the operator who pastes the key into the PATH, which
// is logged raw when no route matches.
func TestTheOverlayKeyNeverReachesTheAccessLog(t *testing.T) {
	_, h, sign, _ := overlayServer(t)
	o := createOverlay(t, h, sign, "All", nil)
	s := serverUnderTest(t, h)
	logs := &lockedBuffer{}
	s.log = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h = s.Handler()

	for _, rq := range []struct {
		method, path string
		header       bool
	}{
		{http.MethodGet, "/api/v1/overlay/config", true},                  // 200
		{http.MethodGet, "/api/v1/overlay/config?key=" + o.OBSKey, false}, // 401
		{http.MethodPost, "/api/v1/overlay/config", true},                 // 405
		{http.MethodGet, "/api/v1/overlay/" + o.OBSKey, false},            // 404
		{http.MethodGet, "/overlay/" + o.OBSKey, false},                   // SPA fallback
		{http.MethodGet, "/overlay?key=" + o.OBSKey, false},               // page
		{http.MethodGet, "/api/v1/nope/" + o.OBSKey + "/x", false},        // 404
	} {
		r := httptest.NewRequest(rq.method, rq.path, nil)
		if rq.header {
			r.Header.Set(OverlayKeyHeader, o.OBSKey)
		}
		do(t, h, r)
	}
	out := logs.String()
	if !strings.Contains(out, "path=") {
		t.Fatal("nothing was logged; the test is not observing the access log")
	}
	secret := strings.TrimPrefix(o.OBSKey, db.OverlayKeyPrefix)
	if strings.Contains(out, secret) {
		t.Errorf("the overlay key reached the log:\n%s", out)
	}
}

// TestTheOverlayPagesSendNoReferrer: Referrer-Policy on both pages, and no
// caching of a page whose URL carries a credential.
func TestTheOverlayPagesSendNoReferrer(t *testing.T) {
	_, h, _, _ := overlayServer(t)
	for _, p := range []string{OverlayPath, OverlayPath + "/dock"} {
		w := do(t, h, httptest.NewRequest(http.MethodGet, p, nil))
		if got := w.Header().Get("Referrer-Policy"); got != "no-referrer" {
			t.Errorf("%s: Referrer-Policy %q", p, got)
		}
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control %q", p, got)
		}
		// The first paint must already be transparent: OBS shows it.
		if _, built := transparentIndex(); built {
			body := w.Body.String()
			if !strings.Contains(body, `<body style="background:transparent">`) ||
				strings.Contains(body, "#0f1115") {
				t.Errorf("%s: the page is not transparent before the bundle loads", p)
			}
		}
	}
	w := do(t, h, httptest.NewRequest(http.MethodGet, "/api/v1/overlay/events", nil))
	if got := w.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("stream: Referrer-Policy %q", got)
	}
}

// ---------------------------------------------------- moderation, end to end

// wordModerator removes any message containing "BLOCKME" with a local hide,
// and passes everything else. A real automod rule's shape, without the rule
// engine.
type wordModerator struct{}

func (wordModerator) CheckFast(p db.Platform, authorID, text string) automod.Verdict {
	if !strings.Contains(text, "BLOCKME") {
		return automod.Verdict{}
	}
	f := automod.Finding{Checker: automod.CheckerRules, Action: automod.ActionHideLocal, Reason: "test"}
	return automod.Verdict{Findings: []automod.Finding{f}, Act: []automod.Finding{f}}
}
func (wordModerator) CheckModel(context.Context, db.Platform, string) (automod.Verdict, error) {
	return automod.Verdict{}, nil
}
func (wordModerator) ModelEnabled() bool             { return false }
func (wordModerator) ModelStats() automod.ModelStats { return automod.ModelStats{} }

// chanAdapter delivers whatever the test sends it.
type chanAdapter struct {
	platform db.Platform
	in       chan chat.Message
}

func (a *chanAdapter) Platform() db.Platform { return a.platform }
func (a *chanAdapter) Account() string       { return "acct" }
func (a *chanAdapter) Run(ctx context.Context, sink chat.Sink) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case m := <-a.in:
			sink.Deliver(m)
		}
	}
}

// TestHiddenAndAutomodBlockedMessagesNeverReachTheOverlay drives the whole
// production chain: an adapter delivers into the real chat hub, real automod
// wiring rules on it, the real gate publishes, and the overlay's HTTP stream
// is read off a socket. The blocked line never appears; the clean one does;
// an operator's local hide through the real API takes it off again.
func TestHiddenAndAutomodBlockedMessagesNeverReachTheOverlay(t *testing.T) {
	srv, h, sign, hub := overlayServer(t)
	s := serverUnderTest(t, h)
	gate := overlay.New(hub)
	ch := chat.New(chat.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(ch.Close)
	ch.SetDisplay(gate)
	ch.SetModerator(wordModerator{})
	s.chat = ch
	tw := &chanAdapter{platform: db.PlatformTwitch, in: make(chan chat.Message, 8)}
	yt := &chanAdapter{platform: db.PlatformYouTube, in: make(chan chat.Message, 8)}
	for _, a := range []*chanAdapter{tw, yt} {
		if err := ch.Attach(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}

	o := createOverlay(t, h, sign, "Twitch", []string{"twitch"})
	res := openOverlay(t, srv, "/api/v1/overlay/events", o.OBSKey)
	waitForListener(t, hub)
	br := bufio.NewReader(res.Body)

	tw.in <- chat.Message{ID: "bad", Author: chat.Author{ID: "u9", Name: "troll"}, Text: "BLOCKME now"}
	yt.in <- chat.Message{ID: "yt1", Author: chat.Author{ID: "u2", Name: "ytviewer"}, Text: "from youtube"}
	tw.in <- chat.Message{ID: "ok", Author: chat.Author{ID: "u1", Name: "viewer"}, Text: "hello stream"}
	tw.in <- chat.Message{ID: "end", Author: chat.Author{ID: "u1", Name: "viewer"}, Text: "SENTINEL"}

	frames := collectUntil(t, br, "SENTINEL")
	if len(frames) != 1 || frames[0].Env.Data.(map[string]any)["text"] != "hello stream" {
		t.Fatalf("frames before the sentinel = %+v; want only the clean Twitch line", frames)
	}
	shownID := frames[0].Env.Data.(map[string]any)["id"].(string)
	if strings.Contains(frames[0].Env.Data.(map[string]any)["name"].(string), "u1") {
		t.Error("the author id reached the overlay")
	}

	r := jsonRequest(t, http.MethodPost, "/api/v1/chat/messages/hide?platform=twitch&account=acct&id=ok&scope=local", nil)
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("hide: %d %s", w.Code, w.Body.String())
	}
	f := streamtest.ExpectSSE(t, br, eventstream.TypeChatUndisplay)
	ids, _ := f.Env.Data.(map[string]any)["ids"].([]any)
	if len(ids) != 1 || ids[0] != shownID {
		t.Errorf("undisplay %+v, want [%s]", f.Env.Data, shownID)
	}
}

// overlayServer is eventsServer for the overlay tests.
func overlayServer(t *testing.T) (*httptest.Server, http.Handler, func(*http.Request), *eventstream.Hub) {
	t.Helper()
	srv, _, h, sign, hub := eventsServer(t)
	return srv, h, sign, hub
}

// TestAReconnectingOverlayIsNotReplayedARetractedLine is the replay half of the
// moderation promise, over HTTP: an OBS source that reloads and resumes with
// Last-Event-ID from before a line that was since hidden must not get that line
// back, even for one frame.
func TestAReconnectingOverlayIsNotReplayedARetractedLine(t *testing.T) {
	srv, h, sign, hub := overlayServer(t)
	o := createOverlay(t, h, sign, "All", nil)

	hub.Publish(eventstream.Envelope{Type: eventstream.TypeChatDisplay, Data: eventstream.DisplayLine{ID: "before", Platform: "twitch", Text: "first"}})
	hub.Publish(eventstream.Envelope{Type: eventstream.TypeChatDisplay, Data: eventstream.DisplayLine{ID: "hidden", Platform: "twitch", Text: "HIDDEN LINE"}})
	hub.Publish(eventstream.Envelope{Type: eventstream.TypeChatUndisplay, Data: eventstream.DisplayRemoval{Platform: "twitch", IDs: []string{"hidden"}}})
	hub.Publish(eventstream.Envelope{Type: eventstream.TypeChatDisplay, Data: eventstream.DisplayLine{ID: "end", Platform: "twitch", Text: "SENTINEL"}})

	// An id this hub cannot place is a gap, which replays EVERYTHING held:
	// the widest resume there is.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/overlay/events", nil)
	req.Header.Set(OverlayKeyHeader, o.OBSKey)
	req.Header.Set("Last-Event-ID", "x-0")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	frames := collectUntil(t, bufio.NewReader(res.Body), "SENTINEL")
	var texts []any
	for _, f := range frames {
		if d, ok := f.Env.Data.(map[string]any); ok {
			texts = append(texts, d["text"])
			if d["text"] == "HIDDEN LINE" {
				t.Error("a reconnecting overlay was replayed a retracted line")
			}
		}
	}
	if len(texts) < 2 {
		t.Errorf("replay %v: the resume replayed nothing, so the absence proves nothing", texts)
	}
}
