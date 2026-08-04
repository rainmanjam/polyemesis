package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// TestIngestOptionsForCarriesTheStoredFacebookChoicesToTheProvider is the
// regression this fix round exists for: refresh-key's mapping from a stored
// destination to oauth.IngestOptions had no test at all, so a hardcoded
// db.FBPrivacyEveryone -- the most-exposing value Facebook offers -- or the
// mapping being deleted outright both left the whole suite green. This pins
// the mapping at the one function that produces it, which is also the one the
// handler actually calls, so there is no second copy to drift from it.
func TestIngestOptionsForCarriesTheStoredFacebookChoicesToTheProvider(t *testing.T) {
	dest := &db.Destination{
		Platform: db.PlatformFacebook,
		Compliance: db.Compliance{
			FacebookPrivacy: db.FBPrivacySelf,
		},
		Facebook: db.FacebookSettings{
			Crosspost:       []db.CrosspostTarget{{PageID: "1234", CreatePost: true}},
			DonateCharityID: "999",
		},
	}

	got := ingestOptionsFor(dest, time.Time{})
	want := oauth.IngestOptions{
		Privacy:         db.FBPrivacySelf,
		Crosspost:       []db.CrosspostTarget{{PageID: "1234", CreatePost: true}},
		DonateCharityID: "999",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ingestOptionsFor = %+v, want %+v", got, want)
	}
}

// TestIngestOptionsForSendsNothingWhenTheDestinationChoseNothing is the other
// half: a destination that never touched Facebook's declarative fields must
// produce the zero IngestOptions, which is what tells IngestFor to leave
// every field alone rather than sending an empty-but-present value.
func TestIngestOptionsForSendsNothingWhenTheDestinationChoseNothing(t *testing.T) {
	dest := &db.Destination{Platform: db.PlatformFacebook}

	got := ingestOptionsFor(dest, time.Time{})
	if !reflect.DeepEqual(got, oauth.IngestOptions{}) {
		t.Errorf("ingestOptionsFor(unconfigured) = %+v, want the zero value", got)
	}
}

// TestRefreshKeySendsTheDestinationsStoredFacebookOptionsToTheProvider closes
// the gap ingestOptionsFor's own test could not: that function being correct
// proves nothing about whether handleRefreshKey still calls it. ingestForFn is
// the seam that makes the call itself observable -- it drives the real HTTP
// handler, through the real ingestOptionsFor(dest), and only replaces the
// point where the mapped options would otherwise leave the process to reach
// Facebook, which internal/oauth gives no other package a way to intercept.
func TestRefreshKeySendsTheDestinationsStoredFacebookOptionsToTheProvider(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	acctID := connectAccount(t, store, s.box, db.PlatformFacebook, "ada")
	if err := store.PutPlatformCreds(s.box, db.PlatformFacebook, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	dest, err := store.CreateDestination(&db.Destination{
		Name: "Facebook", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmps://ingest.example/app", StreamKey: "sk-live-old", AccountID: &acctID,
		Compliance: db.Compliance{FacebookPrivacy: db.FBPrivacySelf},
		Facebook: db.FacebookSettings{
			Crosspost:       []db.CrosspostTarget{{PageID: "1234", CreatePost: true}},
			DonateCharityID: "999",
		},
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}

	var (
		called   bool
		captured oauth.IngestOptions
	)
	// The stub returns an error rather than a fabricated success so the handler
	// exits before s.eng().Reconcile() -- testServer leaves the engine manager
	// nil because no other route here needs one, and a real one requires the
	// same FFmpeg-and-listener machinery renditions_test.go stands up for a
	// different feature entirely. Capture happens unconditionally, before the
	// stub returns, so what it returns has no bearing on what this test checks.
	stubErr := errors.New("stub: no real ingest in this test")
	s.ingestForFn = func(ctx context.Context, provider oauth.Provider, clientID string,
		acct *db.PlatformAccount, opts oauth.IngestOptions) (*oauth.Ingest, string, error) {
		called = true
		captured = opts
		return nil, "", stubErr
	}

	r := jsonRequest(t, http.MethodPost,
		"/api/v1/destinations/"+strconv.FormatInt(dest.ID, 10)+"/refresh-key", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 from the stub's error (body %s)", w.Code, w.Body.String())
	}
	if !called {
		t.Fatal("ingestForFn was never invoked; refresh-key did not reach the seam at all")
	}
	want := oauth.IngestOptions{
		Privacy:         db.FBPrivacySelf,
		Crosspost:       []db.CrosspostTarget{{PageID: "1234", CreatePost: true}},
		DonateCharityID: "999",
	}
	if !reflect.DeepEqual(captured, want) {
		t.Errorf("IngestOptions passed to the provider = %+v, want %+v", captured, want)
	}
}
