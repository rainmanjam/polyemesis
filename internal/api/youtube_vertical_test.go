package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// THE VERTICAL HALF IS CREATED ON ITS OWN, PAIRED IN POLYEMESIS, AND NEVER BOUND TO THE
// HORIZONTAL BROADCAST (#7). These tests drive the pairing endpoints through the real
// router, with a fake YouTube behind an httptest server, so the checks cover the same
// path an operator's click takes.
//
// The fake answers every call the pairing path can make, and it records them. The
// assertions read the RECORDED REQUESTS: a bind that carries the vertical stream id, or a
// listing that reuses the shared stream, would look fine in the response body.

// ytVerticalPairStub serves the channel and the stream endpoints the pairing path touches.
// liveStreams.insert answers with a stream whose key is built at runtime, so the test
// fixture does not look like a credential.
type ytVerticalPairStub struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []string
	// streamsDown makes the health read fail, which is how a dead vertical half looks
	// from polyemesis: the call errors and the pair must stay where it is.
	streamsDown bool
	// createDelay holds liveStreams.insert open, so two pairing requests can overlap in the
	// test. Zero for every other test.
	createDelay time.Duration
	// healthDelay holds the health read open, so concurrent status reads overlap.
	healthDelay time.Duration
}

// verticalFakeKey is the key the fake hands back. It is built from a fixed fragment rather
// than written as a literal, so the fixture never looks like a credential to the scanner.
var verticalFakeKey = "vert-" + strings.Repeat("k", 8)

