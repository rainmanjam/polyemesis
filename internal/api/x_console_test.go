package api

// The X console's server half (#3): the coordinator telling the X chat adapter
// which broadcast it published, the title an X PUBLISH carries, the go-live
// title's live lock, and the Livestream gate state the console reads.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/startupenv"
)

// TestTellingTheXBroadcastReachesTheAccountsRunningAdapter: setXBroadcast is the coordinator's pointChat. It finds
// the adapter by the account's ref and points it at the broadcast, which is proved by where a chat send then goes:
// to the told broadcast, with no list read.
//
// MUTATION: SetBroadcast not called in setXBroadcast. Observed: FAIL, the send listed broadcasts and went to B1run.
func TestTellingTheXBroadcastReachesTheAccountsRunningAdapter(t *testing.T) {
	var calls []string
	xs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/2/broadcasts":
			_, _ = io.WriteString(w, `{"data":[{"id":"B1run","state":"RUNNING"}]}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/chat"):
			_, _ = io.WriteString(w, `{"data":{"success":true,"timestamp":"1"}}`)
		default:
			// The activity stream and the subscription: the adapter is attached but never needs to read here.
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	t.Cleanup(xs.Close)

	s, _, store := testServer(t, config.Config{})
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	acct, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformX, AccountName: "sundaypractice", AccountRef: "1234567890", AccessToken: "at",
	})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	adapter, err := chat.NewX(chat.XConfig{
		AccountRef: "1234567890", Channel: "sundaypractice",
		Token: func(context.Context) (string, error) { return "at", nil }, APIBase: xs.URL,
	})
	if err != nil {
		t.Fatalf("NewX: %v", err)
	}
	if err := s.chat.Attach(context.Background(), adapter); err != nil {
		t.Fatalf("attach: %v", err)
	}

	s.setXBroadcast(acct.ID, "B7told")
	if _, err := adapter.Send(context.Background(), "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	var sentTold, listed bool
	for _, c := range calls {
		sentTold = sentTold || c == "POST /2/broadcasts/B7told/chat"
		listed = listed || c == "GET /2/broadcasts"
	}
	if !sentTold || listed {
		t.Errorf("calls %v: want a send to the told broadcast and no list read", calls)
	}
}

// TestTellingTheXBroadcastIsQuietWithNothingToTell: no hub, an unknown account, and an account with no adapter are
// all normal states (chat off, account removed, gate closed), and none of them may panic the sweep.
func TestTellingTheXBroadcastIsQuietWithNothingToTell(t *testing.T) {
	s, _, _ := testServer(t, config.Config{})
	s.setXBroadcast(1, "B7")
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	s.setXBroadcast(424242, "B7")
	s.setXBroadcast(424242, "")
}

// TestTheXPublishTitlePrefersTheRecurringShowStartingNow (#18 for X): X has no title write before or during a show
// except the PUBLISH, so a recurring template's title reaches X there. Inside the go-live window after the show's
// start the rendered template title is the one sent; outside it, the destination's own stored title.
//
// MUTATION: the template branch skipped in xGoLiveTitleAt. Observed: FAIL, the stored title was sent mid-window.
func TestTheXPublishTitlePrefersTheRecurringShowStartingNow(t *testing.T) {
	s, _, d, _ := newGoLiveFixture(t, db.PlatformX, &fakeGoLivePusher{Provider: oauth.NewX()})
	d.GoLiveTitle = "stored title"

	in := s.xGoLiveTitleAt(d, time.Date(2026, 11, 3, 19, 4, 0, 0, time.UTC))
	if in != "Tuesday live 2026-11-03 at 19:00" {
		t.Errorf("title inside the show's go-live window = %q, want the rendered template title", in)
	}
	out := s.xGoLiveTitleAt(d, time.Date(2026, 11, 3, 21, 0, 0, 0, time.UTC))
	if out != "stored title" {
		t.Errorf("title long after the show started = %q, want the destination's stored title", out)
	}
}

// TestTheGoLiveTitleCannotChangeWhileTheXBroadcastIsLive: X has no write that changes a running broadcast's title,
// so a change saved while it runs would read as applied on the console and never reach X. It is refused with the
// reason. Before the broadcast is published, and with the title unchanged while live, the save goes through.
//
// MUTATION: the live check removed from handleUpdateDestination. Observed: FAIL, the live save answered 200.
func TestTheGoLiveTitleCannotChangeWhileTheXBroadcastIsLive(t *testing.T) {
	_, h, store, sign := engineServer(t, defaultTools(), Options{})
	x, err := store.CreateDestination(&db.Destination{
		Name: "x main", Kind: db.DestRTMP, Platform: db.PlatformX,
		URL: "rtmps://a.live.x.com:443/x", StreamKey: "abc123", AudioBitrate: 160,
		GoLiveTitle: "first",
	})
	if err != nil {
		t.Fatalf("create X destination: %v", err)
	}
	put := func(body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		r := jsonRequest(t, http.MethodPut, "/api/v1/destinations/"+strconv.FormatInt(x.ID, 10), body)
		sign(r)
		return do(t, h, r)
	}
	if w := put(map[string]any{"goLiveTitle": "before the show"}); w.Code != http.StatusOK {
		t.Fatalf("title change before publish: status %d %s, want 200", w.Code, w.Body.String())
	}
	if _, err := store.UpdateLifecycle(x.ID, func(d *db.Destination) bool {
		d.Lifecycle.BroadcastID, d.Lifecycle.Phase = "bc-1", phaseLive
		return true
	}); err != nil {
		t.Fatalf("mark live: %v", err)
	}
	w := put(map[string]any{"goLiveTitle": "during the show"})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "X") {
		t.Fatalf("title change while live: status %d %s, want 409 naming X", w.Code, w.Body.String())
	}
	if w := put(map[string]any{"goLiveTitle": "before the show", "name": "x renamed"}); w.Code != http.StatusOK {
		t.Fatalf("unchanged title while live: status %d %s, want 200", w.Code, w.Body.String())
	}
	got, err := store.GetDestination(x.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.GoLiveTitle != "before the show" {
		t.Errorf("stored title = %q, want the refused change not stored", got.GoLiveTitle)
	}

	// PRE_PUBLISHED: the PUBLISH has already gone to X with the old title, so a change now could not reach it either.
	if _, err := store.UpdateLifecycle(x.ID, func(d *db.Destination) bool {
		d.Lifecycle.Phase = "prepublished"
		return true
	}); err != nil {
		t.Fatalf("mark prepublished: %v", err)
	}
	if w := put(map[string]any{"goLiveTitle": "while X publishes"}); w.Code != http.StatusConflict {
		t.Fatalf("title change while X is publishing: status %d, want 409", w.Code)
	}
}

// TestTheXSetupGuideSaysWhetherTheLivestreamGateIsOpen: the console shows "needs X Livestream switched on" while the gate
// is closed, and it reads that from the guide rather than guessing, because only the server knows whether the
// operator switched the gate on (POLYEMESIS_X_LIVESTREAM). Platforms without a gate carry no field.
//
// MUTATION: the gate fill removed from handlePlatformGuides. Observed: FAIL, the X guide had no livestreamGate.
func TestTheXSetupGuideSaysWhetherTheLivestreamGateIsOpen(t *testing.T) {
	for _, tc := range []struct {
		open bool
		want string
	}{{false, "closed"}, {true, "open"}} {
		t.Run(tc.want, func(t *testing.T) {
			_, h, _ := testServerWith(t, Options{Providers: oauth.NewSet(oauth.WithXLivestream(tc.open))})
			sign := login(t, h)
			r := jsonRequest(t, http.MethodGet, "/api/v1/platforms/guides", nil)
			sign(r)
			w := do(t, h, r)
			if w.Code != http.StatusOK {
				t.Fatalf("guides: status %d", w.Code)
			}
			var guides []struct {
				Platform       string `json:"platform"`
				LivestreamGate string `json:"livestreamGate"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &guides); err != nil {
				t.Fatalf("decode: %v", err)
			}
			seen := false
			for _, g := range guides {
				switch {
				case g.Platform == string(db.PlatformX):
					seen = true
					if g.LivestreamGate != tc.want {
						t.Errorf("X livestreamGate = %q, want %q", g.LivestreamGate, tc.want)
					}
				case g.LivestreamGate != "":
					t.Errorf("%s carries livestreamGate %q, but only X has a gate", g.Platform, g.LivestreamGate)
				}
			}
			if !seen {
				t.Fatal("no X guide")
			}
		})
	}
}

