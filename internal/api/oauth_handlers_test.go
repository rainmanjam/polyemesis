package api

import (
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
// proves nothing about whether handleRefreshKey still calls it.
//
// It drives the real HTTP handler, through the real ingestOptionsFor(dest),
// into the real Facebook provider, and reads the Graph request that came out
// the far side. It used to replace an ingestForFn closure on Server and assert
// on the oauth.IngestOptions it captured, because internal/oauth's graph base
// was unexported and there was nowhere else to stand. There is now:
// oauth.WithBaseURL points the whole provider at this test's own server, so the
// assertion is on what Facebook would have received rather than on what the
// handler intended to send.
//
// Mutation to run against it: in ingestOptionsFor, replace
// `Privacy: dest.Compliance.FacebookPrivacy` with
// `Privacy: db.FBPrivacyEveryone` -- the most-exposing value Facebook offers.
// Observed FAIL ("privacy = EVERYONE, want SELF").
func TestRefreshKeySendsTheDestinationsStoredFacebookOptionsToTheProvider(t *testing.T) {
	s, h, store, stub := stubbedServer(t, config.Config{})
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

	// Graph refuses the create so the handler exits before s.eng().Reconcile()
	// -- testServer leaves the engine manager nil because no other route here
	// needs one, and a real one requires the same FFmpeg-and-listener machinery
	// renditions_test.go stands up for a different feature entirely. The
	// request is recorded before the refusal is written, so what Graph answers
	// has no bearing on what this test reads.
	stub.setCreateErr("stub: no real ingest in this test")

	r := jsonRequest(t, http.MethodPost,
		"/api/v1/destinations/"+strconv.FormatInt(dest.ID, 10)+"/refresh-key", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 from the stub's error (body %s)", w.Code, w.Body.String())
	}

	create := stub.first(http.MethodPost, "/me/live_videos")
	if create == nil {
		t.Fatalf("no broadcast create reached Facebook; refresh-key made these calls instead: %v",
			stub.calls())
	}
	// Graph's own wire form for each stored choice. Asserting the encoded
	// parameter rather than the oauth.IngestOptions struct is the point of the
	// rewrite: a mapping that is correct and a provider that drops the field on
	// the way out are different bugs, and only this catches the second.
	for _, want := range []struct{ key, value string }{
		{"status", "LIVE_NOW"},
		{"privacy", `{"value":"SELF"}`},
		{"crossposting_actions", `[{"page_id":"1234","action":"enable_crossposting_and_create_post"}]`},
		{"donate_button_charity_id", "999"},
	} {
		if got := create.Query.Get(want.key); got != want.value {
			t.Errorf("%s = %q, want %q (whole request %s)", want.key, got, want.value, create)
		}
	}
	// The destination never asked for backup ingest, and Facebook treats a
	// present-but-empty parameter as a value, so "not chosen" has to mean an
	// ABSENT key rather than an empty one.
	if _, ok := create.Query["enable_backup_ingest"]; ok {
		t.Errorf("enable_backup_ingest was sent for a destination that never asked for it: %s", create)
	}
}