func newYTVerticalPairStub(t *testing.T) *ytVerticalPairStub {
	t.Helper()
	p := &ytVerticalPairStub{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.calls = append(p.calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /channels":
			io.WriteString(w, `{"items":[{"id":"chan-1","snippet":{"title":"Night Owl"}}]}`)
		case "POST /liveStreams":
			time.Sleep(p.createDelay)
			io.WriteString(w, `{"id":"stream-new-vert","snippet":{"title":"polyemesis - Show (vertical)"},
				"cdn":{"ingestionType":"rtmp","ingestionInfo":{"streamName":"`+verticalFakeKey+`",
				"ingestionAddress":"rtmp://v.example/live2"}}}`)
		case "GET /liveStreams":
			p.mu.Lock()
			delay := p.healthDelay
			p.mu.Unlock()
			time.Sleep(delay)
			if p.streamsDown {
				w.WriteHeader(http.StatusInternalServerError)
				io.WriteString(w, `{"error":{"message":"backend unavailable"}}`)
				return
			}
			io.WriteString(w, `{"items":[{"id":"stream-new-vert","status":{"streamStatus":"active",
				"healthStatus":{"status":"good"}}}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// called reports whether a request with this method and path reached the fake, and returns
// its raw query string.
func (p *ytVerticalPairStub) called(method, path string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.calls {
		if strings.HasPrefix(c, method+" "+path+"?") {
			return strings.TrimPrefix(c, method+" "+path+"?"), true
		}
	}
	return "", false
}

// TestPairingCreatesAVerticalHalfWithItsOwnStreamAndAPortraitRendition pins the happy path.
// The vertical destination is portrait, carries its own key, has no account link so the
// horizontal-only paths skip it, and is recorded as the pair's vertical half.
func TestPairingCreatesAVerticalHalfWithItsOwnStreamAndAPortraitRendition(t *testing.T) {
	fake := newYTVerticalPairStub(t)
	s, h, store := verticalServer(t, fake.srv.URL)
	sign := login(t, h)
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid-abc", "secret-xyz"); err != nil {
		t.Fatalf("seed youtube credentials: %v", err)
	}
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")
	horiz := ytDest(t, store, acct, "Main channel", "key-abc")

	body := send(t, h, sign, http.MethodPost, "/api/v1/destinations/"+itoa(horiz.ID)+"/youtube-vertical",
		map[string]any{"tier": "vertical1080p30"}, http.StatusOK)
	var out struct {
		VerticalID int64 `json:"verticalId"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.VerticalID == 0 {
		t.Fatalf("pairing response %s carries no verticalId", body)
	}

	pair, err := store.GetYouTubeVerticalPairByHorizontal(horiz.ID)
	if err != nil {
		t.Fatalf("no pair row after pairing: %v", err)
	}
	if pair.VerticalStreamID != "stream-new-vert" {
		t.Fatalf("pair records stream %q, want the newly created vertical stream", pair.VerticalStreamID)
	}
	vert, err := store.GetDestination(out.VerticalID)
	if err != nil {
		t.Fatalf("read the vertical destination: %v", err)
	}
	if vert.AccountID != nil {
		t.Error("the vertical half has an account link; horizontal-only paths (refresh, lifecycle, schedule) would treat it as theirs")
	}
	if vert.StreamKey == "" {
		t.Error("the vertical half has no stream key; the encoder cannot publish to it")
	}
	if vert.RenditionID == nil {
		t.Fatal("the vertical half has no rendition; it would publish the source shape, not 9:16")
	}
	rend, err := store.GetRendition(*vert.RenditionID)
	if err != nil {
		t.Fatalf("read the vertical rendition: %v", err)
	}
	if rend.Height <= rend.Width {
		t.Errorf("the vertical rendition is %dx%d, which is not portrait", rend.Width, rend.Height)
	}
	if !strings.HasSuffix(strings.TrimSpace(vert.Name), "(vertical)") {
		t.Errorf("vertical destination named %q; the operator cannot tell the halves apart", vert.Name)
	}
	if q, ok := fake.called("POST", "/liveStreams"); !ok || !strings.Contains(q, "part=id") {
		t.Error("the vertical stream was not created through liveStreams.insert")
	}
}

// TestPairingNeverBindsTheHorizontalBroadcastToTheVerticalStream is the bind guard at the
// HTTP layer. Pairing makes a vertical stream and nothing else, so no bind call may appear.
func TestPairingNeverBindsTheHorizontalBroadcastToTheVerticalStream(t *testing.T) {
	fake := newYTVerticalPairStub(t)
	s, h, store := verticalServer(t, fake.srv.URL)
	sign := login(t, h)
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid-abc", "secret-xyz"); err != nil {
		t.Fatalf("seed youtube credentials: %v", err)
	}
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")
	horiz := ytDest(t, store, acct, "Main channel", "key-abc")
	send(t, h, sign, http.MethodPost, "/api/v1/destinations/"+itoa(horiz.ID)+"/youtube-vertical",
		map[string]any{"tier": "vertical1080p30"}, http.StatusOK)
	for _, c := range fake.calls {
		if strings.HasPrefix(c, "POST /liveBroadcasts/bind") {
			t.Fatalf("pairing sent %q; the horizontal broadcast must never bind to the vertical stream", c)
		}
	}
}

// TestPairingRefusesASecondVerticalHalfForTheSameDestination pins one-to-one pairing. A
// second pair would leave two vertical halves competing for one horizontal destination.
func TestPairingRefusesASecondVerticalHalfForTheSameDestination(t *testing.T) {
	fake := newYTVerticalPairStub(t)
	s, h, store := verticalServer(t, fake.srv.URL)
	sign := login(t, h)
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid-abc", "secret-xyz"); err != nil {
		t.Fatalf("seed youtube credentials: %v", err)
	}
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")
	horiz := ytDest(t, store, acct, "Main channel", "key-abc")
	path := "/api/v1/destinations/" + itoa(horiz.ID) + "/youtube-vertical"
	send(t, h, sign, http.MethodPost, path, map[string]any{"tier": "vertical1080p30"}, http.StatusOK)
	send(t, h, sign, http.MethodPost, path, map[string]any{"tier": "vertical1080p30"}, http.StatusConflict)
}

// TestPairingRefusesALandscapeTierAndAnUnknownOne pins the tier check. Only the portrait
// tier list is accepted.
func TestPairingRefusesALandscapeTierAndAnUnknownOne(t *testing.T) {
	fake := newYTVerticalPairStub(t)
	s, h, store := verticalServer(t, fake.srv.URL)
	sign := login(t, h)
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid-abc", "secret-xyz"); err != nil {
		t.Fatalf("seed youtube credentials: %v", err)
	}
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")
	horiz := ytDest(t, store, acct, "Main channel", "key-abc")
	path := "/api/v1/destinations/" + itoa(horiz.ID) + "/youtube-vertical"
	send(t, h, sign, http.MethodPost, path, map[string]any{"tier": "1080p60"}, http.StatusBadRequest)
	send(t, h, sign, http.MethodPost, path, map[string]any{"tier": "no-such-tier"}, http.StatusBadRequest)
	if _, ok := fake.called("POST", "/liveStreams"); ok {
		t.Fatal("a refused tier still created a vertical stream on YouTube")
	}
}

