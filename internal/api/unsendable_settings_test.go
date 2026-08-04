package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// A destination can end up holding settings its platform cannot send, and the
// route that produces it does not mention those settings at all: the dialog
// keeps compliance in form state across a platform change, and the update
// handler decodes over the existing row, so {"platform":"kick"} alone is
// enough. Stored compliance is inert on Kick -- ComplianceFor(kick) is absent
// and the push skips it -- but it becomes live again the moment the same
// destination is pointed back at YouTube.
//
// These go through the HANDLER rather than calling dropUnsendableSettings
// directly. The defect that made this necessary was a capability that existed
// and was never invoked; a test of the helper alone would prove the same thing
// about this one.

func kickBody(name string) map[string]any {
	return map[string]any{
		"name": name, "kind": "rtmp", "platform": "kick",
		"url": "rtmp://kick.example/live", "streamKey": "k",
		"compliance": map[string]any{"madeForKids": true, "privacy": "private"},
	}
}

func TestCreatingAKickDestinationDropsComplianceItCannotSend(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())

	var resp struct {
		Destination *db.Destination `json:"destination"`
		Warnings    []string        `json:"warnings"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/destinations",
		kickBody("kick one"), http.StatusCreated), &resp)

	if !resp.Destination.Compliance.Empty() {
		t.Fatalf("compliance was stored on a platform with no compliance surface: %+v",
			resp.Destination.Compliance)
	}
	if len(resp.Warnings) == 0 {
		t.Fatal("compliance was dropped silently; the operator is never told")
	}
	if !strings.Contains(strings.ToLower(resp.Warnings[0]), "compliance") {
		t.Fatalf("the warning does not name what was dropped: %q", resp.Warnings[0])
	}
}

func TestAYouTubeDestinationKeepsItsCompliance(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())

	body := kickBody("youtube one")
	body["platform"] = "youtube"

	var resp struct {
		Destination *db.Destination `json:"destination"`
		Warnings    []string        `json:"warnings"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/destinations",
		body, http.StatusCreated), &resp)

	// The negative case, and the one that matters most. A rule that dropped
	// compliance everywhere would satisfy every other check in this file while
	// quietly discarding the COPPA declarations the feature exists to send.
	if resp.Destination.Compliance.Empty() {
		t.Fatal("compliance was dropped from a platform that has a compliance surface")
	}
	if resp.Destination.Compliance.MadeForKids == nil || !*resp.Destination.Compliance.MadeForKids {
		t.Fatal("the stored COPPA declaration did not survive the write")
	}
	if len(resp.Warnings) != 0 {
		t.Fatalf("warned about a destination with nothing wrong with it: %v", resp.Warnings)
	}
}

func TestSwitchingADestinationToKickDropsTheComplianceItWasHolding(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())

	body := kickBody("was youtube")
	body["platform"] = "youtube"
	created := createDestination(t, h, sign, body)
	if created.Compliance.Empty() {
		t.Fatal("setup failed: the YouTube destination stored no compliance")
	}

	// The body mentions ONLY the platform. This is the path a partial PUT
	// takes, and the reason the check runs after the decode-over-existing
	// rather than against the request: nothing here says "compliance".
	var resp struct {
		Destination *db.Destination `json:"destination"`
		Warnings    []string        `json:"warnings"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPut,
		"/api/v1/destinations/"+itoa(created.ID),
		map[string]any{"platform": "kick"}, http.StatusOK), &resp)

	if !resp.Destination.Compliance.Empty() {
		t.Fatalf("a declaration survived onto a platform that cannot send it, "+
			"and would apply again if this destination were switched back: %+v",
			resp.Destination.Compliance)
	}
	if len(resp.Warnings) == 0 {
		t.Fatal("the declaration was dropped silently")
	}
}

func TestANonFacebookDestinationDropsFacebookCreateSettings(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())

	var resp struct {
		Destination *db.Destination `json:"destination"`
		Warnings    []string        `json:"warnings"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/destinations",
		map[string]any{
			"name": "twitch one", "kind": "rtmp", "platform": "twitch",
			"url": "rtmp://twitch.example/live", "streamKey": "t",
			"facebook": map[string]any{"donateCharityID": "1234"},
		}, http.StatusCreated), &resp)

	if !resp.Destination.Facebook.Empty() {
		t.Fatalf("Facebook create-call arguments were stored on Twitch: %+v",
			resp.Destination.Facebook)
	}
	if len(resp.Warnings) == 0 {
		t.Fatal("the Facebook settings were dropped silently")
	}
}

func TestNothingIsDroppedFromADestinationThatSetNoneOfIt(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())

	// A custom destination has no compliance surface either, so the platform
	// test alone would warn here -- on a destination that set nothing. The
	// emptiness half of the condition is what stops every ordinary save
	// carrying a warning about settings the operator never touched.
	var resp struct {
		Warnings []string `json:"warnings"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/destinations",
		destinationBody("plain", true, nil), http.StatusCreated), &resp)

	if len(resp.Warnings) != 0 {
		t.Fatalf("warned about settings that were never set: %v", resp.Warnings)
	}
}
