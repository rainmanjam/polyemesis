package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/captions"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// The #8 caption mode over the wire. The store and Validate are tested in
// internal/db; these tests cover the two decisions only the HTTP layer makes:
// the Facebook switch, which is read from the process environment and so is not
// part of Validate, and the refusal to change captions while a destination is
// delivering, which is what keeps a YouTube delay from moving under a live feed.
//
// The pure rule is tested on its own first, so a regression in the decision is
// caught without the server fixture in the way.

// TestCaptionModeGateRefusesFacebookOnlyWhileItsSwitchIsClosed pins the switch
// rule. Turning Facebook captions ON needs the switch. A row that already has
// them keeps saving with the switch closed, so closing the switch never strands
// an edit to an unrelated field.
//
// MUTATION: captionModeAllowedNow returns true unconditionally. Observed: FAIL on
// the first row, "facebook_sei turned on with the switch closed was accepted".
func TestCaptionModeGateRefusesFacebookOnlyWhileItsSwitchIsClosed(t *testing.T) {
	cases := []struct {
		name       string
		mode       string
		stored     string
		switchOpen bool
		want       bool
	}{
		{"facebook turned on with the switch closed is refused", "facebook_sei", "off", false, false},
		{"facebook turned on with the switch open is accepted", "facebook_sei", "off", true, true},
		{"facebook already stored keeps saving with the switch closed", "facebook_sei", "facebook_sei", false, true},
		{"sei is never gated by the facebook switch", "sei", "off", false, true},
		{"off is never gated", "off", "facebook_sei", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := captionModeAllowedNow(c.mode, c.stored, c.switchOpen); got != c.want {
				t.Fatalf("captionModeAllowedNow(%q, %q, %v) = %v, want %v", c.mode, c.stored, c.switchOpen, got, c.want)
			}
		})
	}
}

// TestCaptionModeSurvivesTheRoundTripAndRefusesAFacebookSwitchOff drives the
// real PUT. Twitch SEI is stored and read back through the API. A Facebook
// caption turn-on is refused with the message that names the switch, because
// "not allowed" alone sends the operator looking in the wrong place.
//
// MUTATION: the captionModeAllowedNow call removed from handleUpdateDestination.
// Observed: the Facebook PUT returns 200 where the test wants 400.
func TestCaptionModeSurvivesTheRoundTripAndRefusesAFacebookSwitchOff(t *testing.T) {
	t.Setenv(captions.FacebookSEIEnv, "")
	_, h, store, sign := engineServer(t, defaultTools(), Options{})

	tw, err := store.CreateDestination(&db.Destination{
		Name: "twitch", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
		URL: "rtmp://live.twitch.tv/app", StreamKey: "sk-live-caption",
		AudioBitrate: 160,
	})
	if err != nil {
		t.Fatalf("create twitch destination: %v", err)
	}
	put := func(id int64, body map[string]any) *http.Response {
		t.Helper()
		r := jsonRequest(t, http.MethodPut, "/api/v1/destinations/"+strconv.FormatInt(id, 10), body)
		sign(r)
		return &http.Response{StatusCode: do(t, h, r).Code}
	}
	if code := put(tw.ID, map[string]any{"captionMode": "sei"}).StatusCode; code != http.StatusOK {
		t.Fatalf("set twitch caption mode sei: status %d, want 200", code)
	}
	r := jsonRequest(t, http.MethodGet, "/api/v1/destinations/"+strconv.FormatInt(tw.ID, 10), nil)
	sign(r)
	var got struct {
		Destination struct {
			CaptionMode string `json:"captionMode"`
		} `json:"destination"`
	}
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("read destination: status %d", w.Code)
	} else if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Destination.CaptionMode != "sei" {
		t.Fatalf("captionMode came back %q through the API, want sei", got.Destination.CaptionMode)
	}

	fb, err := store.CreateDestination(&db.Destination{
		Name: "facebook", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmps://live-api-s.facebook.com:443/rtmp/", StreamKey: "fb-caption-fake",
		AudioBitrate: 160,
	})
	if err != nil {
		t.Fatalf("create facebook destination: %v", err)
	}
	if code := put(fb.ID, map[string]any{"captionMode": "facebook_sei"}).StatusCode; code != http.StatusBadRequest {
		t.Fatalf("facebook_sei with the switch closed: status %d, want 400", code)
	}
}

// TestCaptionModeRefusesAChangeWhileTheDestinationIsDelivering is the live
// guard. A caption route or delay that moved under a running feed would reach
// the platform as a different stream from the one the operator watched start.
//
// MUTATION: the destinationLive check removed from handleUpdateDestination.
// Observed: the PUT returns 200 where the test wants 409.
func TestCaptionModeRefusesAChangeWhileTheDestinationIsDelivering(t *testing.T) {
	t.Setenv(captions.FacebookSEIEnv, "")
	s, h, store, sign := engineServer(t, defaultTools(), Options{})
	s.deliveringFn = func(int64) bool { return true }

	tw, err := store.CreateDestination(&db.Destination{
		Name: "twitch", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
		URL: "rtmp://live.twitch.tv/app", StreamKey: "sk-live-live",
		AudioBitrate: 160,
	})
	if err != nil {
		t.Fatalf("create twitch destination: %v", err)
	}
	r := jsonRequest(t, http.MethodPut, "/api/v1/destinations/"+strconv.FormatInt(tw.ID, 10),
		map[string]any{"captionMode": "sei"})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusConflict {
		t.Fatalf("caption mode change while delivering: status %d, want 409", w.Code)
	}
}