// TestTheVerticalStreamIsExcludedFromTheHorizontalIngestOptions pins the wiring between the
// pair table and the fallback. The horizontal destination's ingest options must name the
// vertical stream, so a rename in Studio cannot make the fallback adopt it.
func TestTheVerticalStreamIsExcludedFromTheHorizontalIngestOptions(t *testing.T) {
	fake := newYTVerticalPairStub(t)
	s, h, store := verticalServer(t, fake.srv.URL)
	sign := login(t, h)
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid-abc", "secret-xyz"); err != nil {
		t.Fatalf("seed youtube credentials: %v", err)
	}
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")
	horiz := ytDest(t, store, acct, "Main channel", "key-abc")
	send(t, h, sign, http.MethodPost, "/api/v1/destinations/"+itoa(horiz.ID)+"/youtube-vertical",
		map[string]any{"tier": "vertical1080p30"}, http.StatusOK)

	fresh, err := store.GetDestination(horiz.ID)
	if err != nil {
		t.Fatalf("reread horizontal: %v", err)
	}
	opts := s.ingestOptions(fresh, time.Time{})
	if !vertIDListed(opts.ExcludeStreamIDs, "stream-new-vert") {
		t.Fatalf("ExcludeStreamIDs = %v; the vertical stream must be excluded from the horizontal fallback", opts.ExcludeStreamIDs)
	}
}

// vertIDListed reports whether want is in ids. A local helper keeps this file independent
// of the oauth package's unexported ones.
func vertIDListed(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// verticalServer is managerServer with the YouTube provider pointed at base. It needs an
// engine, because requireSource refuses every destination route until one runs; the
// fixture is otherwise the same as managerServer's.
func verticalServer(t *testing.T, base string) (*Server, http.Handler, *db.DB) {
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
	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.Listeners.SRTPort = freeUDPPort(t)
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBroker()
	eng := engine.NewManager(quiet, cfg, store, defaultTools(), bus)
	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("engine manager Start: %v", err)
	}
	t.Cleanup(eng.Stop)
	s := New(Options{
		Log: quiet, Config: cfg, DB: store, Secrets: box,
		Engine: eng, Events: bus, Version: "test",
		Providers: oauth.NewSet(oauth.WithBaseURL(base)),
		Chat:      chat.New(),
	})
	// The status read goes out only while a half is running (#7, correction 6). The engine
	// here has nothing running, so the seam says a half is up; the tests that need the gate
	// closed override it. Without this default every status test would read as stopped.
	s.verticalHalfLive = func(...int64) bool { return true }
	h := s.Handler()
	setTestServer(s, h)
	return s, h, store
}

// verticalCreate pairs a fresh horizontal destination and returns the server, the handler,
// the store, the sign-in, the fake, and the horizontal destination's id.
func verticalCreate(t *testing.T) (*Server, http.Handler, *db.DB, func(*http.Request), *ytVerticalPairStub, int64) {
	t.Helper()
	fake := newYTVerticalPairStub(t)
	s, h, store := verticalServer(t, fake.srv.URL)
	sign := login(t, h)
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid-abc", "secret-xyz"); err != nil {
		t.Fatalf("seed youtube credentials: %v", err)
	}
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")
	horiz := ytDest(t, store, acct, "Main channel", "key-abc")
	send(t, h, sign, http.MethodPost, "/api/v1/destinations/"+itoa(horiz.ID)+"/youtube-vertical",
		map[string]any{"tier": "vertical1080p30"}, http.StatusOK)
	return s, h, store, sign, fake, horiz.ID
}

// TestTheVerticalStatusBodyNeverCarriesTheVerticalKey pins the leak rule on the status
// read. The body is shown in the UI and can reach logs, so it must carry the stream id and
// the health, never the key the encoder publishes with.
func TestTheVerticalStatusBodyNeverCarriesTheVerticalKey(t *testing.T) {
	_, h, _, sign, _, id := verticalCreate(t)
	body := send(t, h, sign, http.MethodGet, "/api/v1/destinations/"+itoa(id)+"/youtube-vertical", nil, http.StatusOK)
	if strings.Contains(string(body), verticalFakeKey) {
		t.Fatalf("the vertical status body carries the stream key: %s", body)
	}
	if !strings.Contains(string(body), `"healthStatus":"good"`) {
		t.Fatalf("status body %s does not report the vertical health it read", body)
	}
}