// The gate reaches X the way main hands it over: through Options.Env alone, with
// no provider set of the test's own. The server builds the production set
// itself and carries the switch into it.
func TestTheStartupEnvironmentOpensTheXGateInTheServersOwnProviderSet(t *testing.T) {
	for _, open := range []bool{false, true} {
		want := map[bool]string{false: "closed", true: "open"}[open]
		t.Run(want, func(t *testing.T) {
			s, _, _ := testServerWith(t, Options{Env: startupenv.Env{XLivestream: open}})
			if got := s.livestreamGate(db.PlatformX); got != want {
				t.Errorf("livestreamGate(X) = %q, want %q", got, want)
			}
		})
	}
}

// TestPointingAnXDestinationElsewhereDropsItsGoLiveTitleWithAWarning: only X sends the title. A destination moved
// to another platform would otherwise keep a title nothing sends, and the store refuses that, so the save would
// fail for a field the operator never touched. It is dropped and the save says so, as Facebook's settings are.
//
// MUTATION: the go-live title branch removed from dropUnsendableSettings. Observed: FAIL, the save answered 400.
func TestPointingAnXDestinationElsewhereDropsItsGoLiveTitleWithAWarning(t *testing.T) {
	_, h, store, sign := engineServer(t, defaultTools(), Options{})
	x, err := store.CreateDestination(&db.Destination{
		Name: "x main", Kind: db.DestRTMP, Platform: db.PlatformX,
		URL: "rtmps://a.live.x.com:443/x", StreamKey: "abc123", AudioBitrate: 160, GoLiveTitle: "Sunday",
	})
	if err != nil {
		t.Fatalf("create X destination: %v", err)
	}
	r := jsonRequest(t, http.MethodPut, "/api/v1/destinations/"+strconv.FormatInt(x.ID, 10), map[string]any{
		"platform": "twitch", "url": "rtmp://live.twitch.tv/app",
	})
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("moving to Twitch: status %d %s, want 200", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "go-live title") {
		t.Errorf("the save did not say the go-live title was dropped: %s", w.Body.String())
	}
	got, err := store.GetDestination(x.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.GoLiveTitle != "" {
		t.Errorf("stored title on a Twitch destination = %q, want it dropped", got.GoLiveTitle)
	}
}
