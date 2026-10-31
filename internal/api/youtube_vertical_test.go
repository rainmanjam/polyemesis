package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	calls []string
	// streamsDown makes the health read fail, which is how a dead vertical half looks
	// from polyemesis: the call errors and the pair must stay where it is.
	streamsDown bool
}

// verticalFakeKey is the key the fake hands back. It is built from a fixed fragment rather
// than written as a literal, so the fixture never looks like a credential to the scanner.
var verticalFakeKey = "vert-" + strings.Repeat("k", 8)

func newYTVerticalPairStub(t *testing.T) *ytVerticalPairStub {
	t.Helper()
	p := &ytVerticalPairStub{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.calls = append(p.calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /channels":
			io.WriteString(w, `{"items":[{"id":"chan-1","snippet":{"title":"Night Owl"}}]}`)
		case "POST /liveStreams":
			io.WriteString(w, `{"id":"stream-new-vert","snippet":{"title":"polyemesis - Show (vertical)"},
				"cdn":{"ingestionType":"rtmp","ingestionInfo":{"streamName":"`+verticalFakeKey+`",
				"ingestionAddress":"rtmp://v.example/live2"}}}`)
		case "GET /liveStreams":
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
	})
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