// TestAFailedVerticalHealthReadStaysOnTheSubRowAndLeavesThePairIntact pins the failure
// isolation the spec asks for. A dead vertical read is reported on its own sub-row; the
// horizontal destination and the pair row are untouched.
func TestAFailedVerticalHealthReadStaysOnTheSubRowAndLeavesThePairIntact(t *testing.T) {
	s, h, store, sign, fake, id := verticalCreate(t)
	fake.streamsDown = true
	body := send(t, h, sign, http.MethodGet, "/api/v1/destinations/"+itoa(id)+"/youtube-vertical", nil, http.StatusOK)
	var view struct {
		HealthError string `json:"healthError"`
	}
	if err := json.Unmarshal(body, &view); err != nil || view.HealthError == "" {
		t.Fatalf("a failed health read was not reported on the sub-row: %s", body)
	}
	if _, err := store.GetDestination(id); err != nil {
		t.Fatalf("the horizontal destination disappeared after a vertical read failed: %v", err)
	}
	if _, err := store.GetYouTubeVerticalPairByHorizontal(id); err != nil {
		t.Fatalf("the pair row was lost after a vertical read failed: %v", err)
	}
	_ = s
}

// TestConfirmingThePairingStepRecordsItAndRefusesAnUnpairedDestination pins the guided
// step. The confirmation is stored, and it cannot be recorded for a destination with no pair.
func TestConfirmingThePairingStepRecordsItAndRefusesAnUnpairedDestination(t *testing.T) {
	_, h, store, sign, _, id := verticalCreate(t)
	send(t, h, sign, http.MethodPost, "/api/v1/destinations/"+itoa(id)+"/youtube-vertical/confirm", nil, http.StatusOK)
	pair, err := store.GetYouTubeVerticalPairByHorizontal(id)
	if err != nil {
		t.Fatalf("read pair after confirm: %v", err)
	}
	if pair.PairingConfirmedAt == 0 {
		t.Fatal("confirming the pairing step did not record it")
	}
	send(t, h, sign, http.MethodPost, "/api/v1/destinations/999999/youtube-vertical/confirm", nil, http.StatusNotFound)
}

// TestTwoConcurrentPairingsCreateOneVerticalStreamOnYouTube is the low finding on the pair create. The duplicate check
// and the create were not one step, so two requests that arrived together both passed the check and both created a
// vertical stream on YouTube. The second pair row then failed and only its destination row was rolled back, leaving an
// orphan stream on the channel. The guard must stop the second call before it reaches YouTube.
func TestTwoConcurrentPairingsCreateOneVerticalStreamOnYouTube(t *testing.T) {
	fake := newYTVerticalPairStub(t)
	fake.createDelay = 200 * time.Millisecond
	s, h, store := verticalServer(t, fake.srv.URL)
	sign := login(t, h)
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid-abc", "secret-xyz"); err != nil {
		t.Fatalf("seed youtube credentials: %v", err)
	}
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")
	horiz := ytDest(t, store, acct, "Main channel", "key-abc")
	path := "/api/v1/destinations/" + itoa(horiz.ID) + "/youtube-vertical"

	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := jsonRequest(t, http.MethodPost, path, map[string]any{"tier": "vertical1080p30"})
			sign(r)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			statuses <- w.Code
		}()
	}
	wg.Wait()
	close(statuses)

	var ok, refused int
	for code := range statuses {
		switch code {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			refused++
		default:
			t.Errorf("a concurrent pairing answered %d, want 200 for one and 409 for the other", code)
		}
	}
	if ok != 1 || refused != 1 {
		t.Errorf("answers: %d ok, %d refused; want exactly one of each", ok, refused)
	}
	creates := 0
	fake.mu.Lock()
	for _, c := range fake.calls {
		if strings.HasPrefix(c, "POST /liveStreams?") {
			creates++
		}
	}
	fake.mu.Unlock()
	if creates != 1 {
		t.Errorf("YouTube saw %d liveStreams.insert calls, want 1; a second one leaves an orphan stream on the channel", creates)
	}
}

