package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// The URL Kick's Stream Settings page prints, verbatim, and the one that works.
const (
	kickDashboardURL = "rtmps://fa723fc1b171.global-contribute.live-video.net/"
	kickWorkingURL   = "rtmps://fa723fc1b171.global-contribute.live-video.net:443/app"
)

func ivsKickBody(name, url string) map[string]any {
	return map[string]any{
		"name":         name,
		"kind":         "rtmp",
		"platform":     "kick",
		"url":          url,
		"streamKey":    "sk_placeholder_key",
		"enabled":      false,
		"audioBitrate": 160,
		"profile": map[string]any{
			"mode": "simple", "tracks": trackSel(0), "matrix": []any{},
			"normalize": "off", "sampleRate": 48000,
		},
	}
}

// A Kick destination saved with the URL Kick's dashboard prints was accepted
// with a ten-second toast, and then reported a TLS failure for ever after: the
// stream key became the RTMP application name and Amazon IVS dropped the
// connection. An IVS ingest takes exactly one application, so this one URL
// shape is refused at save, and the refusal names the URL that works.
//
// Proven able to fail by making refuseIVSWithoutApp return false: the create
// answers 201 again.
func TestAKickURLWithoutAppIsRefusedOnCreateWithTheWorkingURL(t *testing.T) {
	h, store, sign := sourceServer(t)

	raw := send(t, h, sign, http.MethodPost, "/api/v1/destinations",
		ivsKickBody("kick-pasted-verbatim", kickDashboardURL), http.StatusBadRequest)

	if !strings.Contains(string(raw), kickWorkingURL) {
		t.Errorf("the refusal does not name the URL that works (%s):\n%s", kickWorkingURL, raw)
	}
	if !strings.Contains(string(raw), "/app") {
		t.Errorf("the refusal does not say what is missing:\n%s", raw)
	}
	all, err := store.ListDestinations()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range all {
		if d.Name == "kick-pasted-verbatim" {
			t.Fatalf("the refused destination was stored anyway: %+v", d)
		}
	}
}

// An edit is where a fresh address usually gets pasted in, so the same URL is
// refused when a save changes the URL to it -- and the stored row is untouched.
//
// Proven able to fail by deleting the refuseIVSWithoutApp call in
// handleUpdateDestination: the PUT answers 200 and stores the pathless URL.
func TestEditingAKickURLToOneWithoutAppIsRefused(t *testing.T) {
	h, store, sign := sourceServer(t)

	var out map[string]any
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/destinations",
		ivsKickBody("kick-ok", kickWorkingURL), http.StatusCreated), &out)
	id := int64(out["destination"].(map[string]any)["id"].(float64))

	raw := send(t, h, sign, http.MethodPut, "/api/v1/destinations/"+strconv.FormatInt(id, 10),
		map[string]any{"url": kickDashboardURL}, http.StatusBadRequest)
	if !strings.Contains(string(raw), kickWorkingURL) {
		t.Errorf("the refusal does not name the URL that works:\n%s", raw)
	}
	got, err := store.GetDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != kickWorkingURL {
		t.Errorf("the refused edit changed the stored URL to %q", got.URL)
	}
}

// A row that ALREADY holds the URL -- saved before this refusal existed -- must
// stay editable: renaming it, switching it off, or saving the dialog without
// touching the URL is not the save that introduced the problem. It is warned
// about instead, with the same working URL.
//
// Proven able to fail by dropping the `existing.URL != storedURL` condition in
// handleUpdateDestination: the rename is refused.
func TestARowAlreadyHoldingTheKickURLStaysEditableAndIsWarned(t *testing.T) {
	h, store, sign := sourceServer(t)

	src := int64(1)
	row := &db.Destination{
		Name: "kick-legacy", Kind: db.DestRTMP, Platform: db.PlatformKick,
		URL: kickDashboardURL, StreamKey: "sk_placeholder_key",
		AudioBitrate: 160, SourceID: &src, Profile: routing.DefaultProfile(),
	}
	created, err := store.CreateDestination(row)
	if err != nil {
		t.Fatalf("seeding the legacy row: %v", err)
	}

	raw := send(t, h, sign, http.MethodPut, "/api/v1/destinations/"+strconv.FormatInt(created.ID, 10),
		map[string]any{"name": "kick-renamed", "url": kickDashboardURL}, http.StatusOK)
	var out map[string]any
	decodeInto(t, raw, &out)
	ws, _ := out["warnings"].([]any)
	joined := ""
	for _, w := range ws {
		joined += w.(string) + "\n"
	}
	if !strings.Contains(joined, kickWorkingURL) {
		t.Errorf("the legacy row's save is not warned with the working URL:\n%s", raw)
	}
}

// Refresh stream key writes the ingest URL the platform answers. If Kick's API
// answers with the shape its dashboard prints -- an IVS host with no /app -- that
// write would put back the URL a save now refuses, over one the operator had
// already corrected. It is corrected instead, and the response says so.
//
// Proven able to fail by deleting the IVSAppPathFix branch in
// handleRefreshStreamKey: the stored URL becomes the pathless one.
func TestRefreshKeyDoesNotStoreAKickURLWithoutApp(t *testing.T) {
	kick := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"broadcaster_user_id":7,"slug":"chan",
			"stream":{"is_live":false,"url":"`+kickDashboardURL+`","key":"sk_placeholder_fetched"}}]}`)
	}))
	t.Cleanup(kick.Close)

	s, h, store, sign := engineServer(t, defaultTools(), Options{
		Providers: oauth.NewSet(oauth.WithBaseURL(kick.URL)),
	})
	acctID := connectAccount(t, store, s.box, db.PlatformKick, "kicker")
	if err := store.PutPlatformCreds(s.box, db.PlatformKick, "cid", "placeholder-secret"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	dest, err := store.CreateDestination(&db.Destination{
		Name: "Kick", Kind: db.DestRTMP, Platform: db.PlatformKick,
		URL: kickWorkingURL, StreamKey: "sk_placeholder_old", AccountID: &acctID,
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}

	r := jsonRequest(t, http.MethodPost, "/api/v1/destinations/"+strconv.FormatInt(dest.ID, 10)+"/refresh-key", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	got, err := store.GetDestination(dest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != kickWorkingURL {
		t.Errorf("stored URL = %q, want %q", got.URL, kickWorkingURL)
	}
	if got.StreamKey != "sk_placeholder_fetched" {
		t.Errorf("the fetched key was not stored")
	}
	if !strings.Contains(w.Body.String(), "/app") {
		t.Errorf("the correction is not said out loud: %s", w.Body.String())
	}
}
