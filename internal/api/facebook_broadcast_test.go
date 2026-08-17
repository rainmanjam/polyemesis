package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// fbBroadcastDest builds a Facebook destination with a connected account, and
// optionally with a broadcast already recorded against it.
//
// The broadcast id is the interesting axis. A Facebook destination that has
// never gone live has none, and that state is NORMAL rather than broken -- it
// is what every destination looks like before its first broadcast, so both
// handlers have to answer it without reading as a failure.
func fbBroadcastDest(t *testing.T, s *Server, store *db.DB, broadcastID string) int64 {
	t.Helper()
	acctID := connectAccount(t, store, s.box, db.PlatformFacebook, "pagey")
	if err := store.PutPlatformCreds(s.box, db.PlatformFacebook, "cid", "secret"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	row := &db.Destination{
		Name: "FB", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmps://live-api-s.facebook.com:443/rtmp/", StreamKey: "fb-key", AccountID: &acctID,
	}
	if broadcastID != "" {
		row.Facebook.BroadcastID = broadcastID
	}
	dest, err := store.CreateDestination(row)
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}
	return dest.ID
}

// A DESTINATION WITH NO BROADCAST IS THE STATE EVERY DESTINATION STARTS IN, and
// the two handlers answer it differently on purpose.
//
// The health read answers 200 with supported:false, copying handleAccountStats:
// "we cannot ask" and "the destination is gone" are different problems and a
// client that cannot tell them apart shows the wrong one. There is nothing
// wrong with a destination that has not gone live.
//
// The end answers 409. It is a WRITE the operator explicitly asked for, and
// telling them it succeeded -- or answering 404, which reads as "polyemesis
// does not support this" -- would both be worse than saying plainly that there
// is no broadcast to end.
func TestFacebookBroadcastRoutesAnswerADestinationThatHasNeverGoneLive(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)
	id := strconv.FormatInt(fbBroadcastDest(t, s, store, ""), 10)

	t.Run("stream health says supported:false with a reason, not an error", func(t *testing.T) {
		r := jsonRequest(t, http.MethodGet, "/api/v1/destinations/"+id+"/facebook/stream-health", nil)
		sign(r)
		w := do(t, h, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
		}
		var got struct {
			Supported bool   `json:"supported"`
			Reason    string `json:"reason"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Supported {
			t.Error("claimed stream health is available for a destination with no broadcast")
		}
		if got.Reason == "" {
			t.Error("absence was reported with no explanation, so the UI has nothing to show")
		}
	})

	t.Run("ending refuses with 409 rather than pretending", func(t *testing.T) {
		r := jsonRequest(t, http.MethodPost, "/api/v1/destinations/"+id+"/facebook/end-broadcast", nil)
		sign(r)
		w := do(t, h, r)
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409. A 2xx would tell an operator their broadcast "+
				"ended when there was never one to end; a 404 would read as the feature "+
				"not existing. (body %s)", w.Code, w.Body.String())
		}
	})
}

// A non-Facebook destination must be refused rather than quietly attempted.
// Both handlers reach the platform check before they touch a token, so the
// answer names the real problem instead of failing later on a Graph call that
// was never going to make sense.
func TestFacebookBroadcastRoutesRefuseANonFacebookDestination(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	acctID := connectAccount(t, store, s.box, db.PlatformKick, "kicker")
	dest, err := store.CreateDestination(&db.Destination{
		Name: "Kick", Kind: db.DestRTMP, Platform: db.PlatformKick,
		URL: "rtmps://ingest.example/app", StreamKey: "sk", AccountID: &acctID,
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}
	id := strconv.FormatInt(dest.ID, 10)

	for _, tc := range []struct {
		name, method, path string
	}{
		{"stream health", http.MethodGet, "/api/v1/destinations/" + id + "/facebook/stream-health"},
		{"end broadcast", http.MethodPost, "/api/v1/destinations/" + id + "/facebook/end-broadcast"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := jsonRequest(t, tc.method, tc.path, nil)
			sign(r)
			if w := do(t, h, r); w.Code < 400 {
				t.Fatalf("status = %d, want a refusal: a Kick destination has no Facebook "+
					"broadcast and this must not be attempted (body %s)", w.Code, w.Body.String())
			}
		})
	}
}

// Both routes are state-changing or outbound, so neither may be reachable
// without a session. The end is the one that matters most -- it is an
// irreversible public act on somebody's channel -- but an unauthenticated
// health read would also spend the operator's Graph budget for a stranger.
func TestFacebookBroadcastRoutesNeedASession(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	id := strconv.FormatInt(fbBroadcastDest(t, s, store, "lv-1"), 10)

	for _, tc := range []struct {
		name, method, path string
	}{
		{"stream health", http.MethodGet, "/api/v1/destinations/" + id + "/facebook/stream-health"},
		{"end broadcast", http.MethodPost, "/api/v1/destinations/" + id + "/facebook/end-broadcast"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Deliberately unsigned.
			r := jsonRequest(t, tc.method, tc.path, nil)
			if w := do(t, h, r); w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 401 or 403 for an unauthenticated caller (body %s)",
					w.Code, w.Body.String())
			}
		})
	}
}

// A POLLED READ MUST NOT PUT ERRORS IN A BROWSER CONSOLE FOR AN ORDINARY
// CONFIGURATION.
//
// The stream-health pane polls. A Facebook destination with no connected
// account is not a fault — it is a valid, permanent setup, and the honest
// answer is "there is nothing to report", not a 412. Answering 412 filled the
// console with "Failed to load resource" on a loop and failed
// live-status-rendering.spec.ts, which asserts a destination card logs nothing.
// That assertion is right: an operator opening devtools on a working install
// should not find errors from a panel with nothing to say.
//
// The WRITE next door keeps its error statuses. An end-broadcast the operator
// asked for and did not get is a real failure and must not be reported as a
// shrug.
func TestStreamHealthAnswersSupportedFalseRatherThanAnErrorItCannotAct(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	// A Facebook destination with a broadcast recorded but NO connected
	// account: nothing to ask with.
	dest, err := store.CreateDestination(&db.Destination{
		Name: "FB no account", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmps://live-api-s.facebook.com:443/rtmp/", StreamKey: "k",
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}
	_ = s
	id := strconv.FormatInt(dest.ID, 10)

	r := jsonRequest(t, http.MethodGet, "/api/v1/destinations/"+id+"/facebook/stream-health", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. A polled read that cannot ask must not report "+
			"an error status: the browser logs every 4xx, once per poll, on a "+
			"destination that is configured perfectly well. (body %s)",
			w.Code, w.Body.String())
	}
	var got struct {
		Supported bool   `json:"supported"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Supported {
		t.Error("claimed stream health is available for a destination with no account")
	}
	if got.Reason == "" {
		t.Error("said no without saying why, so the pane has nothing to show")
	}
}