// countCalls reports how many recorded requests start with prefix. The status-pacing tests
// count the YouTube reads that actually went out, so a cached or refused read is visible as
// the absence of a call rather than as a response that merely looks right.
func (p *ytVerticalPairStub) countCalls(prefix string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// TestVerticalStatusReadIsPacedToOneYouTubeCallPerMinute pins correction 6 of the YouTube
// truth sheet. Two reads inside a minute reach YouTube once: the second is answered from the
// read the first one made. The count is taken from the recorded requests, so a server that
// returned the right body while still calling YouTube would fail here.
func TestVerticalStatusReadIsPacedToOneYouTubeCallPerMinute(t *testing.T) {
	s, h, _, sign, fake, id := verticalCreate(t)
	before := fake.countCalls("GET /liveStreams?")
	for i := 0; i < 2; i++ {
		send(t, h, sign, http.MethodGet, "/api/v1/destinations/"+itoa(id)+"/youtube-vertical", nil, http.StatusOK)
	}
	if got := fake.countCalls("GET /liveStreams?") - before; got != 1 {
		t.Fatalf("two status reads inside one minute made %d YouTube liveStreams reads, want 1", got)
	}
	_ = s
}

// TestVerticalStatusReadGoesOutAgainOnceTheMinuteHasPassed pins the other half of the pacing:
// the cache expires, so the operator sees a fresh reading rather than a stale one forever.
func TestVerticalStatusReadGoesOutAgainOnceTheMinuteHasPassed(t *testing.T) {
	s, h, _, sign, fake, id := verticalCreate(t)
	clock := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	s.verticalNow = func() time.Time { return clock }
	send(t, h, sign, http.MethodGet, "/api/v1/destinations/"+itoa(id)+"/youtube-vertical", nil, http.StatusOK)
	clock = clock.Add(61 * time.Second)
	before := fake.countCalls("GET /liveStreams?")
	send(t, h, sign, http.MethodGet, "/api/v1/destinations/"+itoa(id)+"/youtube-vertical", nil, http.StatusOK)
	if got := fake.countCalls("GET /liveStreams?") - before; got != 1 {
		t.Fatalf("a read 61 s after the last one made %d YouTube reads, want 1", got)
	}
}

// TestVerticalStatusReadMakesNoYouTubeCallWhileNeitherHalfRuns pins the live gate. With both
// halves stopped there is nothing to watch, so the status read answers from polyemesis alone
// and YouTube is not called. The response says so with live:false.
func TestVerticalStatusReadMakesNoYouTubeCallWhileNeitherHalfRuns(t *testing.T) {
	s, h, _, sign, fake, id := verticalCreate(t)
	s.verticalHalfLive = func(...int64) bool { return false }
	before := fake.countCalls("GET /liveStreams?")
	body := send(t, h, sign, http.MethodGet, "/api/v1/destinations/"+itoa(id)+"/youtube-vertical", nil, http.StatusOK)
	if got := fake.countCalls("GET /liveStreams?") - before; got != 0 {
		t.Fatalf("a status read with both halves stopped made %d YouTube reads, want 0", got)
	}
	if !strings.Contains(string(body), `"live":false`) {
		t.Fatalf("status body %s does not say the halves are stopped", body)
	}
}

// TestVerticalStatusReadIsRefusedWhenTheYouTubeBudgetHasNoRoom pins the quota pacer. The read
// is charged to the same budget the chat adapter spends from, and a read the budget cannot
// afford is refused on the sub-row without a call. The adapter itself is covered in
// internal/chat; this test asserts the API consults it on the status path.
func TestVerticalStatusReadIsRefusedWhenTheYouTubeBudgetHasNoRoom(t *testing.T) {
	s, h, _, sign, fake, id := verticalCreate(t)
	s.verticalHalfLive = func(...int64) bool { return true }
	s.verticalChargeQuota = func(cost int) bool { return false }
	before := fake.countCalls("GET /liveStreams?")
	body := send(t, h, sign, http.MethodGet, "/api/v1/destinations/"+itoa(id)+"/youtube-vertical", nil, http.StatusOK)
	if got := fake.countCalls("GET /liveStreams?") - before; got != 0 {
		t.Fatalf("a read the budget refused still reached YouTube %d times", got)
	}
	if !strings.Contains(string(body), "quota") {
		t.Fatalf("the refused read does not say why on the sub-row: %s", body)
	}
}

// TestPairedStatusPayloadLinksTheTwoHalves pins the payload fields the UI, the reminders and
// the Stream Deck read. The horizontal row names its vertical half and whether the pairing is
// confirmed; the vertical row names the horizontal it belongs to. Both come from /status, the
// one payload every consumer reads, and cost no YouTube call.
func TestPairedStatusPayloadLinksTheTwoHalves(t *testing.T) {
	_, h, store, sign, _, id := verticalCreate(t)
	pair, err := store.GetYouTubeVerticalPairByHorizontal(id)
	if err != nil {
		t.Fatalf("pair row: %v", err)
	}
	body := send(t, h, sign, http.MethodGet, "/api/v1/status", nil, http.StatusOK)
	var st struct {
		Destinations []struct {
			ID              int64 `json:"id"`
			YouTubeVertical *struct {
				VerticalID       int64 `json:"verticalId"`
				PairingConfirmed bool  `json:"pairingConfirmed"`
			} `json:"youtubeVertical"`
			VerticalHalfOf *int64 `json:"verticalHalfOf"`
		} `json:"destinations"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	var horiz, vert bool
	for _, d := range st.Destinations {
		if d.ID == id {
			horiz = d.YouTubeVertical != nil && d.YouTubeVertical.VerticalID == pair.VerticalID && !d.YouTubeVertical.PairingConfirmed
		}
		if d.ID == pair.VerticalID {
			vert = d.VerticalHalfOf != nil && *d.VerticalHalfOf == id
		}
	}
	if !horiz {
		t.Fatalf("the horizontal row does not name its vertical half: %s", body)
	}
	if !vert {
		t.Fatalf("the vertical row does not name the horizontal it belongs to: %s", body)
	}
}

// TestVerticalStatusReadIsRefusedByTheRealYouTubeAdapterWhenItsBudgetIsSpent pins that the
// status read reaches the quota pacer on the hub, not only the seam. A real chat adapter is
// attached to the server's own hub with a one-unit allowance. Its discovery loop spends that unit
// before the chat token is even used, so the budget is spent by the time the status read runs. The
// read must then be refused, and YouTube must see no liveStreams read at all. Without the charge
// the read would go out, and this test would fail.
func TestVerticalStatusReadIsRefusedByTheRealYouTubeAdapterWhenItsBudgetIsSpent(t *testing.T) {
	s, h, store, sign, fake, id := verticalCreate(t)
	dest, err := store.GetDestination(id)
	if err != nil || dest.AccountID == nil {
		t.Fatalf("read the horizontal destination: %v", err)
	}
	acct, err := s.tokenFor(context.Background(), *dest.AccountID)
	if err != nil {
		t.Fatalf("read the account: %v", err)
	}
	noChatToken := func(context.Context) (string, error) { return "", errors.New("no chat token in this test") }
	yt, err := chat.NewYouTube(chat.YouTubeConfig{
		AccountRef: acct.AccountRef, Channel: acct.AccountName, Token: noChatToken,
		APIBase: fake.srv.URL, QuotaUnits: 1, QuotaReserve: 0,
		Sleep: func(ctx context.Context, _ time.Duration) bool { <-ctx.Done(); return false },
	})
	if err != nil {
		t.Fatalf("new chat adapter: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := s.chat.Attach(ctx, yt); err != nil {
		t.Fatalf("attach to the server's hub: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for yt.Health().Quota == nil || yt.Health().Quota.Used < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the attached adapter never spent its one unit, so the budget was not exhausted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	before := fake.countCalls("GET /liveStreams?")
	body := send(t, h, sign, http.MethodGet, "/api/v1/destinations/"+itoa(id)+"/youtube-vertical", nil, http.StatusOK)
	if got := fake.countCalls("GET /liveStreams?") - before; got != 0 {
		t.Fatalf("a status read against a spent budget still reached YouTube %d times", got)
	}
	if !strings.Contains(string(body), "quota") {
		t.Fatalf("the refused read does not say why: %s", body)
	}
}
