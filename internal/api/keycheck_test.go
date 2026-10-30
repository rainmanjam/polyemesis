package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// keyCheckFixture is a keyCrossCheck aimed at a stub YouTube whose channel
// lists the given stream keys, and which fails the test on anything but a GET.
func keyCheckFixture(t *testing.T, row *db.Destination, acctPlatform db.Platform, listed string) *keyCrossCheck {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("the key cross-check sent %s %s; it must only read", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, _ = io.WriteString(w, listed)
	}))
	t.Cleanup(srv.Close)
	return &keyCrossCheck{
		dest: func(int64) (*db.Destination, error) { return row, nil },
		token: func(context.Context, int64) (*db.PlatformAccount, error) {
			return &db.PlatformAccount{ID: 5, Platform: acctPlatform, AccessToken: "tok"}, nil
		},
		clientID:  func(db.Platform) (string, error) { return "cid", nil },
		providers: oauth.NewSet(oauth.WithBaseURL(srv.URL)),
	}
}

const ytListedRotated = `{"items":[{"id":"s1","cdn":{"ingestionType":"rtmp","ingestionInfo":{"streamName":"key-after-reset"}}}]}`

func ytRow(key string) *db.Destination {
	acct := int64(5)
	return &db.Destination{ID: 3, Platform: db.PlatformYouTube, StreamKey: key, AccountID: &acct}
}

// TestAKeyTheChannelNoLongerListsIsReportedChanged is the case the card's
// "YouTube's key changed — click to update" exists for: the key was reset in
// Studio, so the one we hold is gone from liveStreams.list.
func TestAKeyTheChannelNoLongerListsIsReportedChanged(t *testing.T) {
	k := keyCheckFixture(t, ytRow("key-before-reset"), db.PlatformYouTube, ytListedRotated)
	changed, ok, err := k.StreamKeyChanged(context.Background(), 3)
	if err != nil || !ok || !changed {
		t.Fatalf("changed=%v ok=%v err=%v, want a changed key", changed, ok, err)
	}
}

// TestAKeyTheChannelStillListsIsNotChanged: the key is fine, so the refusal is
// something else and the card must not send the operator to refresh it.
func TestAKeyTheChannelStillListsIsNotChanged(t *testing.T) {
	k := keyCheckFixture(t, ytRow("key-after-reset"), db.PlatformYouTube, ytListedRotated)
	changed, ok, err := k.StreamKeyChanged(context.Background(), 3)
	if err != nil || !ok || changed {
		t.Fatalf("changed=%v ok=%v err=%v, want an unchanged key", changed, ok, err)
	}
}

// TestTheComparisonIsByteForByte: #306 was a "helpful" normalisation between
// the stored key and the platform's spelling. A key differing only in case is
// a different key to the platform, so it is changed.
func TestTheComparisonIsByteForByte(t *testing.T) {
	k := keyCheckFixture(t, ytRow("KEY-AFTER-RESET"), db.PlatformYouTube, ytListedRotated)
	changed, _, _ := k.StreamKeyChanged(context.Background(), 3)
	if !changed {
		t.Fatal("a key differing only in case was treated as the same key")
	}
}

// TestPlatformsWithoutAReadOnlyKeyEndpointAreNotAsked: Facebook's key is a
// live video, so reading one creates one. The stub fails the test on any
// request at all.
func TestPlatformsWithoutAReadOnlyKeyEndpointAreNotAsked(t *testing.T) {
	row := ytRow("k")
	row.Platform = db.PlatformFacebook
	k := keyCheckFixture(t, row, db.PlatformFacebook, `{}`)
	k.providers = oauth.NewSet(oauth.WithBaseURL("http://127.0.0.1:1"))
	changed, ok, err := k.StreamKeyChanged(context.Background(), 3)
	if changed || ok || err != nil {
		t.Fatalf("changed=%v ok=%v err=%v, want no answer and no request", changed, ok, err)
	}
}

// TestAnAccountForAnotherPlatformIsNotAsked: a row whose account is on a
// different platform than the row says is one dropUnsendableSettings has not
// repaired yet. Asking YouTube about a Twitch token could only produce a false
// "changed", and a false "changed" sends the operator to rotate a working key.
func TestAnAccountForAnotherPlatformIsNotAsked(t *testing.T) {
	k := keyCheckFixture(t, ytRow("k"), db.PlatformTwitch, ytListedRotated)
	if changed, ok, _ := k.StreamKeyChanged(context.Background(), 3); changed || ok {
		t.Fatalf("changed=%v ok=%v for a mismatched account, want no answer", changed, ok)
	}
}
